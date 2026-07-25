// Package agent defines the stream events and turn-owned wire vocabulary
// shared by agent runtimes and the conversation layer.
package agent

import (
	"encoding/json"
	"strings"
	"time"
)

// StreamEventType identifies the kind of stream event.
type StreamEventType string

const (
	AgentStart          StreamEventType = "agent_start"
	TextStart           StreamEventType = "text_start"
	TextDelta           StreamEventType = "text_delta"
	TextEnd             StreamEventType = "text_end"
	ReasoningStart      StreamEventType = "reasoning_start"
	ReasoningDelta      StreamEventType = "reasoning_delta"
	ReasoningEnd        StreamEventType = "reasoning_end"
	ToolCallInputStart  StreamEventType = "tool_call_input_start"
	ToolCallStart       StreamEventType = "tool_call_start"
	ToolCallMetadata    StreamEventType = "tool_call_metadata"
	ToolCallProgress    StreamEventType = "tool_call_progress"
	ToolCallEnd         StreamEventType = "tool_call_end"
	ToolApprovalRequest StreamEventType = "tool_approval_request"
	UserInputRequest    StreamEventType = "user_input_request"
	AttachmentDelta     StreamEventType = "attachment_delta"
	Reaction            StreamEventType = "reaction_delta"
	Speech              StreamEventType = "speech_delta"
	AgentEnd            StreamEventType = "agent_end"
	AgentAbort          StreamEventType = "agent_abort"
	Retry               StreamEventType = "retry"
	Progress            StreamEventType = "progress"
	Error               StreamEventType = "error"
)

// StreamEvent is emitted by an agent runtime during streaming. The JSON
// shape is the wire format WebSocket clients consume; do not change tags.
type StreamEvent struct {
	Type           StreamEventType  `json:"type"`
	Delta          string           `json:"delta,omitempty"`
	ToolName       string           `json:"toolName,omitempty"`
	ToolCallID     string           `json:"toolCallId,omitempty"`
	ApprovalID     string           `json:"approvalId,omitempty"`
	UserInputID    string           `json:"userInputId,omitempty"`
	ShortID        int              `json:"shortId,omitempty"`
	Status         string           `json:"status,omitempty"`
	Input          any              `json:"input,omitempty"`
	Metadata       map[string]any   `json:"metadata,omitempty"`
	Progress       any              `json:"progress,omitempty"`
	Result         any              `json:"result,omitempty"`
	Attachments    []FileAttachment `json:"attachments,omitempty"`
	Reactions      []ReactionItem   `json:"reactions,omitempty"`
	Speeches       []SpeechItem     `json:"speeches,omitempty"`
	Messages       json.RawMessage  `json:"messages,omitempty"`
	Usage          json.RawMessage  `json:"usage,omitempty"`
	Reasoning      []string         `json:"reasoning,omitempty"`
	Error          string           `json:"error,omitempty"`
	Attempt        int              `json:"attempt,omitempty"`
	MaxAttempt     int              `json:"maxAttempt,omitempty"`
	RetryError     string           `json:"retryError,omitempty"`
	StepNumber     int              `json:"stepNumber,omitempty"`
	TotalSteps     int              `json:"totalSteps,omitempty"`
	ProgressStatus string           `json:"progressStatus,omitempty"`
}

// IsTerminal returns true for events that signal end of stream.
func (e StreamEvent) IsTerminal() bool {
	return e.Type == AgentEnd || e.Type == AgentAbort
}

