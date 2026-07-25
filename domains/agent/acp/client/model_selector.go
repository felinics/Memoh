package client

import (
	"context"

	acp "github.com/coder/acp-go-sdk"
)

const legacyAgentMethodSessionSetModel = "session/set_model"

// newSessionResponse preserves the standard ACP response while retaining the
// dedicated model selector used by agents that still implement the former
// models + session/set_model protocol. Keeping this compatibility DTO at the
// wire boundary prevents the legacy shape from leaking into the pool or UI.
type newSessionResponse struct {
	acp.NewSessionResponse
	Models *legacySessionModelState `json:"models,omitempty"`
}

type legacySessionModelState struct {
	Meta            map[string]any    `json:"_meta,omitempty"`
	AvailableModels []legacyModelInfo `json:"availableModels"`
	CurrentModelID  string            `json:"currentModelId"`
}

type legacyModelInfo struct {
	Meta        map[string]any `json:"_meta,omitempty"`
	Description *string        `json:"description,omitempty"`
	ModelID     string         `json:"modelId"`
	Name        string         `json:"name"`
}

type legacySetSessionModelRequest struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	ModelID   string         `json:"modelId"`
	SessionID acp.SessionId  `json:"sessionId"`
}

type legacySetSessionModelResponse struct {
	Meta map[string]any `json:"_meta,omitempty"`
}

type modelSelectionUpdate struct {
	configOptions         []acp.SessionConfigOption
	currentModelID        string
	hasConfigOptionsState bool
}

// modelSelector is the protocol adapter behind Session.SetModel. Agents using
// ACP's model config category and agents using the former dedicated model RPC
// expose the same ModelState contract to all higher layers.
type modelSelector interface {
	selectModel(ctx context.Context, modelID string) (modelSelectionUpdate, error)
}

type configOptionModelSelector struct {
	conn      *clientConnection
	sessionID acp.SessionId
	configID  acp.SessionConfigId
}

func (s *configOptionModelSelector) selectModel(ctx context.Context, modelID string) (modelSelectionUpdate, error) {
	resp, err := s.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: s.sessionID,
			ConfigId:  s.configID,
			Value:     acp.SessionConfigValueId(modelID),
		},
	})
	if err != nil {
		return modelSelectionUpdate{}, err
	}
	return modelSelectionUpdate{
		configOptions:         resp.ConfigOptions,
		hasConfigOptionsState: true,
	}, nil
}

type legacyModelSelector struct {
	conn      *clientConnection
	sessionID acp.SessionId
}

func (s *legacyModelSelector) selectModel(ctx context.Context, modelID string) (modelSelectionUpdate, error) {
	_, err := s.conn.SetLegacySessionModel(ctx, legacySetSessionModelRequest{
		SessionID: s.sessionID,
		ModelID:   modelID,
	})
	if err != nil {
		return modelSelectionUpdate{}, err
	}
	return modelSelectionUpdate{currentModelID: modelID}, nil
}

func isLegacyModelSelector(selector modelSelector) bool {
	_, ok := selector.(*legacyModelSelector)
	return ok
}
