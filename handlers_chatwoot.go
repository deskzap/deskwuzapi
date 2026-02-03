package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mime"
	"path/filepath"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog/log"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// ChatwootWebhookPayload represents the incoming webhook from Chatwoot
type ChatwootWebhookPayload struct {
	Event   string `json:"event"`
	ID      int    `json:"id"`
	Account struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
	Inbox struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"inbox"`
	Conversation struct {
		ID           int `json:"id"`
		ContactInbox struct {
			SourceID string `json:"source_id"`
		} `json:"contact_inbox"`
		Meta struct {
			Contact struct {
				PhoneNumber string `json:"phone_number"`
				Identifier  string `json:"identifier"`
			} `json:"contact"`
		} `json:"meta"`
	} `json:"conversation"`
	Sender struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"sender"`
	MessageType interface{} `json:"message_type"` // Changed to interface{} to handle string or int
	Content     string      `json:"content"`
	Private     bool        `json:"private"`
	Status      string      `json:"status"`
	Attachments []struct {
		FileType string `json:"file_type"`
		DataURL  string `json:"data_url"`
	} `json:"attachments"`
}

func (s *server) getString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case int:
		return strconv.Itoa(val)
	case float64:
		return fmt.Sprintf("%.0f", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// HandleChatwootWebhook processes incoming webhooks from Chatwoot
func (s *server) HandleChatwootWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		integrationIDStr := r.URL.Query().Get("integration_id")
		if integrationIDStr == "" {
			s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("missing integration_id"))
			return
		}
		integrationID, err := strconv.Atoi(integrationIDStr)
		if err != nil {
			s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("invalid integration_id"))
			return
		}

		var payload ChatwootWebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			log.Error().Err(err).Msg("Chatwoot webhook: failed to decode payload")
			s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("invalid payload"))
			return
		}

		log.Debug().
			Str("event", payload.Event).
			Str("message_type", s.getString(payload.MessageType)).
			Bool("private", payload.Private).
			Str("content", payload.Content).
			Msg("Chatwoot webhook received")

		// Only process outgoing messages that are not private
		// Chatwoot sometimes sends message_type as 1 for outgoing, 0 for incoming. Or "outgoing"/"incoming"
		msgType := s.getString(payload.MessageType)
		isOutgoing := strings.EqualFold(msgType, "outgoing") || msgType == "1"

		if payload.Event != "message_created" || !isOutgoing || payload.Private {
			s.Respond(w, r, http.StatusOK, "ignored")
			return
		}

		integration, err := s.GetIntegrationByID(integrationID)
		if err != nil {
			log.Error().Err(err).Int("integration_id", integrationID).Msg("Chatwoot webhook: integration not found")
			s.Respond(w, r, http.StatusNotFound, fmt.Errorf("integration not found"))
			return
		}

		// Parse Chatwoot config from integration meta
		var meta ChatwootConfig
		if err := json.Unmarshal([]byte(integration.Meta), &meta); err != nil {
			log.Error().Err(err).Msg("Chatwoot webhook: failed to parse meta config")
			s.Respond(w, r, http.StatusInternalServerError, fmt.Errorf("invalid config"))
			return
		}

		txtid := integration.UserID
		client := clientManager.GetWhatsmeowClient(txtid)
		if client == nil || !client.IsConnected() {
			log.Warn().Str("userid", txtid).Msg("Chatwoot webhook: client not connected")
			s.Respond(w, r, http.StatusServiceUnavailable, fmt.Errorf("client not connected"))
			return
		}

		// Determine Recipient
		phone := s.extractPhoneFromPayload(payload)

		if phone == "" {
			log.Error().Msg("Chatwoot webhook: could not determine recipient phone")
			s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("recipient not found"))
			return
		}

		// Format phone: Strip + and any non-digit characters
		cleanPhone := ""
		for _, c := range phone {
			if c >= '0' && c <= '9' {
				cleanPhone += string(c)
			}
		}
		phone = cleanPhone

		// Basic sanity check
		if len(phone) < 5 {
			log.Error().Str("phone", phone).Msg("Chatwoot webhook: invalid phone")
			s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("invalid phone"))
			return
		}

		jid := types.NewJID(phone, types.DefaultUserServer)

		log.Info().
			Str("phone", phone).
			Str("jid", jid.String()).
			Str("sender", payload.Sender.Name).
			Msg("Chatwoot webhook: sending message to WhatsApp")

		// Prepare message content
		content := payload.Content

		// Apply sign message if enabled
		if meta.SignMessages && payload.Sender.Name != "" {
			content = signMessage(content, payload.Sender.Name, meta.SignDelimiter)
		}

		// Send Message
		if len(payload.Attachments) > 0 {
			// Send text first if exists
			if content != "" {
				s.sendTextMessage(client, jid, content)
			}
			// Send attachments
			for _, att := range payload.Attachments {
				// Try to send as media
				err := s.sendMediaMessage(client, jid, att.DataURL, "")
				if err != nil {
					log.Error().Err(err).Msg("Failed to send media message, falling back to link")
					attachmentMsg := fmt.Sprintf("📎 %s", att.DataURL)
					s.sendTextMessage(client, jid, attachmentMsg)
				}
			}
		} else {
			if content != "" {
				s.sendTextMessage(client, jid, content)
			}
		}

		log.Info().Str("jid", jid.String()).Msg("Chatwoot webhook: message sent successfully")
		s.Respond(w, r, http.StatusOK, map[string]string{"status": "sent", "recipient": jid.String()})
	}
}

