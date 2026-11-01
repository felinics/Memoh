package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/agent/tool/internal/toolset"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

// SkillDetail holds the description and content of a loadable skill.
type SkillDetail struct {
	Description string
	Content     string
	Path        string
}

// StreamEventType identifies the kind of stream event emitted by tools.
type StreamEventType string

const (
	StreamEventAttachment    StreamEventType = "attachment"
	StreamEventReaction      StreamEventType = "reaction"
	StreamEventSpeech        StreamEventType = "speech"
	StreamEventSpawnProgress StreamEventType = "spawn_progress"
	StreamEventToolApproval  StreamEventType = "tool_approval_request"
)

// ToolStreamEvent is a side-effect event emitted by a tool targeting the
// current conversation (e.g. inline attachment, reaction, or TTS speech).
// The agent framework converts these into the appropriate wire-level events.
type ToolStreamEvent struct {
	Approval *toolapproval.Request
	Type     StreamEventType
	// ToolCallID identifies the tool call that produced this side effect, so
	// downstream persistence can anchor it to the right message (and keep the
	// live ordering after a history reload). Empty when unknown.
	ToolCallID  string
	Attachments []Attachment
	Reactions   []Reaction
	Speeches    []Speech
}

// Attachment describes a file reference emitted by a tool.
type Attachment struct {
	Type        string         `json:"type"`
	Path        string         `json:"path,omitempty"`
	URL         string         `json:"url,omitempty"`
	Base64      string         `json:"base64,omitempty"`
	PlatformKey string         `json:"platform_key,omitempty"`
	Mime        string         `json:"mime,omitempty"`
	Name        string         `json:"name,omitempty"`
	ContentHash string         `json:"content_hash,omitempty"`
	Size        int64          `json:"size,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Reaction describes an emoji reaction emitted by a tool.
type Reaction struct {
	Emoji     string `json:"emoji"`
	MessageID string `json:"message_id"`
	Remove    bool   `json:"remove,omitempty"`
}

// Speech describes a TTS speech request emitted by a tool.
type Speech struct {
	Text string `json:"text"`
}

// StreamEmitter pushes a side-effect event into the current agent stream.
// Nil when no stream is available (e.g. subagent or non-streaming contexts
// where the caller collects events after generation).
type StreamEmitter func(ToolStreamEvent)

// MessageSnapshot holds the messages currently visible to the model. Agent
// steps update the snapshot before each model call; tools can safely read it
// while sibling tool calls execute concurrently.
//
// Storing costs one provider-neutral copy of the message and part headers: the
// payloads (text, tool argument and output bytes) are shared with the request,
// which treats them as immutable values. A step, a retry or an in-run fold
// replaces messages and parts; none writes into a payload. Store replaces the
// held slices and never writes into them, so a reader may keep using what it
// loaded. Readers receive an independent copy, paid only when a forked
// subagent asks for it.
type MessageSnapshot struct {
	mu               sync.RWMutex
	messages         []sdk.Message
	sourceMessageIDs []string
}

func NewMessageSnapshot(messages []sdk.Message) *MessageSnapshot {
	return NewMessageSnapshotWithSources(messages, nil)
}

// NewMessageSnapshotWithSources seeds a snapshot with the database message ID
// backing each message. Runtime-only messages use an empty source ID. Later
// Store calls preserve IDs only for unchanged messages that remain in order.
func NewMessageSnapshotWithSources(messages []sdk.Message, sourceMessageIDs []string) *MessageSnapshot {
	clean := providerNeutralMessages(messages)
	sources := make([]string, len(clean))
	copy(sources, sourceMessageIDs)
	return &MessageSnapshot{messages: clean, sourceMessageIDs: sources}
}

// Store records the messages of the provider attempt about to be dispatched.
func (s *MessageSnapshot) Store(messages []sdk.Message) {
	if s == nil {
		return
	}
	clean := providerNeutralMessages(messages)
	s.mu.Lock()
	s.sourceMessageIDs = retainSnapshotSources(s.messages, s.sourceMessageIDs, clean)
	s.messages = clean
	s.mu.Unlock()
}

// retainSnapshotSources carries a source ID forward to each new message whose
// provider-neutral JSON equals an old one, matching in order and taking the
// earliest unmatched old message. An attempt usually extends the previous
// request, and the in-order match of an identical prefix pairs each message
// with its own position, so the prefix is compared field by field and only
// messages after the first difference are compared as JSON.
func retainSnapshotSources(old []sdk.Message, oldSources []string, messages []sdk.Message) []string {
	sources := make([]string, len(messages))
	prefix := 0
	for prefix < len(old) && prefix < len(messages) && sameNeutralMessage(old[prefix], messages[prefix]) {
		sources[prefix] = oldSources[prefix]
		prefix++
	}
	old, oldSources = old[prefix:], oldSources[prefix:]
	messages, rest := messages[prefix:], sources[prefix:]
	if len(old) == 0 || len(messages) == 0 {
		return sources
	}
	positions := make(map[string][]int, len(old))
	for i, message := range old {
		key, ok := snapshotMessageKey(message)
		if ok {
			positions[key] = append(positions[key], i)
		}
	}
	cursors := make(map[string]int, len(positions))
	lastMatched := -1
	for i, message := range messages {
		key, ok := snapshotMessageKey(message)
		if !ok {
			continue
		}
		candidates := positions[key]
		cursor := cursors[key]
		for cursor < len(candidates) && candidates[cursor] <= lastMatched {
			cursor++
		}
		cursors[key] = cursor
		if cursor >= len(candidates) {
			continue
		}
		oldIndex := candidates[cursor]
		cursors[key] = cursor + 1
		lastMatched = oldIndex
		rest[i] = oldSources[oldIndex]
	}
	return sources
}

func snapshotMessageKey(message sdk.Message) (string, bool) {
	raw, err := json.Marshal(message)
	return string(raw), err == nil
}

// sameNeutralMessage reports whether two provider-neutral messages hold equal
// fields, which always encode to equal JSON. Payloads shared with the previous
// request compare by pointer, so an unchanged prefix costs no byte scan.
func sameNeutralMessage(a, b sdk.Message) bool {
	if a.Role != b.Role || len(a.Content) != len(b.Content) {
		return false
	}
	for i := range a.Content {
		if !sameNeutralPart(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

func sameNeutralPart(a, b sdk.MessagePart) bool {
	switch x := a.(type) {
	case sdk.TextPart:
		y, ok := b.(sdk.TextPart)
		return ok && x.Text == y.Text
	case sdk.ReasoningPart:
		y, ok := b.(sdk.ReasoningPart)
		return ok && x.Text == y.Text
	case sdk.ImagePart:
		y, ok := b.(sdk.ImagePart)
		return ok && x.Image == y.Image && x.MediaType == y.MediaType
	case sdk.FilePart:
		y, ok := b.(sdk.FilePart)
		return ok && x.Data == y.Data && x.MediaType == y.MediaType && x.Filename == y.Filename
	case sdk.ToolCallPart:
		y, ok := b.(sdk.ToolCallPart)
		return ok && x.ToolCallID == y.ToolCallID && x.ToolName == y.ToolName &&
			x.Input.Text == y.Input.Text && bytes.Equal(x.Input.JSON, y.Input.JSON)
	case sdk.ToolResultPart:
		y, ok := b.(sdk.ToolResultPart)
		return ok && x.ToolCallID == y.ToolCallID && x.ToolName == y.ToolName && x.IsError == y.IsError &&
			x.Result.Text == y.Result.Text && bytes.Equal(x.Result.JSON, y.Result.JSON) &&
			sameCacheControl(x.CacheControl, y.CacheControl)
	default:
		return false
	}
}

func sameCacheControl(a, b *sdk.CacheControl) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func providerNeutralMessages(messages []sdk.Message) []sdk.Message {
	out := make([]sdk.Message, 0, len(messages))
	for _, message := range messages {
		clean := sdk.Message{Role: message.Role, Content: make([]sdk.MessagePart, 0, len(message.Content))}
		for _, part := range message.Content {
			switch value := part.(type) {
			case sdk.TextPart:
				clean.Content = append(clean.Content, sdk.TextPart{Text: value.Text})
			case *sdk.TextPart:
				if value != nil {
					clean.Content = append(clean.Content, sdk.TextPart{Text: value.Text})
				}
			case sdk.ReasoningPart:
				clean.Content = append(clean.Content, sdk.ReasoningPart{Text: value.Text})
			case *sdk.ReasoningPart:
				if value != nil {
					clean.Content = append(clean.Content, sdk.ReasoningPart{Text: value.Text})
				}
			case sdk.ImagePart:
				clean.Content = append(clean.Content, sdk.ImagePart{Image: value.Image, MediaType: value.MediaType})
			case *sdk.ImagePart:
				if value != nil {
					clean.Content = append(clean.Content, sdk.ImagePart{Image: value.Image, MediaType: value.MediaType})
				}
			case sdk.FilePart:
				clean.Content = append(clean.Content, sdk.FilePart{Data: value.Data, MediaType: value.MediaType, Filename: value.Filename})
			case *sdk.FilePart:
				if value != nil {
					clean.Content = append(clean.Content, sdk.FilePart{Data: value.Data, MediaType: value.MediaType, Filename: value.Filename})
				}
			case sdk.ToolCallPart:
				clean.Content = append(clean.Content, sdk.ToolCallPart{ToolCallID: value.ToolCallID, ToolName: value.ToolName, Input: toolexec.ArgumentsFromValue(value.Input)})
			case *sdk.ToolCallPart:
				if value != nil {
					clean.Content = append(clean.Content, sdk.ToolCallPart{ToolCallID: value.ToolCallID, ToolName: value.ToolName, Input: toolexec.ArgumentsFromValue(value.Input)})
				}
			case sdk.ToolResultPart:
				clean.Content = append(clean.Content, value)
			case *sdk.ToolResultPart:
				if value != nil {
					clean.Content = append(clean.Content, *value)
				}
			default:
				clean.Content = append(clean.Content, part)
			}
		}
		out = append(out, clean)
	}
	return out
}

// Messages returns an independent copy of the provider-neutral messages.
func (s *MessageSnapshot) Messages() ([]sdk.Message, error) {
	if s == nil {
		return []sdk.Message{}, nil
	}
	s.mu.RLock()
	messages := s.messages
	s.mu.RUnlock()
	return copySnapshotMessages(messages)
}

// Entries returns the provider-neutral messages together with any persisted
// source message IDs retained from the resolver's initial history snapshot.
func (s *MessageSnapshot) Entries() ([]MessageSnapshotEntry, error) {
	if s == nil {
		return []MessageSnapshotEntry{}, nil
	}
	s.mu.RLock()
	messages, sources := s.messages, s.sourceMessageIDs
	s.mu.RUnlock()
	copied, err := copySnapshotMessages(messages)
	if err != nil {
		return nil, err
	}
	entries := make([]MessageSnapshotEntry, 0, len(copied))
	for i, message := range copied {
		entries = append(entries, MessageSnapshotEntry{
			Message:         message,
			SourceMessageID: sources[i],
		})
	}
	return entries, nil
}

// copySnapshotMessages decodes a JSON copy, so the reader owns every part and
// payload it receives.
func copySnapshotMessages(messages []sdk.Message) ([]sdk.Message, error) {
	raw, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	var copied []sdk.Message
	if err := json.Unmarshal(raw, &copied); err != nil {
		return nil, err
	}
	if copied == nil {
		copied = []sdk.Message{}
	}
	return copied, nil
}

// MessageSnapshotEntry describes one message in a forkable model context.
type MessageSnapshotEntry struct {
	Message         sdk.Message
	SourceMessageID string
}

// SessionContext carries request-scoped identity for tool execution.
type SessionContext struct {
	CapabilitiesChanged    func()
	ApprovalEmitter        func(toolapproval.Request) bool
	BotID                  string
	ChatID                 string
	SessionID              string
	SessionType            string
	UserID                 string
	ChannelIdentityID      string
	SessionToken           string //nolint:gosec // carries session credential material at runtime
	CurrentPlatform        string
	ReplyTarget            string
	ConversationType       string
	CanRequestUserInput    bool
	CanListUserInput       bool
	SupportsImageInput     bool
	SupportsFileInput      bool
	IsSubagent             bool
	CurrentModelUUID       string
	CurrentModelID         string
	CurrentModelProvider   string
	CurrentModelProviderID string
	// ReasoningStoredEffort and ReasoningRequestedEffort are the unresolved
	// parent-turn inputs. A tool that selects another model must resolve them for
	// that model rather than inheriting the parent's provider-specific decision.
	ReasoningStoredEffort    string
	ReasoningRequestedEffort string
	ForkContext              *MessageSnapshot
	// WorkspaceTargetID is the request-scoped default for file and command
	// tools. An explicit tool target_id still takes precedence.
	WorkspaceTargetID   string
	WorkspaceTargetKind string
	WorkspaceTargetName string
	// WorkdirPath is the session's immutable working directory. When set,
	// relative tool paths resolve under it and exec defaults its cwd to it.
	WorkdirPath            string
	Skills                 map[string]SkillDetail
	TimezoneLocation       *time.Location
	Emitter                StreamEmitter
	LiveStream             bool
	ContextBudgetMaxTokens int
}

// CanAskUser reports whether ask_user can be both shown to the model and
// delivered to the user in this run.
func (s SessionContext) CanAskUser() bool {
	return s.CanRequestUserInput && sessionmode.IsInteractive(s.SessionType)
}

// IsSameConversation reports whether the given platform+target pair refers to
// the conversation that the agent is currently replying to.
func (s SessionContext) IsSameConversation(platform, target string) bool {
	if strings.TrimSpace(s.ReplyTarget) == "" {
		return false
	}
	if platform == "" {
		platform = strings.TrimSpace(s.CurrentPlatform)
	}
	if target == "" {
		target = strings.TrimSpace(s.ReplyTarget)
	}
	return strings.EqualFold(platform, strings.TrimSpace(s.CurrentPlatform)) &&
		target == strings.TrimSpace(s.ReplyTarget)
}

// CanOmitMessagingTarget reports whether messaging tools can safely default to
// the current conversation. Background sessions may have no live reply target,
// so their usage guidance should ask for explicit platform/target instead.
func (s SessionContext) CanOmitMessagingTarget() bool {
	switch s.SessionType {
	case sessionmode.Schedule:
		return false
	default:
		return strings.TrimSpace(s.CurrentPlatform) != "" &&
			strings.TrimSpace(s.ReplyTarget) != ""
	}
}

// CanUseLocalMessagingShortcut reports whether current-conversation side
// effects can be represented by the live agent stream instead of the channel
// sender. Non-interactive runs must use the real sender even when their target
// equals the current conversation.
func (s SessionContext) CanUseLocalMessagingShortcut() bool {
	if !s.LiveStream || s.Emitter == nil || !s.CanOmitMessagingTarget() {
		return false
	}
	switch s.SessionType {
	case "", sessionmode.Chat:
		return true
	default:
		return false
	}
}

// FormatTime formats a time.Time using the session timezone (falls back to UTC).
func (s SessionContext) FormatTime(t time.Time) string {
	if s.TimezoneLocation != nil {
		t = t.In(s.TimezoneLocation)
	}
	return t.Format(time.RFC3339)
}

// ProviderLabeler optionally names the accounting bucket a provider's tool
// definitions belong to. Providers without it count as native tools.
type ProviderLabeler interface {
	ProviderLabel() string
}

// ToolProvider supplies a set of tools for the agent.
// Tools() is called per-request; implementations may return different
// tool sets based on session context (e.g. subagent restrictions, bot settings).
type ToolProvider interface {
	Tools(ctx context.Context, session SessionContext) ([]toolexec.Tool, error)
}

// AvailableTools is the set of tool names registered for the current session.
type AvailableTools = toolset.Available

func NewAvailableTools(tools []toolexec.Tool) AvailableTools {
	names := make([]ToolName, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if builtIn, ok := lookupBuiltInToolName(name); ok {
			names = append(names, builtIn)
		}
	}
	return toolset.New(names)
}

func usageSection(title string, items []string) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			lines = append(lines, "- "+item)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "### " + title + "\n\n" + strings.Join(lines, "\n")
}

func joinRefs(refs []string, conjunction string) string {
	switch len(refs) {
	case 0:
		return ""
	case 1:
		return refs[0]
	case 2:
		return refs[0] + " " + strings.TrimSpace(conjunction) + " " + refs[1]
	default:
		return strings.Join(refs[:len(refs)-1], ", ") + ", " + strings.TrimSpace(conjunction) + " " + refs[len(refs)-1]
	}
}

// ToolUsage is an optional capability a ToolProvider may also implement to
// contribute group-level usage guidance to the system prompt — how this set of
// tools is meant to be used together (e.g. "look up a target with get_contacts
// before messaging another conversation"). The agent injects the returned text
// only when the same provider actually returns tools for the session, so the
// guidance shares that provider's gating and stays in lockstep with the tools
// that provider registers. available contains the complete registered tool set
// for this session; use available.Ref/Refs before naming cross-provider tools.
// Return "" to contribute nothing.
type ToolUsage interface {
	Usage(ctx context.Context, session SessionContext, available AvailableTools) string
}

// ToolNotice lets a provider tell the model about tools it could not
// register this turn, for example a federated source whose upstream failed.
// Unlike ToolUsage the text is collected even when the provider contributed
// no tools, which is exactly when a silent gap would mislead the model.
// Return "" to contribute nothing.
type ToolNotice interface {
	ToolNotice(ctx context.Context, session SessionContext) string
}

// ---- argument parsing helpers ----

func StringArg(arguments map[string]any, key string) string {
	if arguments == nil {
		return ""
	}
	raw, ok := arguments[key]
	if !ok {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", raw))
	}
}

func IntArg(arguments map[string]any, key string) (int, bool, error) {
	if arguments == nil {
		return 0, false, nil
	}
	raw, ok := arguments[key]
	if !ok || raw == nil {
		return 0, false, nil
	}
	switch value := raw.(type) {
	case int:
		return value, true, nil
	case int64:
		if value < int64(math.MinInt) || value > int64(math.MaxInt) {
			return 0, true, fmt.Errorf("%s out of range", key)
		}
		return int(value), true, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, true, fmt.Errorf("%s must be a valid number", key)
		}
		if value < float64(math.MinInt) || value > float64(math.MaxInt) {
			return 0, true, fmt.Errorf("%s out of range", key)
		}
		return int(value), true, nil
	case json.Number:
		i, err := value.Int64()
		if err != nil {
			return 0, true, fmt.Errorf("%s must be an integer", key)
		}
		if i < int64(math.MinInt) || i > int64(math.MaxInt) {
			return 0, true, fmt.Errorf("%s out of range", key)
		}
		return int(i), true, nil
	default:
		return 0, true, fmt.Errorf("%s must be a number", key)
	}
}

// inputAsMap decodes the model's arguments as an object. Invalid arguments
// never reach a handler through the executor, so the fallback only covers a
// direct caller; a non-object document yields the empty object.
func inputAsMap(input sdk.ToolArguments) map[string]any {
	args := map[string]any{}
	if err := input.Unmarshal(&args); err != nil || args == nil {
		return map[string]any{}
	}
	return args
}
