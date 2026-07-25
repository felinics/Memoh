package runtimefence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrRecordNotFound = errors.New("runtime fence persistence record not found")

type Store interface {
	LockBot(context.Context, string) error
	LockForActivation(context.Context, Fence) (int64, error)
	Activate(context.Context, Fence) (int64, error)
	ClaimToolApproval(context.Context, string, Fence) error
	ClaimUserInput(context.Context, string, Fence) error
	SupersedeToolApprovals(context.Context, Fence, string, string) error
	SupersedeUserInputs(context.Context, Fence, string, []byte) error
	Lock(context.Context, Fence) error
}

type Transactor interface {
	InRuntimeFenceTransaction(context.Context, func(Store) error) error
}

type Persistence interface {
	Store
	Transactor
}

// Activate is the persistence ownership cutover. Redis may already reserve the
// successor as admitting, but a writer holding the previous token still
// linearizes before this transaction if it acquired the session lock first.
// Once activation commits, the previous token can never write again. Cleanup
// uses later statements in the same transaction so it sees rows committed by a
// writer that activation had to wait for.
func Activate(ctx context.Context, persistence Persistence, fence Fence) error {
	return ActivateWithOptions(ctx, persistence, fence, ActivationOptions{})
}

func ActivateWithOptions(ctx context.Context, persistence Persistence, fence Fence, options ActivationOptions) error {
	if !fence.Valid() {
		return errors.New("runtime persistence fence is invalid")
	}
	if persistence == nil {
		return ErrTransactionsUnsupported
	}
	if preserved := options.PreserveDecision; preserved != nil {
		switch strings.TrimSpace(preserved.Kind) {
		case DecisionToolApproval, DecisionUserInput:
		default:
			return fmt.Errorf("unsupported preserved runtime decision kind %q", preserved.Kind)
		}
	}
	inputResult, err := json.Marshal(map[string]any{
		"status":      "canceled",
		"reason":      "runtime_superseded",
		"instruction": "The request was canceled because a newer runtime run took ownership.",
	})
	if err != nil {
		return fmt.Errorf("encode superseded runtime input result: %w", err)
	}

	err = persistence.InRuntimeFenceTransaction(ctx, func(store Store) error {
		if err := store.LockBot(ctx, fence.BotID); err != nil {
			return err
		}
		current, err := store.LockForActivation(ctx, fence)
		if errors.Is(err, ErrRecordNotFound) {
			return ErrStale
		}
		if err != nil {
			return fmt.Errorf("lock runtime fence for activation: %w", err)
		}
		switch {
		case current > fence.Token:
			return ErrStale
		case current == fence.Token:
			return nil
		}
		if preserved := options.PreserveDecision; preserved != nil {
			if err := claimPreservedDecision(ctx, store, *preserved, fence); err != nil {
				return err
			}
		}
		activated, err := store.Activate(ctx, fence)
		if errors.Is(err, ErrRecordNotFound) {
			return ErrStale
		}
		if err != nil {
			return fmt.Errorf("advance runtime persistence fence: %w", err)
		}
		if activated != fence.Token {
			return ErrStale
		}
		preserveToolApprovalID, preserveUserInputID := "", ""
		if preserved := options.PreserveDecision; preserved != nil {
			if strings.TrimSpace(preserved.Kind) == DecisionToolApproval {
				preserveToolApprovalID = strings.TrimSpace(preserved.ID)
			} else {
				preserveUserInputID = strings.TrimSpace(preserved.ID)
			}
		}
		if err := store.SupersedeToolApprovals(ctx, fence, preserveToolApprovalID, "tool approval cancelled: superseded by a newer runtime run"); err != nil {
			return fmt.Errorf("cancel superseded tool approvals: %w", err)
		}
		if err := store.SupersedeUserInputs(ctx, fence, preserveUserInputID, inputResult); err != nil {
			return fmt.Errorf("cancel superseded user inputs: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("activate runtime persistence fence: %w", err)
	}
	return nil
}

func claimPreservedDecision(
	ctx context.Context,
	store Store,
	preserved PreservedDecision,
	fence Fence,
) error {
	var err error
	switch strings.TrimSpace(preserved.Kind) {
	case DecisionToolApproval:
		err = store.ClaimToolApproval(ctx, strings.TrimSpace(preserved.ID), fence)
	case DecisionUserInput:
		err = store.ClaimUserInput(ctx, strings.TrimSpace(preserved.ID), fence)
	default:
		return fmt.Errorf("unsupported preserved runtime decision kind %q", preserved.Kind)
	}
	if errors.Is(err, ErrRecordNotFound) {
		return ErrPreservedDecisionUnavailable
	}
	if err != nil {
		return fmt.Errorf("claim preserved runtime decision: %w", err)
	}
	return nil
}

// InTransaction locks and validates the durable token in the same real
// PostgreSQL transaction as fn. Redis admission reserves control ownership;
// token activation is the linearization point for persistence ownership.
func InTransaction(
	ctx context.Context,
	persistence Persistence,
	botID string,
	sessionID string,
	fn func(Store) error,
) error {
	if fn == nil {
		return errors.New("runtime-fenced transaction callback is required")
	}
	fence, ok := FromContext(ctx)
	if !ok {
		return errors.New("runtime persistence fence is missing")
	}
	if strings.TrimSpace(botID) == "" {
		botID = fence.BotID
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = fence.SessionID
	}
	if err := ValidateScope(ctx, botID, sessionID); err != nil {
		return err
	}
	if persistence == nil {
		return ErrTransactionsUnsupported
	}
	return persistence.InRuntimeFenceTransaction(ctx, func(store Store) error {
		if err := store.LockBot(ctx, botID); err != nil {
			return err
		}
		if err := lock(ctx, store, botID, sessionID); err != nil {
			return err
		}
		return fn(store)
	})
}

// lock validates the current fence and serializes writers on the session row
// until the caller's transaction ends. A successor cannot activate its token
// until that write commits, and an older token cannot write after activation.
func lock(ctx context.Context, store Store, botID, sessionID string) error {
	fence, ok := FromContext(ctx)
	if !ok {
		return errors.New("runtime persistence fence is missing")
	}
	if err := ValidateScope(ctx, botID, sessionID); err != nil {
		return err
	}
	err := store.Lock(ctx, fence)
	if errors.Is(err, ErrRecordNotFound) {
		return ErrStale
	}
	if err != nil {
		return fmt.Errorf("lock runtime persistence fence: %w", err)
	}
	return nil
}