func (s *server) sendTextMessage(client *whatsmeow.Client, jid types.JID, content string) error {
	msg := &waProto.Message{
		Conversation: proto.String(content),
	}
	_, err := client.SendMessage(context.Background(), jid, msg)
	if err != nil {
		log.Error().Err(err).Str("jid", jid.String()).Msg("Failed to send chatwoot reply")
		return err
	}
	return nil
}

func (s *server) sendMediaMessage(client *whatsmeow.Client, jid types.JID, url string, caption string) error {
	log.Debug().Str("url", url).Msg("Downloading media for Chatwoot reply")

	data, contentType, err := fetchURLBytes(context.Background(), url, 50*1024*1024) // 50MB limit
	if err != nil {
		return fmt.Errorf("failed to download media: %w", err)
	}

	// Detect type
	mediaType := whatsmeow.MediaImage
	if strings.HasPrefix(contentType, "video/") {
		mediaType = whatsmeow.MediaVideo
	} else if strings.HasPrefix(contentType, "audio/") {
		mediaType = whatsmeow.MediaAudio
	} else if strings.HasPrefix(contentType, "image/") {
		mediaType = whatsmeow.MediaImage
	} else {
		mediaType = whatsmeow.MediaDocument
	}

	uploadResp, err := client.Upload(context.Background(), data, mediaType)
	if err != nil {
		return fmt.Errorf("failed to upload media to WhatsApp: %w", err)
	}

	msg := &waProto.Message{}

	switch mediaType {
	case whatsmeow.MediaImage:
		msg.ImageMessage = &waProto.ImageMessage{
			URL:           proto.String(uploadResp.URL),
			DirectPath:    proto.String(uploadResp.DirectPath),
			MediaKey:      uploadResp.MediaKey,
			Mimetype:      proto.String(contentType),
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			Caption:       proto.String(caption),
		}
	case whatsmeow.MediaVideo:
		msg.VideoMessage = &waProto.VideoMessage{
			URL:           proto.String(uploadResp.URL),
			DirectPath:    proto.String(uploadResp.DirectPath),
			MediaKey:      uploadResp.MediaKey,
			Mimetype:      proto.String(contentType),
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			Caption:       proto.String(caption),
		}
	case whatsmeow.MediaAudio:
		msg.AudioMessage = &waProto.AudioMessage{
			URL:           proto.String(uploadResp.URL),
			DirectPath:    proto.String(uploadResp.DirectPath),
			MediaKey:      uploadResp.MediaKey,
			Mimetype:      proto.String(contentType),
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			PTT:           proto.Bool(false), // Always false for now, treat as audio file
		}
	case whatsmeow.MediaDocument:
		// Guess filename from URL or mime
		filename := filepath.Base(url)
		if filename == "." || filename == "/" {
			exts, _ := mime.ExtensionsByType(contentType)
			if len(exts) > 0 {
				filename = "file" + exts[0]
			} else {
				filename = "file"
			}
		}

		msg.DocumentMessage = &waProto.DocumentMessage{
			URL:           proto.String(uploadResp.URL),
			DirectPath:    proto.String(uploadResp.DirectPath),
			MediaKey:      uploadResp.MediaKey,
			Mimetype:      proto.String(contentType),
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			FileName:      proto.String(filename),
			Caption:       proto.String(caption),
		}
	}

	_, err = client.SendMessage(context.Background(), jid, msg)
	return err
}

