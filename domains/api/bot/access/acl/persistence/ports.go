// Package persistence defines the ACL persistence ports and the rule/override
// vocabulary they exchange, separately from the service that consumes them.
package persistence

import (
	"context"
	"errors"
)

var ErrTransactionsRequired = errors.New("ACL persistence requires transactions")

// Evaluation identifies one ACL decision lookup.
type Evaluation struct {
	BotID             string
	Action            string
	ChannelIdentityID string
	ChannelType       string
	SourceScope       SourceScope
}

// RuleWrite is the persistence-neutral command shared by rule creation and
// updates. ID is required only for updates; BotID and CreatedByUserID are
// required only for creates.
type RuleWrite struct {
	ID                 string
	BotID              string
	CreatedByUserID    string
	Enabled            bool
	Description        string
	Effect             string
	ChannelIdentityID  string
	SubjectChannelType string
	SourceChannel      string
	SourceScope        SourceScope
}

// ChannelIdentity is the current Channel-owned identity projection consumed by
// ACL validation and response enrichment.
type ChannelIdentity struct {
	ID               string
	Channel          string
	ChannelSubjectID string
	DisplayName      string
	AvatarURL        string
}

// ManageOverrideWrite describes one local Manage capability override.
type ManageOverrideWrite struct {
	BotID             string
	ChannelIdentityID string
	Granted           bool
	CreatedByUserID   string
}

// ObservedConversationQuery selects the source conversations exposed by the
// ACL rule editor. Exactly one of ChannelIdentityID and ChannelType is set.
type ObservedConversationQuery struct {
	BotID             string
	ChannelIdentityID string
	ChannelType       string
}

type RuleStore interface {
	EvaluateRule(context.Context, Evaluation) (string, error)
	GetDefaultEffect(context.Context, string) (string, error)
	SetDefaultEffect(context.Context, string, string) error
	ListRules(context.Context, string) ([]Rule, error)
	CreateRule(context.Context, RuleWrite) (Rule, error)
	UpdateRule(context.Context, RuleWrite) (Rule, error)
	DeleteRule(context.Context, string) error
}

type ManageOverrideStore interface {
	GetManageOverride(context.Context, string, string) (ManageOverride, bool, error)
	ListManageOverrides(context.Context, string) ([]ManageOverride, error)
	UpsertManageOverride(context.Context, ManageOverrideWrite) (ManageOverride, error)
	DeleteManageOverride(context.Context, string, string) error
}

// ChannelIdentityReader is the minimal Channel application contract consumed
// by ACL. Missing IDs are omitted from the returned slice.
type ChannelIdentityReader interface {
	ListChannelIdentities(context.Context, []string) ([]ChannelIdentity, error)
}

type ObservedConversationReader interface {
	ListObservedConversations(context.Context, ObservedConversationQuery) ([]ObservedConversationCandidate, error)
}

// PresetTransaction exposes only the two writes that make up a preset.
type PresetTransaction interface {
	SetDefaultEffect(context.Context, string, string) error
	CreateRule(context.Context, RuleWrite) (Rule, error)
}

// PresetTransactor owns the atomic boundary for applying one ACL preset.
type PresetTransactionRunner interface {
	RunPresetTransaction(context.Context, func(PresetTransaction) error) error
}

// Store is the API-owned persistence surface consumed by Service.
type Store interface {
	RuleStore
	ManageOverrideStore
	PresetTransactionRunner
}
