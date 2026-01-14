package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/rs/zerolog/log"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
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
			s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("invalid payload"))
			return
		}

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

		txtid := integration.UserID
		client := clientManager.GetWhatsmeowClient(txtid)
		if client == nil || !client.IsConnected() {
			log.Warn().Str("userid", txtid).Msg("Chatwoot webhook: client not connected")
			s.Respond(w, r, http.StatusServiceUnavailable, fmt.Errorf("client not connected"))
			return
		}

		// Determine Recipient
		// Prefer PhoneNumber from meta
		phone := payload.Conversation.Meta.Contact.PhoneNumber
		if phone == "" {
			// Fallback to identifier if it looks like a phone?
			if payload.Conversation.Meta.Contact.Identifier != "" {
				phone = payload.Conversation.Meta.Contact.Identifier
			} else {
				// Last resort, ContactInbox SourceID
				phone = payload.Conversation.ContactInbox.SourceID
			}
		}

		if phone == "" {
			log.Error().Msg("Chatwoot webhook: could not determine recipient phone")
			s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("recipient not found"))
			return
		}

		// Format JID
		// Assume phone is +1234... or 1234...
		// Strip +
		if len(phone) > 0 && phone[0] == '+' {
			phone = phone[1:]
		}
		// Basic sanity check
		if len(phone) < 5 {
			log.Error().Str("phone", phone).Msg("Chatwoot webhook: invalid phone")
			s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("invalid phone"))
			return
		}

		jid := types.NewJID(phone, types.DefaultUserServer)

		// Send Message
		// If has attachments, handle image? (Simplified for now: Text)
		// If attachments exist, we might need to download and send.
		// For MVP: Send Text + Link to attachment?

		if len(payload.Attachments) > 0 {
			// Send attachment as text link or handle Properly?
			// Let's send text first if exists
			if payload.Content != "" {
				s.sendTextMessage(client, jid, payload.Content)
			}
			for _, att := range payload.Attachments {
				// Send link
				s.sendTextMessage(client, jid, fmt.Sprintf("[Attachment]: %s", att.DataURL))
			}
		} else {
			if payload.Content != "" {
				s.sendTextMessage(client, jid, payload.Content)
			}
		}

		s.Respond(w, r, http.StatusOK, "sent")
	}
}

func (s *server) sendTextMessage(client *whatsmeow.Client, jid types.JID, content string) {
	msg := &waProto.Message{
		Conversation: proto.String(content),
	}
	_, err := client.SendMessage(context.Background(), jid, msg)
	if err != nil {
		log.Error().Err(err).Str("jid", jid.String()).Msg("Failed to send chatwoot reply")
	}
}
