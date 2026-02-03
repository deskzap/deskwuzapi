package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/rs/zerolog/log"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
)

type ChatwootConfig struct {
	URL                 string `json:"url"`
	AccountID           string `json:"account_id"`
	Token               string `json:"token"`
	InboxName           string `json:"inbox_name"`
	InboxID             int    `json:"inbox_id"`
	SignMessages        bool   `json:"sign_messages"`
	SignDelimiter       string `json:"sign_delimiter"`
	AutoCreate          bool   `json:"auto_create"`
	Organization        string `json:"organization"`
	Logo                string `json:"logo"`
	ConversationPending bool   `json:"conversation_pending"`
	ReopenConversation  bool   `json:"reopen_conversation"`
	ImportContacts      bool   `json:"import_contacts"`
	ImportMessages      bool   `json:"import_messages"`
	ImportDays          int    `json:"import_days"`
	MergeBrazilContacts bool   `json:"merge_brazil_contacts"`
	IgnoreJids          string `json:"ignore_jids"`
	Enabled             bool   `json:"enabled"`
}

// normalizeBrazilPhone normalizes Brazilian phone numbers
// Handles the 9th digit issue: some numbers have it, some don't
func normalizeBrazilPhone(phone string) string {
	// Remove any non-digit characters
	digits := ""
	for _, c := range phone {
		if c >= '0' && c <= '9' {
			digits += string(c)
		}
	}

	// Check if it's a Brazilian number (starts with 55)
	if len(digits) >= 12 && digits[:2] == "55" {
		ddd := digits[2:4]
		rest := digits[4:]

		// Mobile DDDs that use 9th digit: 11-28, 91-99
		dddNum := 0
		if len(ddd) == 2 {
			dddNum = int(ddd[0]-'0')*10 + int(ddd[1]-'0')
		}

		isMobileDDD := (dddNum >= 11 && dddNum <= 28) || (dddNum >= 91 && dddNum <= 99)

		if isMobileDDD {
			// If number has 9 digits after DDD, it already has the 9th digit
			// If it has 8 digits, we might need to add the 9
			if len(rest) == 8 && rest[0] >= '6' && rest[0] <= '9' {
				// Likely a mobile number missing the 9th digit
				return "55" + ddd + "9" + rest
			}
		}
	}

	return digits
}

// shouldIgnoreJid checks if a JID should be ignored based on the ignore list
func shouldIgnoreJid(jid string, ignoreList string) bool {
	if ignoreList == "" {
		return false
	}

	// Split by comma or newline
	jids := []string{}
	current := ""
	for _, c := range ignoreList {
		if c == ',' || c == '\n' || c == '\r' {
			if current != "" {
				jids = append(jids, current)
				current = ""
			}
		} else if c != ' ' {
			current += string(c)
		}
	}
	if current != "" {
		jids = append(jids, current)
	}

	for _, ignored := range jids {
		if ignored == jid {
			return true
		}
	}
	return false
}

// signMessage adds the sender signature to the message content
func signMessage(content, senderName, delimiter string) string {
	if senderName == "" {
		return content
	}
	if delimiter == "" {
		delimiter = "\n"
	}
	// Handle escaped newline
	if delimiter == "\\n" {
		delimiter = "\n"
	}
	return fmt.Sprintf("*%s*%s%s", senderName, delimiter, content)
}

// ChatwootPayload matches the structure needed for Chatwoot API calls
type ChatwootMessage struct {
	Content           string                 `json:"content"`
	MessageType       string                 `json:"message_type"` // incoming/outgoing
	Private           bool                   `json:"private"`
	ContentType       string                 `json:"content_type"` // text/input_select/etc
	ContentAttributes map[string]interface{} `json:"content_attributes,omitempty"`
}

type ChatwootContact struct {
	Name        string `json:"name"`
	Email       string `json:"email,omitempty"`
	PhoneNumber string `json:"phone_number"`
	Identifier  string `json:"identifier,omitempty"`
}

