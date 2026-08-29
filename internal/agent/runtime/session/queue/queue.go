// Package queue contains the durable session input queue contracts. Steer and
// follow-up intentionally have separate types and stores: their ownership and
// handoff rules are different even though their item state machines match.
package queue

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

type Status string

const (
	Accepted Status = "accepted"
	Claimed  Status = "claimed"
	Applied  Status = "applied"
	Rejected Status = "rejected"
	Expired  Status = "expired"
	Canceled Status = "canceled"
)

var (
	ErrNoActiveRun        = errors.New("queue: no active run")
	ErrInvalidReference   = errors.New("queue: invalid claim reference")
	ErrNotPending         = errors.New("queue: item is not an accepted pending item")
	ErrTargetRunNotActive = errors.New("queue: target run is not active")
	ErrContinuationLost   = errors.New("queue: continuation run was lost")
	ErrInvocationConflict = errors.New("queue: invocation payload conflicts with an existing item")
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
	// active run accepted the item, but does not pin the item to that run: later
	// FIFO items continue through R2/R3 after the first item starts R1.
	EnqueuedDuringRunID string
	Payload             []byte
	Status              Status
	Position            int64
	AssignedRunID       string
	Claim               *FollowUpClaimRef
	CreatedAt           time.Time
}

// References are capabilities scoped to one queue and one execution run.
// Callers cannot use a follow-up reference to mutate steer state (or vice versa).
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

type Admission struct {
	BotID, SessionID, RunID string
	Active                  bool
	OwnerID                 string
	FencingToken            int64
}
type AdmissionPort interface {
	AdmitQueue(ctx context.Context, botID, sessionID string) (Admission, error)
}

type EnqueueReceipt[T ~string] struct {
	ItemID     T
	Status     Status
	AcceptedAt time.Time
}

type SteerQueue struct {
	mu        sync.Mutex
	next      int64
	items     map[SteerItemID]*SteerItem
	admission AdmissionPort
}

func NewSteerQueue(admission AdmissionPort) *SteerQueue {
	return &SteerQueue{items: map[SteerItemID]*SteerItem{}, admission: admission}
}

