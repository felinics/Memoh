package sessionruntime

import (
	"time"

	"github.com/google/uuid"
)

// Queue transitions have one implementation. Backends supply serialization,
// ownership validation and their authoritative clock, then store the result.
func (state *steerQueueState) claimNext(handle RunHandle, sealIfEmpty bool, now time.Time) (SteerItem, SteerClaimRef, bool) {
	for i := range state.Items {
		item := &state.Items[i]
		if item.Status == QueueClaimed && item.Claim != nil && item.Claim.RunID == handle.RunID {
			if advanceSteerClaim(item, handle) {
				state.UpdatedAt = now
			}
			return cloneSteerItem(*item), *item.Claim, true
		}
	}
	best := -1
	for i := range state.Items {
		if state.Items[i].Status == QueueAccepted && state.Items[i].TargetRunID == handle.RunID &&
			(best < 0 || state.Items[i].Position < state.Items[best].Position) {
			best = i
		}
	}
	if best < 0 {
		if sealIfEmpty {
			state.ClosedRunID = handle.RunID
			state.UpdatedAt = now
		}
		return SteerItem{}, SteerClaimRef{}, false
	}
	claim := SteerClaimRef{ItemID: state.Items[best].ID, RunID: handle.RunID, OwnerID: handle.OwnerID, Generation: handle.Generation, FencingToken: handle.FencingToken, ClaimToken: uuid.NewString()}
	state.Items[best].Status = QueueClaimed
	state.Items[best].Claim = &claim
	state.UpdatedAt = now
	return cloneSteerItem(state.Items[best]), claim, true
}

func (state *followUpQueueState) claimNext(triggerRunID string, now time.Time) (FollowUpItem, FollowUpClaimRef, bool) {
	if state.TerminalClaims == nil {
		state.TerminalClaims = make(map[string]string)
	}
	if itemID := state.TerminalClaims[triggerRunID]; itemID != "" {
		for _, item := range state.Items {
			if string(item.ID) == itemID && item.Status == QueueClaimed && item.Claim != nil {
				return cloneFollowUpItem(item), *item.Claim, true
			}
		}
		return FollowUpItem{}, FollowUpClaimRef{}, false
	}
	best := -1
	for i := range state.Items {
		if state.Items[i].Status == QueueAccepted && (best < 0 || state.Items[i].Position < state.Items[best].Position) {
			best = i
		}
	}
	if best < 0 {
		return FollowUpItem{}, FollowUpClaimRef{}, false
	}
	claim := FollowUpClaimRef{ItemID: state.Items[best].ID, TriggerRunID: triggerRunID, ClaimToken: uuid.NewString()}
	state.Items[best].Status = QueueClaimed
	state.Items[best].Claim = &claim
	state.TerminalClaims[triggerRunID] = string(state.Items[best].ID)
	state.UpdatedAt = now
	return cloneFollowUpItem(state.Items[best]), claim, true
}

func (state *steerQueueState) apply(ref SteerClaimRef, now time.Time) error {
	for i := range state.Items {
		claim := state.Items[i].Claim
		if state.Items[i].ID == ref.ItemID && state.Items[i].Status == QueueClaimed && claim != nil && *claim == ref {
			state.Items[i].Status = QueueApplied
			state.UpdatedAt = now
			state.compact()
			return nil
		}
	}
	return ErrQueueInvalidReference
}

func (state *steerQueueState) release(ref SteerClaimRef, now time.Time) error {
	for i := range state.Items {
		claim := state.Items[i].Claim
		if state.Items[i].ID == ref.ItemID && state.Items[i].Status == QueueClaimed && claim != nil && *claim == ref {
			state.Items[i].Status = QueueAccepted
			state.Items[i].Claim = nil
			state.UpdatedAt = now
			return nil
		}
	}
	return ErrQueueInvalidReference
}

func (state *followUpQueueState) apply(ref FollowUpClaimRef, now time.Time) error {
	for i := range state.Items {
		claim := state.Items[i].Claim
		if state.Items[i].ID == ref.ItemID && state.Items[i].Status == QueueClaimed && claim != nil && *claim == ref {
			state.Items[i].Status = QueueApplied
			state.UpdatedAt = now
			state.compact()
			return nil
		}
	}
	return ErrQueueInvalidReference
}

func (state *followUpQueueState) release(ref FollowUpClaimRef, now time.Time) error {
	for i := range state.Items {
		claim := state.Items[i].Claim
		if state.Items[i].ID == ref.ItemID && state.Items[i].Status == QueueClaimed && claim != nil && *claim == ref {
			state.Items[i].Status = QueueAccepted
			state.Items[i].Claim = nil
			delete(state.TerminalClaims, ref.TriggerRunID)
			state.UpdatedAt = now
			return nil
		}
	}
	return ErrQueueInvalidReference
}

func (state *followUpQueueState) reject(ref FollowUpClaimRef, errorCode string, now time.Time) error {
	for i := range state.Items {
		claim := state.Items[i].Claim
		if state.Items[i].ID == ref.ItemID && state.Items[i].Status == QueueClaimed && claim != nil && *claim == ref {
			state.Items[i].Status = QueueRejected
			state.Items[i].ErrorCode = errorCode
			state.Items[i].Claim = nil
			delete(state.TerminalClaims, ref.TriggerRunID)
			state.UpdatedAt = now
			state.compact()
			return nil
		}
	}
	return ErrQueueInvalidReference
}

