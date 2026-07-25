package input

import (
	"context"
	"errors"
	"strings"
	"time"
)

// DefaultWaitTimeout bounds how long ask_user waits for a response before
// canceling the request.
const DefaultWaitTimeout = 10 * time.Minute

// FlowService is the subset of Service needed by the shared ask_user flow.
type FlowService interface {
	CreatePending(ctx context.Context, input CreatePendingInput) (Request, error)
	Cancel(ctx context.Context, input CancelInput) (Request, error)
	WaitForRegisteredResponse(ctx context.Context, requestID string) (Request, error)
	RegisterWaiter(requestID string) func()
}

type FlowRequest struct {
	Input CreatePendingInput
	// ActorChannelIdentityID is recorded on system cancels.
	ActorChannelIdentityID string
	Interactive            bool
	WaitTimeout            time.Duration
	Emit                   func(Request) bool
	NonInteractiveReason   string
	UndeliveredReason      string
	TimeoutReason          string
	AbortReason            string
}

type FlowResult struct {
	Request Request
}

// RunFlow executes the blocking ask_user state machine shared by native tool
// runtimes: create the pending request, publish it, wait for a response, and
// cancel on timeout, abort, or delivery failure.
func RunFlow(ctx context.Context, svc FlowService, flow FlowRequest) (FlowResult, error) {
	req, err := svc.CreatePending(ctx, flow.Input)
	if err != nil {
		return FlowResult{}, err
	}
	if req.Status != StatusPending {
		return FlowResult{}, ErrAlreadyDecided
	}
	if !flow.Interactive {
		canceled, err := cancelFlowRequest(ctx, svc, req.ID, flow.ActorChannelIdentityID, firstReason(flow.NonInteractiveReason, "user input requested without an interactive stream"))
		if err != nil {
			return FlowResult{}, err
		}
		return FlowResult{Request: canceled}, nil
	}

	emit := flow.Emit
	if emit == nil {
		emit = func(Request) bool { return true }
	}
	release := svc.RegisterWaiter(req.ID)
	released := false
	releaseWaiter := func() {
		if released {
			return
		}
		released = true
		release()
	}
	defer releaseWaiter()
	if !emit(req) {
		releaseWaiter()
		canceled, err := cancelFlowRequest(ctx, svc, req.ID, flow.ActorChannelIdentityID, firstReason(flow.UndeliveredReason, "user input request was not delivered to the interactive stream"))
		if err != nil {
			return FlowResult{}, err
		}
		return FlowResult{Request: canceled}, nil
	}

	waitTimeout := flow.WaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = DefaultWaitTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	resolved, err := svc.WaitForRegisteredResponse(waitCtx, req.ID)
	releaseWaiter()
	if err == nil {
		_ = emit(resolved)
		return FlowResult{Request: resolved}, nil
	}

	timedOut := errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
	reason := firstReason(flow.AbortReason, "user input aborted")
	if timedOut {
		reason = firstReason(flow.TimeoutReason, "user input timed out")
	}
	canceled, cancelErr := cancelFlowRequest(ctx, svc, req.ID, flow.ActorChannelIdentityID, reason)
	if cancelErr != nil {
		if late, waitErr := waitForLateFlowResponse(ctx, svc, req.ID); waitErr == nil &&
			late.Status != StatusPending && len(late.Result) > 0 {
			_ = emit(late)
			return FlowResult{Request: late}, nil
		}
	}
	if !timedOut {
		if cancelErr == nil {
			_ = emit(canceled)
		}
		return FlowResult{}, err
	}
	if cancelErr != nil {
		return FlowResult{}, cancelErr
	}
	_ = emit(canceled)
	return FlowResult{Request: canceled}, nil
}

func cancelFlowRequest(ctx context.Context, svc FlowService, requestID, actorID, reason string) (Request, error) {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return svc.Cancel(cancelCtx, CancelInput{
		RequestID:              requestID,
		ActorChannelIdentityID: actorID,
		Reason:                 reason,
	})
}

func waitForLateFlowResponse(ctx context.Context, svc FlowService, requestID string) (Request, error) {
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return svc.WaitForRegisteredResponse(waitCtx, requestID)
}

func firstReason(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}
