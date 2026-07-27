package mcp

import (
	"strings"
	"sync"

	"github.com/memohai/memoh/internal/agent/event"
)

// ToolSessionContextStore keeps the latest per-prompt context for long-lived
// ACP MCP sessions whose HTTP headers are fixed when the agent process starts.
type ToolSessionContextStore struct {
	mu       sync.RWMutex
	sessions map[string]ToolSessionContext
	sinks    map[string]*toolEventSinkEntry
}

type toolEventSinkEntry struct {
	sink func(ToolStreamEvent)
}

func NewToolSessionContextStore() *ToolSessionContextStore {
	return &ToolSessionContextStore{
		sessions: map[string]ToolSessionContext{},
		sinks:    map[string]*toolEventSinkEntry{},
	}
}

// ToolStreamEvent is the stream-safe subset of MCP tools/call lifecycle
// events that ACP HTTP MCP calls record for live UI replay and persistence.
type ToolStreamEvent struct {
	Type       string `json:"type"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Input      any    `json:"input,omitempty"`
	Result     any    `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	// Approval request fields (Type "tool_approval_request").
	ApprovalID string `json:"approval_id,omitempty"`
	// Interactive request fields (Type "user_input_request"). Carrying the
	// pending interaction over the same channel as tool_call_start lets the UI
	// attach it to the existing tool call block instead of rendering a
	// separate synthetic message.
	UserInputID string         `json:"user_input_id,omitempty"`
	ShortID     int            `json:"short_id,omitempty"`
	Status      string         `json:"status,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

func (e ToolStreamEvent) ToAgentStreamEvent() (event.StreamEvent, bool) {
	typ := event.StreamEventType(strings.TrimSpace(e.Type))
	switch typ {
	case event.ToolCallStart, event.ToolCallEnd:
		return event.StreamEvent{
			Type:       typ,
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Input:      e.Input,
			Result:     e.Result,
			Error:      e.Error,
		}, true
	case event.ToolApprovalRequest:
		return event.StreamEvent{
			Type:       typ,
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Input:      e.Input,
			ApprovalID: e.ApprovalID,
			ShortID:    e.ShortID,
			Status:     e.Status,
			Metadata:   e.Metadata,
		}, true
	case event.UserInputRequest:
		return event.StreamEvent{
			Type:        typ,
			ToolCallID:  e.ToolCallID,
			ToolName:    e.ToolName,
			Input:       e.Input,
			UserInputID: e.UserInputID,
			ShortID:     e.ShortID,
			Status:      e.Status,
			Metadata:    e.Metadata,
		}, true
	default:
		return event.StreamEvent{}, false
	}
}

func (s *ToolSessionContextStore) Put(session ToolSessionContext) {
	if s == nil {
		return
	}
	session.BotID = strings.TrimSpace(session.BotID)
	session.SessionID = strings.TrimSpace(session.SessionID)
	key := toolSessionContextKey(session.BotID, session.SessionID)
	if key == "" {
		return
	}
	s.mu.Lock()
	if existing, ok := s.sessions[key]; ok {
		session = MergeToolSessionContext(existing, session)
	}
	s.sessions[key] = session
	s.mu.Unlock()
}

func (s *ToolSessionContextStore) Merge(session ToolSessionContext) ToolSessionContext {
	if s == nil {
		return session
	}
	key := toolSessionContextKey(session.BotID, session.SessionID)
	if key == "" {
		return session
	}
	s.mu.RLock()
	latest, ok := s.sessions[key]
	s.mu.RUnlock()
	if !ok {
		return session
	}
	return MergeToolSessionContext(session, latest)
}

func (s *ToolSessionContextStore) CloseSession(sessionID string) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	for key := range s.sessions {
		if toolSessionKeyHasSessionID(key, sessionID) {
			delete(s.sessions, key)
		}
	}
	for key := range s.sinks {
		if toolStreamEventKeyHasSessionID(key, sessionID) {
			delete(s.sinks, key)
		}
	}
	s.mu.Unlock()
}

func (s *ToolSessionContextStore) AppendToolEvent(session ToolSessionContext, event ToolStreamEvent) bool {
	if s == nil {
		return false
	}
	key := toolRunEventKey(session.BotID, session.SessionID, session.RunID)
	if key == "" {
		return false
	}
	event.Type = strings.TrimSpace(event.Type)
	event.ToolCallID = strings.TrimSpace(event.ToolCallID)
	event.ToolName = strings.TrimSpace(event.ToolName)
	if event.Type == "" || event.ToolCallID == "" || event.ToolName == "" {
		return false
	}
	s.mu.RLock()
	entry := s.sinks[key]
	s.mu.RUnlock()
	if entry != nil && entry.sink != nil {
		entry.sink(event)
		return true
	}
	return false
}

func (s *ToolSessionContextStore) RegisterToolEventSink(session ToolSessionContext, sink func(ToolStreamEvent)) func() {
	if s == nil || sink == nil {
		return func() {}
	}
	key := toolRunEventKey(session.BotID, session.SessionID, session.RunID)
	if key == "" {
		return func() {}
	}
	entry := &toolEventSinkEntry{sink: sink}
	s.mu.Lock()
	s.sinks[key] = entry
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if current := s.sinks[key]; current == entry {
			delete(s.sinks, key)
		}
		s.mu.Unlock()
	}
}

func toolSessionContextKey(botID, sessionID string) string {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		return ""
	}
	return botID + "\x00" + sessionID
}

func toolRunEventKey(botID, sessionID, runID string) string {
	sessionKey := toolSessionContextKey(botID, sessionID)
	runID = strings.TrimSpace(runID)
	if sessionKey == "" || runID == "" {
		return ""
	}
	return sessionKey + "\x00" + runID
}

func toolSessionKeyHasSessionID(key, sessionID string) bool {
	parts := strings.Split(key, "\x00")
	return len(parts) == 2 && parts[1] == sessionID
}

func toolStreamEventKeyHasSessionID(key, sessionID string) bool {
	parts := strings.Split(key, "\x00")
	return len(parts) == 3 && parts[1] == sessionID
}

// MergeToolSessionContext overlays every non-empty field of latest onto base
// (bools are sticky-true). It is the single merge for ToolSessionContext -
// used by the tool-context store and the ACP client callbacks - so a new
// field only needs to be wired up here.
func MergeToolSessionContext(base, latest ToolSessionContext) ToolSessionContext {
	merged := base
	if value := strings.TrimSpace(latest.BotID); value != "" {
		merged.BotID = value
	}
	if value := strings.TrimSpace(latest.ChatID); value != "" {
		merged.ChatID = value
	}
	if value := strings.TrimSpace(latest.RuntimeID); value != "" {
		merged.RuntimeID = value
	}
	if value := strings.TrimSpace(latest.RuntimeToken); value != "" {
		merged.RuntimeToken = value
	}
	if value := strings.TrimSpace(latest.SessionID); value != "" {
		merged.SessionID = value
	}
	if value := strings.TrimSpace(latest.RunID); value != "" {
		merged.RunID = value
	}
	if value := strings.TrimSpace(latest.SessionType); value != "" {
		merged.SessionType = value
	}
	if value := strings.TrimSpace(latest.RouteID); value != "" {
		merged.RouteID = value
	}
	if value := strings.TrimSpace(latest.ChannelIdentityID); value != "" {
		merged.ChannelIdentityID = value
	}
	if value := strings.TrimSpace(latest.SessionToken); value != "" {
		merged.SessionToken = value
	}
	if value := strings.TrimSpace(latest.CurrentPlatform); value != "" {
		merged.CurrentPlatform = value
	}
	if value := strings.TrimSpace(latest.ReplyTarget); value != "" {
		merged.ReplyTarget = value
	}
	if value := strings.TrimSpace(latest.ConversationType); value != "" {
		merged.ConversationType = value
	}
	if latest.CanRequestUserInput {
		merged.CanRequestUserInput = true
	}
	if latest.CanListUserInput {
		merged.CanListUserInput = true
	}
	if latest.IsSubagent {
		merged.IsSubagent = true
	}
	if latest.RuntimeActive {
		merged.RuntimeActive = true
	}
	if latest.SupportsImageInput {
		merged.SupportsImageInput = true
	}
	if latest.RuntimeFence.Valid() {
		merged.RuntimeFence = latest.RuntimeFence
	}
	if latest.RunContext != nil {
		merged.RunContext = latest.RunContext
	}
	if latest.RuntimeGuard != nil {
		merged.RuntimeGuard = latest.RuntimeGuard
	}
	return merged
}
