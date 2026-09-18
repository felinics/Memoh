package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/felinics/memoh/internal/botworkspace"
	"github.com/felinics/memoh/internal/workspace"
)

// workspaceIntents is the slice of the botworkspace reconciler the HTTP layer
// uses: record an intent, follow its progress, wait for the outcome.
type workspaceIntents interface {
	EnsurePresent(ctx context.Context, botID, image string) (botworkspace.Workspace, error)
	RequestAbsent(ctx context.Context, botID string, preserve bool) (botworkspace.Workspace, error)
	Subscribe(botID string) (<-chan botworkspace.ProgressEvent, func())
	Await(ctx context.Context, botID string, generation int64) (botworkspace.Workspace, error)
	Observe(ctx context.Context, botID string) (botworkspace.Workspace, error)
}

// workspaceStreamOutcome is what a provisioning stream ended with.
type workspaceStreamOutcome struct {
	Workspace botworkspace.Workspace
	Failed    bool
	// ErrorSent is true when an error event was already written to the stream.
	ErrorSent bool
}

// streamWorkspaceProvisioning relays reconciler progress for one intent to an
// SSE writer until the workspace settles. Live progress (pull layers, phases)
// comes from the in-process subscription; the outcome itself comes from the
// repository through await, so a stream served by another Server instance,
// or one that missed events, still ends correctly.
//
// events must have been subscribed before the intent was recorded.
func streamWorkspaceProvisioning(
	ctx context.Context,
	send func(payload any) bool,
	events <-chan botworkspace.ProgressEvent,
	await func(ctx context.Context) (botworkspace.Workspace, error),
	requestID string,
	sendError func(code, i18nKey, message string),
) workspaceStreamOutcome {
	type awaited struct {
		w   botworkspace.Workspace
		err error
	}
	done := make(chan awaited, 1)
	go func() {
		w, err := await(ctx)
		done <- awaited{w: w, err: err}
	}()

	relay := func(ev botworkspace.ProgressEvent) {
		switch ev.Type {
		case "pulling":
			send(createContainerPullingEvent{Type: "pulling", Image: ev.Image})
		case "pull_progress":
			send(createContainerPullProgressEvent{Type: "pull_progress", Layers: ev.Layers})
		case "pull_skipped", "pull_delegated":
			send(createContainerPullStatusEvent{Type: ev.Type, Image: ev.Image, Message: ev.Message})
		case "creating":
			send(createContainerCreatingEvent{Type: "creating"})
		case "restoring":
			send(createContainerRestoringEvent{Type: "restoring"})
		case "complete":
			send(createContainerCompleteEvent{
				Type: "complete",
				Container: CreateContainerResponse{
					ContainerID:      ev.ContainerID,
					WorkspaceBackend: ev.WorkspaceBackend,
					RuntimeBackend:   ev.RuntimeBackend,
					ContainerPath:    ev.ContainerPath,
					Image:            ev.Image,
					CDIDevices:       ev.CDIDevices,
					Started:          ev.Started,
					DataRestored:     ev.DataRestored,
					HasPreservedData: ev.HasPreservedData,
				},
			})
		case botworkspace.EventReady, botworkspace.EventError:
			// Terminal events are authoritative only through await, so a stale
			// subscriber event from a previous generation cannot end the stream
			// early.
		}
	}
	// drain relays progress that was published before await observed the
	// settled row, so the client still sees the final "complete" step.
	drain := func() {
		for {
			select {
			case ev := <-events:
				relay(ev)
			default:
				return
			}
		}
	}

	for {
		select {
		case ev := <-events:
			relay(ev)
		case res := <-done:
			drain()
			if res.err != nil {
				if errors.Is(res.err, context.DeadlineExceeded) || errors.Is(res.err, context.Canceled) {
					sendError("workspace_setup_timeout", "bots.create.failedSubtitle", "workspace setup is still in progress; check the bot's workspace page")
				} else {
					sendError("workspace_setup_failed", "bots.create.failedSubtitle", "workspace setup failed")
				}
				return workspaceStreamOutcome{Workspace: res.w, Failed: true, ErrorSent: true}
			}
			w := res.w
			if w.Observed != botworkspace.ObservedFailed {
				return workspaceStreamOutcome{Workspace: w}
			}
			sendWorkspaceFailure(send, sendError, w, requestID)
			return workspaceStreamOutcome{Workspace: w, Failed: true, ErrorSent: true}
		}
	}
}

// sendWorkspaceFailure emits the stable error event for a failed observation.
// Template bootstrap failures keep their dedicated app-error code; everything
// else is the generic setup failure without leaking backend details.
func sendWorkspaceFailure(send func(payload any) bool, sendError func(code, i18nKey, message string), w botworkspace.Workspace, requestID string) {
	if w.LastErrorPhase == botworkspace.PhaseBootstrap {
		err := errors.New(w.LastError)
		if event, ok := newWorkspaceSetupAppError(errors.Join(workspace.ErrWorkspaceTemplateBootstrapFailed, err), requestID); ok {
			_ = send(event)
			return
		}
	}
	sendError("workspace_setup_failed", "bots.create.failedSubtitle", "workspace setup failed")
}

// workspaceStreamBudget bounds how long an SSE stream follows a provisioning
// before telling the client to look at the workspace page instead. The
// reconciler keeps working after the stream ends.
const workspaceStreamBudget = 15 * time.Minute
