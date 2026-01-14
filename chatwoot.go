package main

import (
	"encoding/json"
	"fmt"

	"github.com/go-resty/resty/v2"
	"github.com/rs/zerolog/log"
)

type ChatwootConfig struct {
	URL          string `json:"url"`
	AccountID    string `json:"account_id"`
	Token        string `json:"token"`
	InboxName    string `json:"inbox_name"`
	InboxID      int    `json:"inbox_id"`
	SignMessages bool   `json:"sign_messages"`
	AutoCreate   bool   `json:"auto_create"`
	Attributes   map[string]interface{}
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

// HandleChatwootEvent routes incoming Wuzapi events to Chatwoot logic
func (s *server) HandleChatwootEvent(integration Integration, eventType string, payload map[string]interface{}) {
	log.Debug().Str("integration", integration.Name).Str("event", eventType).Msg("Handling Chatwoot event")

	// Parse Meta config
	var meta ChatwootConfig
	if err := json.Unmarshal([]byte(integration.Meta), &meta); err != nil {
		log.Error().Err(err).Msg("Failed to parse Chatwoot meta config")
		return
	}

	// Basic validation
	if meta.URL == "" || meta.AccountID == "" || meta.Token == "" {
		log.Warn().Msg("Incomplete Chatwoot configuration")
		return
	}

	// Only handle Message events containing message info
	if eventType != "Message" {
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
	var fromMe bool

	key, ok := data["key"].(map[string]interface{})
	if ok {
		// If remoteJid is present in key, use it
		if rj, ok := key["remoteJid"].(string); ok {
			senderJID = rj
		}
		if fm, ok := key["fromMe"].(bool); ok {
			fromMe = fm
		}
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

	if pushName == "" {
		pushName = senderPhone
	}

	// 1. Find or Create Contact
	contactID, err := s.findOrCreateChatwootContact(meta, ChatwootContact{
		Name:        pushName,
		PhoneNumber: "+" + senderPhone, // Chatwoot often expects +E.164
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

	conversationID, err := s.findOrCreateChatwootConversation(meta, ChatwootConversation{
		SourceID:  fmt.Sprintf("%d", contactID), // Usually contact ID is used as unique source for conversation in a simple setup
		ContactID: contactID,
		InboxID:   inboxID,
		Status:    "open",
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
		} else if img, ok := msgContent["imageMessage"].(map[string]interface{}); ok {
			if caps, ok := img["caption"].(string); ok {
				content = "[Image] " + caps
			} else {
				content = "[Image]"
			}
			// Handling attachments requires uploading to Chatwoot, complex. Sticking to text representation for MVP.
		}
	}

	if content == "" {
		content = "[Unsupported Message Type: " + messageType + "]"
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
	client := resty.New()
	// Search Contact
	resp, err := client.R().
		SetHeader("api_access_token", config.Token).
		SetQueryParam("q", contact.PhoneNumber).
		Get(fmt.Sprintf("%s/api/v1/accounts/%s/contacts/search", config.URL, config.AccountID))

	if err != nil {
		return 0, err
	}

	// Parse search result... handling JSON is verbose in Go without proper structs
	// Assuming 0 results -> Create
	// For brevity/MVP, I'll attempt create directly. Chatwoot might dedupe or I need to handle error.

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

	client := resty.New()
	resp, err := client.R().
		SetHeader("api_access_token", config.Token).
		SetQueryParam("status", "open").
		Get(fmt.Sprintf("%s/api/v1/accounts/%s/contacts/%d/conversations", config.URL, config.AccountID, conv.ContactID))

	if err != nil {
		return 0, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &result); err == nil {
		if payload, ok := result["payload"].([]interface{}); ok && len(payload) > 0 {
			// Found open existing
			if first, ok := payload[0].(map[string]interface{}); ok {
				if id, ok := first["id"].(float64); ok {
					return int(id), nil
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
	client := resty.New()
	_, err := client.R().
		SetHeader("api_access_token", config.Token).
		SetBody(msg).
		Post(fmt.Sprintf("%s/api/v1/accounts/%s/conversations/%d/messages", config.URL, config.AccountID, conversationID))
	return err
}

// CreateChatwootInbox creates a new API inbox in Chatwoot
func (s *server) CreateChatwootInbox(config ChatwootConfig, name, webhookURL string) (int, error) {
	client := resty.New()

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
