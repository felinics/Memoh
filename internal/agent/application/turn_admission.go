package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/runtimefence"
)

// turnAdmitter is the durable admission gate StartTurn depends on.
//
// It is an interface so this package can be tested for what it actually owns —
// translating the runtime's answers into the turn port's vocabulary and closing
// the run's record when it ends — without standing up a ledger. What admission
// itself guarantees is tested where it is implemented; restating it against a
// stub here would only assert that the stub agrees with itself.
type turnAdmitter interface {
	Admit(context.Context, sessionruntime.AdmitInput) (sessionruntime.Admission, error)
	FinishRun(ctx context.Context, handle sessionruntime.RunHandle, status, message string) error
}

type codedTurnFinisher interface {
	FinishRunWithErrorCode(ctx context.Context, handle sessionruntime.RunHandle, status, errorCode string) error
}

// SetSessionRuntime injects the durable admission gate. Setter injection rather
// than a constructor argument because the manager and this service are wired
// into the same fx graph and each is reachable from the other's dependencies.
func (s *Service) SetSessionRuntime(manager *sessionruntime.Manager) {
	if manager == nil {
		// A typed nil stored in the interface would read as configured and then
		// panic on first use, so an absent manager stays absent.
		return
	}
	s.sessionRuntime = manager
	s.decisionRuntime = manager
	s.abortRuntime = manager
	s.publishTurnEvent = func(ctx context.Context, handle sessionruntime.RunHandle, event native.StreamEvent) error {
		_, err := manager.HandleAgentEvent(ctx, handle, event)
		return err
	}
	manager.SetDecisionStore(s)
	manager.SetCommandHandler(s.handleRuntimeDecisionCommand)
	manager.SetTerminalObserver(s.reconcileTerminalContextLifecycle)
	manager.SetTerminalReconciler(s.reconcileTerminalContextLifecycles)
}

// admitTurnRun puts a StartTurnCommand through durable admission and answers in
// the vocabulary of the turn port, which is the only agent surface its callers
// can see.
//
// Three answers matter to a channel adapter, and they are deliberately distinct.
// Busy means nothing was persisted and a redelivery will be admitted once the
// thread frees up. Duplicate means this invocation already has a run, so the
// redelivery has been answered and must be dropped. Anything else is a genuine
// failure to report.
func (s *Service) admitTurnRun(
	ctx context.Context,
	cmd turn.StartTurnCommand,
	cancel context.CancelFunc,
	ownershipCancel context.CancelCauseFunc,
	injectCh chan turn.InjectMessage,
) (sessionruntime.Admission, error) {
	if s.sessionRuntime == nil {
		return sessionruntime.Admission{}, errors.New("turn: session runtime is not configured")
	}
	payload, err := turnSubmissionPayload(cmd)
	if err != nil {
		return sessionruntime.Admission{}, fmt.Errorf("encode turn submission: %w", err)
	}
	invocationID := turnInvocationID(cmd)

	admission, err := s.sessionRuntime.Admit(ctx, sessionruntime.AdmitInput{
		BotID:        cmd.BotID,
		SessionID:    cmd.ThreadID,
		InvocationID: invocationID,
		Payload:      payload,
		Execution: sessionruntime.Execution{
			// Project the inbound user message so subscribers (an open web
			// session on the thread) see what fired the run while it still
			// executes — the same contract the ws and schedule admissions
			// already honor. NewRequestUserTurn returns nil for commands that
			// persist no user message (discuss-shaped, attachment-only), and
			// those runs stay contentless in the projection.
			Admission: func(_ context.Context, handle sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{
					RequestUserTurn: chatview.NewRequestUserTurn(cmd, handle.TurnID),
				}, nil
			},
			Cancel: cancel,
			// Nil for discuss, which has no reader for steering messages.
			InjectCh:        injectCh,
			OwnershipCancel: ownershipCancel,
		},
	})
	switch {
	case errors.Is(err, sessionruntime.ErrSessionBusy):
		return sessionruntime.Admission{}, fmt.Errorf("%w: thread %s", turn.ErrSessionBusy, cmd.ThreadID)
	case errors.Is(err, sessionruntime.ErrInvocationConflict):
		// The same retry identity naming different content. Whichever side is
		// wrong, running it would double-answer a message that already has a
		// run, so it is dropped exactly like a redelivery.
		return sessionruntime.Admission{}, fmt.Errorf("%w: %s conflicts with an earlier submission", turn.ErrDuplicateTurn, invocationID)
	case err != nil:
		return sessionruntime.Admission{}, fmt.Errorf("admit turn: %w", err)
	}
	if !admission.Started {
		// Someone owns this run already, or it has finished. Either way this
		// call has no execution to perform and the caller has its answer.
		return sessionruntime.Admission{}, fmt.Errorf("%w: %s", turn.ErrDuplicateTurn, invocationID)
	}
	return admission, nil
}

