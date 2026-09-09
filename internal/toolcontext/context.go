// Package toolcontext owns the request-scoped identity and lifecycle shared by
// every Memoh tool transport. It is independent of MCP, ACP, and native tool
// protocols so those adapters can all bind work to the same agent run.
package toolcontext

import (
	"context"
	"errors"
	"strings"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/runtimefence"
)

// Session carries request-scoped identity and runtime ownership for tool
// execution. Runtime-only fields remain process-local and are never serialized.
type Session struct {
	BotID             string
	ChatID            string
	RuntimeID         string
	RuntimeToken      string `json:"-"`
	SessionID         string
	RunID             string
	ToolCallID        string
	SessionType       string
	RouteID           string
	ChannelIdentityID string
	SessionToken      string `json:"-"`
	CurrentPlatform   string
	ReplyTarget       string
	ConversationType  string
	// ReasoningStoredEffort and ReasoningRequestedEffort are unresolved turn
	// inputs. A tool that selects another model must resolve them against that
	// model instead of inheriting the parent runtime's provider-specific result.
	ReasoningStoredEffort    string
	ReasoningRequestedEffort string
	CanRequestUserInput      bool
	CanListUserInput         bool
	IsSubagent               bool
	RuntimeActive            bool
	// RequireActiveRun declares that this session's tool surface exists only
	// for the duration of a runtime turn. Workspace tool-gateway mounts set it;
	// the general HTTP tool API remains available outside a turn.
	RequireActiveRun          bool
	SupportsImageInput        bool
	SupportsFileInput         bool
	ContextBudgetMaxTokens    int
	ContextToolExchangePolicy *contextfrag.ToolExchangePolicy
	RuntimeFence              runtimefence.Fence          `json:"-"`
	RunContext                context.Context             `json:"-"`
	RuntimeGuard              func(context.Context) error `json:"-"`
}

const runtimeGuardTimeout = 5 * time.Second

// Bind gives a tool callback the owning run's request-scoped values and makes
// it stop when either the callback or the run stops. The run context is the
// authoritative source of tenant, identity, trace, and other turn values;
// transport callback contexts contribute their independent cancellation.
func Bind(callbackCtx context.Context, session Session) (context.Context, context.CancelFunc) {
	if callbackCtx == nil {
		callbackCtx = context.Background()
	}
	if session.RunContext == nil {
		return runtimefence.WithContext(callbackCtx, session.RuntimeFence), func() {}
	}

	base := runtimefence.WithContext(session.RunContext, session.RuntimeFence)
	bound, cancel := context.WithCancel(base)
	stop := context.AfterFunc(callbackCtx, cancel)
	if callbackCtx.Err() != nil {
		cancel()
	}
	return bound, func() {
		stop()
		cancel()
	}
}

// ValidateRuntimeGuard performs the last ownership check immediately before a
// tool effect. The check remains bound to both the request and owning run.
func ValidateRuntimeGuard(ctx context.Context, session Session) error {
	if ctx == nil {
		return errors.New("runtime guard context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if session.RunContext != nil {
		if err := session.RunContext.Err(); err != nil {
			return err
		}
	}
	if session.RuntimeGuard == nil {
		return nil
	}
	guardCtx, cancel := context.WithTimeout(ctx, runtimeGuardTimeout)
	defer cancel()
	guardCtx = runtimefence.WithContext(guardCtx, session.RuntimeFence)
	if err := session.RuntimeGuard(guardCtx); err != nil {
		return err
	}
	if session.RunContext != nil {
		if err := session.RunContext.Err(); err != nil {
			return err
		}
	}
	return guardCtx.Err()
}

// Merge overlays every non-empty field of latest onto base; boolean
// capabilities are sticky-true. ToolCallID is excluded on purpose: it is
// per-call and assigned by the caller after merging.
func Merge(base, latest Session) Session {
	merged := base
	if value := strings.TrimSpace(latest.BotID); value != "" {
		merged.BotID = value
	}
	if value := strings.TrimSpace(latest.ChatID); value != "" {
		merged.ChatID = value
	}
	if value := strings.TrimSpace(latest.RuntimeID); value != "" {
		merged.RuntimeID = value
	}
	if value := strings.TrimSpace(latest.RuntimeToken); value != "" {
		merged.RuntimeToken = value
	}
	if value := strings.TrimSpace(latest.SessionID); value != "" {
		merged.SessionID = value
	}
	if value := strings.TrimSpace(latest.RunID); value != "" {
		merged.RunID = value
	}
	if value := strings.TrimSpace(latest.SessionType); value != "" {
		merged.SessionType = value
	}
	if value := strings.TrimSpace(latest.RouteID); value != "" {
		merged.RouteID = value
	}
	if value := strings.TrimSpace(latest.ChannelIdentityID); value != "" {
		merged.ChannelIdentityID = value
	}
	if value := strings.TrimSpace(latest.SessionToken); value != "" {
		merged.SessionToken = value
	}
	if value := strings.TrimSpace(latest.CurrentPlatform); value != "" {
		merged.CurrentPlatform = value
	}
	if value := strings.TrimSpace(latest.ReplyTarget); value != "" {
		merged.ReplyTarget = value
	}
	if value := strings.TrimSpace(latest.ConversationType); value != "" {
		merged.ConversationType = value
	}
	if value := strings.TrimSpace(latest.ReasoningStoredEffort); value != "" {
		merged.ReasoningStoredEffort = value
	}
	if value := strings.TrimSpace(latest.ReasoningRequestedEffort); value != "" {
		merged.ReasoningRequestedEffort = value
	}
	if latest.CanRequestUserInput {
		merged.CanRequestUserInput = true
	}
	if latest.CanListUserInput {
		merged.CanListUserInput = true
	}
	if latest.IsSubagent {
		merged.IsSubagent = true
	}
	if latest.RuntimeActive {
		merged.RuntimeActive = true
	}
	if latest.RequireActiveRun {
		merged.RequireActiveRun = true
	}
	if latest.SupportsImageInput {
		merged.SupportsImageInput = true
	}
	if latest.SupportsFileInput {
		merged.SupportsFileInput = true
	}
	if latest.ContextBudgetMaxTokens > 0 {
		merged.ContextBudgetMaxTokens = latest.ContextBudgetMaxTokens
	}
	if latest.ContextToolExchangePolicy != nil {
		merged.ContextToolExchangePolicy = latest.ContextToolExchangePolicy
	}
	if latest.RuntimeFence.Valid() {
		merged.RuntimeFence = latest.RuntimeFence
	}
	if latest.RunContext != nil {
		merged.RunContext = latest.RunContext
	}
	if latest.RuntimeGuard != nil {
		merged.RuntimeGuard = latest.RuntimeGuard
	}
	return merged
}
