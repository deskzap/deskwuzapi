package main

import (
	"fmt"

	"github.com/rs/zerolog/log"
)

func (s *server) CreateIntegration(userID, name, integrationType, url, token string, events string, meta string) (*Integration, error) {
	log.Debug().Str("userID", userID).Str("name", name).Msg("Creating integration")

	if meta == "" {
		meta = "{}"
	}

	query := `
		INSERT INTO integrations (user_id, name, type, url, token, events, status, meta, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		RETURNING id, user_id, name, type, url, token, events, status, meta, created_at, updated_at`
	query = s.db.Rebind(query)

	var integration Integration
	err := s.db.QueryRowx(query, userID, name, integrationType, url, token, events, true, meta).StructScan(&integration)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create integration")
		return nil, fmt.Errorf("failed to create integration: %w", err)
	}

	log.Info().Int("id", integration.ID).Str("userID", userID).Msg("Integration created successfully")
	return &integration, nil
}

func (s *server) GetIntegrations(userID string) ([]Integration, error) {
	var integrations []Integration
	query := `SELECT * FROM integrations WHERE user_id = ? ORDER BY created_at DESC`
	query = s.db.Rebind(query)
	log.Info().Str("query", query).Str("userID", userID).Msg("Executing GetIntegrations")
	err := s.db.Select(&integrations, query, userID)
	if err != nil {
		log.Error().Err(err).Str("userID", userID).Msg("Failed to get integrations")
		return nil, fmt.Errorf("failed to get integrations: %w", err)
	}
	log.Info().Int("count", len(integrations)).Str("userID", userID).Msg("GetIntegrations returned items")
	return integrations, nil
}

func (s *server) GetIntegration(userID string, integrationID int) (*Integration, error) {
	var integration Integration
	query := "SELECT * FROM integrations WHERE user_id = ? AND id = ?"
	query = s.db.Rebind(query)
	err := s.db.Get(&integration, query, userID, integrationID)
	if err != nil {
		return nil, err
	}
	return &integration, nil
}

// GetIntegrationByID fetches an integration by its ID (ignoring userID)
func (s *server) GetIntegrationByID(integrationID int) (*Integration, error) {
	var integration Integration
	query := "SELECT * FROM integrations WHERE id = ?"
	query = s.db.Rebind(query)
	err := s.db.Get(&integration, query, integrationID)
	if err != nil {
		return nil, err
	}
	return &integration, nil
}

func (s *server) UpdateIntegration(userID string, integrationID int, name, url, token, events string, status bool, meta string) (*Integration, error) {
	if meta == "" {
		meta = "{}"
	}

	query := `
		UPDATE integrations 
		SET name = ?, url = ?, token = ?, events = ?, status = ?, meta = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND user_id = ?
		RETURNING id, user_id, name, type, url, token, events, status, meta, created_at, updated_at`
	query = s.db.Rebind(query)

	var integration Integration
	err := s.db.QueryRowx(query, name, url, token, events, status, meta, integrationID, userID).StructScan(&integration)
	if err != nil {
		log.Error().Err(err).Msg("Failed to update integration")
		return nil, fmt.Errorf("failed to update integration: %w", err)
	}
	return &integration, nil
}

func (s *server) DeleteIntegration(userID string, integrationID int) error {
	query := "DELETE FROM integrations WHERE id = ? AND user_id = ?"
	query = s.db.Rebind(query)
	result, err := s.db.Exec(query, integrationID, userID)
	if err != nil {
		log.Error().Err(err).Msg("Failed to delete integration")
		return fmt.Errorf("failed to delete integration: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("integration not found")
	}

	return nil
}

func (s *server) GetActiveIntegrations(userID string) ([]Integration, error) {
	var integrations []Integration
	query := "SELECT * FROM integrations WHERE user_id = ? AND status = true"
	query = s.db.Rebind(query)
	err := s.db.Select(&integrations, query, userID)
	if err != nil {
		// Log and return empty list if error
		log.Error().Err(err).Str("userID", userID).Msg("Failed to get active integrations")
		return nil, nil
	}
	return integrations, nil
}
