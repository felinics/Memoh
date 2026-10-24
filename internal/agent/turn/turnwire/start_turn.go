// Package turnwire is the serialized form of turn commands that leave the
// process that built them: the turn RPC between the channel and server
// processes, and the follow-up queue that stores a command until its session
// is free. Which fields cross is decided here, field by field, and not by the
// JSON tags of the turn contract types. Those types keep material such as
// requested skill bodies out of every public payload; a command that crosses
// still has to carry it.
package turnwire

import (
	"errors"
	"fmt"

	"github.com/felinics/memoh/internal/agent/turn"
)

// StartTurnVersion is the StartTurnCommand.SchemaVersion this binary reads.
// Producers that send or store a command declare it; a reader that meets any
// other value refuses the command rather than guess which fields it lacks.
const StartTurnVersion = 1

// ErrUnsupportedVersion reports a serialized command whose declared schema
// version this binary does not read.
var ErrUnsupportedVersion = errors.New("turnwire: unsupported start turn schema version")

// StartTurn is the serialized turn.StartTurnCommand. Keys keep the Go field
// names the command was first serialized under, so payloads already stored
// in the follow-up queue and peers of the previous release still decode.
// ThreadID is omitted when empty so the turn RPC can send the thread under
// its legacy SessionID key instead.
type StartTurn struct {
	SchemaVersion int       `json:"SchemaVersion"`
	NoDefer       bool      `json:"NoDefer"`
	TeamID        string    `json:"TeamID"`
	Mode          turn.Mode `json:"Mode"`

	BotID                   string `json:"BotID"`
	ChatID                  string `json:"ChatID"`
	ThreadID                string `json:"ThreadID,omitempty"`
	RouteID                 string `json:"RouteID"`
	Token                   string `json:"Token"`
	ChatToken               string `json:"ChatToken"`
	UserID                  string `json:"UserID"`
	SourceChannelIdentityID string `json:"SourceChannelIdentityID"`
	DisplayName             string `json:"DisplayName"`

	IdempotencyKey    string `json:"IdempotencyKey"`
	ExternalMessageID string `json:"ExternalMessageID"`
	EventID           string `json:"EventID"`

	AgentCommand    string            `json:"AgentCommand"`
	Query           string            `json:"Query"`
	ModelQuery      string            `json:"ModelQuery"`
	UserMessageKind string            `json:"UserMessageKind"`
	UserVisibleText string            `json:"UserVisibleText"`
	Attachments     []turn.Attachment `json:"Attachments"`

	ReplyTarget            string            `json:"ReplyTarget"`
	ConversationType       string            `json:"ConversationType"`
	ConversationName       string            `json:"ConversationName"`
	SourceReplyToMessageID string            `json:"SourceReplyToMessageID"`
	ReplySender            string            `json:"ReplySender"`
	ReplyPreview           string            `json:"ReplyPreview"`
	ReplyAttachments       []turn.Attachment `json:"ReplyAttachments"`
	MentionsBot            bool              `json:"MentionsBot"`
	RepliesToBot           bool              `json:"RepliesToBot"`

	ForwardMessageID          string `json:"ForwardMessageID"`
	ForwardFromUserID         string `json:"ForwardFromUserID"`
	ForwardFromConversationID string `json:"ForwardFromConversationID"`
	ForwardSender             string `json:"ForwardSender"`
	ForwardDate               int64  `json:"ForwardDate"`

	CurrentChannel string   `json:"CurrentChannel"`
	Channels       []string `json:"Channels"`

	Model             string `json:"Model"`
	ReasoningEffort   string `json:"ReasoningEffort"`
	WorkspaceTargetID string `json:"WorkspaceTargetID"`

	SkillActivation      *turn.SkillActivation `json:"SkillActivation"`
	RequestedSkills      []RequestedSkill      `json:"RequestedSkills"`
	SkipMemoryExtraction bool                  `json:"SkipMemoryExtraction"`
	SkipTitleGeneration  bool                  `json:"SkipTitleGeneration"`
	UserMessagePersisted bool                  `json:"UserMessagePersisted"`

	SessionToken string `json:"SessionToken"` //nolint:gosec // session credential material; crosses only the authenticated internal RPC and the server-side queue
	ToolHTTPURL  string `json:"ToolHTTPURL"`

	DiscussCurrentSources    []turn.ContextMessageSource `json:"DiscussCurrentSources"`
	DiscussOmittedSources    []turn.ContextMessageSource `json:"DiscussOmittedSources"`
	DiscussMessages          []turn.DiscussMessage       `json:"DiscussMessages"`
	DiscussImageRefs         []turn.DiscussImageRef      `json:"DiscussImageRefs"`
	DiscussContextTokens     int                         `json:"DiscussContextTokens"`
	DiscussRecoveryExhausted bool                        `json:"DiscussRecoveryExhausted"`
	DiscussContextOverflow   bool                        `json:"DiscussContextOverflow"`
	DiscussCurrentTokens     int                         `json:"DiscussCurrentTokens"`
	DiscussAddressed         bool                        `json:"DiscussAddressed"`
}