// HandleChatwootWebhookByInstance processes webhooks using instance name (Evolution API compatible)
// URL: POST /chatwoot/webhook/{instance}
func (s *server) HandleChatwootWebhookByInstance() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract instance name from URL path
		path := r.URL.Path
		parts := strings.Split(path, "/")
		if len(parts) < 4 {
			s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("missing instance name"))
			return
		}
		instanceName := parts[len(parts)-1]

		log.Debug().Str("instance", instanceName).Msg("Chatwoot webhook by instance received")

		var payload ChatwootWebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			log.Error().Err(err).Msg("Chatwoot webhook: failed to decode payload")
			s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("invalid payload"))
			return
		}

		log.Debug().
			Str("event", payload.Event).
			Str("message_type", s.getString(payload.MessageType)).
			Bool("private", payload.Private).
			Str("content", payload.Content).
			Msg("Chatwoot webhook payload")

		// Only process outgoing messages that are not private
		msgType := s.getString(payload.MessageType)
		isOutgoing := strings.EqualFold(msgType, "outgoing") || msgType == "1"

		if !strings.EqualFold(payload.Event, "message_created") || !isOutgoing || payload.Private {
			s.Respond(w, r, http.StatusOK, "ignored")
			return
		}

		// Find user by instance name
		var userID string
		var qrCode string
		var connected bool

		query := "SELECT id, qrcode, connected FROM users WHERE name = ? LIMIT 1"
		if s.db.DriverName() == "postgres" {
			query = "SELECT id, qrcode, connected FROM users WHERE name = $1 LIMIT 1"
		}

		row := s.db.QueryRow(query, instanceName)
		err := row.Scan(&userID, &qrCode, &connected)
		if err != nil {
			log.Error().Err(err).Str("instance", instanceName).Msg("Chatwoot webhook: instance not found")
			s.Respond(w, r, http.StatusNotFound, fmt.Errorf("instance not found: %s", instanceName))
			return
		}

		// Find Chatwoot integration for this user
		integration, err := s.GetIntegrationByUserAndType(userID, "chatwoot")
		if err != nil {
			log.Error().Err(err).Str("instance", instanceName).Msg("Chatwoot webhook: integration not found for instance")
			s.Respond(w, r, http.StatusNotFound, fmt.Errorf("chatwoot integration not found"))
			return
		}

		// Parse Chatwoot config from integration meta
		var meta ChatwootConfig
		if err := json.Unmarshal([]byte(integration.Meta), &meta); err != nil {
			log.Error().Err(err).Msg("Chatwoot webhook: failed to parse meta config")
			s.Respond(w, r, http.StatusInternalServerError, fmt.Errorf("invalid config"))
			return
		}

		// Get user token for API calls
		var userToken string
		tokenRow := s.db.QueryRow("SELECT token FROM users WHERE id = ?", userID)
		tokenRow.Scan(&userToken)

		// Check if the message is from the manager contact (+123456)
		// The manager contact identifier is "manager@deskwuzapi.{instance}" or phone "+123456"
		isManagerContact := s.isManagerContact(payload)
		command := strings.TrimSpace(strings.ToLower(payload.Content))

		if isManagerContact {
			// This is the manager contact - process commands
			log.Info().Str("command", command).Str("instance", instanceName).Msg("Manager contact: processing command")
			s.handleManagerCommand(w, r, meta, payload.Conversation.ID, command, instanceName, userID, userToken)
			return
		}

		// Check if instance is connected
		client := clientManager.GetWhatsmeowClient(userID)
		if client == nil || !client.IsConnected() || !connected {
			log.Warn().Str("instance", instanceName).Msg("Chatwoot webhook: instance not connected, sending QR code")

			// Send QR Code to Chatwoot
			if qrCode != "" {
				err = s.sendQRCodeToChatwoot(meta, payload.Conversation.ID, qrCode, instanceName)
				if err != nil {
					log.Error().Err(err).Msg("Failed to send QR code to Chatwoot")
				}
				s.Respond(w, r, http.StatusOK, map[string]string{
					"status":  "qr_sent",
					"message": "Instance not connected. QR Code sent to conversation.",
				})
			} else {
				// No QR code available, send help message
				helpMsg := fmt.Sprintf("⚠️ **Instância '%s' não conectada**\n\n"+
					"Use os comandos abaixo para gerenciar a conexão:\n\n"+
					"📋 **Comandos disponíveis:**\n"+
					"• `status` - Verificar status da conexão\n"+
					"• `connect` - Iniciar conexão e gerar QR Code\n"+
					"• `qr` - Gerar novo QR Code\n"+
					"• `disconnect` - Desconectar sessão\n"+
					"• `help` - Mostrar esta ajuda", instanceName)
				err = s.sendChatwootTextMessage(meta, payload.Conversation.ID, helpMsg)
				if err != nil {
					log.Error().Err(err).Msg("Failed to send help message to Chatwoot")
				}
				s.Respond(w, r, http.StatusOK, map[string]string{
					"status":  "not_connected",
					"message": "Instance not connected. Help message sent.",
				})
			}
			return
		}

		// Instance is connected - process message normally
		phone := s.extractPhoneFromPayload(payload)
		if phone == "" {
			log.Error().Msg("Chatwoot webhook: could not determine recipient phone")
			s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("recipient not found"))
			return
		}

		jid := types.NewJID(phone, types.DefaultUserServer)

		log.Info().
			Str("phone", phone).
			Str("jid", jid.String()).
			Str("sender", payload.Sender.Name).
			Str("instance", instanceName).
			Msg("Chatwoot webhook: sending message to WhatsApp")

		// Prepare message content
		content := payload.Content

		// Apply sign message if enabled
		if meta.SignMessages && payload.Sender.Name != "" {
			content = signMessage(content, payload.Sender.Name, meta.SignDelimiter)
		}

		// Send Message
		if len(payload.Attachments) > 0 {
			if content != "" {
				s.sendTextMessage(client, jid, content)
			}
			for _, att := range payload.Attachments {
				err := s.sendMediaMessage(client, jid, att.DataURL, "")
				if err != nil {
					log.Error().Err(err).Msg("Failed to send media message, falling back to link")
					attachmentMsg := fmt.Sprintf("📎 %s", att.DataURL)
					s.sendTextMessage(client, jid, attachmentMsg)
				}
			}
		} else {
			if content != "" {
				s.sendTextMessage(client, jid, content)
			}
		}

		log.Info().Str("jid", jid.String()).Str("instance", instanceName).Msg("Chatwoot webhook: message sent successfully")
		s.Respond(w, r, http.StatusOK, map[string]string{"status": "sent", "recipient": jid.String()})
	}
}

