package client

import (
	"context"
	"errors"
	"fmt"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

var (
	ErrReasoningSelectionUnsupported = errors.New("ACP agent does not expose selectable reasoning effort")
	ErrReasoningEffortUnavailable    = errors.New("ACP reasoning effort is not available for this session")
	ErrReasoningEffortRequired       = errors.New("reasoning_effort is required")
)

// ReasoningEffortInfo is one agent-advertised thought-level value. IDs are
// protocol values; names and descriptions are display metadata supplied by
// the agent and are never inferred from a model name.
type ReasoningEffortInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
} // @name acpclient.ReasoningEffortInfo

// ReasoningState is the normalized thought-level slice of ACP configOptions.
// The private config ID stays inside Session so callers cannot couple to an
// agent-specific option such as "effort".
type ReasoningState struct {
	Supported     bool                  `json:"supported"`
	CurrentEffort string                `json:"current_effort,omitempty"`
	Available     []ReasoningEffortInfo `json:"available_efforts,omitempty"`
} // @name acpclient.ReasoningState

func reasoningStateFromACP(options []acp.SessionConfigOption, fallbackConfigID string) (string, ReasoningState) {
	var fallback *acp.SessionConfigOptionSelect
	fallbackConfigID = strings.TrimSpace(fallbackConfigID)
	for i := range options {
		option := options[i].Select
		if option == nil || strings.TrimSpace(string(option.Id)) == "" {
			continue
		}
		if option.Category != nil && *option.Category == acp.SessionConfigOptionCategoryThoughtLevel {
			return reasoningStateFromSelect(option)
		}
		if fallback == nil && option.Category == nil && fallbackConfigID != "" && strings.TrimSpace(string(option.Id)) == fallbackConfigID {
			fallback = option
		}
	}
	if fallback != nil {
		return reasoningStateFromSelect(fallback)
	}
	return "", ReasoningState{Supported: false}
}

func reasoningStateFromSelect(option *acp.SessionConfigOptionSelect) (string, ReasoningState) {
	if option == nil {
		return "", ReasoningState{Supported: false}
	}
	state := ReasoningState{
		Supported:     true,
		CurrentEffort: strings.TrimSpace(string(option.CurrentValue)),
		Available:     reasoningEffortsFromSelectOptions(option.Options),
	}
	return strings.TrimSpace(string(option.Id)), state
}

func reasoningEffortsFromSelectOptions(options acp.SessionConfigSelectOptions) []ReasoningEffortInfo {
	var source []acp.SessionConfigSelectOption
	if options.Ungrouped != nil {
		source = append(source, (*options.Ungrouped)...)
	}
	if options.Grouped != nil {
		for _, group := range *options.Grouped {
			source = append(source, group.Options...)
		}
	}
	out := make([]ReasoningEffortInfo, 0, len(source))
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
		item := ReasoningEffortInfo{ID: id, Name: name}
		if option.Description != nil {
			item.Description = strings.TrimSpace(*option.Description)
		}
		out = append(out, item)
	}
	return out
}

func cloneReasoningState(state ReasoningState) ReasoningState {
	state.Available = append([]ReasoningEffortInfo(nil), state.Available...)
	return state
}

func (s *Session) ReasoningState() ReasoningState {
	if s == nil {
		return ReasoningState{Supported: false}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneReasoningState(s.reasoningState)
}

// ConfigurationState returns the model and reasoning capabilities from one
// config snapshot. ACP updates both values together, so runtime status must
// not read them under separate locks and expose a combination that never
// existed in the Agent.
func (s *Session) ConfigurationState() (ModelState, ReasoningState) {
	if s == nil {
		return ModelState{Supported: false}, ReasoningState{Supported: false}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneModelState(s.modelState), cloneReasoningState(s.reasoningState)
}

func (s *Session) replaceConfigOptions(sessionID acp.SessionId, options []acp.SessionConfigOption) {
	if s == nil || sessionID != s.sessionID {
		return
	}
	modelConfigID, modelState := modelStateFromACP(options)
	reasoningConfigID, reasoningState := reasoningStateFromACP(options, s.reasoningConfigFallbackID)
	s.mu.Lock()
	if modelConfigID != "" {
		s.modelSelector = &configOptionModelSelector{
			conn:      s.conn,
			sessionID: s.sessionID,
			configID:  acp.SessionConfigId(modelConfigID),
		}
		s.modelState = modelState
	} else if !isLegacyModelSelector(s.modelSelector) {
		// Config-option responses are full snapshots. The model capability was
		// removed unless this session uses the independent legacy model protocol.
		s.modelSelector = nil
		s.modelState = ModelState{Supported: false}
	}
	s.reasoningConfigID = reasoningConfigID
	s.reasoningState = reasoningState
	s.mu.Unlock()
}

func (s *Session) SetReasoningEffort(ctx context.Context, effort string) (ReasoningState, error) {
	if s == nil {
		return ReasoningState{Supported: false}, ErrSessionNotInitialized
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return s.ReasoningState(), ErrReasoningEffortRequired
	}

	s.mu.Lock()
	if s.conn == nil {
		s.mu.Unlock()
		return ReasoningState{Supported: false}, ErrSessionNotInitialized
	}
	state := cloneReasoningState(s.reasoningState)
	if s.closed {
		s.mu.Unlock()
		return state, ErrSessionClosed
	}
	if !state.Supported || strings.TrimSpace(s.reasoningConfigID) == "" {
		s.mu.Unlock()
		return state, ErrReasoningSelectionUnsupported
	}
	available := false
	for _, option := range state.Available {
		if option.ID == effort {
			available = true
			break
		}
	}
	if !available {
		s.mu.Unlock()
		return state, fmt.Errorf("%w: %s", ErrReasoningEffortUnavailable, effort)
	}
	if state.CurrentEffort == effort {
		s.mu.Unlock()
		return state, nil
	}
	conn := s.conn
	sessionID := s.sessionID
	configID := s.reasoningConfigID
	fallbackConfigID := s.reasoningConfigFallbackID
	s.mu.Unlock()

	resp, err := conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: sessionID,
			ConfigId:  acp.SessionConfigId(configID),
			Value:     acp.SessionConfigValueId(effort),
		},
	})
	if err != nil {
		return s.ReasoningState(), err
	}
	_, confirmed := reasoningStateFromACP(resp.ConfigOptions, fallbackConfigID)
	if !confirmed.Supported || confirmed.CurrentEffort != effort {
		return s.ReasoningState(), fmt.Errorf("%w: reasoning effort %q", ErrSessionConfigUpdateUnconfirmed, effort)
	}
	// ACP returns the complete config state. Replacing rather than patching is
	// important because a model change may add, remove, or rename values.
	s.replaceConfigOptions(sessionID, resp.ConfigOptions)
	state = s.ReasoningState()
	if !state.Supported || state.CurrentEffort != effort {
		return state, fmt.Errorf("%w: reasoning effort %q", ErrSessionConfigUpdateUnconfirmed, effort)
	}
	return state, nil
}
