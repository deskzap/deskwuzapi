package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"mime"
	"path/filepath"

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
	MessageType string `json:"message_type"`
	Content     string `json:"content"`
	Private     bool   `json:"private"`
	Status      string `json:"status"`
	Attachments []struct {
		FileType string `json:"file_type"`
		DataURL  string `json:"data_url"`
	} `json:"attachments"`
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
			Str("message_type", payload.MessageType).
			Bool("private", payload.Private).
			Str("content", payload.Content).
			Msg("Chatwoot webhook received")

		// Only process outgoing messages that are not private
		if payload.Event != "message_created" || payload.MessageType != "outgoing" || payload.Private {
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
		// Priority: Identifier (JID) -> PhoneNumber -> SourceID
		phone := ""

		// First, try to use Identifier which should contain the JID
		identifier := payload.Conversation.Meta.Contact.Identifier
		if identifier != "" && strings.Contains(identifier, "@") {
			// It's a JID, extract the phone number
			phone = extractPhoneNumber(identifier)
		}

		// Fallback to phone number
		if phone == "" {
			phone = payload.Conversation.Meta.Contact.PhoneNumber
		}

		// Last resort: SourceID
		if phone == "" {
			sourceID := payload.Conversation.ContactInbox.SourceID
			if strings.Contains(sourceID, "@") {
				phone = extractPhoneNumber(sourceID)
			} else {
				phone = sourceID
			}
		}

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
			Str("message_type", payload.MessageType).
			Bool("private", payload.Private).
			Str("content", payload.Content).
			Msg("Chatwoot webhook payload")

		// Only process outgoing messages that are not private
		if !strings.EqualFold(payload.Event, "message_created") || !strings.EqualFold(payload.MessageType, "outgoing") || payload.Private {
			s.Respond(w, r, http.StatusOK, "ignored")
			return
		}

		// Find user by instance name
		var userID string
		var qrCode string
		var connected bool
		row := s.db.QueryRow("SELECT id, qrcode, connected FROM users WHERE name = ? LIMIT 1", instanceName)
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

	// First, try to use Identifier which should contain the JID
	identifier := payload.Conversation.Meta.Contact.Identifier
	if identifier != "" && strings.Contains(identifier, "@") {
		phone = extractPhoneNumber(identifier)
	}

	// Fallback to phone number
	if phone == "" {
		phone = payload.Conversation.Meta.Contact.PhoneNumber
	}

	// Last resort: SourceID
	if phone == "" {
		sourceID := payload.Conversation.ContactInbox.SourceID
		if strings.Contains(sourceID, "@") {
			phone = extractPhoneNumber(sourceID)
		} else {
			phone = sourceID
		}
	}

	// Clean phone number
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
	validCommands := []string{"status", "connect", "qr", "disconnect", "help", "desconectar", "conectar", "ajuda"}
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

// handleHelpCommand returns the help message
func (s *server) handleHelpCommand(instanceName string) string {
	return fmt.Sprintf("📋 **Comandos disponíveis - Instância '%s'**\n\n"+
		"• `status` - Verificar status da conexão\n"+
		"• `connect` - Iniciar conexão e gerar QR Code\n"+
		"• `qr` - Gerar novo QR Code\n"+
		"• `disconnect` - Desconectar sessão (mantém login)\n"+
		"• `help` - Mostrar esta ajuda\n\n"+
		"💡 **Dica**: Quando desconectado, envie qualquer mensagem para receber ajuda.", instanceName)
}
