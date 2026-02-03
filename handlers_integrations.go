package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-resty/resty/v2"
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
		txtid := ""
		if val := r.Context().Value("userinfo"); val != nil {
			if v, ok := val.(Values); ok {
				txtid = v.Get("Id")
			}
		}

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

			// Get instance name from user
			var instanceName string
			query := "SELECT name FROM users WHERE id = ?"
			if s.db.DriverName() == "postgres" {
				query = "SELECT name FROM users WHERE id = $1"
			}
			nameRow := s.db.QueryRow(query, txtid)
			nameRow.Scan(&instanceName)
			if instanceName == "" {
				instanceName = txtid
			}

			if config.AutoCreate && config.InboxID == 0 {
				// Construct Webhook URL - using new format with instance name
				scheme := "http"
				if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
					scheme = "https"
				}
				baseURL := fmt.Sprintf("%s://%s", scheme, r.Host)
				webhookURL := fmt.Sprintf("%s/chatwoot/webhook/%s", baseURL, instanceName)

				inboxID, err := s.CreateChatwootInbox(config, config.InboxName, webhookURL)
				if err != nil {
					log.Error().Err(err).Msg("Failed to auto-create Chatwoot inbox")
					// Cleanup
					s.DeleteIntegration(txtid, integration.ID)
					s.Respond(w, r, http.StatusBadRequest, fmt.Errorf("failed to auto-create Chatwoot inbox: %v", err))
					return
				}
				req.Meta["inbox_id"] = inboxID
				config.InboxID = inboxID

				// Create manager contact (+123456) for connection management
				contactID, conversationID, err := s.CreateChatwootManagerContact(config, instanceName)
				if err != nil {
					log.Warn().Err(err).Msg("Failed to create manager contact, but inbox was created successfully")
				} else {
					req.Meta["manager_contact_id"] = contactID
					req.Meta["manager_conversation_id"] = conversationID
				}

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

// TestIntegrationHandler tests an integration connection
func (s *server) TestIntegrationHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		idStr := vars["id"]
		id, err := strconv.Atoi(idStr)
		if err != nil {
			s.Respond(w, r, http.StatusBadRequest, errors.New("invalid integration ID"))
			return
		}

		txtid := r.Context().Value("userinfo").(Values).Get("Id")
		integration, err := s.GetIntegrationByID(id)
		if err != nil {
			s.Respond(w, r, http.StatusNotFound, errors.New("integration not found"))
			return
		}

		// Verify ownership
		if integration.UserID != txtid {
			s.Respond(w, r, http.StatusForbidden, errors.New("forbidden"))
			return
		}

		if integration.Type != "chatwoot" {
			s.Respond(w, r, http.StatusBadRequest, errors.New("only chatwoot integrations can be tested currently"))
			return
		}

		// Parse config
		var config ChatwootConfig
		if err := json.Unmarshal([]byte(integration.Meta), &config); err != nil {
			s.Respond(w, r, http.StatusInternalServerError, errors.New("invalid configuration"))
			return
		}

		// Test Connection by fetching account info
		client := resty.New()
		// Add User-Agent to avoid Cloudflare blocking
		client.SetHeader("User-Agent", "Mozilla/5.0 (Compatible; Wuzapi/1.0)")
		resp, err := client.R().
			SetHeader("api_access_token", config.Token).
			Get(fmt.Sprintf("%s/api/v1/accounts/%s/inboxes", config.URL, config.AccountID))

		if err != nil {
			s.Respond(w, r, http.StatusOK, map[string]interface{}{
				"success": false,
				"message": fmt.Sprintf("Network error: %v", err),
			})
			return
		}

		if resp.StatusCode() >= 400 {
			s.Respond(w, r, http.StatusOK, map[string]interface{}{
				"success": false,
				"message": fmt.Sprintf("API Error: %d - %s", resp.StatusCode(), string(resp.Body())),
			})
			return
		}

		s.Respond(w, r, http.StatusOK, map[string]interface{}{
			"success": true,
			"message": "Connection successful! Chatwoot API is reachable.",
		})
	}
}