func (q *SteerQueue) Enqueue(ctx context.Context, botID, sessionID, id string, payload []byte) (EnqueueReceipt[SteerItemID], error) {
	if q == nil || q.admission == nil {
		return EnqueueReceipt[SteerItemID]{}, errors.New("queue: admission is required")
	}
	a, err := q.admission.AdmitQueue(ctx, botID, sessionID)
	if err != nil {
		return EnqueueReceipt[SteerItemID]{}, err
	}
	if !a.Active || a.RunID == "" {
		return EnqueueReceipt[SteerItemID]{}, ErrNoActiveRun
	}
	if id == "" {
		return EnqueueReceipt[SteerItemID]{}, errors.New("queue: item id is required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.items[SteerItemID(id)]; ok {
		if !bytes.Equal(q.items[SteerItemID(id)].Payload, payload) {
			return EnqueueReceipt[SteerItemID]{}, ErrInvocationConflict
		}
		return EnqueueReceipt[SteerItemID]{ItemID: SteerItemID(id), Status: Accepted}, nil
	}
	q.next++
	q.items[SteerItemID(id)] = &SteerItem{ID: SteerItemID(id), BotID: botID, SessionID: sessionID, TargetRunID: a.RunID, Payload: append([]byte(nil), payload...), Status: Accepted, Position: q.next, CreatedAt: time.Now()}
	return EnqueueReceipt[SteerItemID]{ItemID: SteerItemID(id), Status: Accepted, AcceptedAt: time.Now()}, nil
}

func (q *SteerQueue) Get(id SteerItemID) (SteerItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[id]
	if !ok {
		return SteerItem{}, false
	}
	return cloneSteer(*v), true
}

func (q *SteerQueue) Reorder(id SteerItemID, before SteerItemID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return reorderSteer(q.items, id, before)
}

func (q *SteerQueue) Update(id SteerItemID, payload []byte) (SteerItem, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[id]
	if !ok || v.Status != Accepted {
		return SteerItem{}, ErrNotPending
	}
	v.Payload = append(v.Payload[:0], payload...)
	return cloneSteer(*v), nil
}

func (q *SteerQueue) Cancel(id SteerItemID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[id]
	if !ok || v.Status != Accepted {
		return ErrNotPending
	}
	v.Status = Canceled
	return nil
}

func (q *SteerQueue) Claim(run sessionruntime.RunHandle) (SteerItem, SteerClaimRef, error) {
	if err := validateRun(run); err != nil {
		return SteerItem{}, SteerClaimRef{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, v := range sortedSteer(q.items) {
		if v.Status == Accepted && v.TargetRunID == run.RunID {
			v.Status = Claimed
			ref := SteerClaimRef{v.ID, run.RunID, run.OwnerID, run.FencingToken}
			v.Claim = &ref
			return cloneSteer(*v), ref, nil
		}
	}
	return SteerItem{}, SteerClaimRef{}, ErrNotPending
}

func (q *SteerQueue) Reclaim(ref SteerClaimRef, run sessionruntime.RunHandle) (SteerClaimRef, error) {
	if err := validateRun(run); err != nil {
		return SteerClaimRef{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[ref.ItemID]
	if !ok || v.Claim == nil || v.Claim.RunID != run.RunID || v.Status != Claimed {
		return SteerClaimRef{}, ErrInvalidReference
	}
	n := SteerClaimRef{ref.ItemID, run.RunID, run.OwnerID, run.FencingToken}
	v.Claim = &n
	return n, nil
}

func (q *SteerQueue) Apply(ref SteerClaimRef, run sessionruntime.RunHandle) error {
	if err := validateRun(run); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[ref.ItemID]
	if !ok || v.Claim == nil || v.Status != Claimed || ref.RunID != run.RunID || ref.OwnerID != run.OwnerID || ref.FencingToken != run.FencingToken {
		return ErrInvalidReference
	}
	v.Status = Applied
	return nil
}

func (q *SteerQueue) RejectRun(runID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, v := range q.items {
		if v.TargetRunID == runID && (v.Status == Accepted || v.Status == Claimed) {
			v.Status = Rejected
			v.Claim = nil
		}
	}
}

type FollowUpQueue struct {
	mu        sync.Mutex
	next      int64
	items     map[FollowUpItemID]*FollowUpItem
	admission AdmissionPort
}

func NewFollowUpQueue(admission AdmissionPort) *FollowUpQueue {
	return &FollowUpQueue{items: map[FollowUpItemID]*FollowUpItem{}, admission: admission}
}

func (q *FollowUpQueue) Enqueue(ctx context.Context, botID, sessionID, id string, payload []byte) (EnqueueReceipt[FollowUpItemID], error) {
	if q == nil || q.admission == nil {
		return EnqueueReceipt[FollowUpItemID]{}, errors.New("queue: admission is required")
	}
	a, err := q.admission.AdmitQueue(ctx, botID, sessionID)
	if err != nil {
		return EnqueueReceipt[FollowUpItemID]{}, err
	}
	if !a.Active || a.RunID == "" {
		return EnqueueReceipt[FollowUpItemID]{}, ErrNoActiveRun
	}
	if id == "" {
		return EnqueueReceipt[FollowUpItemID]{}, errors.New("queue: item id is required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.items[FollowUpItemID(id)]; ok {
		if !bytes.Equal(q.items[FollowUpItemID(id)].Payload, payload) {
			return EnqueueReceipt[FollowUpItemID]{}, ErrInvocationConflict
		}
		return EnqueueReceipt[FollowUpItemID]{ItemID: FollowUpItemID(id), Status: Accepted}, nil
	}
	q.next++
	q.items[FollowUpItemID(id)] = &FollowUpItem{ID: FollowUpItemID(id), BotID: botID, SessionID: sessionID, EnqueuedDuringRunID: a.RunID, Payload: append([]byte(nil), payload...), Status: Accepted, Position: q.next, CreatedAt: time.Now()}
	return EnqueueReceipt[FollowUpItemID]{ItemID: FollowUpItemID(id), Status: Accepted, AcceptedAt: time.Now()}, nil
}

func (q *FollowUpQueue) Get(id FollowUpItemID) (FollowUpItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[id]
	if !ok {
		return FollowUpItem{}, false
	}
	return cloneFollowUp(*v), true
}

func (q *FollowUpQueue) Reorder(id, before FollowUpItemID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return reorderFollowUp(q.items, id, before)
}

func (q *FollowUpQueue) Update(id FollowUpItemID, payload []byte) (FollowUpItem, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[id]
	if !ok || v.Status != Accepted || v.AssignedRunID != "" {
		return FollowUpItem{}, ErrNotPending
	}
	v.Payload = append(v.Payload[:0], payload...)
	return cloneFollowUp(*v), nil
}

func (q *FollowUpQueue) Cancel(id FollowUpItemID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[id]
	if !ok || v.Status != Accepted || v.AssignedRunID != "" {
		return ErrNotPending
	}
	v.Status = Canceled
	return nil
}

func reorderFollowUp(items map[FollowUpItemID]*FollowUpItem, id, before FollowUpItemID) error {
	a, ok := items[id]
	if !ok || a.Status != Accepted || a.AssignedRunID != "" {
		return ErrNotPending
	}
	if before != "" {
		b, ok := items[before]
		if !ok || b.Status != Accepted || b.AssignedRunID != "" {
			return ErrNotPending
		}
	}
	if id == before {
		return nil
	}
	ordered := make([]*FollowUpItem, 0, len(items))
	for _, v := range sortedFollowUp(items) {
		if v.Status == Accepted && v.AssignedRunID == "" {
			ordered = append(ordered, v)
		}
	}
	ordered = removeFollowUp(ordered, id)
	if before == "" {
		ordered = append(ordered, a)
	} else {
		idx := indexFollowUp(ordered, before)
		if idx < 0 {
			return ErrNotPending
		}
		ordered = append(ordered, nil)
		copy(ordered[idx+1:], ordered[idx:])
		ordered[idx] = a
	}
	for i, v := range ordered {
		if v.Status == Accepted && v.AssignedRunID == "" {
			v.Position = int64(i + 1)
		}
	}
	return nil
}

func (q *FollowUpQueue) AssignNext(runID string) (FollowUpItem, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, v := range sortedFollowUp(q.items) {
		if v.Status == Accepted && v.AssignedRunID == "" {
			v.AssignedRunID = runID
			return cloneFollowUp(*v), nil
		}
	}
	return FollowUpItem{}, ErrNotPending
}

func (q *FollowUpQueue) NextPending() (FollowUpItem, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, v := range sortedFollowUp(q.items) {
		if v.Status == Accepted && v.AssignedRunID == "" {
			return cloneFollowUp(*v), nil
		}
	}
	return FollowUpItem{}, ErrNotPending
}

// Assign binds an accepted pending item to a pre-created continuation run.
// The binding is durable and exclusive; it is separate from claiming so an
// ownerless continuation can be recovered after a process crash.
func (q *FollowUpQueue) Assign(itemID FollowUpItemID, runID string) (FollowUpItem, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[itemID]
	if !ok || v.Status != Accepted || v.AssignedRunID != "" || runID == "" {
		return FollowUpItem{}, ErrInvalidReference
	}
	v.AssignedRunID = runID
	return cloneFollowUp(*v), nil
}

func (q *FollowUpQueue) Unassign(itemID FollowUpItemID, runID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[itemID]
	if !ok || v.Status != Accepted || v.AssignedRunID != runID {
		return ErrInvalidReference
	}
	v.AssignedRunID = ""
	return nil
}

func (q *FollowUpQueue) ClaimAssigned(itemID FollowUpItemID, run sessionruntime.RunHandle) (FollowUpItem, FollowUpClaimRef, error) {
	if err := validateRun(run); err != nil {
		return FollowUpItem{}, FollowUpClaimRef{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[itemID]
	if !ok || v.AssignedRunID != run.RunID || v.Status != Accepted {
		return FollowUpItem{}, FollowUpClaimRef{}, ErrInvalidReference
	}
	v.Status = Claimed
	ref := FollowUpClaimRef{itemID, run.RunID, run.OwnerID, run.FencingToken}
	v.Claim = &ref
	return cloneFollowUp(*v), ref, nil
}

func (q *FollowUpQueue) Reclaim(ref FollowUpClaimRef, run sessionruntime.RunHandle) (FollowUpClaimRef, error) {
	if err := validateRun(run); err != nil {
		return FollowUpClaimRef{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[ref.ItemID]
	if !ok || v.Claim == nil || v.Status != Claimed || v.AssignedRunID != run.RunID {
		return FollowUpClaimRef{}, ErrInvalidReference
	}
	n := FollowUpClaimRef{ref.ItemID, run.RunID, run.OwnerID, run.FencingToken}
	v.Claim = &n
	return n, nil
}

func (q *FollowUpQueue) Apply(ref FollowUpClaimRef, run sessionruntime.RunHandle) error {
	if err := validateRun(run); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[ref.ItemID]
	if !ok || v.Claim == nil || v.Status != Claimed || ref.RunID != run.RunID || ref.OwnerID != run.OwnerID || ref.FencingToken != run.FencingToken {
		return ErrInvalidReference
	}
	v.Status = Applied
	return nil
}

func (q *FollowUpQueue) RejectAssigned(runID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, v := range q.items {
		if v.AssignedRunID == runID && (v.Status == Accepted || v.Status == Claimed) {
			v.Status = Rejected
			v.Claim = nil
		}
	}
}

func validateRun(run sessionruntime.RunHandle) error {
	if strings.TrimSpace(run.RunID) == "" || strings.TrimSpace(run.OwnerID) == "" || run.FencingToken <= 0 {
		return errors.New("queue: owned run handle is required")
	}
	return nil
}

func sortedSteer(m map[SteerItemID]*SteerItem) []*SteerItem {
	r := make([]*SteerItem, 0, len(m))
	for _, v := range m {
		r = append(r, v)
	}
	sort.Slice(r, func(i, j int) bool { return r[i].Position < r[j].Position })
	return r
}

func sortedFollowUp(m map[FollowUpItemID]*FollowUpItem) []*FollowUpItem {
	r := make([]*FollowUpItem, 0, len(m))
	for _, v := range m {
		r = append(r, v)
	}
	sort.Slice(r, func(i, j int) bool { return r[i].Position < r[j].Position })
	return r
}

func reorderSteer(m map[SteerItemID]*SteerItem, id, before SteerItemID) error {
	a, ok := m[id]
	if !ok || a.Status != Accepted {
		return ErrNotPending
	}
	if before != "" {
		b, ok := m[before]
		if !ok || b.Status != Accepted {
			return ErrNotPending
		}
	}
	if id == before {
		return nil
	}
	ordered := make([]*SteerItem, 0, len(m))
	for _, v := range sortedSteer(m) {
		if v.Status == Accepted {
			ordered = append(ordered, v)
		}
	}
	ordered = removeSteer(ordered, id)
	if before == "" {
		ordered = append(ordered, a)
	} else {
		idx := indexSteer(ordered, before)
		if idx < 0 {
			return ErrNotPending
		}
		ordered = append(ordered, nil)
		copy(ordered[idx+1:], ordered[idx:])
		ordered[idx] = a
	}
	for i, v := range ordered {
		if v.Status == Accepted {
			v.Position = int64(i + 1)
		}
	}
	return nil
}

func removeSteer(items []*SteerItem, id SteerItemID) []*SteerItem {
	for i, v := range items {
		if v.ID == id {
			return append(items[:i], items[i+1:]...)
		}
	}
	return items
}

func indexSteer(items []*SteerItem, id SteerItemID) int {
	for i, v := range items {
		if v.ID == id {
			return i
		}
	}
	return -1
}

func removeFollowUp(items []*FollowUpItem, id FollowUpItemID) []*FollowUpItem {
	for i, v := range items {
		if v.ID == id {
			return append(items[:i], items[i+1:]...)
		}
	}
	return items
}

func indexFollowUp(items []*FollowUpItem, id FollowUpItemID) int {
	for i, v := range items {
		if v.ID == id {
			return i
		}
	}
	return -1
}

func cloneSteer(v SteerItem) SteerItem {
	v.Payload = append([]byte(nil), v.Payload...)
	if v.Claim != nil {
		c := *v.Claim
		v.Claim = &c
	}
	return v
}

func cloneFollowUp(v FollowUpItem) FollowUpItem {
	v.Payload = append([]byte(nil), v.Payload...)
	if v.Claim != nil {
		c := *v.Claim
		v.Claim = &c
	}
	return v
}
