package sessionruntime

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
)

type QueueStatus string

const (
	QueueAccepted QueueStatus = "accepted"
	QueueClaimed  QueueStatus = "claimed"
	QueueApplied  QueueStatus = "applied"
	QueueRejected QueueStatus = "rejected"
	QueueExpired  QueueStatus = "expired"
	QueueCanceled QueueStatus = "canceled"
)

// Stable error codes recorded on rejected queue items. They are runtime
// vocabulary, not transport codes: the item stays readable through the queue
// API until compaction drops it.
const (
	// QueueErrorTargetRunNotActive marks a steer whose target run reached a
	// terminal state before the steer entered a model step.
	QueueErrorTargetRunNotActive = "queue_target_run_not_active"
	// QueueErrorFollowUpCommandInvalid marks a follow-up whose stored payload
	// cannot be replayed as a turn: it carries no command, names another
	// session, lacks a team, or names a team this deployment does not serve.
	// Such an item is terminal; retrying it at the next boundary would fail
	// the same way.
	QueueErrorFollowUpCommandInvalid = "queue_follow_up_command_invalid"
)

const (
	// MaxPendingQueueItems bounds accepted items per queue and session. Queue
	// state is one serialized document per queue, so an unbounded pending set
	// would grow every mutation's read and write linearly.
	MaxPendingQueueItems = 64
	// queueTerminalRetention is how many applied, rejected, and canceled
	// items each queue keeps for invocation replay and status lookups. Older
	// terminal items are dropped by compaction after every mutation.
	queueTerminalRetention = 64
	// terminalClaimRetention bounds the follow-up per-run claim record. It is
	// deliberately larger than the item retention so the record outlives the
	// items it points at.
	terminalClaimRetention = 4 * queueTerminalRetention
)