// FileAttachment represents a file reference extracted from agent output.
type FileAttachment struct {
	Type        string         `json:"type"`
	Base64      string         `json:"base64,omitempty"`
	Path        string         `json:"path,omitempty"`
	URL         string         `json:"url,omitempty"`
	PlatformKey string         `json:"platform_key,omitempty"`
	Mime        string         `json:"mime,omitempty"`
	Name        string         `json:"name,omitempty"`
	ContentHash string         `json:"content_hash,omitempty"`
	Size        int64          `json:"size,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ReactionItem represents an emoji reaction extracted from agent output.
type ReactionItem struct {
	Emoji string `json:"emoji"`
}

// SpeechItem represents a TTS request extracted from agent output.
type SpeechItem struct {
	Text string `json:"text"`
}

const (
	ConversationTypePrivate = "private"
	ConversationTypeGroup   = "group"
	ConversationTypeThread  = "thread"
)

// NormalizeConversationType normalizes delivery-specific aliases into the
// application-level conversation vocabulary carried by turn commands.
func NormalizeConversationType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "p2p", "direct", ConversationTypePrivate:
		return ConversationTypePrivate
	case ConversationTypeThread:
		return ConversationTypeThread
	case ConversationTypeGroup:
		return ConversationTypeGroup
	default:
		return ConversationTypeGroup
	}
}

// IsPrivateConversationType reports whether raw describes a direct
// conversation.
func IsPrivateConversationType(raw string) bool {
	return NormalizeConversationType(raw) == ConversationTypePrivate
}

// Attachment is a media attachment carried in a turn request.
type Attachment struct {
	Type        string         `json:"type"`
	Base64      string         `json:"base64,omitempty"`
	Path        string         `json:"path,omitempty"`
	URL         string         `json:"url,omitempty"`
	PlatformKey string         `json:"platform_key,omitempty"`
	ContentHash string         `json:"content_hash,omitempty"`
	Name        string         `json:"name,omitempty"`
	Mime        string         `json:"mime,omitempty"`
	Size        int64          `json:"size,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// RequestedSkillContext is the full skill material made available to one turn.
// Its fields are internal execution context and are intentionally omitted from
// JSON payloads.
type RequestedSkillContext struct {
	Name           string `json:"-"`
	Description    string `json:"-"`
	Content        string `json:"-"`
	SourceKind     string `json:"-"`
	OpaqueSourceID string `json:"-"`
	ContentHash    string `json:"-"`
	Identity       string `json:"-"`
}

// UserMessageKindSkillActivation identifies persisted skill activation messages.
const UserMessageKindSkillActivation = "skill_activation"

// SkillActivationSkill is one effective skill named by an activation message.
type SkillActivationSkill struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	SourceKind  string `json:"source_kind,omitempty"`
	State       string `json:"state,omitempty"`
} // @name conversation.SkillActivationSkill

// SkillActivation is the stable user-message payload for skills activated on a
// turn.
type SkillActivation struct {
	Skills []SkillActivationSkill `json:"skills,omitempty"`
	Prompt string                 `json:"prompt,omitempty"`
} // @name conversation.SkillActivation

// NewSkillActivation constructs a deduplicated activation payload.
func NewSkillActivation(items []RequestedSkillContext, prompt string) *SkillActivation {
	activation := &SkillActivation{Prompt: strings.TrimSpace(prompt)}
	seen := map[string]struct{}{}
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		key := strings.TrimSpace(item.Identity)
		if key == "" {
			key = name + "\x00" + strings.TrimSpace(item.SourceKind)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		activation.Skills = append(activation.Skills, SkillActivationSkill{
			Name:        name,
			DisplayName: name,
			Description: strings.TrimSpace(item.Description),
			SourceKind:  strings.TrimSpace(item.SourceKind),
			State:       "effective",
		})
	}
	if len(activation.Skills) == 0 && activation.Prompt == "" {
		return nil
	}
	return activation
}

