package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog/log"
)

// ListIntegrations returns all integrations for the authenticated user
func (s *server) ListIntegrations() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		txtid := r.Context().Value("userinfo").(Values).Get("Id")

		integrations, err := s.GetIntegrations(txtid)
		if err != nil {
			s.Respond(w, r, http.StatusInternalServerError, err)
			return
		}

		response := map[string]interface{}{"integrations": integrations}
		log.Info().Int("count", len(integrations)).Str("userID", txtid).Msg("ListIntegrations: Found items")
		responseJson, err := json.Marshal(response)
		if err != nil {
			log.Error().Err(err).Msg("ListIntegrations: JSON marshal failed")
			s.Respond(w, r, http.StatusInternalServerError, err)
			return
		}
		log.Info().Str("json", string(responseJson)).Msg("ListIntegrations: Sending response")
		s.Respond(w, r, http.StatusOK, string(responseJson))
	}
}

// CreateIntegrationHandler creates a new integration
func (s *server) CreateIntegrationHandler() http.HandlerFunc {
	type createIntegrationRequest struct {
		Name   string                 `json:"name"`
		Type   string                 `json:"type"`
		URL    string                 `json:"url"`
		Token  string                 `json:"token"`
		Events string                 `json:"events"`
		Meta   map[string]interface{} `json:"meta"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		txtid := r.Context().Value("userinfo").(Values).Get("Id")

		var req createIntegrationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.Respond(w, r, http.StatusBadRequest, errors.New("invalid request body"))
			return
		}

		if req.Name == "" || req.URL == "" || req.Type == "" {
			s.Respond(w, r, http.StatusBadRequest, errors.New("name, type and url are required"))
			return
		}

		// Initial creation to get ID
		metaStr := "{}"
		if req.Meta != nil {
			metaBytes, _ := json.Marshal(req.Meta)
			metaStr = string(metaBytes)
		}

		integration, err := s.CreateIntegration(txtid, req.Name, req.Type, req.URL, req.Token, req.Events, metaStr)
		if err != nil {
			s.Respond(w, r, http.StatusInternalServerError, err)
			return
		}

		// Handle Chatwoot Auto Create with Integration ID
		if req.Type == "chatwoot" && req.Meta != nil {
			var config ChatwootConfig
			metaBytes, _ := json.Marshal(req.Meta)
			json.Unmarshal(metaBytes, &config)

			// Fallback: If URL is missing in Meta, use the main Integration URL
			if config.URL == "" {
				config.URL = req.URL
			}

			if config.AutoCreate && config.InboxID == 0 {
				// Construct Webhook URL
				scheme := "http"
				if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
					scheme = "https"
				}
				baseURL := fmt.Sprintf("%s://%s", scheme, r.Host)
				webhookURL := fmt.Sprintf("%s/session/integrations/chatwoot/webhook?integration_id=%d", baseURL, integration.ID)

				inboxID, err := s.CreateChatwootInbox(config, config.InboxName, webhookURL)
				if err != nil {
					log.Error().Err(err).Msg("Failed to auto-create Chatwoot inbox")
					// Cleanup
					s.DeleteIntegration(txtid, integration.ID)
					s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("failed to auto-create Chatwoot inbox: %v", err))
					return
				}
				req.Meta["inbox_id"] = inboxID

				// Update Integration with new Meta
				newMetaBytes, _ := json.Marshal(req.Meta)
				integration, err = s.UpdateIntegration(txtid, integration.ID, req.Name, req.URL, req.Token, req.Events, true, string(newMetaBytes))
				if err != nil {
					log.Error().Err(err).Msg("Failed to update integration after inbox creation")
					s.Respond(w, r, http.StatusInternalServerError, err)
					return
				}
			}
		}

		responseJson, err := json.Marshal(integration)
		if err != nil {
			s.Respond(w, r, http.StatusInternalServerError, err)
			return
		}
		s.Respond(w, r, http.StatusOK, string(responseJson))
	}
}

// UpdateIntegrationHandler updates an existing integration
func (s *server) UpdateIntegrationHandler() http.HandlerFunc {
	type updateIntegrationRequest struct {
		Name   string                 `json:"name"`
		URL    string                 `json:"url"`
		Token  string                 `json:"token"`
		Events string                 `json:"events"`
		Status bool                   `json:"status"`
		Meta   map[string]interface{} `json:"meta"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		txtid := r.Context().Value("userinfo").(Values).Get("Id")
		vars := mux.Vars(r)
		idStr := vars["id"]
		id, err := strconv.Atoi(idStr)
		if err != nil {
			s.Respond(w, r, http.StatusBadRequest, errors.New("invalid integration ID"))
			return
		}

		var req updateIntegrationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.Respond(w, r, http.StatusBadRequest, errors.New("invalid request body"))
			return
		}

		// Marshal meta to JSON string
		metaStr := "{}"
		if req.Meta != nil {
			metaBytes, err := json.Marshal(req.Meta)
			if err != nil {
				s.Respond(w, r, http.StatusBadRequest, errors.New("invalid meta JSON"))
				return
			}
			metaStr = string(metaBytes)
		}

		integration, err := s.UpdateIntegration(txtid, id, req.Name, req.URL, req.Token, req.Events, req.Status, metaStr)
		if err != nil {
			s.Respond(w, r, http.StatusInternalServerError, err)
			return
		}

		responseJson, err := json.Marshal(integration)
		if err != nil {
			s.Respond(w, r, http.StatusInternalServerError, err)
			return
		}
		s.Respond(w, r, http.StatusOK, string(responseJson))
	}
}

// DeleteIntegrationHandler deletes an integration
func (s *server) DeleteIntegrationHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		txtid := r.Context().Value("userinfo").(Values).Get("Id")
		vars := mux.Vars(r)
		idStr := vars["id"]
		id, err := strconv.Atoi(idStr)
		if err != nil {
			s.Respond(w, r, http.StatusBadRequest, errors.New("invalid integration ID"))
			return
		}

		err = s.DeleteIntegration(txtid, id)
		if err != nil {
			s.Respond(w, r, http.StatusInternalServerError, err)
			return
		}

		s.Respond(w, r, http.StatusOK, `{"status":"success"}`)
	}
}
