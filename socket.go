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
			log.Info().Str("token", token).Msg("Socket auth: Token not found in cache, checking DB")

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

		// Trigger webhook manually for N8N integration (API-like)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error().Interface("panic", r).Msg("Panic in Socket manual webhook trigger")
				}
			}()

			log.Debug().Str("msgID", resp).Msg("Starting API-like webhook trigger for Socket Message")

			postmap := make(map[string]interface{})
			postmap["type"] = "Message"
			postmap["id"] = resp
			postmap["timestamp"] = time.Now().Unix()

			senderJID := "unknown"

			// Try to get Sender JID safely
			if client.WAClient != nil && client.WAClient.Store != nil && client.WAClient.Store.ID != nil {
				senderJID = client.WAClient.Store.ID.ToJID().String()
			} else {
				// Fallback to JID from cache/db if available in context or other struct
				// For now let's prioritize not crashing. "unknown" is better than panic.
			}

			postmap["from"] = senderJID
			postmap["to"] = payload.Phone // This might need parsing if it's not a JID, but SendText handles formatting inside, payload.Phone usually comes as JID or clean number.

			// Ensure format is JID-like for webhook consistency if possible, but raw is ok if N8N handles it.
			// Ideally we should parseJID(payload.Phone) to be sure.

			postmap["fromMe"] = true
			postmap["content"] = payload.Message
			postmap["source"] = "websocket"

			// CRITICAL FIX: Ensure client has a token, otherwise webhook fails silently
			if client.token == "" {
				log.Warn().Str("userID", userID).Msg("Client token is empty in Socket handler! Attempting recovery from DB...")
				if db != nil {
					var recoveredToken string
					// Ensure we select just the token string
					err := db.Get(&recoveredToken, "SELECT token FROM users WHERE id=$1", userID)
					if err == nil && recoveredToken != "" {
						client.token = recoveredToken
						log.Info().Str("userID", userID).Msg("Successfully recovered token from DB and injected into Client")
					} else {
						log.Error().Err(err).Str("userID", userID).Msg("Failed to recover token from DB")
					}
				} else {
					log.Error().Msg("DB connection is nil in Socket handler, cannot recover token")
				}
			}

			log.Debug().Interface("postmap", postmap).Str("token_preview", client.token).Msg("Dispatching direct webhook from Socket with Token Check")
			sendEventWithWebHook(client, postmap, "")

			// Broadcast to other connected clients (supports external listeners like N8N JS snippet)
			// Using the socket instance 's' captured from closure.
			// Is 's' accessible here? 's' is 'socketio.Conn'.
			// We need 'SocketServer' instance.
			// Wait, BroadcastToInstance is a method of *SocketServer.
			// We don't have 'SocketServer' instance variable easily accessible inside the anonymous function passed to OnEvent?
			// Actually 'server' variable (the socketio.Server) is available.
			// But BroadcastToInstance is a helper method on our struct.
			// We can just call server.BroadcastToRoom directly.
			// Room name is "instance:" + userID

			server.BroadcastToRoom("/", "instance:"+userID, "message", postmap)
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
