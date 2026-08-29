package queue

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	dbstore "github.com/felinics/memoh/internal/db/store"
)

// PromoteFollowUpRequest is a coordinator command, not a mixed queue item.
// The typed follow-up reference is consumed and a distinct typed steer item is
// created for the active run selected by server-side admission.
type PromoteFollowUpRequest struct {
	BotID, SessionID string
	FollowUp         FollowUpPendingRef
}

type PromoteFollowUpResult struct {
	FollowUp FollowUpPendingRef
	Steer    SteerItem
}

// PromotionCoordinator atomically moves explicit user intent across the two
// independent queue boundaries. Its caller owns the transaction and holds the
// session admission lock shared with CommitStep reconciliation.
type PromotionCoordinator struct {
	q dbstore.Queries
}

func NewPromotionCoordinator(q dbstore.Queries) *PromotionCoordinator {
	return &PromotionCoordinator{q: q}
}

func (c *PromotionCoordinator) PromoteFollowUpToSteer(
	ctx context.Context,
	req PromoteFollowUpRequest,
) (PromoteFollowUpResult, error) {
	var result PromoteFollowUpResult
	if c == nil || c.q == nil {
		return result, errors.New("queue: promotion coordinator is not configured")
	}
	req.BotID = strings.TrimSpace(req.BotID)
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.BotID == "" || req.SessionID == "" || req.FollowUp.ItemID == "" {
		return result, ErrInvalidReference
	}

	store := NewPostgresStore(c.q)
	// Queue types remain distinct even though the server deterministically uses
	// the source UUID for the new typed steer identity. A retry after commit can
	// therefore replay without reading or mutating the canceled follow-up.
	steerID := SteerItemID(req.FollowUp.ItemID)
	if existing, err := store.SteerByID(ctx, steerID); err == nil {
		if existing.BotID != req.BotID || existing.SessionID != req.SessionID {
			return result, ErrInvalidReference
		}
		return PromoteFollowUpResult{FollowUp: req.FollowUp, Steer: existing}, nil
	} else if !errors.Is(err, ErrInvalidReference) {
		return result, err
	}

	followUp, err := store.FollowUpByID(ctx, req.FollowUp.ItemID)
	if err != nil {
		return result, ErrNotPending
	}
	if followUp.BotID != req.BotID || followUp.SessionID != req.SessionID ||
		followUp.Status != Accepted || followUp.AssignedRunID != "" || followUp.Claim != nil {
		return result, ErrNotPending
	}

	steer, err := store.EnqueueSteer(
		ctx,
		req.BotID,
		req.SessionID,
		string(steerID),
		uuid.NewString(),
		followUp.Payload,
	)
	if err != nil {
		return result, err
	}
	if err := store.CancelFollowUp(ctx, req.SessionID, string(req.FollowUp.ItemID)); err != nil {
		return result, err
	}
	return PromoteFollowUpResult{FollowUp: req.FollowUp, Steer: steer}, nil
}