var (
	ErrQueueSteerUnsupported    = errors.New("queue: active run has no steer consumer")
	ErrQueueNoActiveRun         = errors.New("queue: no active run")
	ErrQueueInvalidReference    = errors.New("queue: invalid claim reference")
	ErrQueueNotPending          = errors.New("queue: item is not an accepted pending item")
	ErrQueueInvocationConflict  = errors.New("queue: invocation payload conflicts with an existing item")
	ErrQueueCapacityExceeded    = errors.New("queue: pending capacity exceeded")
	ErrLiveQueueUnavailable     = errors.New("session runtime queue is unavailable")
	ErrQueueAdmissionOverloaded = errors.New("queue: admission overloaded")
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

type SteerClaimRef struct {
	ItemID       SteerItemID
	RunID        string
	OwnerID      string
	Generation   string
	FencingToken int64
	ClaimToken   string
}

type FollowUpClaimRef struct {
	ItemID       FollowUpItemID
	TriggerRunID string
	ClaimToken   string
}

type SteerItem struct {
	ID                            SteerItemID
	BotID, SessionID, TargetRunID string
	InvocationID                  string
	Payload                       []byte
	Status                        QueueStatus
	Position                      int64
	Claim                         *SteerClaimRef
	// ErrorCode is set when Status is rejected.
	ErrorCode string
	CreatedAt time.Time
}

type FollowUpItem struct {
	ID                  FollowUpItemID
	BotID, SessionID    string
	EnqueuedDuringRunID string
	InvocationID        string
	Payload             []byte
	Status              QueueStatus
	Position            int64
	Claim               *FollowUpClaimRef
	// ErrorCode is set when Status is rejected.
	ErrorCode string
	CreatedAt time.Time
}

type PromoteFollowUpResult struct {
	FollowUp FollowUpPendingRef
	Steer    SteerItem
}

// LiveQueueBackend is transient session coordination. Implementations must
// serialize each operation with the live run state for the same session.
type LiveQueueBackend interface {
	EnqueueSteer(context.Context, Key, string, string, []byte) (SteerItem, error)
	EnqueueFollowUp(context.Context, Key, string, string, []byte) (FollowUpItem, error)
	PendingQueues(context.Context, Key, int) ([]SteerItem, []FollowUpItem, error)
	ReorderSteer(context.Context, Key, SteerPendingRef, SteerPendingRef) ([]SteerItem, error)
	ReorderFollowUp(context.Context, Key, FollowUpPendingRef, FollowUpPendingRef) ([]FollowUpItem, error)
	UpdateSteer(context.Context, Key, SteerItemID, []byte) (SteerItem, error)
	UpdateFollowUp(context.Context, Key, FollowUpItemID, []byte) (FollowUpItem, error)
	CancelSteer(context.Context, Key, SteerItemID) error
	CancelFollowUp(context.Context, Key, FollowUpItemID) error
	PromoteFollowUpToSteer(context.Context, Key, FollowUpPendingRef) (PromoteFollowUpResult, error)
	ClaimNextSteer(context.Context, RunHandle, bool) (SteerItem, SteerClaimRef, bool, error)
	ApplySteer(context.Context, Key, SteerClaimRef) error
	ReleaseSteer(context.Context, Key, SteerClaimRef) error
	// CloseSteerRun rejects every accepted or claimed steer that targets the
	// given run and seals the run against later steer admission. It is called
	// once the run is durably terminal; a steer is bound to its run and has no
	// meaning for any later run of the session.
	CloseSteerRun(context.Context, Key, string) error
	ClaimNextFollowUp(context.Context, Key, string) (FollowUpItem, FollowUpClaimRef, bool, error)
	ApplyFollowUp(context.Context, Key, FollowUpClaimRef) error
	ReleaseFollowUp(context.Context, Key, FollowUpClaimRef) error
	// RejectFollowUp moves a claimed follow-up to rejected with the given
	// error code and frees its trigger run's claim slot, so the same terminal
	// boundary can go on to the next accepted item. It is for items the
	// continuation cannot start at all; a transient start failure releases
	// the item instead.
	RejectFollowUp(context.Context, Key, FollowUpClaimRef, string) error
}

type steerQueueState struct {
	Items                 []SteerItem       `json:"items"`
	PromotedFollowUpItems map[string]string `json:"promoted_follow_up_items,omitempty"`
	ClosedRunID           string            `json:"closed_run_id,omitempty"`
	UpdatedAt             time.Time         `json:"updated_at"`
}

type followUpQueueState struct {
	Items          []FollowUpItem    `json:"items"`
	TerminalClaims map[string]string `json:"terminal_claims,omitempty"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// SteerRunAvailable reports whether the current executor accepts new step inputs.
func SteerRunAvailable(run *CurrentRunView) bool {
	return run != nil && run.SteerSupported && (run.Status == RunStatusRunning || run.Status == RunStatusWaitingDecision)
}

func activeRun(snapshot Snapshot, ok bool) (*CurrentRunView, bool) {
	if !ok || snapshot.CurrentRunView == nil || !isActiveRunStatus(snapshot.CurrentRunView.Status) {
		return nil, false
	}
	return snapshot.CurrentRunView, true
}

func validateQueueKey(key Key) error {
	if strings.TrimSpace(key.BotID) == "" || strings.TrimSpace(key.SessionID) == "" {
		return ErrQueueInvalidReference
	}
	return nil
}

func validatePayload(payload []byte) error {
	if len(payload) == 0 {
		return ErrQueueInvalidReference
	}
	return nil
}

func validateSteerClaim(key Key, ref SteerClaimRef) error {
	if err := validateQueueKey(key); err != nil {
		return err
	}
	if ref.ItemID == "" || strings.TrimSpace(ref.RunID) == "" || strings.TrimSpace(ref.OwnerID) == "" ||
		strings.TrimSpace(ref.Generation) == "" || ref.FencingToken <= 0 || strings.TrimSpace(ref.ClaimToken) == "" {
		return ErrQueueInvalidReference
	}
	return nil
}

func validateFollowUpClaim(key Key, ref FollowUpClaimRef) error {
	if err := validateQueueKey(key); err != nil {
		return err
	}
	if ref.ItemID == "" || strings.TrimSpace(ref.TriggerRunID) == "" || strings.TrimSpace(ref.ClaimToken) == "" {
		return ErrQueueInvalidReference
	}
	return nil
}

// advanceSteerClaim moves an unapplied claim to the run's current execution
// identity after an owner change. The consumer run and claim token stay the
// same, so this is one logical consumption continued by a new owner, and the
// previous owner's reference no longer matches the stored claim.
func advanceSteerClaim(item *SteerItem, handle RunHandle) bool {
	if item == nil || item.Claim == nil || item.Status != QueueClaimed || item.Claim.RunID != handle.RunID {
		return false
	}
	claim := item.Claim
	if claim.OwnerID == handle.OwnerID && claim.Generation == handle.Generation && claim.FencingToken == handle.FencingToken {
		return false
	}
	claim.OwnerID = handle.OwnerID
	claim.Generation = handle.Generation
	claim.FencingToken = handle.FencingToken
	return true
}

// runViewOwnedBy reports whether the projected run is owned by ownerID. The
// memory backend has a single process and never records an owner on the live
// run view, so an empty view owner matches any handle; a distributed backend
// records the owner and must match it exactly.
func runViewOwnedBy(run *CurrentRunView, ownerID string) bool {
	if run == nil {
		return false
	}
	viewOwner := strings.TrimSpace(run.OwnerID)
	return viewOwner == "" || viewOwner == strings.TrimSpace(ownerID)
}

func runMatchesSteerClaim(run *CurrentRunView, ref SteerClaimRef) bool {
	return run != nil && run.RunID == ref.RunID && runViewOwnedBy(run, ref.OwnerID) && run.Generation == ref.Generation && isActiveRunStatus(run.Status)
}

func cloneSteerItem(item SteerItem) SteerItem {
	item.Payload = append([]byte(nil), item.Payload...)
	if item.Claim != nil {
		claim := *item.Claim
		item.Claim = &claim
	}
	return item
}

func cloneFollowUpItem(item FollowUpItem) FollowUpItem {
	item.Payload = append([]byte(nil), item.Payload...)
	if item.Claim != nil {
		claim := *item.Claim
		item.Claim = &claim
	}
	return item
}

func pendingSteers(state steerQueueState, limit int) []SteerItem {
	items := make([]SteerItem, 0, len(state.Items))
	for _, item := range state.Items {
		if item.Status == QueueAccepted {
			items = append(items, cloneSteerItem(item))
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Position < items[j].Position })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

func pendingFollowUps(state followUpQueueState, limit int) []FollowUpItem {
	items := make([]FollowUpItem, 0, len(state.Items))
	for _, item := range state.Items {
		if item.Status == QueueAccepted {
			items = append(items, cloneFollowUpItem(item))
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Position < items[j].Position })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

func countPendingSteers(state steerQueueState) int {
	count := 0
	for _, item := range state.Items {
		if item.Status == QueueAccepted {
			count++
		}
	}
	return count
}

func countPendingFollowUps(state followUpQueueState) int {
	count := 0
	for _, item := range state.Items {
		if item.Status == QueueAccepted {
			count++
		}
	}
	return count
}

// closeSteerRun rejects the run's unapplied steers and records the run as
// closed so a steer admitted after the terminal decision is refused even while
// the live snapshot still shows the run as active. It reports whether any
// item changed.
func closeSteerRun(state *steerQueueState, runID string, now time.Time) bool {
	changed := false
	for i := range state.Items {
		item := &state.Items[i]
		if item.TargetRunID != runID || (item.Status != QueueAccepted && item.Status != QueueClaimed) {
			continue
		}
		item.Status = QueueRejected
		item.ErrorCode = QueueErrorTargetRunNotActive
		item.Claim = nil
		changed = true
	}
	if state.ClosedRunID != runID {
		state.ClosedRunID = runID
		changed = true
	}
	if changed {
		state.UpdatedAt = now
	}
	return changed
}

func (s QueueStatus) terminal() bool {
	switch s {
	case QueueApplied, QueueRejected, QueueExpired, QueueCanceled:
		return true
	default:
		return false
	}
}

// compact keeps every accepted or claimed item and only the newest terminal
// items, so a long-lived session's queue document stays bounded. Map entries
// that point at dropped items are removed with them.
func (state *steerQueueState) compact() {
	if state == nil {
		return
	}
	state.Items = compactQueueItems(state.Items, func(item SteerItem) (QueueStatus, time.Time, int64) {
		return item.Status, item.CreatedAt, item.Position
	})
	if len(state.PromotedFollowUpItems) == 0 {
		return
	}
	present := make(map[string]struct{}, len(state.Items))
	for _, item := range state.Items {
		present[string(item.ID)] = struct{}{}
	}
	for followUpID, steerID := range state.PromotedFollowUpItems {
		if _, ok := present[steerID]; !ok {
			delete(state.PromotedFollowUpItems, followUpID)
		}
	}
}

func (state *followUpQueueState) compact() {
	if state == nil {
		return
	}
	state.Items = compactQueueItems(state.Items, func(item FollowUpItem) (QueueStatus, time.Time, int64) {
		return item.Status, item.CreatedAt, item.Position
	})
	// TerminalClaims is the per-trigger-run idempotency record: a repeated
	// terminal observation for the same run must find its entry and claim
	// nothing more, even after retention dropped the applied item. Entries
	// therefore outlive their items and are only pruned once the map itself
	// grows past its bound.
	if len(state.TerminalClaims) <= terminalClaimRetention {
		return
	}
	present := make(map[string]struct{}, len(state.Items))
	for _, item := range state.Items {
		present[string(item.ID)] = struct{}{}
	}
	for runID, itemID := range state.TerminalClaims {
		if _, ok := present[itemID]; !ok {
			delete(state.TerminalClaims, runID)
		}
	}
}

func compactQueueItems[T any](items []T, describe func(T) (QueueStatus, time.Time, int64)) []T {
	terminal := 0
	for _, item := range items {
		if status, _, _ := describe(item); status.terminal() {
			terminal++
		}
	}
	if terminal <= queueTerminalRetention {
		return items
	}
	type indexed struct {
		index     int
		createdAt time.Time
		position  int64
	}
	candidates := make([]indexed, 0, terminal)
	for i, item := range items {
		if status, createdAt, position := describe(item); status.terminal() {
			candidates = append(candidates, indexed{index: i, createdAt: createdAt, position: position})
		}
	}
	// Oldest first; the head of this order is dropped.
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].createdAt.Equal(candidates[j].createdAt) {
			return candidates[i].createdAt.Before(candidates[j].createdAt)
		}
		return candidates[i].position < candidates[j].position
	})
	drop := make(map[int]struct{}, terminal-queueTerminalRetention)
	for _, candidate := range candidates[:terminal-queueTerminalRetention] {
		drop[candidate.index] = struct{}{}
	}
	kept := make([]T, 0, len(items)-len(drop))
	for i, item := range items {
		if _, dropped := drop[i]; !dropped {
			kept = append(kept, item)
		}
	}
	return kept
}

func nextSteerPosition(state steerQueueState) int64 {
	var position int64
	for _, item := range state.Items {
		if item.Position > position {
			position = item.Position
		}
	}
	return position + 1
}

func nextFollowUpPosition(state followUpQueueState) int64 {
	var position int64
	for _, item := range state.Items {
		if item.Position > position {
			position = item.Position
		}
	}
	return position + 1
}

// reorderQueuePositions sorts lightweight references to the current positions.
// Only the returned public items need payload copies; ordering must not clone
// a second complete queue or construct an ID-to-position map.
func reorderQueuePositions[T any, ID ~string](items []T, item, before ID, describe func(*T) (ID, QueueStatus, *int64)) error {
	if item == "" || item == before {
		return ErrQueueInvalidReference
	}
	type positionRef struct {
		id       ID
		position *int64
	}
	pending := make([]positionRef, 0, len(items))
	for i := range items {
		id, status, position := describe(&items[i])
		if status == QueueAccepted {
			pending = append(pending, positionRef{id: id, position: position})
		}
	}
	slices.SortStableFunc(pending, func(a, b positionRef) int { return cmp.Compare(*a.position, *b.position) })
	itemIndex, beforeIndex := -1, -1
	for i, ref := range pending {
		if ref.id == item {
			itemIndex = i
		}
		if ref.id == before {
			beforeIndex = i
		}
	}
	if itemIndex < 0 || (before != "" && beforeIndex < 0) {
		return ErrQueueNotPending
	}
	moving := pending[itemIndex]
	pending = append(pending[:itemIndex], pending[itemIndex+1:]...)
	if before == "" {
		beforeIndex = len(pending)
	} else if itemIndex < beforeIndex {
		beforeIndex--
	}
	pending = append(pending, moving)
	copy(pending[beforeIndex+1:], pending[beforeIndex:len(pending)-1])
	pending[beforeIndex] = moving
	for i, ref := range pending {
		*ref.position = int64(i + 1)
	}
	return nil
}

func reorderSteerState(state *steerQueueState, itemRef, beforeRef SteerPendingRef) ([]SteerItem, error) {
	if state == nil {
		return nil, ErrQueueInvalidReference
	}
	err := reorderQueuePositions(state.Items, itemRef.ItemID, beforeRef.ItemID, func(item *SteerItem) (SteerItemID, QueueStatus, *int64) {
		return item.ID, item.Status, &item.Position
	})
	if err != nil {
		return nil, err
	}
	return pendingSteers(*state, 0), nil
}

func reorderFollowUpState(state *followUpQueueState, itemRef, beforeRef FollowUpPendingRef) ([]FollowUpItem, error) {
	if state == nil {
		return nil, ErrQueueInvalidReference
	}
	err := reorderQueuePositions(state.Items, itemRef.ItemID, beforeRef.ItemID, func(item *FollowUpItem) (FollowUpItemID, QueueStatus, *int64) {
		return item.ID, item.Status, &item.Position
	})
	if err != nil {
		return nil, err
	}
	return pendingFollowUps(*state, 0), nil
}

func replaySteer(state steerQueueState, invocationID string, payload []byte) (SteerItem, bool, error) {
	for _, item := range state.Items {
		if item.InvocationID != invocationID {
			continue
		}
		if !bytes.Equal(item.Payload, payload) {
			return SteerItem{}, true, ErrQueueInvocationConflict
		}
		return cloneSteerItem(item), true, nil
	}
	return SteerItem{}, false, nil
}

func replayFollowUp(state followUpQueueState, invocationID string, payload []byte) (FollowUpItem, bool, error) {
	for _, item := range state.Items {
		if item.InvocationID != invocationID {
			continue
		}
		if !bytes.Equal(item.Payload, payload) {
			return FollowUpItem{}, true, ErrQueueInvocationConflict
		}
		return cloneFollowUpItem(item), true, nil
	}
	return FollowUpItem{}, false, nil
}