// terminalWriteTimeout bounds the terminal write. It runs after the run's own
// context is already canceled, so it cannot inherit that deadline, and it must
// not be able to hold a pump goroutine open indefinitely either.
const terminalWriteTimeout = 10 * time.Second

// turnRunFinisher returns the terminal write for an admitted run. Without it the
// durable row stays active and the thread's only slot is held until the reaper
// times the lease out, which turns one finished turn into minutes of session_busy.
//
// Only the stable error code reaches the run's recorded state; the error itself
// is a private diagnostic and stays in the log.
func (s *Service) turnRunFinisher(ctx context.Context, admission sessionruntime.Admission) func(status string, cause error) {
	if s.sessionRuntime == nil || admission.Handle.FencingToken <= 0 {
		return nil
	}
	handle := admission.Handle
	runCtx := ctx
	// The run's context is already canceled by the time the terminal write runs,
	// so this detaches from its cancellation while keeping its values: the write
	// still needs whatever scoping the caller's context carries.
	writeCtx := context.WithoutCancel(ctx)
	return func(status string, cause error) {
		lifecycleCause := cause
		switch {
		case lifecycleCause == nil && status == sessionruntime.RunStatusAborted:
			lifecycleCause = context.Canceled
		case lifecycleCause == nil && status == sessionruntime.RunStatusErrored:
			lifecycleCause = errors.New("run finished with an unspecified error")
		}
		minimal := minimalContextLifecycleSnapshot()
		staged := s.stageContextLifecycleCandidate(
			runCtx,
			handle.RunID,
			handle.BotID,
			handle.SessionID,
			&minimal,
			lifecycleCause,
			contextLifecycleCandidateMinimal,
		)
		errorCode := strings.TrimSpace(string(apperror.CodeOf(cause)))
		ctx, cancel := context.WithTimeout(writeCtx, terminalWriteTimeout)
		defer cancel()
		var err error
		if coded, ok := s.sessionRuntime.(codedTurnFinisher); ok && errorCode != "" {
			err = coded.FinishRunWithErrorCode(ctx, handle, status, errorCode)
		} else {
			// Compatibility implementations still receive only stable codes; raw
			// provider diagnostics never cross this terminal boundary.
			err = s.sessionRuntime.FinishRun(ctx, handle, status, errorCode)
		}
		switch {
		case err == nil:
			if !staged && (status != "" || cause != nil) {
				s.EnsureTerminalContextLifecycle(
					runCtx,
					handle.RunID,
					handle.BotID,
					handle.SessionID,
					lifecycleCause,
				)
			}
			return
		case s.logger == nil:
			return
		case errors.Is(err, sessionruntime.ErrRunOwnershipLost):
			// Expected, not a failure: this process was superseded mid-run, so the
			// terminal write was refused and the reaper names the outcome instead.
			s.logger.Warn("skip finishing turn run after ownership loss",
				slog.String("run_id", handle.RunID))
		default:
			s.logger.Error("finish turn run failed",
				slog.Any("error", err),
				slog.String("run_id", handle.RunID),
				slog.String("status", status))
		}
	}
}

// runOwnershipLost reports that this process was told, mid-run, that it no
// longer owns the run.
//
// It is the one interruption whose output must not be written. Every other stop
// — client disconnect, idle timeout, user abort — leaves this process still
// entitled to say how the turn ended, so persisting what it has is the honest
// record. Ownership loss does not: another incarnation of the runtime now
// decides that run's outcome, and a superseded owner writing history would
// attach output to a turn whose ending it can no longer name (SR-DUR-002).
//
// The distinction only survives because the revocation carries a cause. On the
// wire both arrive as the same cancelled context.
func runOwnershipLost(ctx context.Context) bool {
	return ctx != nil && errors.Is(context.Cause(ctx), sessionruntime.ErrRunOwnershipLost)
}

// admitTriggeredRun admits a non-interactive schedule fire and returns the run
// context to execute in plus the terminal write that
// closes the run's record.
//
// These callers reach the agent directly rather than through StartTurn, but they
// occupy a thread exactly like any other turn, so they take the same durable slot
// on the same terms. The returned context is cancelable so a routed abort can
// stop a long schedule run, and so a lost owner lease revokes execution instead
// of letting a superseded owner keep writing.
//
// viewFn optionally supplies the subscriber-facing admission view (the request
// user turn projection); nil keeps the run contentless in the projection, the
// historical default for triggers and subagents alike.
//
// The finish function is always returned non-nil when the error is nil, so a
// caller can defer it unconditionally.
type triggeredRunTerminal struct {
	status string
	cause  error
}

