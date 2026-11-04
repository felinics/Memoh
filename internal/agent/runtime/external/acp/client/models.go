package client

import (
	"context"
	"errors"
	"fmt"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

var (
	ErrModelSelectionUnsupported      = errors.New("ACP agent does not expose selectable models")
	ErrModelUnavailable               = errors.New("ACP model is not available for this session")
	ErrModelIDRequired                = errors.New("model_id is required")
	ErrSessionConfigUpdateUnconfirmed = errors.New("ACP agent did not confirm the session configuration update")
	ErrSessionNotInitialized          = errors.New("ACP session is not initialized")
	ErrSessionClosed                  = errors.New("ACP session is closed")
	// ErrSessionCloseUnconfirmed reports a session the agent did not confirm
	// closing: it offers no session/close, or did not answer it. The agent may
	// still hold the session until its process ends.
	ErrSessionCloseUnconfirmed = errors.New("ACP agent did not confirm closing the session")
	ErrPromptRequired          = errors.New("prompt is required")
	// ErrPromptCancellationUnconfirmed means session/cancel was sent, but the
	// original session/prompt did not reach a confirmed terminal response (with
	// its pre-response notification watermark drained) before the grace period.
	// A runtime in this state must not be reused for another prompt.
	ErrPromptCancellationUnconfirmed = errors.New("ACP prompt cancellation was not confirmed")
	ErrImagePromptUnsupported        = errors.New("ACP agent does not support image prompts")
	ErrInvalidPromptImage            = errors.New("invalid ACP prompt image")
)

type ModelInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
} // @name acpclient.ModelInfo

type ModelState struct {
	Supported      bool        `json:"supported"`
	CurrentModelID string      `json:"current_model_id,omitempty"`
	Available      []ModelInfo `json:"available_models,omitempty"`
} // @name acpclient.ModelState

func modelStateFromACP(options []acp.SessionConfigOption) (string, ModelState) {
	for i := range options {
		option := options[i].Select
		if option == nil || option.Category == nil ||
			*option.Category != acp.SessionConfigOptionCategoryModel ||
			strings.TrimSpace(string(option.Id)) == "" {
			continue
		}
		state := ModelState{
			Supported:      true,
			CurrentModelID: strings.TrimSpace(string(option.CurrentValue)),
			Available:      modelsFromSelectOptions(option.Options),
		}
		return strings.TrimSpace(string(option.Id)), state
	}
	return "", ModelState{Supported: false}
}

func modelsFromSelectOptions(options acp.SessionConfigSelectOptions) []ModelInfo {
	var source []acp.SessionConfigSelectOption
	if options.Ungrouped != nil {
		source = append(source, (*options.Ungrouped)...)
	}
	if options.Grouped != nil {
		for _, group := range *options.Grouped {
			source = append(source, group.Options...)
		}
	}
	out := make([]ModelInfo, 0, len(source))
	seen := make(map[string]struct{}, len(source))
	for _, option := range source {
		id := strings.TrimSpace(string(option.Value))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(option.Name)
		if name == "" {
			name = id
		}
		item := ModelInfo{ID: id, Name: name}
		if option.Description != nil {
			item.Description = strings.TrimSpace(*option.Description)
		}
		out = append(out, item)
	}
	return out
}

func cloneModelState(state ModelState) ModelState {
	state.Available = append([]ModelInfo(nil), state.Available...)
	return state
}

func (s *Session) ModelState() ModelState {
	if s == nil {
		return ModelState{Supported: false}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneModelState(s.modelState)
}

func (s *Session) SetModel(ctx context.Context, modelID string) (ModelState, error) {
	if s == nil {
		return ModelState{Supported: false}, ErrSessionNotInitialized
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return s.ModelState(), ErrModelIDRequired
	}

	s.mu.Lock()
	if s.conn == nil {
		s.mu.Unlock()
		return ModelState{Supported: false}, ErrSessionNotInitialized
	}
	if s.closed {
		state := cloneModelState(s.modelState)
		s.mu.Unlock()
		return state, ErrSessionClosed
	}
	configID := s.modelConfigID
	if !s.modelState.Supported || configID == "" {
		state := cloneModelState(s.modelState)
		s.mu.Unlock()
		return state, ErrModelSelectionUnsupported
	}
	available := false
	for _, model := range s.modelState.Available {
		if model.ID == modelID {
			available = true
			break
		}
	}
	if !available {
		state := cloneModelState(s.modelState)
		s.mu.Unlock()
		return state, fmt.Errorf("%w: %s", ErrModelUnavailable, modelID)
	}
	if s.modelState.CurrentModelID == modelID {
		state := cloneModelState(s.modelState)
		s.mu.Unlock()
		return state, nil
	}
	conn := s.conn
	sessionID := s.sessionID
	s.mu.Unlock()

	resp, err := conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: sessionID,
			ConfigId:  acp.SessionConfigId(configID),
			Value:     acp.SessionConfigValueId(modelID),
		},
	})
	if err != nil {
		return s.ModelState(), err
	}
	_, confirmed := modelStateFromACP(resp.ConfigOptions)
	if !confirmed.Supported || confirmed.CurrentModelID != modelID {
		return s.ModelState(), fmt.Errorf("%w: model %q", ErrSessionConfigUpdateUnconfirmed, modelID)
	}
	// The response is the authoritative full config snapshot. A model switch
	// may also change the available or selected thought levels.
	s.replaceConfigOptions(sessionID, resp.ConfigOptions)
	state := s.ModelState()
	if state.CurrentModelID != modelID {
		return state, fmt.Errorf("%w: model %q", ErrSessionConfigUpdateUnconfirmed, modelID)
	}
	return state, nil
}