// SkillActivationModelQuery renders the user query represented by an
// activation payload.
func SkillActivationModelQuery(activation *SkillActivation) string {
	if activation == nil {
		return ""
	}
	if prompt := strings.TrimSpace(activation.Prompt); prompt != "" {
		return prompt
	}
	names := make([]string, 0, len(activation.Skills))
	for _, skill := range activation.Skills {
		name := strings.TrimSpace(skill.DisplayName)
		if name == "" {
			name = strings.TrimSpace(skill.Name)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "The user activated the following skill for this turn without an additional prompt: " +
		strings.Join(names, ", ") + "."
}

// OutboundAssetRef carries an asset reference accumulated during outbound
// streaming.
type OutboundAssetRef struct {
	ContentHash string
	Role        string
	Ordinal     int
	Mime        string
	SizeBytes   int64
	StorageKey  string
	Name        string
	Metadata    map[string]any
}

// InjectMessage carries a user message to inject into a running agent stream
// between tool rounds.
type InjectMessage struct {
	Text            string
	Attachments     []Attachment
	HeaderifiedText string
	Applied         func()
}

// ModelMessage is the canonical message format exchanged at the turn boundary.
type ModelMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	Usage      json.RawMessage `json:"-"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

// TextContent extracts plain text from string or multipart content.
func (m ModelMessage) TextContent() string {
	if len(m.Content) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(m.Content, &text); err == nil {
		return text
	}
	var parts []ContentPart
	if err := json.Unmarshal(m.Content, &parts); err == nil {
		texts := make([]string, 0, len(parts))
		for _, part := range parts {
			if part.Type == "reasoning" {
				continue
			}
			if strings.TrimSpace(part.Text) != "" {
				texts = append(texts, part.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

// ContentParts parses multipart content, returning nil for strings or invalid
// JSON.
func (m ModelMessage) ContentParts() []ContentPart {
	if len(m.Content) == 0 {
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return nil
	}
	return parts
}

// HasContent reports whether the message carries non-empty content or tool
// calls.
func (m ModelMessage) HasContent() bool {
	if strings.TrimSpace(m.TextContent()) != "" {
		return true
	}
	if len(m.ContentParts()) > 0 {
		return true
	}
	return len(m.ToolCalls) > 0
}

// NewTextContent creates a JSON string value from plain text.
func NewTextContent(text string) json.RawMessage {
	data, err := json.Marshal(text)
	if err != nil {
		return nil
	}
	return data
}

// AssistantOutput holds extracted assistant content for downstream consumers.
type AssistantOutput struct {
	Content string
	Parts   []ContentPart
}

// ContentPart is one element of multipart message content.
type ContentPart struct {
	Type              string         `json:"type"`
	Text              string         `json:"text,omitempty"`
	URL               string         `json:"url,omitempty"`
	Styles            []string       `json:"styles,omitempty"`
	Language          string         `json:"language,omitempty"`
	ChannelIdentityID string         `json:"channel_identity_id,omitempty"`
	Emoji             string         `json:"emoji,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// HasValue reports whether the content part carries a meaningful value.
func (p ContentPart) HasValue() bool {
	return strings.TrimSpace(p.Text) != "" ||
		strings.TrimSpace(p.URL) != "" ||
		strings.TrimSpace(p.Emoji) != ""
}

// ToolCall represents a function/tool invocation in an assistant message.
type ToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the name and serialized arguments of a tool call.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// QuestionAnswer is the user's answer to one ask_user question.
type QuestionAnswer struct {
	QuestionID string   `json:"question_id"`
	OptionIDs  []string `json:"option_ids,omitempty"`
	CustomText string   `json:"custom_text,omitempty"`
	Text       string   `json:"text,omitempty"`
	Skipped    bool     `json:"skipped,omitempty"`
}

// ExtractAssistantOutputs collects assistant-role outputs from a slice of ModelMessages.
func ExtractAssistantOutputs(messages []ModelMessage) []AssistantOutput {
	if len(messages) == 0 {
		return nil
	}
	outputs := make([]AssistantOutput, 0, len(messages))
	for _, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		if HasToolCallContent(msg) {
			continue
		}
		rawParts := msg.ContentParts()
		parts := filterVisibleContentParts(rawParts)
		content := visibleContentText(parts)
		if len(rawParts) == 0 {
			content = strings.TrimSpace(msg.TextContent())
		}
		if content == "" && len(parts) == 0 {
			continue
		}
		outputs = append(outputs, AssistantOutput{Content: content, Parts: parts})
	}
	return outputs
}

func HasToolCallContent(msg ModelMessage) bool {
	if len(msg.ToolCalls) > 0 {
		return true
	}
	for _, p := range msg.ContentParts() {
		if p.Type == "tool-call" {
			return true
		}
	}
	return false
}

func filterVisibleContentParts(parts []ContentPart) []ContentPart {
	if len(parts) == 0 {
		return nil
	}
	filtered := make([]ContentPart, 0, len(parts))
	for _, p := range parts {
		if isVisibleContentPart(p) {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

func isVisibleContentPart(part ContentPart) bool {
	if !part.HasValue() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(part.Type)) {
	case "reasoning", "tool-call", "tool-result":
		return false
	default:
		return true
	}
}

func visibleContentText(parts []ContentPart) string {
	if len(parts) == 0 {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(visibleContentPartText(part))
		if text == "" {
			continue
		}
		texts = append(texts, text)
	}
	return strings.TrimSpace(strings.Join(texts, "\n"))
}

func visibleContentPartText(part ContentPart) string {
	if strings.TrimSpace(part.Text) != "" {
		return part.Text
	}
	if strings.TrimSpace(part.URL) != "" {
		return part.URL
	}
	if strings.TrimSpace(part.Emoji) != "" {
		return part.Emoji
	}
	return ""
}

// UserMessageMeta holds the structured metadata attached to every user
// message. It is the single source of truth for the XML message tag sent to the LLM.
type UserMessageMeta struct {
	MessageID         string   `json:"message-id,omitempty"`
	ChannelIdentityID string   `json:"channel-identity-id"`
	DisplayName       string   `json:"display-name"`
	Channel           string   `json:"channel"`
	ConversationType  string   `json:"conversation-type"`
	ConversationName  string   `json:"conversation-name,omitempty"`
	Target            string   `json:"target,omitempty"`
	Time              string   `json:"time"`
	Timezone          string   `json:"timezone,omitempty"`
	AttachmentPaths   []string `json:"attachments"`
}

// UserMessageHeaderInput is the unified input for building user message headers.
// Keeping this as a struct avoids long positional argument lists and makes
// future metadata extension backward-compatible for call sites.
type UserMessageHeaderInput struct {
	MessageID         string
	ChannelIdentityID string
	DisplayName       string
	Channel           string
	ConversationType  string
	ConversationName  string
	Target            string
	AttachmentPaths   []string
	Time              time.Time
	Timezone          string
}

// BuildUserMessageMetaFromInput constructs metadata from one cohesive input.
func BuildUserMessageMetaFromInput(input UserMessageHeaderInput) UserMessageMeta {
	attachmentPaths := input.AttachmentPaths
	if attachmentPaths == nil {
		attachmentPaths = []string{}
	}
	meta := UserMessageMeta{
		MessageID:         input.MessageID,
		ChannelIdentityID: input.ChannelIdentityID,
		DisplayName:       input.DisplayName,
		Channel:           input.Channel,
		ConversationType:  input.ConversationType,
		ConversationName:  input.ConversationName,
		Target:            strings.TrimSpace(input.Target),
		Time:              time.Now().UTC().Format(time.RFC3339),
		Timezone:          strings.TrimSpace(input.Timezone),
		AttachmentPaths:   attachmentPaths,
	}
	if !input.Time.IsZero() {
		meta.Time = input.Time.Format(time.RFC3339)
	}
	return meta
}

// BuildUserMessageMetaWithTime constructs metadata with an explicit timestamp
// and timezone label for user-facing prompts.
func BuildUserMessageMetaWithTime(messageID, channelIdentityID, displayName, channel, conversationType, conversationName string, attachmentPaths []string, now time.Time, timezone string) UserMessageMeta {
	meta := BuildUserMessageMetaFromInput(UserMessageHeaderInput{
		MessageID:         messageID,
		ChannelIdentityID: channelIdentityID,
		DisplayName:       displayName,
		Channel:           channel,
		ConversationType:  conversationType,
		ConversationName:  conversationName,
		AttachmentPaths:   attachmentPaths,
		Time:              now,
		Timezone:          timezone,
	})
	if !now.IsZero() {
		meta.Time = now.Format(time.RFC3339)
	}
	meta.Timezone = strings.TrimSpace(timezone)
	return meta
}

// ToMap returns the metadata as a map with the same keys used in the XML
// attributes, suitable for storing as inbox content JSONB.
func (m UserMessageMeta) ToMap() map[string]any {
	result := map[string]any{
		"channel-identity-id": m.ChannelIdentityID,
		"display-name":        m.DisplayName,
		"channel":             m.Channel,
		"conversation-type":   m.ConversationType,
		"time":                m.Time,
		"attachments":         m.AttachmentPaths,
	}
	if m.MessageID != "" {
		result["message-id"] = m.MessageID
	}
	if m.ConversationName != "" {
		result["conversation-name"] = m.ConversationName
	}
	if m.Target != "" {
		result["target"] = m.Target
	}
	if strings.TrimSpace(m.Timezone) != "" {
		result["timezone"] = m.Timezone
	}
	return result
}

// FormatUserHeader wraps a user query in an XML <message> tag so the LLM sees
// structured context (sender, channel, conversation, time, attachments)
// alongside the raw message. This must be the single source of truth for
// user-message formatting — the agent gateway must NOT add its own header.
func FormatUserHeader(input UserMessageHeaderInput, query string) string {
	meta := BuildUserMessageMetaFromInput(input)
	return FormatUserHeaderFromMeta(meta, query)
}

// FormatUserHeaderFromMeta formats a pre-built UserMessageMeta into the
// XML <message> string sent to the LLM.
func FormatUserHeaderFromMeta(meta UserMessageMeta, query string) string {
	var sb strings.Builder

	sb.WriteString("<message")
	if meta.MessageID != "" {
		writeXMLAttr(&sb, "id", meta.MessageID)
	}
	writeXMLAttr(&sb, "sender", meta.DisplayName)
	writeXMLAttr(&sb, "t", meta.Time)
	writeXMLAttr(&sb, "channel", meta.Channel)
	if meta.ConversationName != "" {
		writeXMLAttr(&sb, "conversation", meta.ConversationName)
	}
	if meta.ConversationType != "" {
		writeXMLAttr(&sb, "type", meta.ConversationType)
	}
	if meta.Target != "" {
		writeXMLAttr(&sb, "target", meta.Target)
	}
	sb.WriteString(">\n")

	if len(meta.AttachmentPaths) > 0 {
		for _, p := range meta.AttachmentPaths {
			sb.WriteString("<attachment path=\"")
			sb.WriteString(escapeXMLAttr(p))
			sb.WriteString("\"/>\n")
		}
	}

	sb.WriteString(query)
	sb.WriteString("\n</message>")
	return sb.String()
}

// escapeXMLAttr escapes a string for use inside an XML attribute value.
func escapeXMLAttr(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
	)
	return r.Replace(s)
}

func writeXMLAttr(sb *strings.Builder, key, value string) {
	sb.WriteByte(' ')
	sb.WriteString(key)
	sb.WriteString("=\"")
	sb.WriteString(escapeXMLAttr(value))
	sb.WriteByte('"')
}