// triggeredAdmissionView lets a triggered caller inject the subscriber-facing
// projection for its run. The hook fires at activation, when the handle already
// carries the allocated turn identity — which is why the view is a factory
// rather than a value: the turn id only exists by then.
// Nil means the run projects no request user turn (the historical default).
type triggeredAdmissionView func(handle sessionruntime.RunHandle) *sessionruntime.RunAdmissionView

func (s *Service) admitTriggeredRun(ctx context.Context, botID, threadID, invocationID string, submission []byte, viewFn triggeredAdmissionView) (context.Context, sessionruntime.Admission, func(triggeredRunTerminal), error) {
	if s.sessionRuntime == nil {
		return nil, sessionruntime.Admission{}, nil, errors.New("session runtime is not configured")
	}
	runCtx, cancelCause := context.WithCancelCause(ctx)
	admission, err := s.sessionRuntime.Admit(runCtx, sessionruntime.AdmitInput{
		BotID:        botID,
		SessionID:    threadID,
		InvocationID: invocationID,
		Payload:      submission,
		Execution: sessionruntime.Execution{
			Admission: func(_ context.Context, handle sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				if viewFn == nil {
					return sessionruntime.RunAdmissionView{}, nil
				}
				if view := viewFn(handle); view != nil {
					return *view, nil
				}
				return sessionruntime.RunAdmissionView{}, nil
			},
			Cancel:          func() { cancelCause(context.Canceled) },
			OwnershipCancel: cancelCause,
		},
	})
	if err != nil {
		cancelCause(context.Canceled)
		return nil, sessionruntime.Admission{}, nil, err
	}
	if !admission.Started {
		// A trigger that finds its own invocation already admitted has nothing to
		// do: the run exists and either someone owns it or it has finished.
		cancelCause(context.Canceled)
		return nil, sessionruntime.Admission{}, nil, fmt.Errorf("%w: %s", sessionruntime.ErrInvocationConflict, invocationID)
	}
	runCtx = s.withAdmissionRuntimeFence(runCtx, admission)
	finishRun := s.turnRunFinisher(runCtx, admission)
	finish := func(terminal triggeredRunTerminal) {
		defer cancelCause(context.Canceled)
		if finishRun == nil {
			return
		}
		if strings.TrimSpace(terminal.status) != "" {
			finishRun(terminal.status, terminal.cause)
			return
		}
		cause := terminal.cause
		failureCause := cause
		if privateCause := apperror.CauseOf(cause); privateCause != nil {
			failureCause = privateCause
		}
		explicitlyCanceled := cause != nil &&
			errors.Is(failureCause, context.Canceled) &&
			errors.Is(context.Cause(runCtx), context.Canceled)
		switch {
		case cause != nil && !explicitlyCanceled:
			finishRun(sessionruntime.RunStatusErrored, cause)
		case runCtx.Err() != nil:
			finishRun(sessionruntime.RunStatusAborted, nil)
		default:
			finishRun(sessionruntime.RunStatusCompleted, nil)
		}
	}
	return runCtx, admission, finish, nil
}

func (s *Service) withAdmissionRuntimeFence(ctx context.Context, admission sessionruntime.Admission) context.Context {
	if !s.usesDurableTerminalObserver() {
		return ctx
	}
	return runtimefence.WithContext(ctx, runtimefence.Fence{
		BotID:     admission.Handle.BotID,
		SessionID: admission.Handle.SessionID,
		Token:     admission.Handle.FencingToken,
	})
}

func (s *Service) usesDurableTerminalObserver() bool {
	_, durable := s.sessionRuntime.(*sessionruntime.Manager)
	return durable
}