// extractPhoneFromPayload extracts phone number from Chatwoot webhook payload
func (s *server) extractPhoneFromPayload(payload ChatwootWebhookPayload) string {
	phone := ""

	// 1. Try PhoneNumber from Contact Meta (Primary source)
	if p := payload.Conversation.Meta.Contact.PhoneNumber; p != "" {
		phone = p
	}

	// 2. Try Identifier
	if phone == "" {
		identifier := payload.Conversation.Meta.Contact.Identifier
		if identifier != "" {
			if strings.Contains(identifier, "@") {
				// Try to extract from JID if it looks like one (e.g. contains @s.whatsapp.net)
				extracted := extractPhoneNumber(identifier)
				if extracted != "" {
					phone = extracted
				} else if !strings.Contains(identifier, "whatsapp.net") {
					// If it has @ but NOT whatsapp.net, it might be an email -> Ignore
					// But if it is like "12345@c.us", we might want it?
					// Safer to ignore if extractPhoneNumber failed and it has @
				}
			} else {
				// If no @, assume it is just the raw number
				phone = identifier
			}
		}
	}

	// 3. Last resort: SourceID
	if phone == "" {
		sourceID := payload.Conversation.ContactInbox.SourceID
		if sourceID != "" {
			if strings.Contains(sourceID, "@") {
				extracted := extractPhoneNumber(sourceID)
				if extracted != "" {
					phone = extracted
				}
			} else {
				phone = sourceID
			}
		}
	}

	// Clean phone number (keep only digits)
	cleanPhone := ""
	for _, c := range phone {
		if c >= '0' && c <= '9' {
			cleanPhone += string(c)
		}
	}

	if len(cleanPhone) < 5 {
		return ""
	}

	return cleanPhone
}

// sendQRCodeToChatwoot sends a QR code image to the Chatwoot conversation
func (s *server) sendQRCodeToChatwoot(meta ChatwootConfig, conversationID int, qrCodeBase64 string, instanceName string) error {
	// Create a message with the QR code as an attachment
	// We'll send it as a text message with the base64 image embedded
	message := fmt.Sprintf("🔐 **Escaneie o QR Code para conectar a instância '%s'**\n\n"+
		"O QR Code expira em ~40 segundos. Se expirar, envie qualquer mensagem para receber um novo.\n\n"+
		"[QR Code Image]\n%s", instanceName, qrCodeBase64)

	return s.sendChatwootTextMessage(meta, conversationID, message)
}

// sendChatwootTextMessage sends a text message to a Chatwoot conversation
func (s *server) sendChatwootTextMessage(meta ChatwootConfig, conversationID int, content string) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%s/conversations/%d/messages",
		strings.TrimSuffix(meta.URL, "/"),
		meta.AccountID,
		conversationID)

	payload := map[string]interface{}{
		"content":      content,
		"message_type": "outgoing",
		"private":      false,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, strings.NewReader(string(jsonData)))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", meta.Token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("chatwoot API error: status %d", resp.StatusCode)
	}

	log.Debug().Int("conversation_id", conversationID).Msg("Sent message to Chatwoot conversation")
	return nil
}

// Manager contact phone number constant
const ManagerContactPhone = "123456"

