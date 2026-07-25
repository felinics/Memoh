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
	ErrPromptRequired                 = errors.New("prompt is required")
	ErrImagePromptUnsupported         = errors.New("ACP agent does not support image prompts")
	ErrInvalidPromptImage             = errors.New("invalid ACP prompt image")
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

func modelStateFromLegacy(state *legacySessionModelState) ModelState {
	if state == nil {
		return ModelState{Supported: false}
	}
	out := ModelState{
		Supported:      true,
		CurrentModelID: strings.TrimSpace(state.CurrentModelID),
		Available:      make([]ModelInfo, 0, len(state.AvailableModels)),
	}
	seen := make(map[string]struct{}, len(state.AvailableModels))
	for _, model := range state.AvailableModels {
		id := strings.TrimSpace(model.ModelID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = id
		}
		item := ModelInfo{ID: id, Name: name}
		if model.Description != nil {
			item.Description = strings.TrimSpace(*model.Description)
		}
		out.Available = append(out.Available, item)
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
	selector := s.modelSelector
	if !s.modelState.Supported || selector == nil {
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
	s.mu.Unlock()

	update, err := selector.selectModel(ctx, modelID)
	if err != nil {
		return s.ModelState(), err
	}
	if update.hasConfigOptionsState {
		_, confirmed := modelStateFromACP(update.configOptions)
		if !confirmed.Supported || confirmed.CurrentModelID != modelID {
			return s.ModelState(), fmt.Errorf("%w: model %q", ErrSessionConfigUpdateUnconfirmed, modelID)
		}
		// The response is the authoritative full config snapshot. A model switch
		// may also change the available or selected thought levels.
		s.replaceConfigOptions(s.sessionID, update.configOptions)
	} else {
		// The dedicated model RPC has no state in its response. Update the cached
		// value only if the session did not switch protocol adapters concurrently.
		s.mu.Lock()
		if s.modelSelector == selector {
			s.modelState.CurrentModelID = strings.TrimSpace(update.currentModelID)
		}
		s.mu.Unlock()
	}
	state := s.ModelState()
	if state.CurrentModelID != modelID {
		return state, fmt.Errorf("%w: model %q", ErrSessionConfigUpdateUnconfirmed, modelID)
	}
	return state, nil
}

func (s *Session) installLegacyModels(state *legacySessionModelState) {
	parsed := modelStateFromLegacy(state)
	if s == nil || !parsed.Supported {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, standard := s.modelSelector.(*configOptionModelSelector); standard {
		return
	}
	s.modelSelector = &legacyModelSelector{conn: s.conn, sessionID: s.sessionID}
	s.modelState = parsed
}
