package turn

import (
	"encoding/json"
	"strings"

	"github.com/felinics/memoh/internal/attachment"
)

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

// AttachmentFromBundle converts the shared internal bundle shape to the turn
// contract without normalizing or otherwise changing its values.
func AttachmentFromBundle(bundle attachment.Bundle) Attachment {
	return Attachment{
		Type:        bundle.Type,
		Base64:      bundle.Base64,
		Path:        bundle.Path,
		URL:         bundle.URL,
		PlatformKey: bundle.PlatformKey,
		ContentHash: bundle.ContentHash,
		Name:        bundle.Name,
		Mime:        bundle.Mime,
		Size:        bundle.Size,
		Metadata:    bundle.Metadata,
	}
}

// Bundle converts the turn attachment to the shared normalized attachment
// representation used by media ingestion and persistence.
func (a Attachment) Bundle() attachment.Bundle {
	return attachment.Bundle{
		Type:        a.Type,
		Base64:      a.Base64,
		Path:        a.Path,
		URL:         a.URL,
		PlatformKey: a.PlatformKey,
		ContentHash: a.ContentHash,
		Name:        a.Name,
		Mime:        a.Mime,
		Size:        a.Size,
		Metadata:    a.Metadata,
	}.Normalize()
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