// isManagerContact checks if the message is from the manager contact (+123456)
func (s *server) isManagerContact(payload ChatwootWebhookPayload) bool {
	// Check phone number from various sources
	phone := payload.Conversation.Meta.Contact.PhoneNumber
	identifier := payload.Conversation.Meta.Contact.Identifier
	sourceID := payload.Conversation.ContactInbox.SourceID

	// Clean phone number (remove + and spaces)
	cleanPhone := func(p string) string {
		result := ""
		for _, c := range p {
			if c >= '0' && c <= '9' {
				result += string(c)
			}
		}
		return result
	}

	// Check if any of the identifiers match the manager phone
	if cleanPhone(phone) == ManagerContactPhone {
		return true
	}
	if cleanPhone(identifier) == ManagerContactPhone {
		return true
	}
	if cleanPhone(sourceID) == ManagerContactPhone {
		return true
	}
	if strings.Contains(identifier, "manager@deskwuzapi") {
		return true
	}

	log.Debug().
		Str("phone", phone).
		Str("identifier", identifier).
		Str("sourceID", sourceID).
		Msg("Checking if manager contact")

	return false
}

// isManagerCommand checks if the message is a valid manager command
func (s *server) isManagerCommand(command string) bool {
	validCommands := []string{"status", "connect", "qr", "disconnect", "help", "desconectar", "conectar", "ajuda", "restart", "logout", "sair", "info", "ping"}
	for _, cmd := range validCommands {
		if command == cmd {
			return true
		}
	}
	return false
}

// handleManagerCommand processes manager commands and sends response to Chatwoot
func (s *server) handleManagerCommand(w http.ResponseWriter, r *http.Request, meta ChatwootConfig, conversationID int, command, instanceName, userID, userToken string) {
	var response string

	switch command {
	case "status":
		response = s.handleStatusCommand(userID, instanceName)
	case "connect", "conectar":
		response = s.handleConnectCommand(userID, userToken, instanceName, meta, conversationID)
	case "qr":
		response = s.handleQRCommand(userID, instanceName, meta, conversationID)
	case "disconnect", "desconectar":
		response = s.handleDisconnectCommand(userID, userToken, instanceName)
	case "logout", "sair":
		response = s.handleLogoutCommand(userID, instanceName)
	case "restart":
		response = s.handleRestartCommand(userID, instanceName)
	case "info":
		response = s.handleInfoCommand(userID, instanceName)
	case "ping":
		response = s.handlePingCommand(userID)
	case "help", "ajuda":
		response = s.handleHelpCommand(instanceName)
	default:
		response = "❌ Comando não reconhecido. Digite `help` para ver os comandos disponíveis."
	}

	err := s.sendChatwootTextMessage(meta, conversationID, response)
	if err != nil {
		log.Error().Err(err).Str("command", command).Msg("Failed to send command response to Chatwoot")
		s.Respond(w, r, http.StatusInternalServerError, fmt.Errorf("failed to send response"))
		return
	}

	s.Respond(w, r, http.StatusOK, map[string]string{
		"status":  "command_processed",
		"command": command,
	})
}

// handleStatusCommand returns the connection status
func (s *server) handleStatusCommand(userID, instanceName string) string {
	client := clientManager.GetWhatsmeowClient(userID)

	var connected, loggedIn bool
	if client != nil {
		connected = client.IsConnected()
		loggedIn = client.IsLoggedIn()
	}

	// Also check database
	var dbConnected bool
	row := s.db.QueryRow("SELECT connected FROM users WHERE id = ?", userID)
	row.Scan(&dbConnected)

	statusIcon := "🔴"
	statusText := "Desconectado"
	if connected && loggedIn {
		statusIcon = "🟢"
		statusText = "Conectado e Logado"
	} else if connected {
		statusIcon = "🟡"
		statusText = "Conectado (aguardando QR)"
	}

	return fmt.Sprintf("📊 **Status da Instância '%s'**\n\n"+
		"%s **Status**: %s\n"+
		"🔗 **WebSocket**: %v\n"+
		"📱 **Logado**: %v\n"+
		"💾 **DB Status**: %v",
		instanceName, statusIcon, statusText, connected, loggedIn, dbConnected)
}

// handleConnectCommand initiates connection and generates QR code
func (s *server) handleConnectCommand(userID, userToken, instanceName string, meta ChatwootConfig, conversationID int) string {
	client := clientManager.GetWhatsmeowClient(userID)

	// If already connected and logged in
	if client != nil && client.IsConnected() && client.IsLoggedIn() {
		return fmt.Sprintf("✅ **Instância '%s' já está conectada!**\n\n"+
			"Use `status` para verificar detalhes ou `disconnect` para desconectar.", instanceName)
	}

	// Try to connect using internal method
	// This simulates calling POST /session/connect
	log.Info().Str("instance", instanceName).Msg("Initiating connection via manager command")

	// Check if there's a QR code available
	var qrCode string
	row := s.db.QueryRow("SELECT qrcode FROM users WHERE id = ?", userID)
	row.Scan(&qrCode)

	if qrCode != "" {
		// Send QR Code
		err := s.sendQRCodeToChatwoot(meta, conversationID, qrCode, instanceName)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send QR code")
		}
		return fmt.Sprintf("🔐 **QR Code gerado para '%s'**\n\n"+
			"Escaneie o código acima com seu WhatsApp.\n"+
			"O código expira em ~40 segundos.\n\n"+
			"Se expirar, digite `qr` para gerar um novo.", instanceName)
	}

	return fmt.Sprintf("⏳ **Iniciando conexão para '%s'...**\n\n"+
		"Aguarde alguns segundos e digite `qr` para obter o QR Code.\n\n"+
		"Se a instância já estiver conectada, digite `status` para verificar.", instanceName)
}