func (state *steerQueueState) edit(itemID SteerItemID, payload []byte, now time.Time) (SteerItem, error) {
	for i := range state.Items {
		if state.Items[i].ID == itemID && state.Items[i].Status == QueueAccepted {
			state.Items[i].Payload = append([]byte(nil), payload...)
			state.UpdatedAt = now
			return cloneSteerItem(state.Items[i]), nil
		}
	}
	return SteerItem{}, ErrQueueNotPending
}

func (state *steerQueueState) cancel(itemID SteerItemID, now time.Time) error {
	for i := range state.Items {
		if state.Items[i].ID == itemID && state.Items[i].Status == QueueAccepted {
			state.Items[i].Status = QueueCanceled
			state.UpdatedAt = now
			state.compact()
			return nil
		}
	}
	return ErrQueueNotPending
}

func (state *followUpQueueState) edit(itemID FollowUpItemID, payload []byte, now time.Time) (FollowUpItem, error) {
	for i := range state.Items {
		if state.Items[i].ID == itemID && state.Items[i].Status == QueueAccepted {
			state.Items[i].Payload = append([]byte(nil), payload...)
			state.UpdatedAt = now
			return cloneFollowUpItem(state.Items[i]), nil
		}
	}
	return FollowUpItem{}, ErrQueueNotPending
}

func (state *followUpQueueState) cancel(itemID FollowUpItemID, now time.Time) error {
	for i := range state.Items {
		if state.Items[i].ID == itemID && state.Items[i].Status == QueueAccepted {
			state.Items[i].Status = QueueCanceled
			state.UpdatedAt = now
			state.compact()
			return nil
		}
	}
	return ErrQueueNotPending
}

func (steers *steerQueueState) promote(follows *followUpQueueState, key Key, runID string, ref FollowUpPendingRef, now time.Time, newID string) (PromoteFollowUpResult, error) {
	if steerID := steers.PromotedFollowUpItems[string(ref.ItemID)]; steerID != "" {
		for _, existing := range steers.Items {
			if existing.ID == SteerItemID(steerID) {
				return PromoteFollowUpResult{FollowUp: ref, Steer: cloneSteerItem(existing)}, nil
			}
		}
		return PromoteFollowUpResult{}, ErrQueueInvalidReference
	}
	for i := range follows.Items {
		if follows.Items[i].ID != ref.ItemID || follows.Items[i].Status != QueueAccepted {
			continue
		}
		if countPendingSteers(*steers) >= MaxPendingQueueItems {
			return PromoteFollowUpResult{}, ErrQueueCapacityExceeded
		}
		steer := SteerItem{
			ID: SteerItemID(newID), BotID: key.BotID, SessionID: key.SessionID,
			TargetRunID: runID, InvocationID: "promote:" + string(ref.ItemID),
			Payload: append([]byte(nil), follows.Items[i].Payload...), Status: QueueAccepted,
			Position: nextSteerPosition(*steers), CreatedAt: now,
		}
		steers.Items = append(steers.Items, steer)
		if steers.PromotedFollowUpItems == nil {
			steers.PromotedFollowUpItems = make(map[string]string)
		}
		steers.PromotedFollowUpItems[string(ref.ItemID)] = string(steer.ID)
		steers.UpdatedAt = now
		follows.Items[i].Status = QueueCanceled
		follows.UpdatedAt = now
		follows.compact()
		return PromoteFollowUpResult{FollowUp: ref, Steer: cloneSteerItem(steer)}, nil
	}
	return PromoteFollowUpResult{}, ErrQueueNotPending
}

func (state *steerQueueState) enqueue(key Key, itemID, invocationID, runID string, payload []byte, now time.Time) (SteerItem, error) {
	if countPendingSteers(*state) >= MaxPendingQueueItems {
		return SteerItem{}, ErrQueueCapacityExceeded
	}
	item := SteerItem{
		ID: SteerItemID(itemID), BotID: key.BotID, SessionID: key.SessionID,
		TargetRunID: runID, InvocationID: invocationID, Payload: append([]byte(nil), payload...),
		Status: QueueAccepted, Position: nextSteerPosition(*state), CreatedAt: now,
	}
	state.Items = append(state.Items, item)
	state.UpdatedAt = now
	if state.ClosedRunID != "" && state.ClosedRunID != runID {
		state.ClosedRunID = ""
	}
	return cloneSteerItem(item), nil
}

func (state *followUpQueueState) enqueue(key Key, itemID, invocationID, runID string, payload []byte, now time.Time) (FollowUpItem, error) {
	if countPendingFollowUps(*state) >= MaxPendingQueueItems {
		return FollowUpItem{}, ErrQueueCapacityExceeded
	}
	item := FollowUpItem{
		ID: FollowUpItemID(itemID), BotID: key.BotID, SessionID: key.SessionID,
		EnqueuedDuringRunID: runID, InvocationID: invocationID, Payload: append([]byte(nil), payload...),
		Status: QueueAccepted, Position: nextFollowUpPosition(*state), CreatedAt: now,
	}
	state.Items = append(state.Items, item)
	state.UpdatedAt = now
	return cloneFollowUpItem(item), nil
}