// RequestedSkill is the serialized turn.RequestedSkillContext, skill body
// included: the process that runs the turn renders it into the model input.
type RequestedSkill struct {
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	Content        string `json:"content"`
	SourceKind     string `json:"source_kind,omitempty"`
	OpaqueSourceID string `json:"opaque_source_id,omitempty"`
	ContentHash    string `json:"content_hash,omitempty"`
	Identity       string `json:"identity,omitempty"`
}

// FromStartTurn serializes cmd as its producer built it, declared schema
// version included.
func FromStartTurn(cmd turn.StartTurnCommand) StartTurn {
	return StartTurn{
		SchemaVersion:             cmd.SchemaVersion,
		NoDefer:                   cmd.NoDefer,
		TeamID:                    cmd.TeamID,
		Mode:                      cmd.Mode,
		BotID:                     cmd.BotID,
		ChatID:                    cmd.ChatID,
		ThreadID:                  cmd.ThreadID,
		RouteID:                   cmd.RouteID,
		Token:                     cmd.Token,
		ChatToken:                 cmd.ChatToken,
		UserID:                    cmd.UserID,
		SourceChannelIdentityID:   cmd.SourceChannelIdentityID,
		DisplayName:               cmd.DisplayName,
		IdempotencyKey:            cmd.IdempotencyKey,
		ExternalMessageID:         cmd.ExternalMessageID,
		EventID:                   cmd.EventID,
		AgentCommand:              cmd.AgentCommand,
		Query:                     cmd.Query,
		ModelQuery:                cmd.ModelQuery,
		UserMessageKind:           cmd.UserMessageKind,
		UserVisibleText:           cmd.UserVisibleText,
		Attachments:               cmd.Attachments,
		ReplyTarget:               cmd.ReplyTarget,
		ConversationType:          cmd.ConversationType,
		ConversationName:          cmd.ConversationName,
		SourceReplyToMessageID:    cmd.SourceReplyToMessageID,
		ReplySender:               cmd.ReplySender,
		ReplyPreview:              cmd.ReplyPreview,
		ReplyAttachments:          cmd.ReplyAttachments,
		MentionsBot:               cmd.MentionsBot,
		RepliesToBot:              cmd.RepliesToBot,
		ForwardMessageID:          cmd.ForwardMessageID,
		ForwardFromUserID:         cmd.ForwardFromUserID,
		ForwardFromConversationID: cmd.ForwardFromConversationID,
		ForwardSender:             cmd.ForwardSender,
		ForwardDate:               cmd.ForwardDate,
		CurrentChannel:            cmd.CurrentChannel,
		Channels:                  cmd.Channels,
		Model:                     cmd.Model,
		ReasoningEffort:           cmd.ReasoningEffort,
		WorkspaceTargetID:         cmd.WorkspaceTargetID,
		SkillActivation:           cmd.SkillActivation,
		RequestedSkills:           requestedSkillsToWire(cmd.RequestedSkills),
		SkipMemoryExtraction:      cmd.SkipMemoryExtraction,
		SkipTitleGeneration:       cmd.SkipTitleGeneration,
		UserMessagePersisted:      cmd.UserMessagePersisted,
		SessionToken:              cmd.SessionToken,
		ToolHTTPURL:               cmd.ToolHTTPURL,
		DiscussCurrentSources:     cmd.DiscussCurrentSources,
		DiscussOmittedSources:     cmd.DiscussOmittedSources,
		DiscussMessages:           cmd.DiscussMessages,
		DiscussImageRefs:          cmd.DiscussImageRefs,
		DiscussContextTokens:      cmd.DiscussContextTokens,
		DiscussRecoveryExhausted:  cmd.DiscussRecoveryExhausted,
		DiscussContextOverflow:    cmd.DiscussContextOverflow,
		DiscussCurrentTokens:      cmd.DiscussCurrentTokens,
		DiscussAddressed:          cmd.DiscussAddressed,
	}
}