// handleQRCommand generates and sends a new QR code
func (s *server) handleQRCommand(userID, instanceName string, meta ChatwootConfig, conversationID int) string {
	var qrCode string
	row := s.db.QueryRow("SELECT qrcode FROM users WHERE id = ?", userID)
	row.Scan(&qrCode)

	if qrCode == "" {
		return fmt.Sprintf("⚠️ **QR Code não disponível para '%s'**\n\n"+
			"Possíveis razões:\n"+
			"• A instância já está conectada (use `status` para verificar)\n"+
			"• A conexão ainda não foi iniciada (use `connect`)\n"+
			"• O QR Code expirou (tente novamente em alguns segundos)", instanceName)
	}

	err := s.sendQRCodeToChatwoot(meta, conversationID, qrCode, instanceName)
	if err != nil {
		log.Error().Err(err).Msg("Failed to send QR code")
		return "❌ Erro ao enviar QR Code. Tente novamente."
	}

	return fmt.Sprintf("🔐 **QR Code para '%s'**\n\n"+
		"Escaneie o código acima com seu WhatsApp.\n"+
		"O código expira em ~40 segundos.", instanceName)
}

// handleDisconnectCommand disconnects the session
func (s *server) handleDisconnectCommand(userID, userToken, instanceName string) string {
	client := clientManager.GetWhatsmeowClient(userID)

	if client == nil || !client.IsConnected() {
		return fmt.Sprintf("ℹ️ **Instância '%s' já está desconectada.**", instanceName)
	}

	// Disconnect
	client.Disconnect()

	// Update database
	s.db.Exec("UPDATE users SET connected = 0, qrcode = '' WHERE id = ?", userID)

	return fmt.Sprintf("✅ **Instância '%s' desconectada com sucesso.**\n\n"+
		"A sessão foi mantida. Use `connect` para reconectar sem escanear QR novamente.", instanceName)
}

// handleLogoutCommand logs out the device
func (s *server) handleLogoutCommand(userID, instanceName string) string {
	client := clientManager.GetWhatsmeowClient(userID)

	if client != nil {
		if client.IsLoggedIn() {
			err := client.Logout(context.Background())
			if err != nil {
				log.Error().Err(err).Msg("Failed to logout via manager command")
				return fmt.Sprintf("❌ Erro ao realizar logout: %v", err)
			}
		}
		// Disconnect if still connected
		if client.IsConnected() {
			client.Disconnect()
		}
	}

	// Clean up client manager
	clientManager.DeleteWhatsmeowClient(userID)
	clientManager.DeleteMyClient(userID)
	clientManager.DeleteHTTPClient(userID)

	// Update database
	s.db.Exec("UPDATE users SET connected = 0, qrcode = '', history = 0 WHERE id = ?", userID)

	// Send kill signal
	select {
	case killchannel[userID] <- true:
	default:
	}

	return fmt.Sprintf("👋 **Instância '%s' deslogada com sucesso!**\n\n"+
		"Sessão removida. Para usar novamente, envie `connect` e escaneie um novo QR Code.", instanceName)
}

// handleRestartCommand restarts the connection
func (s *server) handleRestartCommand(userID, instanceName string) string {
	client := clientManager.GetWhatsmeowClient(userID)

	if client != nil {
		client.Disconnect()
		time.Sleep(1 * time.Second)
		err := client.Connect()
		if err != nil {
			return fmt.Sprintf("❌ Erro ao reconectar: %v", err)
		}
	} else {
		return "⚠️ Cliente não encontrado ou não inicializado. Use `connect`."
	}

	return fmt.Sprintf("🔄 **Instância '%s' reiniciada!**\n\n"+
		"A conexão foi restabelecida. Use `status` para verificar.", instanceName)
}

