package approval

import (
	"context"
	"errors"
	"strings"
	"time"
)

// DefaultWaitTimeout bounds how long the approval flow waits for a decision
// before rejecting the request as timed out.
const DefaultWaitTimeout = 10 * time.Minute

// FlowService is the subset of the approval service the shared flow needs.
// Both the native MCP gateway and the ACP client callbacks satisfy it with
// *Service.
type FlowService interface {
	EvaluatePolicy(ctx context.Context, input CreatePendingInput) (Evaluation, error)
	CreatePending(ctx context.Context, input CreatePendingInput) (Request, error)
	Get(ctx context.Context, approvalID string) (Request, error)
	Reject(ctx context.Context, approvalID, actorID, reason string) (Request, error)
	WaitForDecision(ctx context.Context, approvalID string) (Request, error)
}

type ApprovalTimeoutNotifier interface {
	NotifyApprovalTimeout(ctx context.Context, req Request)
}

// FlowRequest parameterizes one run of the pending-approval state machine.
type FlowRequest struct {
	Input CreatePendingInput
	// Interactive reports whether a live stream can deliver the approval
	// prompt to a user. Non-interactive requests are created and immediately
	// rejected so the pending row never dangles.
	Interactive bool
	// NonInteractiveReason is the rejection reason recorded when Interactive
	// is false.
	NonInteractiveReason string
	// WaitTimeout bounds the wait for a decision; zero means
	// DefaultWaitTimeout. Production callers should leave it zero - it exists
	// so tests can exercise the timeout path quickly.
	WaitTimeout time.Duration
	// Emit receives every user-visible state change (pending, then the terminal
	// snapshot). It returns whether the event was delivered. Pending delivery
	// failure closes the request immediately; terminal delivery is best-effort.
	// May be nil. Policy bypasses and non-interactive auto-rejections are not
	// emitted - there is no stream to show them on.
	Emit func(Request) bool
	// RegisterWaiter records that this process is waiting on the request.
	// Callers must provide it when responders use waiter presence to reject
	// orphaned answers. It runs before Emit.
	RegisterWaiter func(approvalID string) func()
	// UndeliveredReason is recorded when an interactive pending approval could
	// not be delivered to the live stream.
	UndeliveredReason string
	// AbortReason is recorded when the caller is cancelled while waiting.
	AbortReason string
}

// FlowResult describes how the approval flow concluded.
type FlowResult struct {
	Approved bool
	// Status is the terminal status after defaulting (approved/rejected/...).
	Status string
	// DecisionReason carries the decider's (or the system's) reason.
	DecisionReason string
	// DecidedByUser is true only when a live decision arrived from a user;
	// system outcomes (policy bypass, non-interactive auto-reject, timeout)
	// leave it false so callers can distinguish "user said no" from "nobody
	// was there to ask".
	DecidedByUser bool
}

// RunFlow executes the pending-approval state machine shared by the native
// MCP gateway and the ACP client callbacks: evaluate policy, create a pending
// request, publish it, wait for the decision (rejecting on timeout), and
// publish the terminal snapshot.
func RunFlow(ctx context.Context, svc FlowService, flow FlowRequest) (FlowResult, error) {
	eval, err := svc.EvaluatePolicy(ctx, flow.Input)
	if err != nil {
		return FlowResult{}, err
	}
	flow.Input.ExecutionLocation = eval.ExecutionLocation
	if eval.Decision == DecisionBypass {
		return FlowResult{Approved: true, Status: StatusApproved}, nil
	}
	if eval.Decision == DecisionDeny {
		return FlowResult{
			Status:         StatusRejected,
			DecisionReason: PolicyDeniedReason,
		}, nil
	}

	waitTimeout := flow.WaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = DefaultWaitTimeout
	}
	req, err := svc.CreatePending(ctx, flow.Input)
	if err != nil {
		return FlowResult{}, err
	}
	if !strings.EqualFold(NormalizedStatus(req.Status), StatusPending) {
		return FlowResult{}, ErrAlreadyDecided
	}
	if !flow.Interactive {
		reason := strings.TrimSpace(flow.NonInteractiveReason)
		if reason == "" {
			reason = "tool execution requires approval, but this request is not attached to an interactive stream"
		}
		// Reject on a detached ctx for the same reason as the timeout path:
		// the pending row must not dangle if the caller is torn down here.
		rejectCtx, rejectCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer rejectCancel()
		rejected, rejectErr := svc.Reject(rejectCtx, req.ID, "", reason)
		if rejectErr != nil {
			if recovered, ok := recoverTerminalDecision(rejectCtx, svc, req.ID, rejectErr); ok {
				return resultFromDecision(req, recovered, func(Request) bool { return true }), nil
			}
			return FlowResult{}, rejectErr
		}
		if recorded := strings.TrimSpace(rejected.DecisionReason); recorded != "" {
			reason = recorded
		}
		return FlowResult{Status: StatusRejected, DecisionReason: reason}, nil
	}

	emit := flow.Emit
	if emit == nil {
		emit = func(Request) bool { return true }
	}
	releaseWaiter := func() {}
	if flow.RegisterWaiter != nil {
		releaseWaiter = flow.RegisterWaiter(req.ID)
	}
	defer releaseWaiter()
	if !emit(req) {
		reason := strings.TrimSpace(flow.UndeliveredReason)
		if reason == "" {
			reason = "tool approval request was not delivered to the interactive stream"
		}
		rejectCtx, rejectCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer rejectCancel()
		rejected, rejectErr := svc.Reject(rejectCtx, req.ID, "", reason)
		if rejectErr != nil {
			if recovered, ok := recoverTerminalDecision(rejectCtx, svc, req.ID, rejectErr); ok {
				return resultFromDecision(req, recovered, emit), nil
			}
			return FlowResult{}, rejectErr
		}
		if recorded := strings.TrimSpace(rejected.DecisionReason); recorded != "" {
			reason = recorded
		}
		return FlowResult{Status: StatusRejected, DecisionReason: reason}, nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	decided, err := svc.WaitForDecision(waitCtx, req.ID)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			// The caller's ctx is still live: the user simply never decided.
			// Reject on a detached ctx so the pending row cannot dangle even
			// if the caller is torn down mid-flight.
			rejectCtx, rejectCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer rejectCancel()
			rejected, rejectErr := svc.Reject(rejectCtx, req.ID, "", "tool approval timed out")
			if rejectErr != nil {
				if recovered, ok := recoverTerminalDecision(rejectCtx, svc, req.ID, rejectErr); ok {
					return resultFromDecision(req, recovered, emit), nil
				}
				return FlowResult{}, rejectErr
			}
			reason := strings.TrimSpace(rejected.DecisionReason)
			if reason == "" {
				reason = "tool approval timed out"
			}
			timeoutReq := req
			timeoutReq.Status = StatusRejected
			timeoutReq.DecisionReason = reason
			if notifier, ok := svc.(ApprovalTimeoutNotifier); ok {
				notifier.NotifyApprovalTimeout(rejectCtx, timeoutReq)
			}
			_ = emit(timeoutReq)
			return FlowResult{Status: StatusRejected, DecisionReason: reason}, nil
		}
		if ctx.Err() != nil {
			reason := strings.TrimSpace(flow.AbortReason)
			if reason == "" {
				reason = "tool approval aborted"
			}
			rejectCtx, rejectCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer rejectCancel()
			rejected, rejectErr := svc.Reject(rejectCtx, req.ID, "", reason)
			if rejectErr != nil {
				if recovered, ok := recoverTerminalDecision(rejectCtx, svc, req.ID, rejectErr); ok {
					return resultFromDecision(req, recovered, emit), nil
				}
				return FlowResult{}, rejectErr
			}
			if recorded := strings.TrimSpace(rejected.DecisionReason); recorded != "" {
				reason = recorded
			}
			abortReq := req
			abortReq.Status = StatusRejected
			abortReq.DecisionReason = reason
			_ = emit(abortReq)
			return FlowResult{}, err
		}
		return FlowResult{}, err
	}

	return resultFromDecision(req, decided, emit), nil
}

