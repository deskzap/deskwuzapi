package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/googollee/go-socket.io/engineio"
	"github.com/googollee/go-socket.io/engineio/transport"
	"github.com/googollee/go-socket.io/engineio/transport/polling"
	"github.com/googollee/go-socket.io/engineio/transport/websocket"

	socketio "github.com/googollee/go-socket.io"
	"github.com/jmoiron/sqlx"
	"github.com/patrickmn/go-cache"
	"github.com/rs/zerolog/log"
)

type SocketServer struct {
	Server *socketio.Server
	db     *sqlx.DB
}

// NewSocketServer initializes the Socket.IO server
func NewSocketServer(db *sqlx.DB) *SocketServer {
	server := socketio.NewServer(&engineio.Options{
		Transports: []transport.Transport{
			&polling.Transport{
				CheckOrigin: func(r *http.Request) bool {
					return true
				},
			},
			&websocket.Transport{
				CheckOrigin: func(r *http.Request) bool {
					return true
				},
			},
		},
		PingInterval: 25 * time.Second,
		PingTimeout:  60 * time.Second,
	})

	server.OnConnect("/", func(s socketio.Conn) error {
		url := s.URL()
		token := url.Query().Get("token")

		if token == "" {
			return errors.New("authentication failed: missing token")
		}

		// 1. Check Cache
		val, found := userinfocache.Get(token)
		if !found {
			// Security: Mask Token in Logs
			maskedToken := "hidden"
			if len(token) > 6 {
				maskedToken = token[:3] + "..." + token[len(token)-3:]
			}
			log.Info().Str("token_mask", maskedToken).Msg("Socket auth: Token not found in cache, checking DB")

			// 2. Check DB (Fallback)
			if db != nil {
				var txtid, name, webhook, jid, events, proxy_url, qrcode string

				query := "SELECT id, name, webhook, jid, events, proxy_url, qrcode FROM users WHERE token=? LIMIT 1"
				query = db.Rebind(query)

				row := db.QueryRowx(query, token)
				err := row.Scan(&txtid, &name, &webhook, &jid, &events, &proxy_url, &qrcode)
				if err != nil {
					log.Warn().Err(err).Str("token", token).Msg("Socket auth: Token validation failed in DB")
					return errors.New("authentication failed: invalid token")
				}

				// Create Value map
				v := Values{map[string]string{
					"Id":      txtid,
					"Name":    name,
					"Jid":     jid,
					"Webhook": webhook,
					"Token":   token,
					"Proxy":   proxy_url,
					"Events":  events,
					"Qrcode":  qrcode,
				}}

				// Populate Cache manually
				userinfocache.Set(token, v, cache.NoExpiration)
				log.Info().Str("userID", txtid).Msg("Socket auth: User restored from DB to Cache")

				val = v
				found = true
			} else {
				log.Warn().Msg("Socket auth: DB connection is nil, cannot fallback")
				return errors.New("authentication failed: invalid token")
			}
		}

		userInfo := val.(Values)
		userID := userInfo.Get("Id")

		s.SetContext(userID)
		s.Join("instance:" + userID)

		log.Debug().Str("userID", userID).Str("sid", s.ID()).Msg("Socket connected and joined instance room")

		return nil
	})

	server.OnEvent("/", "message", func(s socketio.Conn, msg string) string {
		// Recovery to prevent server crash
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Msg("Recovered from panic in socket message handler")
			}
		}()

		if s == nil || s.Context() == nil {
			log.Warn().Msg("Socket connection or context is nil")
			return `{"status":"error","error":"no connection context"}`
		}

		userID, ok := s.Context().(string)
		if !ok {
			log.Warn().Msg("Socket context userID is not a string")
			return `{"status":"error","error":"invalid context"}`
		}

		log.Debug().Str("userID", userID).Str("msg", msg).Msg("Received socket message")

		var payload struct {
			Phone   string `json:"phone"`
			Message string `json:"message"`
		}

		// Handle both JSON object (if client mistakenly sends object) and string
		var jsonBytes []byte
		jsonBytes = []byte(msg)

		if err := json.Unmarshal(jsonBytes, &payload); err != nil {
			log.Error().Err(err).Str("msg", msg).Msg("Invalid JSON in socket message")
			return `{"status":"error","error":"invalid json"}`
		}

		client := clientManager.GetMyClient(userID)
		if client == nil {
			log.Warn().Str("userID", userID).Msg("Client not found in manager")
			return `{"status":"error","error":"client not connected"}`
		}

		resp, err := client.SendText(payload.Phone, payload.Message)
		if err != nil {
			log.Error().Err(err).Str("userID", userID).Msg("Failed to send text message via socket")
			return `{"status":"error","error":"` + err.Error() + `"}`
		}

		// Prepare postmap for events
		postmap := make(map[string]interface{})
		postmap["timestamp"] = time.Now().Unix()

		senderJID := "unknown"
		if client.WAClient != nil && client.WAClient.Store != nil && client.WAClient.Store.ID != nil {
			senderJID = client.WAClient.Store.ID.String()
		}

		// Normalizar JID para garantir formato padrão do WhatsApp (@s.whatsapp.net)
		recipientJID, _ := parseJID(payload.Phone)

		// Use TitleCase keys to match Whatsmeow event structure
		postmap["Type"] = "Message"
		postmap["IsFromMe"] = true
		postmap["Content"] = payload.Message
		postmap["Source"] = "websocket"
		postmap["Timestamp"] = time.Now().Unix()
		postmap["ID"] = resp
		postmap["To"] = recipientJID.String()
		postmap["From"] = senderJID

		// Legacy lowercase keys
		postmap["type"] = "Message"
		postmap["to"] = recipientJID.String()
		postmap["fromMe"] = true
		postmap["content"] = payload.Message
		postmap["source"] = "websocket"
		postmap["id"] = resp

		log.Info().Str("msgID", resp).Msg("SOCKET: Sync Prepare")

		// FINAL ATTEMPT: Async Broadcast with Wrapper (Identical to wmiau.go)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error().Interface("panic", r).Msg("Panic in Async Broadcast")
				}
			}()

			// 1. Wrapper for 'events' channel (Matches ReadReceipt structure)
			wrapper := map[string]interface{}{
				"event": postmap,
				"type":  "Message",
			}

			log.Info().Msg("SOCKET: Broadcasting to 'events' and 'message' (Async)")

			// Broadcast to 'events' (Confirmed Channel)
			server.BroadcastToRoom("/", "instance:"+userID, "events", wrapper)

			// Broadcast to 'message' (Target Channel - Flat)
			server.BroadcastToRoom("/", "instance:"+userID, "message", postmap)

			// Emit back to sender
			s.Emit("message", postmap)

			// Webhook
			sendEventWithWebHook(client, postmap, "")
		}()

		respJSON, _ := json.Marshal(map[string]interface{}{
			"status": "success",
			"id":     resp,
		})
		return string(respJSON)
	})

	server.OnError("/", func(s socketio.Conn, e error) {
		log.Error().Err(e).Msg("Socket error")
	})

	server.OnDisconnect("/", func(s socketio.Conn, reason string) {
		// Optional: Log disconnection
		// log.Debug().Str("reason", reason).Msg("Socket disconnected")
	})

	return &SocketServer{
		Server: server,
		db:     db,
	}
}

// BroadcastToInstance emits an event to all clients connected to a specific instance
func (s *SocketServer) BroadcastToInstance(instanceID string, event string, data interface{}) {
	if s.Server != nil {
		s.Server.BroadcastToRoom("/", "instance:"+instanceID, event, data)
	}
}
