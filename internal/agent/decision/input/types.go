package input

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	ToolNameAskUser = "ask_user"

	// Provider sources record which surface created a request, for audit and
	// display only. Nothing classifies on them: whether an answer feeds an
	// in-process waiter or a native continuation is decided by the session's
	// runtime (runtimekind.UsesDecisionWaiter), the same as tool approvals.
	ProviderSourceACPMCP           = "acp_mcp"
	ProviderSourceACPElicitation   = "acp_elicitation"
	ProviderSourceCodexUserInput   = "codex_request_user_input"
	ProviderSourceCodexElicitation = "codex_mcp_elicitation"

	StatusPending   = "pending"
	StatusSubmitted = "submitted"
	StatusCanceled  = "canceled"
	StatusExpired   = "expired"
	StatusFailed    = "failed"

	DeferredKind = "user_input"

	// PayloadVersion is the canonical ask_user payload version written to
	// storage. Older rows are upgraded on read by PayloadFromStored.
	PayloadVersion = 2

	QuestionKindSingleSelect = "single_select"
	QuestionKindMultiSelect  = "multi_select"
	QuestionKindText         = "text"

	MaxQuestionsPerRequest = 4
	MinOptionsPerQuestion  = 2
	MaxOptionsPerQuestion  = 20
)

var (
	ErrNotFound       = errors.New("user input request not found")
	ErrAlreadyDecided = errors.New("user input request already decided")
	ErrForbidden      = errors.New("user input forbidden")
)

type CreatePendingInput struct {
	BotID                        string
	SessionID                    string
	RouteID                      string
	ChannelIdentityID            string
	WorkspaceTargetID            string
	RequestedByChannelIdentityID string
	ToolCallID                   string
	ToolName                     string
	Input                        any
	ProviderMetadata             map[string]any
	SourcePlatform               string
	ReplyTarget                  string
	ConversationType             string
	ExpiresAt                    *time.Time
}

type ResolveInput struct {
	BotID                  string
	SessionID              string
	ExplicitID             string
	ReplyExternalMessageID string
}

// QuestionAnswer is the user's answer to a single question of an ask_user
// request. Selections are always arrays; single_select is an array of one.
type QuestionAnswer struct {
	QuestionID string   `json:"question_id"`
	OptionIDs  []string `json:"option_ids,omitempty"`
	CustomText string   `json:"custom_text,omitempty"`
	Text       string   `json:"text,omitempty"`
	Skipped    bool     `json:"skipped,omitempty"`
}

type SubmitInput struct {
	RequestID              string
	ActorChannelIdentityID string
	Answers                []QuestionAnswer
}

type CancelInput struct {
	RequestID              string
	ActorChannelIdentityID string
	Reason                 string
}

type Request struct {
	ID                      string               `json:"id"`
	BotID                   string               `json:"bot_id"`
	SessionID               string               `json:"session_id"`
	RouteID                 string               `json:"route_id,omitempty"`
	ChannelIdentityID       string               `json:"channel_identity_id,omitempty"`
	WorkspaceTargetID       string               `json:"workspace_target_id,omitempty"`
	ToolCallID              string               `json:"tool_call_id"`
	ToolName                string               `json:"tool_name"`
	ShortID                 int                  `json:"short_id"`
	Status                  string               `json:"status"`
	Input                   map[string]any       `json:"input,omitempty"`
	UIPayload               UIPayload            `json:"ui_payload"`
	Interaction             TextInteractionState `json:"interaction"`
	InteractionRevision     int                  `json:"interaction_revision"`
	Result                  map[string]any       `json:"result,omitempty"`
	ProviderMetadata        map[string]any       `json:"-"`
	PromptExternalMessageID string               `json:"prompt_external_message_id,omitempty"`
	SourcePlatform          string               `json:"source_platform,omitempty"`
	ReplyTarget             string               `json:"reply_target,omitempty"`
	ConversationType        string               `json:"conversation_type,omitempty"`
	ExpiresAt               *time.Time           `json:"expires_at,omitempty"`
	CreatedAt               time.Time            `json:"created_at"`
	RespondedAt             *time.Time           `json:"responded_at,omitempty"`
	CanceledAt              *time.Time           `json:"canceled_at,omitempty"`
	RuntimeFenced           bool                 `json:"-"`
}

// TextInteractionState is the durable ask_user cursor shared by every input
// surface — plain-text replies (AdvanceText) and native buttons
// (AdvanceInteraction). Answers remain present when the user moves backward.
type TextInteractionState struct {
	QuestionIndex int              `json:"question_index"`
	Answers       []QuestionAnswer `json:"answers,omitempty"`
	Completed     bool             `json:"completed,omitempty"`
}

// Answer returns the saved answer for a question. ok is false when the user
// has not answered it yet (a persisted skip still counts as answered).
func (s TextInteractionState) Answer(questionID string) (QuestionAnswer, bool) {
	for _, answer := range s.Answers {
		if answer.QuestionID == questionID {
			return answer, true
		}
	}
	return QuestionAnswer{}, false
}

type AdvanceTextInput struct {
	BotID                  string
	SessionID              string
	ExplicitID             string
	ReplyExternalMessageID string
	Text                   string
}

type AdvanceTextResult struct {
	Handled bool
	Invalid bool
	Request Request
}

// UIPayload is the canonical, normalized ask_user payload (v2). It is the
// single shape stored, streamed, and rendered; ParseAskUserPayload is the
// only writer and PayloadFromStored the only reader.
type UIPayload struct {
	Version   int          `json:"version"`
	Questions []UIQuestion `json:"questions"`
}

// UIQuestion describes one question in the ask_user UI payload.
type UIQuestion struct {
	ID              string     `json:"id"`
	Text            string     `json:"text"`
	Kind            string     `json:"kind"`
	Options         []UIOption `json:"options,omitempty"`
	AllowCustom     bool       `json:"allow_custom,omitempty"`
	CustomExclusive bool       `json:"custom_exclusive,omitempty"`
	// Required is tri-state for compatibility. Legacy/native ask_user payloads
	// omit it and keep their existing surface semantics; ACP forms set it
	// explicitly so false means an optional schema property.
	Required    *bool  `json:"required,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
} // @name userinput.UIQuestion

// UIOption describes one selectable option in an ask_user question.
type UIOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
} // @name userinput.UIOption

// UIAnswer is the public, display-safe projection of one submitted answer.
// Model-only instructions remain in Request.Result and are never exposed here.
type UIAnswer struct {
	QuestionID string     `json:"question_id"`
	Question   string     `json:"question"`
	Selected   []UIOption `json:"selected,omitempty"`
	CustomText string     `json:"custom_text,omitempty"`
	Text       string     `json:"text,omitempty"`
	Skipped    bool       `json:"skipped,omitempty"`
} // @name userinput.UIAnswer

func (p UIPayload) Question(id string) (UIQuestion, bool) {
	for _, question := range p.Questions {
		if question.ID == id {
			return question, true
		}
	}
	return UIQuestion{}, false
}

func (q UIQuestion) Option(id string) (UIOption, bool) {
	for _, option := range q.Options {
		if option.ID == id {
			return option, true
		}
	}
	return UIOption{}, false
}

func ResultBytes(req Request) []byte {
	if len(req.Result) == 0 {
		return []byte("{}")
	}
	data, err := json.Marshal(req.Result)
	if err != nil {
		return []byte("{}")
	}
	return data
}