type ChatwootConversation struct {
	SourceID             string                 `json:"source_id"`
	ContactID            int                    `json:"contact_id,omitempty"`
	InboxID              int                    `json:"inbox_id"`
	Status               string                 `json:"status,omitempty"`
	AdditionalAttributes map[string]interface{} `json:"additional_attributes,omitempty"`
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// HandleChatwootEvent routes incoming Wuzapi events to Chatwoot logic
func (s *server) HandleChatwootEvent(integration Integration, eventType string, payload map[string]interface{}) {
	log.Info().
		Str("integration", integration.Name).
		Int("integration_id", integration.ID).
		Str("event", eventType).
		Str("user_id", integration.UserID).
		Msg("[CHATWOOT] Processing event for integration")

	// Parse Meta config
	var meta ChatwootConfig
	if err := json.Unmarshal([]byte(integration.Meta), &meta); err != nil {
		log.Error().Err(err).Str("meta", integration.Meta).Msg("[CHATWOOT] Failed to parse meta config")
		return
	}

	log.Debug().
		Str("url", meta.URL).
		Str("account_id", meta.AccountID).
		Int("inbox_id", meta.InboxID).
		Bool("enabled", meta.Enabled).
		Msg("[CHATWOOT] Parsed config")

	// Check if integration is enabled
	if !meta.Enabled {
		log.Warn().Str("integration", integration.Name).Msg("[CHATWOOT] Integration is disabled in meta config")
		return
	}

	// Basic validation
	if meta.URL == "" || meta.AccountID == "" || meta.Token == "" {
		log.Error().
			Bool("has_url", meta.URL != "").
			Bool("has_account_id", meta.AccountID != "").
			Bool("has_token", meta.Token != "").
			Msg("[CHATWOOT] Incomplete configuration - missing required fields")
		return
	}

	// Only handle Message events containing message info
	if !strings.EqualFold(eventType, "Message") {
		return
	}

	data, ok := payload["data"].(map[string]interface{})
	if !ok {
		return
	}

	// Extract basic message info
	pushName, _ := data["pushName"].(string)
	messageType, _ := data["messageType"].(string)

	var senderJID string
	// var messageID string
	var fromMe bool

	key, ok := data["key"].(map[string]interface{})
	if ok {
		// If remoteJid is present in key, use it
		if rj, ok := key["remoteJid"].(string); ok {
			senderJID = rj
		}
		// if mid, ok := key["id"].(string); ok {
		// 	messageID = mid
		// }
		if fm, ok := key["fromMe"].(bool); ok {
			fromMe = fm
		}
	}

	// Check if this JID should be ignored
	if shouldIgnoreJid(senderJID, meta.IgnoreJids) {
		log.Debug().Str("jid", senderJID).Msg("Ignoring message from blocked JID")
		return
	}

	// We typically only want to process incoming messages OR outgoing messages if Chatwoot should mirror them
	// For now, let's focus on INCOMING messages from users to be displayed in Chatwoot
	if fromMe {
		return
	}

	senderPhone := extractPhoneNumber(senderJID)
	if senderPhone == "" {
		return
	}

	// Apply Brazil phone normalization if enabled
	if meta.MergeBrazilContacts {
		senderPhone = normalizeBrazilPhone(senderPhone)
	}

	if pushName == "" {
		pushName = senderPhone
	}

	// 1. Find or Create Contact
	contactID, err := s.findOrCreateChatwootContact(meta, ChatwootContact{
		Name:        pushName,
		PhoneNumber: "+" + senderPhone, // Chatwoot often expects +E.164
		Identifier:  senderJID,         // Store original JID as identifier
	})
	if err != nil {
		log.Error().Err(err).Msg("Failed to manage Chatwoot contact")
		return
	}

	// 2. Find or Create Conversation
	// Priority: Meta InboxID -> Default 1
	inboxID := 1
	if meta.InboxID > 0 {
		inboxID = meta.InboxID
	}

	// Determine conversation status based on config
	conversationStatus := "open"
	if meta.ConversationPending {
		conversationStatus = "pending"
	}

	conversationID, err := s.findOrCreateChatwootConversation(meta, ChatwootConversation{
		SourceID:  senderJID, // Use JID as unique source for conversation
		ContactID: contactID,
		InboxID:   inboxID,
		Status:    conversationStatus,
	})
	if err != nil {
		log.Error().Err(err).Msg("Failed to manage Chatwoot conversation")
		return
	}

	// 3. Create Message
	// Need to extract content based on type (text, image, etc.)
	content := ""

	msgContent, ok := data["message"].(map[string]interface{})
	if ok {
		if txt, ok := msgContent["conversation"].(string); ok {
			content = txt
		} else if ext, ok := msgContent["extendedTextMessage"].(map[string]interface{}); ok {
			if txt, ok := ext["text"].(string); ok {
				content = txt
			}
		} else if _, ok := msgContent["imageMessage"].(map[string]interface{}); ok {
			// Handle Image
			err = s.processAndSendMediaToChatwoot(meta, conversationID, data, "image", "imageMessage", integration.UserID)
			if err != nil {
				log.Error().Err(err).Msg("Failed to send image to Chatwoot")
				content = "📷 [Erro ao enviar imagem]"
			} else {
				return // Sent as media
			}
		} else if _, ok := msgContent["videoMessage"].(map[string]interface{}); ok {
			// Handle Video
			err = s.processAndSendMediaToChatwoot(meta, conversationID, data, "video", "videoMessage", integration.UserID)
			if err != nil {
				log.Error().Err(err).Msg("Failed to send video to Chatwoot")
				content = "🎥 [Erro ao enviar vídeo]"
			} else {
				return
			}
		} else if _, ok := msgContent["audioMessage"].(map[string]interface{}); ok {
			// Handle Audio
			err = s.processAndSendMediaToChatwoot(meta, conversationID, data, "audio", "audioMessage", integration.UserID)
			if err != nil {
				log.Error().Err(err).Msg("Failed to send audio to Chatwoot")
				content = "🎵 [Erro ao enviar áudio]"
			} else {
				return
			}
		} else if _, ok := msgContent["documentMessage"].(map[string]interface{}); ok {
			// Handle Document
			err = s.processAndSendMediaToChatwoot(meta, conversationID, data, "file", "documentMessage", integration.UserID)
			if err != nil {
				log.Error().Err(err).Msg("Failed to send document to Chatwoot")
				content = "📄 [Erro ao enviar documento]"
			} else {
				return
			}
		} else if _, ok := msgContent["stickerMessage"].(map[string]interface{}); ok {
			// Handle Sticker
			err = s.processAndSendMediaToChatwoot(meta, conversationID, data, "image", "stickerMessage", integration.UserID)
			if err != nil {
				content = "🎨 [Sticker]"
			} else {
				return
			}
		} else if loc, ok := msgContent["locationMessage"].(map[string]interface{}); ok {
			lat, _ := loc["degreesLatitude"].(float64)
			lng, _ := loc["degreesLongitude"].(float64)
			content = fmt.Sprintf("📍 [Localização: %.6f, %.6f]", lat, lng)
		} else if contact, ok := msgContent["contactMessage"].(map[string]interface{}); ok {
			displayName, _ := contact["displayName"].(string)
			content = fmt.Sprintf("👤 [Contato: %s]", displayName)
		}
	}

	if content == "" {
		content = "[Tipo de mensagem não suportado: " + messageType + "]"
	}

	err = s.createChatwootMessage(meta, conversationID, ChatwootMessage{
		Content:     content,
		MessageType: "incoming",
		Private:     false,
	})
	if err != nil {
		log.Error().Err(err).Msg("Failed to create Chatwoot message")
	}
}

func (s *server) findOrCreateChatwootContact(config ChatwootConfig, contact ChatwootContact) (int, error) {
	// Replace all new Resty clients with one that has the header
	client := resty.New()
	client.SetHeader("User-Agent", "Mozilla/5.0 (Compatible; Wuzapi/1.0)")

	// 1. Search Contact first to avoid duplicates
	// Prefer searching by Identifier (JID) as it is unique and avoids formatting issues
	query := contact.Identifier
	if query == "" {
		query = contact.PhoneNumber
	}

	resp, err := client.R().
		SetHeader("api_access_token", config.Token).
		SetQueryParam("q", query).
		Get(fmt.Sprintf("%s/api/v1/accounts/%s/contacts/search", config.URL, config.AccountID))

	if err != nil {
		log.Warn().Err(err).Str("phone", contact.PhoneNumber).Msg("Chatwoot contact search failed, proceeding to create")
	} else {
		// Parse search result
		var searchResult struct {
			Payload []struct {
				ID int `json:"id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(resp.Body(), &searchResult); err == nil {
			if len(searchResult.Payload) > 0 {
				log.Debug().Int("id", searchResult.Payload[0].ID).Str("phone", contact.PhoneNumber).Msg("Chatwoot contact found")
				return searchResult.Payload[0].ID, nil
			}
		} else {
			log.Warn().Err(err).Msg("Failed to parse Chatwoot search response")
		}
	}

	// 2. Create if not found
	resp, err = client.R().
		SetHeader("api_access_token", config.Token).
		SetBody(contact).
		Post(fmt.Sprintf("%s/api/v1/accounts/%s/contacts", config.URL, config.AccountID))

	if err != nil {
		return 0, err
	}

	// Parse ID from response
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return 0, err
	}

	// Check payload for id for 'payload' -> 'contact' -> 'id' or just 'payload' -> 'id'?
	// Chatwoot response format varies.
	// Standard: { payload: { contact: { id: 1, ... } } }
	if payload, ok := result["payload"].(map[string]interface{}); ok {
		if c, ok := payload["contact"].(map[string]interface{}); ok {
			if id, ok := c["id"].(float64); ok {
				return int(id), nil
			}
		}
	}

	return 0, fmt.Errorf("could not parse contact ID from Chatwoot response")
}

func (s *server) findOrCreateChatwootConversation(config ChatwootConfig, conv ChatwootConversation) (int, error) {
	// Simple CREATE attempt. Chatwoot creates a new one or returns existing open one often?
	// Actually Chatwoot API creates a new conversation every time unless we find one.
	// Correct flow: Search conversations by contact_id. If open, use it. Else create.

	// Replace all new Resty clients with one that has the header
	client := resty.New()
	client.SetHeader("User-Agent", "Mozilla/5.0 (Compatible; Wuzapi/1.0)")

	resp, err := client.R().
		SetHeader("api_access_token", config.Token).
		SetQueryParam("status", "open").
		Get(fmt.Sprintf("%s/api/v1/accounts/%s/contacts/%d/conversations", config.URL, config.AccountID, conv.ContactID))

	if err != nil {
		return 0, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &result); err == nil {
		if payload, ok := result["payload"].([]interface{}); ok {
			// Iterate to find one in the correct Inbox
			for _, item := range payload {
				if convMap, ok := item.(map[string]interface{}); ok {
					// Check if status is open
					if status, ok := convMap["status"].(string); ok && status == "open" {
						// Check inbox_id
						if iID, ok := convMap["inbox_id"].(float64); ok && int(iID) == conv.InboxID {
							if id, ok := convMap["id"].(float64); ok {
								return int(id), nil
							}
						}
					}
				}
			}
		}
	}

	// Create new
	resp, err = client.R().
		SetHeader("api_access_token", config.Token).
		SetBody(map[string]interface{}{
			"inbox_id":   conv.InboxID,
			"contact_id": conv.ContactID,
			"status":     "open",
		}).
		Post(fmt.Sprintf("%s/api/v1/accounts/%s/conversations", config.URL, config.AccountID))

	if err != nil {
		return 0, err
	}

	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return 0, err
	}

	// Response: { id: 1, ... } direct fields usually? Or inside payload? Matches List?
	// Create returns the conversation object directly usually.
	if id, ok := result["id"].(float64); ok {
		return int(id), nil
	}

	return 0, fmt.Errorf("could not parse conversation ID")
}

func (s *server) createChatwootMessage(config ChatwootConfig, conversationID int, msg ChatwootMessage) error {
	// Replace all new Resty clients with one that has the header
	client := resty.New()
	client.SetHeader("User-Agent", "Mozilla/5.0 (Compatible; Wuzapi/1.0)")
	// Add retry for robustness
	client.SetRetryCount(3).SetRetryWaitTime(1 * time.Second)

	url := fmt.Sprintf("%s/api/v1/accounts/%s/conversations/%d/messages", config.URL, config.AccountID, conversationID)

	log.Info().
		Str("url", url).
		Int("conversation_id", conversationID).
		Str("content_preview", truncateString(msg.Content, 50)).
		Msg("[CHATWOOT] Sending message to Chatwoot")

	resp, err := client.R().
		SetHeader("api_access_token", config.Token).
		SetBody(msg).
		Post(url)

	if err != nil {
		log.Error().Err(err).Str("url", url).Msg("[CHATWOOT] Failed to send message - network error")
		return err
	}

	if resp.StatusCode() >= 400 {
		log.Error().
			Int("status_code", resp.StatusCode()).
			Str("response", string(resp.Body())).
			Str("url", url).
			Msg("[CHATWOOT] Failed to send message - API error")
		return fmt.Errorf("chatwoot API error: status %d, body: %s", resp.StatusCode(), string(resp.Body()))
	}

	log.Info().
		Int("status_code", resp.StatusCode()).
		Int("conversation_id", conversationID).
		Msg("[CHATWOOT] Message sent successfully")

	return nil
}

// CreateChatwootInbox creates a new API inbox in Chatwoot
func (s *server) CreateChatwootInbox(config ChatwootConfig, name, webhookURL string) (int, error) {
	// Replace all new Resty clients with one that has the header
	client := resty.New()
	client.SetHeader("User-Agent", "Mozilla/5.0 (Compatible; Wuzapi/1.0)")

	// Payload for creating an API inbox
	// POST /api/v1/accounts/{account_id}/inboxes
	payload := map[string]interface{}{
		"name": name,
		"channel": map[string]interface{}{
			"type":        "api",
			"webhook_url": webhookURL,
		},
	}

	apiURL := fmt.Sprintf("%s/api/v1/accounts/%s/inboxes", config.URL, config.AccountID)

	log.Info().
		Str("url", apiURL).
		Str("name", name).
		Str("webhook_url", webhookURL).
		Msg("Creating Chatwoot inbox")

	resp, err := client.R().
		SetHeader("api_access_token", config.Token).
		SetHeader("Content-Type", "application/json").
		SetBody(payload).
		Post(apiURL)

	if err != nil {
		log.Error().Err(err).Str("url", apiURL).Msg("Failed to connect to Chatwoot API")
		return 0, err
	}

	log.Info().
		Int("status", resp.StatusCode()).
		Str("body", string(resp.Body())).
		Msg("Chatwoot inbox creation response")

	if resp.StatusCode() >= 300 {
		return 0, fmt.Errorf("failed to create inbox. Status: %d, Body: %s", resp.StatusCode(), string(resp.Body()))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return 0, err
	}

	// Response: { id: 1, ... } or { payload: { ... } }?
	// Based on docs usually just the inbox object or payload with it.
	// Let's check for 'id' at top level
	if id, ok := result["id"].(float64); ok {
		log.Info().Int("inbox_id", int(id)).Msg("Chatwoot inbox created successfully")
		return int(id), nil
	}

	return 0, fmt.Errorf("could not parse inbox ID from response: %s", string(resp.Body()))
}

func extractPhoneNumber(jid string) string {
	if len(jid) > 1 && jid[0] == '+' {
		jid = jid[1:]
	}
	if idx := len(jid) - len("@s.whatsapp.net"); idx > 0 && jid[idx:] == "@s.whatsapp.net" {
		return jid[:idx]
	}
	return ""
}

// CreateChatwootManagerContact creates the manager contact (+123456) in Chatwoot
// This contact is used to manage the instance connection via commands
func (s *server) CreateChatwootManagerContact(config ChatwootConfig, instanceName string) (int, int, error) {
	log.Info().Str("instance", instanceName).Msg("Creating Chatwoot manager contact")

	apiURL := strings.TrimSuffix(config.URL, "/")

	// Step 1: Create the contact
	contactPayload := map[string]interface{}{
		"inbox_id":     config.InboxID,
		"name":         fmt.Sprintf("🔧 %s", instanceName),
		"phone_number": "+123456",
		"identifier":   fmt.Sprintf("manager@deskwuzapi.%s", instanceName),
		"custom_attributes": map[string]interface{}{
			"type":     "manager",
			"instance": instanceName,
		},
	}

	// Replace all new Resty clients with one that has the header
	client := resty.New()
	client.SetHeader("User-Agent", "Mozilla/5.0 (Compatible; Wuzapi/1.0)")

	contactResp, err := client.R().
		SetHeader("api_access_token", config.Token).
		SetHeader("Content-Type", "application/json").
		SetBody(contactPayload).
		Post(fmt.Sprintf("%s/api/v1/accounts/%s/contacts", apiURL, config.AccountID))

	if err != nil {
		return 0, 0, fmt.Errorf("failed to create manager contact: %w", err)
	}

	log.Debug().Int("status", contactResp.StatusCode()).Str("body", string(contactResp.Body())).Msg("Manager contact creation response")

	if contactResp.StatusCode() >= 400 {
		// Contact might already exist, try to find it
		log.Warn().Int("status", contactResp.StatusCode()).Msg("Contact creation failed, might already exist")
		return 0, 0, fmt.Errorf("failed to create contact: status %d", contactResp.StatusCode())
	}

	var contactResult map[string]interface{}
	if err := json.Unmarshal(contactResp.Body(), &contactResult); err != nil {
		return 0, 0, err
	}

	// Extract contact ID from payload.contact.id
	contactID := 0
	if payload, ok := contactResult["payload"].(map[string]interface{}); ok {
		if contact, ok := payload["contact"].(map[string]interface{}); ok {
			if id, ok := contact["id"].(float64); ok {
				contactID = int(id)
			}
		}
	} else if id, ok := contactResult["id"].(float64); ok {
		contactID = int(id)
	}

	if contactID == 0 {
		return 0, 0, fmt.Errorf("could not parse contact ID from response")
	}

	// Step 2: Create a conversation with the manager contact
	convPayload := map[string]interface{}{
		"inbox_id":   config.InboxID,
		"contact_id": contactID,
		"source_id":  fmt.Sprintf("manager@deskwuzapi.%s", instanceName),
		"status":     "open",
	}

	convResp, err := client.R().
		SetHeader("api_access_token", config.Token).
		SetHeader("Content-Type", "application/json").
		SetBody(convPayload).
		Post(fmt.Sprintf("%s/api/v1/accounts/%s/conversations", apiURL, config.AccountID))

	if err != nil {
		return contactID, 0, fmt.Errorf("failed to create manager conversation: %w", err)
	}

	log.Debug().Int("status", convResp.StatusCode()).Str("body", string(convResp.Body())).Msg("Manager conversation creation response")

	conversationID := 0
	if convResp.StatusCode() < 400 {
		var convResult map[string]interface{}
		if err := json.Unmarshal(convResp.Body(), &convResult); err == nil {
			if id, ok := convResult["id"].(float64); ok {
				conversationID = int(id)
			}
		}
	}

	// Step 3: Send welcome message
	if conversationID > 0 {
		welcomeMsg := fmt.Sprintf("🔧 **Gerenciador de Conexão - %s**\n\n"+
			"Use os comandos abaixo para gerenciar a conexão desta instância:\n\n"+
			"📋 **Comandos disponíveis:**\n"+
			"• `status` - Verificar status da conexão\n"+
			"• `connect` - Iniciar conexão e gerar QR Code\n"+
			"• `qr` - Gerar novo QR Code\n"+
			"• `disconnect` - Desconectar sessão\n"+
			"• `help` - Mostrar ajuda\n\n"+
			"⚠️ **Importante**: Apenas este contato pode enviar comandos de gerenciamento.", instanceName)

		msgPayload := map[string]interface{}{
			"content":      welcomeMsg,
			"message_type": "outgoing",
			"private":      false,
		}

		client.R().
			SetHeader("api_access_token", config.Token).
			SetHeader("Content-Type", "application/json").
			SetBody(msgPayload).
			Post(fmt.Sprintf("%s/api/v1/accounts/%s/conversations/%d/messages", apiURL, config.AccountID, conversationID))
	}

	log.Info().Int("contact_id", contactID).Int("conversation_id", conversationID).Str("instance", instanceName).Msg("Manager contact created successfully")
	return contactID, conversationID, nil
}

// processAndSendMediaToChatwoot downloads media from message and sends to Chatwoot
func (s *server) processAndSendMediaToChatwoot(config ChatwootConfig, conversationID int, data map[string]interface{}, fileType, msgType, userID string) error {
	client := clientManager.GetWhatsmeowClient(userID)
	if client == nil {
		return fmt.Errorf("client not found for user %s", userID)
	}

	// 1. Reconstruct Message object
	msgBytes, err := json.Marshal(data["message"])
	if err != nil {
		return fmt.Errorf("failed to marshal message data: %w", err)
	}

	var msg waE2E.Message
	if err := protojson.Unmarshal(msgBytes, &msg); err != nil {
		// Fallback: try standard unmarshal if keys match standard JSON
		if err2 := json.Unmarshal(msgBytes, &msg); err2 != nil {
			return fmt.Errorf("failed to unmarshal message: %v", err)
		}
	}

	// 2. Download Media
	var mediaData []byte
	var errDownload error

	switch msgType {
	case "imageMessage":
		if msg.ImageMessage != nil {
			mediaData, errDownload = client.Download(context.Background(), msg.ImageMessage)
		}
	case "videoMessage":
		if msg.VideoMessage != nil {
			mediaData, errDownload = client.Download(context.Background(), msg.VideoMessage)
		}
	case "audioMessage":
		if msg.AudioMessage != nil {
			mediaData, errDownload = client.Download(context.Background(), msg.AudioMessage)
		}
	case "documentMessage":
		if msg.DocumentMessage != nil {
			mediaData, errDownload = client.Download(context.Background(), msg.DocumentMessage)
		}
	case "stickerMessage":
		if msg.StickerMessage != nil {
			mediaData, errDownload = client.Download(context.Background(), msg.StickerMessage)
		}
	default:
		return fmt.Errorf("unsupported media type: %s", msgType)
	}

	if errDownload != nil {
		// If download fails (e.g. timeout), try to fallback to sending text
		return fmt.Errorf("failed to download media: %w", errDownload)
	}

	if len(mediaData) == 0 {
		return fmt.Errorf("empty media data downloaded")
	}

	// 3. Determine Filename and Mime
	// mimeType := http.DetectContentType(mediaData)
	fileName := "file"
	caption := ""

	switch msgType {
	case "imageMessage":
		fileName = "image.jpg"
		// if msg.ImageMessage.Mimetype != nil {
		// 	mimeType = *msg.ImageMessage.Mimetype
		// }
		if msg.ImageMessage.Caption != nil {
			caption = *msg.ImageMessage.Caption
		}
	case "videoMessage":
		fileName = "video.mp4"
		// if msg.VideoMessage.Mimetype != nil {
		// 	mimeType = *msg.VideoMessage.Mimetype
		// }
		if msg.VideoMessage.Caption != nil {
			caption = *msg.VideoMessage.Caption
		}
	case "audioMessage":
		fileName = "audio.ogg"
		// if msg.AudioMessage.Mimetype != nil {
		// 	mimeType = *msg.AudioMessage.Mimetype
		// }
	case "documentMessage":
		fileName = "document"
		if msg.DocumentMessage.FileName != nil && *msg.DocumentMessage.FileName != "" {
			fileName = *msg.DocumentMessage.FileName
		}
		// if msg.DocumentMessage.Mimetype != nil {
		// 	mimeType = *msg.DocumentMessage.Mimetype
		// }
		if msg.DocumentMessage.Caption != nil {
			caption = *msg.DocumentMessage.Caption
		}
	case "stickerMessage":
		fileName = "sticker.webp"
		// if msg.StickerMessage.Mimetype != nil {
		// 	mimeType = *msg.StickerMessage.Mimetype
		// }
	}

	// sanitize filename
	fileName = filepath.Base(fileName)
	if fileName == "." || fileName == "" {
		fileName = "file"
	}

	// 4. Send to Chatwoot
	restyClient := resty.New()
	// Set timeout
	// Set timeout and retry
	restyClient.SetTimeout(30 * time.Second)
	restyClient.SetRetryCount(3).SetRetryWaitTime(1 * time.Second)

	req := restyClient.R().
		SetHeader("api_access_token", config.Token).
		SetFileReader("attachments[]", fileName, bytes.NewReader(mediaData)).
		SetFormData(map[string]string{
			"content":      caption,
			"message_type": "incoming",
			"private":      "false",
		})

	resp, err := req.Post(fmt.Sprintf("%s/api/v1/accounts/%s/conversations/%d/messages", config.URL, config.AccountID, conversationID))

	if err != nil {
		return fmt.Errorf("failed to upload to Chatwoot: %w", err)
	}

	if resp.StatusCode() >= 400 {
		return fmt.Errorf("chatwoot API error: %d - %s", resp.StatusCode(), string(resp.Body()))
	}

	log.Debug().Str("type", msgType).Int("size", len(mediaData)).Msg("Media sent to Chatwoot successfully")
	return nil
}
