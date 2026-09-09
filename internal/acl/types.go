package acl

import (
	"strings"
	"time"

	"github.com/felinics/memoh/internal/channel"
)

const (
	ActionChatTrigger = "chat.trigger"

	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// Rule is the full ACL rule record returned to callers.
type Rule struct {
	ID                          string       `json:"id"`
	BotID                       string       `json:"bot_id"`
	Enabled                     bool         `json:"enabled"`
	Description                 string       `json:"description,omitempty"`
	Action                      string       `json:"action"`
	Effect                      string       `json:"effect"`
	ChannelIdentityID           string       `json:"channel_identity_id,omitempty"`
	SubjectChannelType          string       `json:"subject_channel_type,omitempty"`
	SourceScope                 *SourceScope `json:"source_scope,omitempty"`
	ChannelType                 string       `json:"channel_type,omitempty"`
	ChannelSubjectID            string       `json:"channel_subject_id,omitempty"`
	ChannelIdentityDisplayName  string       `json:"channel_identity_display_name,omitempty"`
	ChannelIdentityAvatarURL    string       `json:"channel_identity_avatar_url,omitempty"`
	SourceConversationName      string       `json:"source_conversation_name,omitempty"`
	SourceConversationAvatarURL string       `json:"source_conversation_avatar_url,omitempty"`
	CreatedAt                   time.Time    `json:"created_at"`
	UpdatedAt                   time.Time    `json:"updated_at"`
}

type ListRulesResponse struct {
	Items []Rule `json:"items"`
}

// ManageOverride is a local Channel Access override of the Manage capability for a
// channel identity on a bot. Granted=true forces ON, Granted=false forces OFF
// (suppressing an inherited grant). Absence of a row means "inherit".
type ManageOverride struct {
	ID                         string    `json:"id"`
	BotID                      string    `json:"bot_id"`
	ChannelIdentityID          string    `json:"channel_identity_id"`
	Granted                    bool      `json:"granted"`
	ChannelType                string    `json:"channel_type,omitempty"`
	ChannelSubjectID           string    `json:"channel_subject_id,omitempty"`
	ChannelIdentityDisplayName string    `json:"channel_identity_display_name,omitempty"`
	ChannelIdentityAvatarURL   string    `json:"channel_identity_avatar_url,omitempty"`
	CreatedAt                  time.Time `json:"created_at"`
}

type DefaultEffectResponse struct {
	DefaultEffect string `json:"default_effect"`
}

// SourceScope narrows a rule to a specific conversation / thread.
// Any zero-value field means "match any".
// Channel filtering is handled at the rule target level.
type SourceScope struct {
	ConversationType string `json:"conversation_type,omitempty"`
	ConversationID   string `json:"conversation_id,omitempty"`
	ThreadID         string `json:"thread_id,omitempty"`
}

// CreateRuleRequest is used to create a new ACL rule.
type CreateRuleRequest struct {
	Enabled            bool         `json:"enabled"`
	Description        string       `json:"description,omitempty"`
	Effect             string       `json:"effect"`
	ChannelIdentityID  string       `json:"channel_identity_id,omitempty"`
	SubjectChannelType string       `json:"subject_channel_type,omitempty"`
	SourceScope        *SourceScope `json:"source_scope,omitempty"`
}

// UpdateRuleRequest is used to update an existing ACL rule.
type UpdateRuleRequest struct {
	Enabled            bool         `json:"enabled"`
	Description        string       `json:"description,omitempty"`
	Effect             string       `json:"effect"`
	ChannelIdentityID  string       `json:"channel_identity_id,omitempty"`
	SubjectChannelType string       `json:"subject_channel_type,omitempty"`
	SourceScope        *SourceScope `json:"source_scope,omitempty"`
}

// EvaluateRequest carries all context needed to evaluate a chat.trigger.
type EvaluateRequest struct {
	BotID             string
	ChannelIdentityID string
	ChannelType       string
	SourceScope       SourceScope
}

type ChannelIdentityCandidate struct {
	ID               string `json:"id"`
	Channel          string `json:"channel"`
	ChannelSubjectID string `json:"channel_subject_id"`
	DisplayName      string `json:"display_name,omitempty"`
	AvatarURL        string `json:"avatar_url,omitempty"`
}

type ChannelIdentityCandidateListResponse struct {
	Items []ChannelIdentityCandidate `json:"items"`
}

type ObservedConversationCandidate struct {
	RouteID               string    `json:"route_id"`
	Channel               string    `json:"channel"`
	ConversationType      string    `json:"conversation_type,omitempty"`
	ConversationID        string    `json:"conversation_id"`
	ThreadID              string    `json:"thread_id,omitempty"`
	ConversationName      string    `json:"conversation_name,omitempty"`
	ConversationAvatarURL string    `json:"conversation_avatar_url,omitempty"`
	LastObservedAt        time.Time `json:"last_observed_at"`
}

type ObservedConversationCandidateListResponse struct {
	Items []ObservedConversationCandidate `json:"items"`
}

func (s SourceScope) Normalize() SourceScope {
	scope := SourceScope{
		ConversationID: strings.TrimSpace(s.ConversationID),
		ThreadID:       strings.TrimSpace(s.ThreadID),
	}
	if raw := strings.TrimSpace(s.ConversationType); raw != "" {
		scope.ConversationType = channel.NormalizeConversationType(raw)
	}
	if scope.ThreadID != "" && scope.ConversationType == "" {
		scope.ConversationType = channel.ConversationTypeThread
	}
	return scope
}

func (s SourceScope) IsZero() bool {
	return strings.TrimSpace(s.ConversationType) == "" &&
		strings.TrimSpace(s.ConversationID) == "" &&
		strings.TrimSpace(s.ThreadID) == ""
}
