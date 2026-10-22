// Package contextepoch owns the Context Epoch state (docs/design/
// context-epoch-spec.md): the per-session epoch counter and the history
// anchor every provider-bound history load starts from. The anchor replaces
// the retired 24-hour wall-clock window — a fixed bookmark keeps the request
// prefix stable across turns, while the byte budget (CM-ADM-001) still bounds
// how much history loads. The anchor only moves forward, and every move is an
// explicit, recorded advance.
//
// State is stored in bot_sessions.metadata under MetadataKey (no new table,
// no migration) and written through a guarded SQL update that rejects any
// move backwards, so a stale or racing writer can never regress the anchor.
package contextepoch

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MetadataKey is the bot_sessions.metadata key holding the session's state.
const MetadataKey = "context_epoch"

// Advance reasons recorded on every explicit anchor/epoch move. Keep the
// values stable: they are the audit vocabulary of rule 6 (every stable-part
// change leaves a record).
const (
	// ReasonInit is the first anchored load of a session: the anchor starts
	// at the beginning of the session's history.
	ReasonInit = "init"
	// ReasonBudget records an anchor advance forced by the byte budget
	// admitting fewer rows than the session holds since the previous anchor.
	// The budget — never the wall clock — is what may move the anchor.
	ReasonBudget = "budget"
	// ReasonBudgetRecovery records an in-turn budget recovery that rewrote
	// the history region (spec, turn boundary rule 1): the anchor stays put,
	// but the stable part changed, so the change is booked as an epoch
	// advance instead of silently rewritten.
	ReasonBudgetRecovery = "budget-recovery"
)

// Beginning is the zero anchor: history loads from the start of the session.
var Beginning = time.Unix(0, 0).UTC()

// State is the persisted Context Epoch state of one (thread × bot) pair.
type State struct {
	// Epoch is the stable prefix's version; it only increments.
	Epoch int64 `json:"epoch"`
	// AnchorCreatedAtMs is the created_at lower bound history loads from.
	// Zero means the beginning of the session.
	AnchorCreatedAtMs int64 `json:"anchor_created_at_ms,omitempty"`
	// AnchorMessageID names the message the anchor was taken from, for
	// auditability.
	AnchorMessageID string `json:"anchor_message_id,omitempty"`
	// LastReason is why the anchor last moved (or who initialized the state).
	LastReason string `json:"last_reason,omitempty"`
	// UpdatedAtMs is when the state was last written.
	UpdatedAtMs int64 `json:"updated_at_ms,omitempty"`
}

// Initial returns the state of a session that never recorded one: epoch 1
// anchored at the beginning of its history.
func Initial(now time.Time) State {
	return State{Epoch: 1, LastReason: ReasonInit, UpdatedAtMs: now.UnixMilli()}
}

// IsZero reports whether no state was ever recorded.
func (s State) IsZero() bool {
	return s.Epoch <= 0
}

// AnchorTime resolves the anchor to the created_at lower bound for loads.
func (s State) AnchorTime() time.Time {
	if s.AnchorCreatedAtMs <= 0 {
		return Beginning
	}
	return time.UnixMilli(s.AnchorCreatedAtMs).UTC()
}

// AdvancedBy returns the next state after an explicit advance to anchorAt.
// The anchor only moves forward and the epoch only increments; a call that
// would regress either is an error, mirroring the SQL-side guard.
func (s State) AdvancedBy(anchorAt time.Time, anchorMessageID, reason string, now time.Time) (State, error) {
	if reason == "" {
		return State{}, errors.New("context epoch advance requires a reason")
	}
	anchorMs := anchorAt.UnixMilli()
	if anchorMs < s.AnchorCreatedAtMs {
		return State{}, fmt.Errorf("context epoch anchor cannot move backward: %d < %d", anchorMs, s.AnchorCreatedAtMs)
	}
	return State{
		Epoch:             s.Epoch + 1,
		AnchorCreatedAtMs: anchorMs,
		AnchorMessageID:   anchorMessageID,
		LastReason:        reason,
		UpdatedAtMs:       now.UnixMilli(),
	}, nil
}

// decodeState parses the stored JSON; empty input means "no state recorded".
func decodeState(raw string) (State, error) {
	if raw == "" {
		return State{}, nil
	}
	var state State
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return State{}, fmt.Errorf("decode context epoch state: %w", err)
	}
	return state, nil
}

// encode serializes the state for the guarded SQL write.
func (s State) encode() ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("encode context epoch state: %w", err)
	}
	return raw, nil
}
