package bots

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const (
	botWorkspaceMetadataKey       = "workspace"
	botLastSetupErrorMetadataKey  = "last_setup_error"
	botSetupFailureMessageMaxRune = 4096
)

var (
	diagnosticURLUserinfoPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)([^/\s:@]+):([^/\s@]+)@`)
	diagnosticSecretParamPattern = regexp.MustCompile(`(?i)\b(token|password|passwd|pwd|secret|api_key|access_token)=([^&\s]+)`)
)

type containerSetupFailure struct {
	Phase   string
	Message string
	At      string
}

// RecordContainerSetupFailure persists a sanitized workspace setup failure so
// runtime diagnostics can explain why a ready bot is unhealthy.
func (s *Service) RecordContainerSetupFailure(ctx context.Context, botID, phase string, setupErr error) error {
	if s.queries == nil {
		return errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	row, err := s.queries.GetBotByID(ctx, botUUID)
	if err != nil {
		return err
	}
	botRow := asSQLCBot(row)
	metadata, err := decodeMetadata(botRow.Metadata)
	if err != nil {
		return err
	}
	workspace := cloneMetadataSection(metadata[botWorkspaceMetadataKey])
	workspace[botLastSetupErrorMetadataKey] = map[string]any{
		"phase":   normalizeSetupFailurePhase(phase),
		"message": sanitizeDiagnosticMessage(errorMessage(setupErr), "workspace setup failed"),
		"at":      time.Now().UTC().Format(time.RFC3339),
	}
	metadata[botWorkspaceMetadataKey] = workspace
	return s.persistBotMetadata(ctx, botRow, metadata)
}

// ClearContainerSetupFailure removes stale setup failure diagnostics after a
// successful workspace setup or manual workspace creation.
func (s *Service) ClearContainerSetupFailure(ctx context.Context, botID string) error {
	if s.queries == nil {
		return errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	row, err := s.queries.GetBotByID(ctx, botUUID)
	if err != nil {
		return err
	}
	botRow := asSQLCBot(row)
	metadata, err := decodeMetadata(botRow.Metadata)
	if err != nil {
		return err
	}
	workspace, ok := metadata[botWorkspaceMetadataKey].(map[string]any)
	if !ok {
		return nil
	}
	if _, ok := workspace[botLastSetupErrorMetadataKey]; !ok {
		return nil
	}
	workspace = cloneMetadataSection(workspace)
	delete(workspace, botLastSetupErrorMetadataKey)
	metadata[botWorkspaceMetadataKey] = workspace
	return s.persistBotMetadata(ctx, botRow, metadata)
}

func (s *Service) persistBotMetadata(ctx context.Context, row sqlc.Bot, metadata map[string]any) error {
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = s.queries.UpdateBotProfile(ctx, sqlc.UpdateBotProfileParams{
		ID:          row.ID,
		Name:        row.Name,
		DisplayName: row.DisplayName,
		AvatarUrl:   row.AvatarUrl,
		Timezone:    row.Timezone,
		IsActive:    row.IsActive,
		Metadata:    payload,
	})
	return err
}

func lastContainerSetupFailure(payload []byte) (containerSetupFailure, bool, error) {
	metadata, err := decodeMetadata(payload)
	if err != nil {
		return containerSetupFailure{}, false, err
	}
	workspace, ok := metadata[botWorkspaceMetadataKey].(map[string]any)
	if !ok {
		return containerSetupFailure{}, false, nil
	}
	raw, ok := workspace[botLastSetupErrorMetadataKey].(map[string]any)
	if !ok {
		return containerSetupFailure{}, false, nil
	}
	failure := containerSetupFailure{
		Phase:   stringValue(raw["phase"]),
		Message: stringValue(raw["message"]),
		At:      stringValue(raw["at"]),
	}
	if strings.TrimSpace(failure.Message) == "" {
		return containerSetupFailure{}, false, nil
	}
	return failure, true, nil
}

func (f containerSetupFailure) metadata() map[string]any {
	data := map[string]any{
		"setup_error_phase": f.Phase,
		"setup_error_at":    f.At,
	}
	return data
}

func cloneMetadataSection(raw any) map[string]any {
	section := make(map[string]any)
	if existing, ok := raw.(map[string]any); ok {
		for key, value := range existing {
			section[key] = value
		}
	}
	return section
}

func normalizeSetupFailurePhase(phase string) string {
	switch strings.TrimSpace(phase) {
	case "image_prepare":
		return "image_prepare"
	case "start":
		return "start"
	default:
		return "setup"
	}
}

func sanitizeDiagnosticMessage(message, fallback string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		message = fallback
	}
	message = diagnosticURLUserinfoPattern.ReplaceAllString(message, "${1}***:***@")
	message = diagnosticSecretParamPattern.ReplaceAllString(message, "${1}=***")
	runes := []rune(message)
	if len(runes) > botSetupFailureMessageMaxRune {
		message = string(runes[:botSetupFailureMessageMaxRune])
	}
	return message
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
