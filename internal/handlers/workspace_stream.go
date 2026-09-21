package handlers

import (
	"context"
	"errors"
	"log/slog"
	"strings"
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

// workspaceStatus is the manager slice that describes a settled workspace for
// the terminal "complete" event.
type workspaceStatus interface {
	GetContainerInfo(ctx context.Context, botID string) (*workspace.ContainerStatus, error)
	HasPreservedData(botID string) bool
}

// workspaceStreamOutcome is what a provisioning stream ended with.
type workspaceStreamOutcome struct {
	Workspace botworkspace.Workspace
	Failed    bool
	// ErrorSent is true when an error event was already written to the stream.
	ErrorSent bool
	// Disconnected is true when the client stopped reading before the
	// workspace settled; nothing more can be sent. The reconciler keeps
	// converging the intent regardless.
	Disconnected bool
	// Progress is the backend's "complete" progress event when this instance
	// observed it. It carries what only the provisioner knows (whether the
	// preserved archive was consumed); everything else about the workspace is
	// read back from the manager so the outcome does not depend on the
	// in-process subscription.
	Progress *botworkspace.ProgressEvent
}

// streamWorkspaceProvisioning relays reconciler progress for one intent to an
// SSE writer until the workspace settles. Live progress (pull layers, phases)
// comes from the in-process subscription; the outcome itself comes from the
// repository through await, so a stream served by another Server instance,
// or one that missed events, still ends correctly. The terminal "complete"
// event is the caller's to send (see workspaceCompleteEvent) once the
// workspace is present.
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
	// The await runs on a child context so a client that disconnects mid-way
	// releases this goroutine instead of holding it for the whole budget.
	awaitCtx, cancelAwait := context.WithCancel(ctx)
	defer cancelAwait()
	type awaited struct {
		w   botworkspace.Workspace
		err error
	}
	done := make(chan awaited, 1)
	go func() {
		w, err := await(awaitCtx)
		done <- awaited{w: w, err: err}
	}()

	var progress *botworkspace.ProgressEvent
	// relay writes one event; false means the client is gone.
	relay := func(ev botworkspace.ProgressEvent) bool {
		switch ev.Type {
		case "pulling":
			return send(createContainerPullingEvent{Type: "pulling", Image: ev.Image})
		case "pull_progress":
			return send(createContainerPullProgressEvent{Type: "pull_progress", Layers: ev.Layers})
		case "pull_skipped", "pull_delegated":
			return send(createContainerPullStatusEvent{Type: ev.Type, Image: ev.Image, Message: ev.Message})
		case "creating":
			return send(createContainerCreatingEvent{Type: "creating"})
		case "restoring":
			return send(createContainerRestoringEvent{Type: "restoring"})
		case "complete":
			// Kept for the caller; the settled workspace is described from the
			// manager once await confirms it.
			ev := ev
			progress = &ev
		case botworkspace.EventReady, botworkspace.EventError:
			// Terminal events are authoritative only through await, so a stale
			// subscriber event from a previous generation cannot end the stream
			// early.
		}
		return true
	}
	// drain relays progress that was published before await observed the
	// settled row, so the client still sees every intermediate step.
	drain := func() bool {
		for {
			select {
			case ev := <-events:
				if !relay(ev) {
					return false
				}
			default:
				return true
			}
		}
	}

	for {
		select {
		case ev := <-events:
			if !relay(ev) {
				return workspaceStreamOutcome{Disconnected: true}
			}
		case res := <-done:
			if !drain() {
				return workspaceStreamOutcome{Workspace: res.w, Disconnected: true}
			}
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
				return workspaceStreamOutcome{Workspace: w, Progress: progress}
			}
			sendWorkspaceFailure(send, sendError, w, requestID)
			return workspaceStreamOutcome{Workspace: w, Failed: true, ErrorSent: true}
		}
	}
}

// workspaceCompleteEvent describes a present workspace for the terminal
// "complete" event. The manager's view is authoritative; the provisioner's
// progress event fills in what the manager cannot know (data_restored) and
// stands in entirely when no manager is wired. ok is false when nothing can
// describe the workspace.
func workspaceCompleteEvent(ctx context.Context, log *slog.Logger, status workspaceStatus, botID string, outcome workspaceStreamOutcome) (createContainerCompleteEvent, bool) {
	response := CreateContainerResponse{
		Image:   outcome.Workspace.Image,
		Started: outcome.Workspace.Observed == botworkspace.ObservedRunning,
	}
	described := false
	if p := outcome.Progress; p != nil {
		response.ContainerID = p.ContainerID
		response.WorkspaceBackend = p.WorkspaceBackend
		response.RuntimeBackend = p.RuntimeBackend
		response.ContainerPath = p.ContainerPath
		response.CDIDevices = p.CDIDevices
		response.Snapshotter = p.Snapshotter
		response.DataRestored = p.DataRestored
		response.HasPreservedData = p.HasPreservedData
		if strings.TrimSpace(p.Image) != "" {
			response.Image = p.Image
		}
		described = true
	}
	if status != nil {
		info, err := status.GetContainerInfo(ctx, botID)
		switch {
		case err != nil:
			if log != nil {
				log.WarnContext(ctx, "describe workspace after provisioning failed", slog.String("bot_id", botID), slog.Any("error", err))
			}
			response.HasPreservedData = status.HasPreservedData(botID)
		default:
			response.ContainerID = info.ContainerID
			response.WorkspaceBackend = info.WorkspaceBackend
			response.RuntimeBackend = info.RuntimeBackend
			response.ContainerPath = info.ContainerPath
			response.CDIDevices = info.CDIDevices
			response.HasPreservedData = info.HasPreservedData
			if strings.TrimSpace(info.Snapshotter) != "" {
				response.Snapshotter = info.Snapshotter
			}
			if strings.TrimSpace(info.Image) != "" {
				response.Image = info.Image
			}
			described = true
		}
	}
	if !described {
		return createContainerCompleteEvent{}, false
	}
	return createContainerCompleteEvent{Type: "complete", Container: response}, true
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
