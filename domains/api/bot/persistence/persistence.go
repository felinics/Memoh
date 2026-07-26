// Package persistence defines the Bot persistence ports and the records they
// exchange, separately from the service that consumes them.
package persistence

import (
	"context"
	"errors"
	"time"
)

type Record struct {
	ID          string
	OwnerUserID string
	Name        string
	DisplayName string
	AvatarURL   string
	Timezone    string
	IsActive    bool
	Status      string
	Metadata    []byte
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CreateInput struct {
	OwnerUserID string
	Name        string
	DisplayName string
	AvatarURL   string
	Timezone    string
	IsActive    bool
	Metadata    []byte
	Status      string
}

type UpdateInput struct {
	ID          string
	Name        string
	DisplayName string
	AvatarURL   string
	Timezone    string
	IsActive    bool
	Metadata    []byte
}

type UserRecord struct {
	ID          string
	Username    string
	DisplayName string
	AvatarURL   string
}

type GrantRecord struct {
	ID              string
	BotID           string
	SubjectType     string
	UserID          string
	Permissions     []byte
	UserUsername    string
	UserDisplayName string
	UserAvatarURL   string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CreateGrantInput struct {
	BotID           string
	SubjectType     string
	UserID          string
	Permissions     []byte
	CreatedByUserID string
}

type ContainerRecord struct {
	ContainerID string
	Namespace   string
	Image       string
	Status      string
}

type HeartbeatRecord struct {
	ID                string
	OwnerUserID       string
	Status            string
	HeartbeatEnabled  bool
	HeartbeatInterval int
}

type BotReader interface {
	GetBotByID(context.Context, string) (Record, error)
	GetBotByName(context.Context, string) (Record, error)
	ListBotsByOwner(context.Context, string) ([]Record, error)
	ListAccessibleBots(context.Context, string) ([]Record, error)
}

type BotWriter interface {
	CreateBot(context.Context, CreateInput) (Record, error)
	UpdateBot(context.Context, UpdateInput) (Record, error)
	UpdateBotOwner(context.Context, string, string) (Record, error)
	UpdateBotStatus(context.Context, string, string) error
	DeleteBot(context.Context, string) error
}

type BotStore interface {
	BotReader
	BotWriter
}

type HeartbeatReader interface {
	ListHeartbeatEnabledBots(context.Context) ([]HeartbeatRecord, error)
}

// TombstoneReader lists bots left in the deleting state by an interrupted
// delete, so the saga can resume them.
type TombstoneReader interface {
	ListDeletingBots(context.Context) ([]Record, error)
}

// BotDataPurger removes every row one owner holds for a bot. Epoch v2 dropped
// the cross-schema cascades that used to do this, so each owner purges its own
// schema in its own transaction and the delete saga drives them in order.
//
// Implementations must be idempotent: the saga retries after a crash, and a
// purge may run against a bot whose rows are already gone.
type BotDataPurger interface {
	// Owner names the schema being purged, for error context and logs.
	Owner() string
	PurgeBotData(ctx context.Context, botID string) error
}

type ActivityWriter interface {
	TouchBot(context.Context, string) error
}

type GrantReader interface {
	ListGrants(context.Context, string) ([]GrantRecord, error)
	ListGrantsForUser(context.Context, string, string) ([]GrantRecord, error)
	GetGrant(context.Context, string) (GrantRecord, error)
}

type GrantWriter interface {
	CreateGrant(context.Context, CreateGrantInput) (GrantRecord, error)
	UpdateGrantPermissions(context.Context, string, []byte) (GrantRecord, error)
	DeleteGrant(context.Context, string) error
}

type GrantStore interface {
	GrantReader
	GrantWriter
}

type UserReader interface {
	GetUser(context.Context, string) (UserRecord, error)
}

type ContainerReader interface {
	GetContainerByBotID(context.Context, string) (ContainerRecord, error)
}

// Sentinel errors are owned here because persistence adapters map storage
// conditions onto them. The bot package re-exports them for callers that
// reason about bots rather than storage.
var (
	// ErrBotNotFound reports a missing bot row.
	ErrBotNotFound = errors.New("bot not found")
	// ErrBotNameTaken reports a unique-name conflict.
	ErrBotNameTaken = errors.New("bot name already taken")
	// ErrGrantNotFound reports a missing bot user grant.
	ErrGrantNotFound = errors.New("bot user grant not found")
	// ErrGrantExists reports a duplicate grant for the same subject.
	ErrGrantExists = errors.New("a grant for this subject already exists")
)
