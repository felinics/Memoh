// Package persistence defines the IAM account persistence ports and the
// persistence-neutral records they exchange.
//
// It is deliberately separate from the account package: adapters under
// domains/iam/internal implement these ports, and account owns the Service that
// consumes them. Keeping ports here lets account depend on its own adapter to
// publish constructors without an import cycle.
package persistence

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors are owned here because persistence adapters map storage
// conditions onto them. The account package re-exports both for callers that
// reason about accounts rather than storage.
var (
	// ErrAccountNotFound reports a missing account or membership row.
	ErrAccountNotFound = errors.New("account not found")
	// ErrLastActiveAdmin reports a write that would leave the team with no
	// active admin.
	ErrLastActiveAdmin = errors.New("team must retain at least one active admin")
)

// Record is the persistence-neutral account and current-membership state used
// by Service. PasswordHash is intentionally internal to the accounts package.
type Record struct {
	ID                  string
	Username            string
	Email               string
	Role                string
	DisplayName         string
	AvatarURL           string
	Timezone            string
	PasswordHash        string
	HasPasswordHash     bool
	IsActive            bool
	PrincipalActive     bool
	MembershipActive    bool
	Metadata            string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	JoinedAt            time.Time
	MembershipUpdatedAt time.Time
	LastLoginAt         time.Time
	TitleModelID        string
}

type CreateUserInput struct {
	IsActive bool
	Metadata []byte
}

type CreateInput struct {
	UserID       string
	Username     string
	Email        string
	PasswordHash string
	Role         string
	DisplayName  string
	AvatarURL    string
	IsActive     bool
}

type AdminUpdate struct {
	UserID   string
	Role     string
	IsActive *bool
}

type ProfileUpdate struct {
	UserID       string
	DisplayName  string
	AvatarURL    string
	Timezone     string
	Metadata     string
	TitleModelID string
}

type PasswordUpdate struct {
	UserID       string
	PasswordHash string
}

// Store contains only account and current-membership persistence consumed by
// Service. Model ownership and bootstrap account counting are separate ports.
type Store interface {
	GetByUserID(context.Context, string) (Record, error)
	GetByIdentity(context.Context, string) (Record, error)
	List(context.Context) ([]Record, error)
	Search(context.Context, string, int) ([]Record, error)
	CreateUser(context.Context, CreateUserInput) (Record, error)
	CreateAccount(context.Context, CreateInput) (Record, error)
	UpdateLastLogin(context.Context, string) error
	UpdateAdmin(context.Context, AdminUpdate) (Record, error)
	UpdateProfile(context.Context, ProfileUpdate) (Record, error)
	UpdatePassword(context.Context, PasswordUpdate) error
	RemoveMember(context.Context, string) error
}

// AccountCounter is the persistence port used only by first-run account
// bootstrap. It stays separate from Store because normal account operations do
// not need repository-wide cardinality.
type AccountCounter interface {
	CountAccounts(context.Context) (int64, error)
}

// TitleModelValidator is the Accounts-side port for validating a Model-owned
// title model before persisting its reference on a membership.
type TitleModelValidator interface {
	IsValidTitleModel(context.Context, string) (bool, error)
}

// AdminRecoveryStore atomically restores a principal password and its current
// admin membership. The transaction belongs to the Accounts persistence
// adapter because both writes are Accounts-owned invariants.
type AdminRecoveryStore interface {
	RecoverAdmin(context.Context, string, string) error
}