// AdmitSubagentRun puts a spawned agent's turn through the same durable
// admission every other turn takes, and answers in the vocabulary of the turn
// port so the tool layer never sees a runtime type.
//
// The slot a subagent takes is its own thread's, not its parent's: a parent may
// have several agents working at once, and each of those threads still runs one
// turn at a time. Busy therefore means *this agent* is already working — a fact
// the parent model can act on — rather than a failure to report.
func (s *Service) AdmitSubagentRun(
	ctx context.Context,
	botID, threadID, invocationID string,
	submission []byte,
) (context.Context, tools.SubagentAdmission, func(tools.SubagentTerminal), error) {
	runCtx, admission, finish, err := s.admitTriggeredRun(ctx, botID, threadID, invocationID, submission, nil)
	switch {
	case errors.Is(err, sessionruntime.ErrSessionBusy):
		return nil, tools.SubagentAdmission{}, nil, fmt.Errorf("%w: thread %s", turn.ErrSessionBusy, threadID)
	case errors.Is(err, sessionruntime.ErrInvocationConflict):
		// This task already has a run. Executing it again would answer one
		// message twice, so it is dropped exactly like a channel redelivery.
		return nil, tools.SubagentAdmission{}, nil, fmt.Errorf("%w: %s", turn.ErrDuplicateTurn, invocationID)
	case err != nil:
		return nil, tools.SubagentAdmission{}, nil, fmt.Errorf("admit subagent turn: %w", err)
	}
	// The durable fence is installed during triggered admission. The opaque run
	// handle also travels on the context so spawned step persistence and live
	// observation can use it without exposing runtime types through this port.
	runCtx = withSubagentRunHandle(runCtx, admission.Handle)
	toolAdmission := tools.SubagentAdmission{
		RunID:        admission.RunID,
		TurnID:       admission.TurnID,
		TurnPosition: admission.TurnPosition,
	}
	var once sync.Once
	terminal := func(result tools.SubagentTerminal) {
		once.Do(func() {
			lifecycleCause := result.Cause
			if lifecycleCause == nil && runCtx.Err() != nil &&
				(!result.OutcomeResolved || result.Outcome != tools.SpawnAttemptCompleted) {
				lifecycleCause = context.Cause(runCtx)
			}
			status := ""
			if result.OutcomeResolved {
				switch result.Outcome {
				case tools.SpawnAttemptCompleted:
					status = sessionruntime.RunStatusCompleted
				case tools.SpawnAttemptAbort:
					status = sessionruntime.RunStatusAborted
				case tools.SpawnAttemptFailure:
					status = sessionruntime.RunStatusErrored
				}
			}
			s.persistContextLifecycleSnapshot(
				runCtx,
				admission.RunID,
				botID,
				threadID,
				result.ContextLifecycle,
				lifecycleCause,
				true,
			)
			finish(triggeredRunTerminal{status: status, cause: result.Cause})
		})
	}
	return runCtx, toolAdmission, terminal, nil
}

// turnInvocationID resolves the command's retry identity.
//
// Channel adapters derive IdempotencyKey from the platform's external message
// id, which is what makes a webhook redelivery the same invocation rather than a
// second turn. When the platform offers nothing stable there is nothing to
// deduplicate against, so each attempt is honestly its own submission — minting
// here keeps that explicit instead of letting an empty id reach admission.
func turnInvocationID(cmd turn.StartTurnCommand) string {
	if key := strings.TrimSpace(cmd.IdempotencyKey); key != "" {
		return key
	}
	return "turn:" + uuid.NewString()
}

// turnSubmissionPayload encodes what the caller actually submitted, and only
// that. The fingerprint of this payload decides whether a repeated invocation id
// is the same submission or a conflict, so it must not carry anything that
// varies between two deliveries of one message: no timestamps, no tokens, no
// per-attempt ids, and attachments by content hash rather than by bytes.
func turnSubmissionPayload(cmd turn.StartTurnCommand) ([]byte, error) {
	return json.Marshal(struct {
		Mode              string   `json:"mode"`
		BotID             string   `json:"bot_id"`
		ThreadID          string   `json:"thread_id"`
		ExternalMessageID string   `json:"external_message_id,omitempty"`
		Query             string   `json:"query,omitempty"`
		UserVisibleText   string   `json:"user_visible_text,omitempty"`
		UserMessageKind   string   `json:"user_message_kind,omitempty"`
		Attachments       []string `json:"attachments,omitempty"`
	}{
		Mode:              string(cmd.Mode),
		BotID:             strings.TrimSpace(cmd.BotID),
		ThreadID:          strings.TrimSpace(cmd.ThreadID),
		ExternalMessageID: strings.TrimSpace(cmd.ExternalMessageID),
		Query:             cmd.Query,
		UserVisibleText:   cmd.UserVisibleText,
		UserMessageKind:   strings.TrimSpace(cmd.UserMessageKind),
		Attachments:       attachmentIdentities(cmd.Attachments),
	})
}

// attachmentIdentities names each attachment by the most stable identity it
// carries. Base64 bytes are deliberately not used: the same upload can arrive
// re-encoded, and the payload is a fingerprint input rather than a copy.
func attachmentIdentities(attachments []turn.Attachment) []string {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]string, 0, len(attachments))
	for _, a := range attachments {
		switch {
		case strings.TrimSpace(a.ContentHash) != "":
			out = append(out, "hash:"+strings.TrimSpace(a.ContentHash))
		case strings.TrimSpace(a.PlatformKey) != "":
			out = append(out, "platform:"+strings.TrimSpace(a.PlatformKey))
		case strings.TrimSpace(a.URL) != "":
			out = append(out, "url:"+strings.TrimSpace(a.URL))
		case strings.TrimSpace(a.Path) != "":
			out = append(out, "path:"+strings.TrimSpace(a.Path))
		default:
			out = append(out, "type:"+strings.TrimSpace(a.Type))
		}
	}
	return out
}
