package runtimefence

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	ErrStale                        = errors.New("session runtime persistence fence is stale")
	ErrResetLeaseLost               = errors.New("session history reset lease was lost")
	ErrTransactionsUnsupported      = errors.New("session runtime persistence fencing requires real transactions")
	ErrPreservedDecisionUnavailable = errors.New("preserved runtime decision is no longer pending")
)

// Fence identifies the PostgreSQL generation allowed to persist one runtime
// run. A newer run increments Token and permanently invalidates older fences.
type Fence struct {
	BotID     string
	SessionID string
	Token     int64
}

const (
	DecisionToolApproval = "tool_approval"
	DecisionUserInput    = "user_input"
)

type PreservedDecision struct {
	Kind string
	ID   string
}

type ActivationOptions struct {
	PreserveDecision       *PreservedDecision
	ReclaimWaitingDecision *WaitingDecisionReclaim
}

// WaitingDecisionReclaim is committed atomically with the persistence-fence
// and preserved-decision token update.
type WaitingDecisionReclaim struct {
	RunID          string
	OwnerID        string
	PreviousToken  int64
	LiveGeneration string
}

func (f Fence) Valid() bool {
	return strings.TrimSpace(f.BotID) != "" && strings.TrimSpace(f.SessionID) != "" && f.Token > 0
}

type contextKey struct{}

type resetContextKey struct{}

type ResetFence struct {
	Scope     string
	BotID     string
	SessionID string
	Token     string
	LeaseTTL  time.Duration
}

func (f ResetFence) Valid() bool {
	f.Scope = strings.TrimSpace(f.Scope)
	f.BotID = strings.TrimSpace(f.BotID)
	f.SessionID = strings.TrimSpace(f.SessionID)
	f.Token = strings.TrimSpace(f.Token)
	return f.BotID != "" && f.Token != "" && (f.Scope == "bot" || f.Scope == "session" && f.SessionID != "")
}

func WithResetContext(ctx context.Context, fence ResetFence) context.Context {
	if ctx == nil || !fence.Valid() {
		return ctx
	}
	fence.Scope = strings.TrimSpace(fence.Scope)
	fence.BotID = strings.TrimSpace(fence.BotID)
	fence.SessionID = strings.TrimSpace(fence.SessionID)
	fence.Token = strings.TrimSpace(fence.Token)
	return context.WithValue(ctx, resetContextKey{}, fence)
}

func ResetFromContext(ctx context.Context) (ResetFence, bool) {
	if ctx == nil {
		return ResetFence{}, false
	}
	fence, ok := ctx.Value(resetContextKey{}).(ResetFence)
	return fence, ok && fence.Valid()
}

func WithContext(ctx context.Context, fence Fence) context.Context {
	if ctx == nil || !fence.Valid() {
		return ctx
	}
	fence.BotID = strings.TrimSpace(fence.BotID)
	fence.SessionID = strings.TrimSpace(fence.SessionID)
	return context.WithValue(ctx, contextKey{}, fence)
}

func FromContext(ctx context.Context) (Fence, bool) {
	if ctx == nil {
		return Fence{}, false
	}
	fence, ok := ctx.Value(contextKey{}).(Fence)
	return fence, ok && fence.Valid()
}

// ValidateScope rejects a durable write that does not belong to the session
// represented by the context fence. Unfenced contexts remain unchanged.
func ValidateScope(ctx context.Context, botID, sessionID string) error {
	fence, ok := FromContext(ctx)
	if !ok {
		return nil
	}
	if strings.TrimSpace(botID) != fence.BotID || strings.TrimSpace(sessionID) != fence.SessionID {
		return ErrStale
	}
	return nil
}