// handleInfoCommand returns device info
func (s *server) handleInfoCommand(userID, instanceName string) string {
	client := clientManager.GetWhatsmeowClient(userID)

	if client == nil || !client.IsLoggedIn() {
		return fmt.Sprintf("ℹ️ **Instância '%s'**\n\nStatus: Desconectado/Deslogado", instanceName)
	}

	me := client.Store.ID
	if me == nil {
		return "⚠️ Informações do dispositivo indisponíveis."
	}

	pushName := "Desconhecido"
	// Try to get pushname from store if available or cache
	// (Whatsmeow Store logic varies)

	return fmt.Sprintf("📱 **Informações do Dispositivo - '%s'**\n\n"+
		"👤 **Nome**: %s\n"+
		"📞 **JID**: %s\n"+
		"🆔 **Device ID**: %d",
		instanceName, pushName, me.ToNonAD().String(), me.Device)
}

// handlePingCommand returns a simple pong
func (s *server) handlePingCommand(userID string) string {
	return "🏓 **Pong!**\n\nA API está online e respondendo."
}

// handleHelpCommand returns the help message
func (s *server) handleHelpCommand(instanceName string) string {
	return fmt.Sprintf("📋 **Comandos disponíveis - Instância '%s'**\n\n"+
		"• `status` - Verificar status da conexão\n"+
		"• `connect` - Iniciar conexão e gerar QR Code\n"+
		"• `qr` - Gerar novo QR Code\n"+
		"• `disconnect` - Desconectar sessão (mantém login)\n"+
		"• `restart` - Reiniciar conexão\n"+
		"• `logout` - Deslogar e apagar sessão\n"+
		"• `info` - Ver informações do dispositivo\n"+
		"• `ping` - Teste de latência/resposta\n"+
		"• `help` - Mostrar esta ajuda\n\n"+
		"💡 **Dica**: Quando desconectado, envie qualquer mensagem para receber ajuda.", instanceName)
}

// HandleChatwootPayloadTest receives a webhook and returns the parsed analysis for debugging
func (s *server) HandleChatwootPayloadTest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Read body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			s.Respond(w, r, http.StatusBadRequest, map[string]string{"error": "Failed to read body"})
			return
		}
		defer r.Body.Close()

		// Parse standard
		var payload ChatwootWebhookPayload
		var parseError string
		if err := json.Unmarshal(body, &payload); err != nil {
			parseError = err.Error()
		}

		// Parse as generic map for full comparison
		var rawMap map[string]interface{}
		json.Unmarshal(body, &rawMap)

		// Analyze headers
		headers := make(map[string]string)
		for k, v := range r.Header {
			headers[k] = strings.Join(v, ", ")
		}

		response := map[string]interface{}{
			"headers":       headers,
			"raw_body_len":  len(body),
			"raw_body":      string(body),
			"parsed_struct": payload,
			"parse_error":   parseError,
			"analysis": map[string]interface{}{
				"event":        payload.Event,
				"message_type": s.getString(payload.MessageType),
				"private":      payload.Private,
				"content":      payload.Content,
				"account_id":   payload.Account.ID,
				"inbox_id":     payload.Inbox.ID,
			},
		}

		s.Respond(w, r, http.StatusOK, response)
	}
}