// ChatwootProxyHandler proxies requests to Chatwoot API to avoid CORS issues
func (s *server) ChatwootProxyHandler() http.HandlerFunc {
	type proxyRequest struct {
		Method string                 `json:"method"` // GET, POST, PUT, DELETE
		Path   string                 `json:"path"`   // e.g., /contacts/search?q=551234567890
		Body   map[string]interface{} `json:"body,omitempty"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		idStr := vars["id"]
		id, err := strconv.Atoi(idStr)
		if err != nil {
			s.Respond(w, r, http.StatusBadRequest, errors.New("invalid integration ID"))
			return
		}

		txtid := r.Context().Value("userinfo").(Values).Get("Id")
		integration, err := s.GetIntegrationByID(id)
		if err != nil {
			s.Respond(w, r, http.StatusNotFound, errors.New("integration not found"))
			return
		}

		// Verify ownership
		if integration.UserID != txtid {
			s.Respond(w, r, http.StatusForbidden, errors.New("forbidden"))
			return
		}

		if integration.Type != "chatwoot" {
			s.Respond(w, r, http.StatusBadRequest, errors.New("only chatwoot integrations support proxy"))
			return
		}

		// Parse config
		var config ChatwootConfig
		if err := json.Unmarshal([]byte(integration.Meta), &config); err != nil {
			s.Respond(w, r, http.StatusInternalServerError, errors.New("invalid configuration"))
			return
		}

		// Parse request
		var req proxyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.Respond(w, r, http.StatusBadRequest, errors.New("invalid request body"))
			return
		}

		if req.Path == "" {
			s.Respond(w, r, http.StatusBadRequest, errors.New("path is required"))
			return
		}

		// Sanitize config
		config.Token = strings.TrimSpace(config.Token)
		config.URL = strings.TrimSpace(config.URL)
		config.AccountID = strings.TrimSpace(config.AccountID)

		// Build target URL
		targetURL := fmt.Sprintf("%s/api/v1/accounts/%s%s", config.URL, config.AccountID, req.Path)
		log.Info().
			Str("targetURL", targetURL).
			Str("method", req.Method).
			Str("token", config.Token). // DEBUG: log token being used
			Str("accountId", config.AccountID).
			Msg("[CHATWOOT PROXY] Forwarding request")

		// Create resty client
		client := resty.New()
		client.SetDebug(true) // Enable debug logging
		client.SetHeader("User-Agent", "Mozilla/5.0 (Compatible; Wuzapi/1.0)")
		client.SetHeader("api_access_token", config.Token)
		client.SetHeader("Content-Type", "application/json")

		var resp *resty.Response
		request := client.R()

		switch req.Method {
		case "GET", "":
			resp, err = request.Get(targetURL)
		case "POST":
			if req.Body != nil {
				request.SetBody(req.Body)
			}
			resp, err = request.Post(targetURL)
		case "PUT":
			if req.Body != nil {
				request.SetBody(req.Body)
			}
			resp, err = request.Put(targetURL)
		case "DELETE":
			resp, err = request.Delete(targetURL)
		default:
			s.Respond(w, r, http.StatusBadRequest, errors.New("unsupported method"))
			return
		}

		if err != nil {
			log.Error().Err(err).Str("url", targetURL).Msg("[CHATWOOT PROXY] Request failed")
			errResp, _ := json.Marshal(map[string]interface{}{
				"error":   "proxy_error",
				"message": err.Error(),
			})
			s.Respond(w, r, http.StatusBadGateway, string(errResp))
			return
		}

		// Return the response from Chatwoot
		log.Info().Int("status", resp.StatusCode()).Str("body", string(resp.Body())).Msg("[CHATWOOT PROXY] Response received")

		// Return as-is if already valid JSON, otherwise wrap as string
		s.Respond(w, r, resp.StatusCode(), string(resp.Body()))
	}
}