func recoverTerminalDecision(ctx context.Context, svc FlowService, approvalID string, cause error) (Request, bool) {
	if !errors.Is(cause, ErrAlreadyDecided) && !errors.Is(cause, ErrNotFound) {
		return Request{}, false
	}
	req, err := svc.Get(ctx, approvalID)
	if err != nil || strings.EqualFold(NormalizedStatus(req.Status), StatusPending) {
		return Request{}, false
	}
	return req, true
}

func resultFromDecision(req Request, decided Request, emit func(Request) bool) FlowResult {
	decisionReq := req
	if status := strings.TrimSpace(decided.Status); status != "" {
		decisionReq.Status = status
	} else {
		decisionReq.Status = StatusRejected
	}
	decisionReq.DecisionReason = decided.DecisionReason
	if decided.DecidedAt != nil {
		decisionReq.DecidedAt = decided.DecidedAt
	}
	if emit != nil {
		_ = emit(decisionReq)
	}
	// Only an explicit approve/reject counts as a user decision; other
	// terminal statuses a decided row can carry (expired, cancelled, a
	// concurrent waiter's timeout-reject) are system outcomes.
	decidedByUser := decided.DecidedByUser && strings.TrimSpace(decided.Status) != "" &&
		(strings.EqualFold(decisionReq.Status, StatusApproved) || strings.EqualFold(decisionReq.Status, StatusRejected))
	return FlowResult{
		Approved:       strings.EqualFold(decisionReq.Status, StatusApproved),
		Status:         decisionReq.Status,
		DecisionReason: decisionReq.DecisionReason,
		DecidedByUser:  decidedByUser,
	}
}

// NormalizedStatus defaults an empty status to pending.
func NormalizedStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return StatusPending
	}
	return status
}

// CanApprove reports whether a request in the given status still accepts a
// decision.
func CanApprove(status string) bool {
	return strings.EqualFold(NormalizedStatus(status), StatusPending)
}

// RejectionMessage renders the agent-visible text for an unapproved flow
// result. User rejections and system rejections are worded differently.
func RejectionMessage(result FlowResult) string {
	reason := strings.TrimSpace(result.DecisionReason)
	if result.DecidedByUser {
		if reason == "" {
			return "tool execution rejected by user"
		}
		return "tool execution rejected by user: " + reason
	}
	if reason == "" && result.Status != "" && !strings.EqualFold(result.Status, StatusRejected) {
		reason = result.Status
	}
	if reason == "" {
		return "tool execution was not approved"
	}
	return "tool execution was not approved: " + reason
}

// RequestMetadata is the shared wire payload for approval request events.
func RequestMetadata(req Request) map[string]any {
	status := NormalizedStatus(req.Status)
	metadata := map[string]any{
		"approval_id": req.ID,
		"short_id":    req.ShortID,
		"status":      status,
		"can_approve": CanApprove(status),
	}
	if operation := strings.TrimSpace(req.Operation); operation != "" {
		metadata["operation"] = operation
	}
	if reason := strings.TrimSpace(req.DecisionReason); reason != "" {
		metadata["decision_reason"] = reason
	}
	if req.ExecutionLocation != nil {
		metadata[ExecutionLocationMetadataKey] = req.ExecutionLocation
	}
	return metadata
}