// Command restores the turn command. It fails with ErrUnsupportedVersion
// unless the producer declared StartTurnVersion.
func (w StartTurn) Command() (turn.StartTurnCommand, error) {
	if w.SchemaVersion != StartTurnVersion {
		return turn.StartTurnCommand{}, fmt.Errorf("%w: %d", ErrUnsupportedVersion, w.SchemaVersion)
	}
	return turn.StartTurnCommand{
		SchemaVersion:             w.SchemaVersion,
		NoDefer:                   w.NoDefer,
		TeamID:                    w.TeamID,
		Mode:                      w.Mode,
		BotID:                     w.BotID,
		ChatID:                    w.ChatID,
		ThreadID:                  w.ThreadID,
		RouteID:                   w.RouteID,
		Token:                     w.Token,
		ChatToken:                 w.ChatToken,
		UserID:                    w.UserID,
		SourceChannelIdentityID:   w.SourceChannelIdentityID,
		DisplayName:               w.DisplayName,
		IdempotencyKey:            w.IdempotencyKey,
		ExternalMessageID:         w.ExternalMessageID,
		EventID:                   w.EventID,
		AgentCommand:              w.AgentCommand,
		Query:                     w.Query,
		ModelQuery:                w.ModelQuery,
		UserMessageKind:           w.UserMessageKind,
		UserVisibleText:           w.UserVisibleText,
		Attachments:               w.Attachments,
		ReplyTarget:               w.ReplyTarget,
		ConversationType:          w.ConversationType,
		ConversationName:          w.ConversationName,
		SourceReplyToMessageID:    w.SourceReplyToMessageID,
		ReplySender:               w.ReplySender,
		ReplyPreview:              w.ReplyPreview,
		ReplyAttachments:          w.ReplyAttachments,
		MentionsBot:               w.MentionsBot,
		RepliesToBot:              w.RepliesToBot,
		ForwardMessageID:          w.ForwardMessageID,
		ForwardFromUserID:         w.ForwardFromUserID,
		ForwardFromConversationID: w.ForwardFromConversationID,
		ForwardSender:             w.ForwardSender,
		ForwardDate:               w.ForwardDate,
		CurrentChannel:            w.CurrentChannel,
		Channels:                  w.Channels,
		Model:                     w.Model,
		ReasoningEffort:           w.ReasoningEffort,
		WorkspaceTargetID:         w.WorkspaceTargetID,
		SkillActivation:           w.SkillActivation,
		RequestedSkills:           requestedSkillsFromWire(w.RequestedSkills),
		SkipMemoryExtraction:      w.SkipMemoryExtraction,
		SkipTitleGeneration:       w.SkipTitleGeneration,
		UserMessagePersisted:      w.UserMessagePersisted,
		SessionToken:              w.SessionToken,
		ToolHTTPURL:               w.ToolHTTPURL,
		DiscussCurrentSources:     w.DiscussCurrentSources,
		DiscussOmittedSources:     w.DiscussOmittedSources,
		DiscussMessages:           w.DiscussMessages,
		DiscussImageRefs:          w.DiscussImageRefs,
		DiscussContextTokens:      w.DiscussContextTokens,
		DiscussRecoveryExhausted:  w.DiscussRecoveryExhausted,
		DiscussContextOverflow:    w.DiscussContextOverflow,
		DiscussCurrentTokens:      w.DiscussCurrentTokens,
		DiscussAddressed:          w.DiscussAddressed,
	}, nil
}

func requestedSkillsToWire(items []turn.RequestedSkillContext) []RequestedSkill {
	if items == nil {
		return nil
	}
	out := make([]RequestedSkill, len(items))
	for i, item := range items {
		out[i] = RequestedSkill{
			Name:           item.Name,
			Description:    item.Description,
			Content:        item.Content,
			SourceKind:     item.SourceKind,
			OpaqueSourceID: item.OpaqueSourceID,
			ContentHash:    item.ContentHash,
			Identity:       item.Identity,
		}
	}
	return out
}

func requestedSkillsFromWire(items []RequestedSkill) []turn.RequestedSkillContext {
	if items == nil {
		return nil
	}
	out := make([]turn.RequestedSkillContext, len(items))
	for i, item := range items {
		out[i] = turn.RequestedSkillContext{
			Name:           item.Name,
			Description:    item.Description,
			Content:        item.Content,
			SourceKind:     item.SourceKind,
			OpaqueSourceID: item.OpaqueSourceID,
			ContentHash:    item.ContentHash,
			Identity:       item.Identity,
		}
	}
	return out
}