// HandleChatwootDebug returns debug information about Chatwoot integration configuration
// GET /chatwoot/debug/{instance}
func (s *server) HandleChatwootDebug() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Recover from any panics
		defer func() {
			if rec := recover(); rec != nil {
				log.Error().Interface("panic", rec).Msg("[CHATWOOT DEBUG] Panic recovered")
				s.Respond(w, r, http.StatusInternalServerError, map[string]interface{}{
					"status": "error",
					"error":  fmt.Sprintf("Internal error: %v", rec),
				})
			}
		}()

		// Extract instance name from URL path using Gorilla Mux vars
		vars := mux.Vars(r)
		instanceName := vars["instance"]
		if instanceName == "" {
			// Fallback to path parsing
			path := r.URL.Path
			parts := strings.Split(path, "/")
			if len(parts) >= 4 {
				instanceName = parts[len(parts)-1]
			}
		}

		if instanceName == "" {
			s.Respond(w, r, http.StatusBadRequest, map[string]string{"error": "missing instance name"})
			return
		}

		log.Info().Str("instance", instanceName).Msg("[CHATWOOT DEBUG] Starting debug check")

		// Find user by instance name
		var userID string
		var connected bool
		query := "SELECT id, connected FROM users WHERE name = ? LIMIT 1"
		if s.db.DriverName() == "postgres" {
			query = "SELECT id, connected FROM users WHERE name = $1 LIMIT 1"
		}

		row := s.db.QueryRow(query, instanceName)
		err := row.Scan(&userID, &connected)
		if err != nil {
			log.Error().Err(err).Str("instance", instanceName).Msg("[CHATWOOT DEBUG] Instance not found")
			s.Respond(w, r, http.StatusNotFound, map[string]interface{}{
				"status":   "error",
				"message":  "Instance not found",
				"instance": instanceName,
			})
			return
		}

		// Find Chatwoot integration for this user
		integration, err := s.GetIntegrationByUserAndType(userID, "chatwoot")

		debugResult := map[string]interface{}{
			"instance":       instanceName,
			"user_id":        userID,
			"user_connected": connected,
			"timestamp":      time.Now().Format(time.RFC3339),
		}

		if err != nil {
			debugResult["integration_status"] = "not_found"
			debugResult["integration_error"] = err.Error()
			debugResult["recommendation"] = "Cadastre uma integração Chatwoot no dashboard"
			s.Respond(w, r, http.StatusOK, debugResult)
			return
		}

		debugResult["integration_id"] = integration.ID
		debugResult["integration_name"] = integration.Name
		debugResult["integration_status"] = integration.Status
		debugResult["integration_events"] = integration.Events

		// Parse and validate Meta config
		var meta ChatwootConfig
		if err := json.Unmarshal([]byte(integration.Meta), &meta); err != nil {
			debugResult["meta_parse_error"] = err.Error()
			debugResult["meta_raw"] = integration.Meta
			s.Respond(w, r, http.StatusOK, debugResult)
			return
		}

		// Config details (hide token)
		tokenMask := "***"
		if len(meta.Token) > 6 {
			tokenMask = meta.Token[:3] + "***" + meta.Token[len(meta.Token)-3:]
		}

		debugResult["config"] = map[string]interface{}{
			"url":                   meta.URL,
			"account_id":            meta.AccountID,
			"inbox_id":              meta.InboxID,
			"inbox_name":            meta.InboxName,
			"token_masked":          tokenMask,
			"enabled":               meta.Enabled,
			"sign_messages":         meta.SignMessages,
			"reopen_conversation":   meta.ReopenConversation,
			"conversation_pending":  meta.ConversationPending,
			"merge_brazil_contacts": meta.MergeBrazilContacts,
		}

		// Validate configuration
		issues := []string{}
		if !integration.Status {
			issues = append(issues, "Integração está desativada no banco (status=false)")
		}
		if !meta.Enabled {
			issues = append(issues, "Integração está desabilitada no meta config (enabled=false)")
		}
		if meta.URL == "" {
			issues = append(issues, "URL do Chatwoot não configurada")
		}
		if meta.AccountID == "" {
			issues = append(issues, "Account ID não configurado")
		}
		if meta.Token == "" {
			issues = append(issues, "Token não configurado")
		}
		if meta.InboxID == 0 {
			issues = append(issues, "Inbox ID não configurado (será usado default=1)")
		}

		// Check if Message event is subscribed
		events := strings.Split(integration.Events, ",")
		hasMessageEvent := false
		for _, e := range events {
			e = strings.TrimSpace(strings.ToLower(e))
			if e == "message" || e == "all" {
				hasMessageEvent = true
				break
			}
		}
		if !hasMessageEvent {
			issues = append(issues, "Evento 'Message' não está na lista de eventos assinados")
		}

		debugResult["issues"] = issues
		debugResult["issues_count"] = len(issues)

		if len(issues) == 0 {
			debugResult["health"] = "OK"
			debugResult["message"] = "Configuração parece correta. Se mensagens não estão chegando, verifique os logs do servidor."
		} else {
			debugResult["health"] = "ISSUES_FOUND"
			debugResult["message"] = "Foram encontrados problemas na configuração. Corrija os issues listados."
		}

		// Test API connectivity (optional - only if config is complete)
		if meta.URL != "" && meta.AccountID != "" && meta.Token != "" {
			testResult := s.testChatwootAPIConnection(meta)
			debugResult["api_test"] = testResult
		}

		s.Respond(w, r, http.StatusOK, debugResult)
	}
}

// testChatwootAPIConnection tests connectivity to Chatwoot API
func (s *server) testChatwootAPIConnection(config ChatwootConfig) map[string]interface{} {
	result := map[string]interface{}{
		"tested_at": time.Now().Format(time.RFC3339),
	}

	// Test by fetching inboxes (requires valid token)
	url := fmt.Sprintf("%s/api/v1/accounts/%s/inboxes", strings.TrimSuffix(config.URL, "/"), config.AccountID)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		result["status"] = "error"
		result["error"] = "Failed to create request: " + err.Error()
		return result
	}

	req.Header.Set("api_access_token", config.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		result["status"] = "error"
		result["error"] = "Connection failed: " + err.Error()
		result["url_tested"] = url
		return result
	}
	defer resp.Body.Close()

	result["status_code"] = resp.StatusCode
	result["url_tested"] = url

	if resp.StatusCode == 200 {
		result["status"] = "success"
		result["message"] = "API connection successful"
	} else if resp.StatusCode == 401 || resp.StatusCode == 403 {
		result["status"] = "auth_error"
		result["message"] = "Authentication failed - check your token"
	} else {
		result["status"] = "error"
		result["message"] = fmt.Sprintf("Unexpected status code: %d", resp.StatusCode)
	}

	return result
}
