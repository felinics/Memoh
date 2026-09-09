package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	acp "github.com/coder/acp-go-sdk"
)

var (
	ErrModeSelectionUnsupported = errors.New("ACP agent does not expose selectable session modes")
	ErrModeUnavailable          = errors.New("ACP session mode is not available for this session")
	ErrModeIDRequired           = errors.New("mode_id is required")
	ErrAgentCommandUnavailable  = errors.New("agent command not advertised by the live ACP session")
)

// ModeInfo is one mode declared by the ACP Agent. Its ID is an opaque
// protocol value and is sent back unchanged when the mode is selected.
type ModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
} // @name acpclient.ModeInfo

type ModeState struct {
	Supported     bool       `json:"supported"`
	CurrentModeID string     `json:"current_mode_id,omitempty"`
	Available     []ModeInfo `json:"available_modes,omitempty"`
} // @name acpclient.ModeState

type AvailableCommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputHint   string `json:"input_hint,omitempty"`
} // @name acpclient.AvailableCommandInfo

func modeStateFromACP(state *acp.SessionModeState) ModeState {
	if state == nil {
		return ModeState{Supported: false}
	}
	out := ModeState{
		Supported:     true,
		CurrentModeID: string(state.CurrentModeId),
		Available:     make([]ModeInfo, 0, len(state.AvailableModes)),
	}
	seen := make(map[string]struct{}, len(state.AvailableModes))
	for _, mode := range state.AvailableModes {
		id := string(mode.Id)
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(mode.Name)
		if name == "" {
			name = id
		}
		item := ModeInfo{ID: id, Name: name}
		if mode.Description != nil {
			item.Description = strings.TrimSpace(*mode.Description)
		}
		out.Available = append(out.Available, item)
	}
	return out
}

func availableCommandsFromACP(commands []acp.AvailableCommand) []AvailableCommandInfo {
	out := make([]AvailableCommandInfo, 0, len(commands))
	for _, command := range commands {
		name := command.Name
		if name == "" || strings.HasPrefix(name, "/") || strings.TrimSpace(name) != name || strings.IndexFunc(name, unicode.IsSpace) >= 0 {
			continue
		}
		item := AvailableCommandInfo{
			Name:        name,
			Description: strings.TrimSpace(command.Description),
		}
		if command.Input != nil && command.Input.Unstructured != nil {
			item.InputHint = strings.TrimSpace(command.Input.Unstructured.Hint)
		}
		out = append(out, item)
	}
	return out
}

func cloneModeState(state ModeState) ModeState {
	state.Available = append([]ModeInfo(nil), state.Available...)
	return state
}

func cloneAvailableCommands(commands []AvailableCommandInfo) []AvailableCommandInfo {
	if commands == nil {
		return nil
	}
	return append(make([]AvailableCommandInfo, 0, len(commands)), commands...)
}

func (s *Session) ModeState() ModeState {
	if s == nil {
		return ModeState{Supported: false}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneModeState(s.modeState)
}

func (s *Session) SetMode(ctx context.Context, modeID string) (ModeState, error) {
	if s == nil {
		return ModeState{Supported: false}, ErrSessionNotInitialized
	}
	if strings.TrimSpace(modeID) == "" {
		return s.ModeState(), ErrModeIDRequired
	}

	s.mu.Lock()
	state := cloneModeState(s.modeState)
	if s.conn == nil {
		s.mu.Unlock()
		return state, ErrSessionNotInitialized
	}
	if s.closed {
		s.mu.Unlock()
		return state, ErrSessionClosed
	}
	if !state.Supported {
		s.mu.Unlock()
		return state, ErrModeSelectionUnsupported
	}
	available := false
	for _, mode := range state.Available {
		if mode.ID == modeID {
			available = true
			break
		}
	}
	if !available {
		s.mu.Unlock()
		return state, fmt.Errorf("%w: %s", ErrModeUnavailable, modeID)
	}
	if state.CurrentModeID == modeID {
		s.mu.Unlock()
		return state, nil
	}
	conn := s.conn
	sessionID := s.sessionID
	modeRevision := s.modeRevision
	s.mu.Unlock()

	if _, err := conn.SetSessionMode(ctx, acp.SetSessionModeRequest{
		SessionId: sessionID,
		ModeId:    acp.SessionModeId(modeID),
	}); err != nil {
		return s.ModeState(), err
	}
	s.mu.Lock()
	// ACP guarantees that notifications sent before the set_mode response are
	// delivered before SetSessionMode returns. If one of those notifications
	// changed the mode, it is newer Agent authority and must win over this
	// request-side fallback.
	if s.modeRevision == modeRevision {
		s.modeState.CurrentModeID = modeID
		s.modeRevision++
	}
	state = cloneModeState(s.modeState)
	s.mu.Unlock()
	return state, nil
}

func (s *Session) installModes(state *acp.SessionModeState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.modeState = modeStateFromACP(state)
	s.modeRevision++
	s.mu.Unlock()
}

func (s *Session) updateCurrentMode(sessionID acp.SessionId, modeID acp.SessionModeId) ModeState {
	if s == nil || sessionID != s.sessionID {
		return ModeState{Supported: false}
	}
	s.mu.Lock()
	if s.modeState.Supported {
		s.modeState.CurrentModeID = string(modeID)
	}
	s.modeRevision++
	state := cloneModeState(s.modeState)
	s.mu.Unlock()
	return state
}

func (s *Session) replaceAvailableCommands(sessionID acp.SessionId, commands []AvailableCommandInfo) []AvailableCommandInfo {
	if s == nil || sessionID != s.sessionID {
		return nil
	}
	s.mu.Lock()
	s.availableCommands = cloneAvailableCommands(commands)
	out := cloneAvailableCommands(s.availableCommands)
	s.mu.Unlock()
	return out
}
