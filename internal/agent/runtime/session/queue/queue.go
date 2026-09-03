// Package queue contains the durable session input queue contracts. Steer and
// follow-up intentionally have separate types: their ownership and handoff
// rules differ even though their item state machines match.
package queue

import (
	"errors"
	"time"
)

type Status string

const (
	Accepted Status = "accepted"
	Claimed  Status = "claimed"
	Applied  Status = "applied"
	Rejected Status = "rejected"
	Canceled Status = "canceled"
)

var (
	ErrNoActiveRun         = errors.New("queue: no active run")
	ErrAdmissionOverloaded = errors.New("queue: admission overloaded")
	ErrInvalidReference    = errors.New("queue: invalid claim reference")
	ErrNotPending          = errors.New("queue: item is not an accepted pending item")
	ErrContinuationLost    = errors.New("queue: continuation run was lost")
	ErrInvocationConflict  = errors.New("queue: invocation payload conflicts with an existing item")
)

type (
	SteerItemID    string
	FollowUpItemID string
)

type SteerPendingRef struct {
	ItemID SteerItemID `json:"item_id"`
}

type FollowUpPendingRef struct {
	ItemID FollowUpItemID `json:"item_id"`
}

type SteerItem struct {
	ID                            SteerItemID
	BotID, SessionID, TargetRunID string
	Payload                       []byte
	Status                        Status
	Position                      int64
	Claim                         *SteerClaimRef
	CreatedAt                     time.Time
}

type FollowUpItem struct {
	ID               FollowUpItemID
	BotID, SessionID string
	// EnqueuedDuringRunID is immutable admission provenance. It proves which
	// active run accepted the item, but does not pin later FIFO items to it.
	EnqueuedDuringRunID string
	Payload             []byte
	Status              Status
	Position            int64
	AssignedRunID       string
	Claim               *FollowUpClaimRef
	CreatedAt           time.Time
}

// References are capabilities scoped to one queue and one execution run.
// Callers cannot use a follow-up reference to mutate steer state or vice versa.
type SteerClaimRef struct {
	ItemID         SteerItemID
	RunID, OwnerID string
	FencingToken   int64
}

type FollowUpClaimRef struct {
	ItemID         FollowUpItemID
	RunID, OwnerID string
	FencingToken   int64
}
