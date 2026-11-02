package sessionruntime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
)

// A run's append-only output — text and reasoning deltas and tool progress —
// arrives a token at a time, while every live-state write copies (memory) or
// re-encodes and ships (Redis) the whole run view, tool results included.
// Writing per token made one run cost deltas × view size. The owner instead
// holds appends for a short window and commits them as one transition: one
// write, one published delta, one sequence number. Subscribers and the stored
// snapshot therefore still agree at every sequence number; they only see the
// output in slices of at most one window.
//
// The window adapts to what a write costs: a flush may take at most a tenth of
// the run's streaming time, so a run whose view has grown large is written
// less often instead of more expensively. Appends a crashed owner had not
// flushed are lost with the run, which is lost anyway.
const (
	runOutputFlushInterval    = 50 * time.Millisecond
	runOutputFlushMaxInterval = time.Second
	runOutputFlushDutyFactor  = 10
)

// runOutput is one run's pending appends. Its mutex also serializes every
// owner-side write of the run's live state, so held appends always commit
// before any transition that follows them.
type runOutput struct {
	mu      sync.Mutex
	pending outputBatch
	timer   *time.Timer
	// window numbers the scheduled flushes, so a timer that fires after a
	// transition already committed its output recognises itself as stale.
	window    uint64
	lastFlush time.Duration
	// err is what an asynchronous flush ran into while its output is still
	// uncommitted, reported by the run's next append. A later flush that
	// commits the held output clears it. Ownership loss stays: every later
	// write would fail the same way.
	err error
}

func (o *runOutput) flushInterval() time.Duration {
	return min(max(runOutputFlushInterval, o.lastFlush*runOutputFlushDutyFactor), runOutputFlushMaxInterval)
}

type outputBatch struct {
	// messages holds the latest version of every message the appends touched,
	// which is what the stored run view takes.
	messages []chatview.UIMessage
	appends  []RuntimeMessageAppend
	progress []RuntimeProgressAppend
}

func (b *outputBatch) empty() bool {
	return len(b.appends) == 0 && len(b.progress) == 0
}

func (b *outputBatch) add(delta RuntimeDelta, messages []chatview.UIMessage) {
	for _, message := range messages {
		replaced := false
		for i := range b.messages {
			if b.messages[i].ID == message.ID {
				b.messages[i] = message
				replaced = true
				break
			}
		}
		if !replaced {
			b.messages = append(b.messages, message)
		}
	}
	b.appends = append(b.appends, delta.MessageAppends...)
	b.progress = append(b.progress, delta.ProgressAppends...)
}

// delta merges consecutive appends to the same block, which is what a
// subscriber applying them one by one would have built.
func (b *outputBatch) delta() RuntimeDelta {
	appends := make([]RuntimeMessageAppend, 0, len(b.appends))
	var content strings.Builder
	for i, item := range b.appends {
		content.WriteString(item.Content)
		if next := i + 1; next < len(b.appends) && b.appends[next].ID == item.ID && b.appends[next].Type == item.Type {
			continue
		}
		item.Content = content.String()
		content.Reset()
		appends = append(appends, item)
	}
	return RuntimeDelta{MessageAppends: appends, ProgressAppends: b.progress}
}

// isRunOutputAppend reports the events whose only effect is appending to the
// run's current blocks.
func isRunOutputAppend(delta RuntimeDelta) bool {
	return (len(delta.MessageAppends) > 0 || len(delta.ProgressAppends) > 0) &&
		delta.CurrentRunView == nil && delta.Run == nil && len(delta.MessageUpserts) == 0 &&
		len(delta.UserTurnUpserts) == 0 && len(delta.SteerTurnUpserts) == 0 &&
		len(delta.SteerTurnRemovals) == 0 && !delta.ResetMessages
}

// appendRunOutput holds one append for the run's next output flush.
func (m *Manager) appendRunOutput(ctx context.Context, ctrl *runControl, delta RuntimeDelta, messages []chatview.UIMessage) error {
	if ctrl.ownershipWasLost() || (m.distributed != nil && !ctrl.leaseIsValidAt(time.Now())) {
		return ErrRunOwnershipLost
	}
	out := &ctrl.output
	out.mu.Lock()
	defer out.mu.Unlock()
	if errors.Is(out.err, ErrRunOwnershipLost) {
		return out.err
	}
	// The append is held even when an earlier write failed: the stream
	// converter has already folded it into its blocks, so dropping it would
	// leave subscribers' appends short of the view the next write stores.
	if out.timer == nil {
		// The window outlives the event that opened it, so the flush keeps
		// the event's values but not its cancellation.
		m.scheduleRunOutputFlush(context.WithoutCancel(ctx), ctrl, out.flushInterval())
	}
	out.pending.add(delta, messages)
	err := out.err
	out.err = nil
	return err
}

// scheduleRunOutputFlush closes the run's output window after delay. The
// caller holds ctrl.output.mu.
func (m *Manager) scheduleRunOutputFlush(ctx context.Context, ctrl *runControl, delay time.Duration) {
	out := &ctrl.output
	out.window++
	window := out.window
	out.timer = time.AfterFunc(delay, func() { m.flushRunOutputLater(ctx, ctrl, window) })
}

// flushRunOutputLater commits held appends when their window closes without a
// later transition having committed them first.
func (m *Manager) flushRunOutputLater(ctx context.Context, ctrl *runControl, window uint64) {
	out := &ctrl.output
	out.mu.Lock()
	defer out.mu.Unlock()
	if out.timer == nil || out.window != window {
		// A transition committed this window's output before it closed.
		return
	}
	out.timer = nil
	if out.pending.empty() {
		return
	}
	if m.localControlForHandle(ctrl.handle()) != ctrl {
		// The run left this process; whoever ended it owns its state now.
		out.pending = outputBatch{}
		return
	}
	flushCtx, cancel := context.WithTimeout(ctx, m.ownerLeaseTTL)
	defer cancel()
	err := m.flushRunOutputLocked(flushCtx, ctrl)
	if err == nil {
		return
	}
	out.err = err
	if errors.Is(err, ErrRunOwnershipLost) {
		return
	}
	// The write is retried with the output still held; a retry that commits
	// it leaves nothing for the run to hear, so the failure is recorded here.
	handle := ctrl.handle()
	event := errlog.Event(ctx, "session_runtime.run_output", errs.WrapDependency(err, "commit run output",
		slog.String("session_id", handle.SessionID), slog.String("run_id", handle.RunID)), errlog.Options{})
	m.logger.LogAttrs(ctx, event.Level, "commit run output failed", event.Attrs()...)
	m.scheduleRunOutputFlush(ctx, ctrl, runOutputFlushMaxInterval)
}

// flushRunOutputLocked commits the run's held appends as one transition. The
// caller holds ctrl.output.mu. Appends a failed write did not commit stay held
// for the next flush, unless the run is no longer this owner's to write.
func (m *Manager) flushRunOutputLocked(ctx context.Context, ctrl *runControl) error {
	out := &ctrl.output
	if out.timer != nil {
		out.timer.Stop()
		out.timer = nil
	}
	if out.pending.empty() {
		return nil
	}
	batch := out.pending
	out.pending = outputBatch{}
	handle := ctrl.handle()
	retryCleared := false
	started := time.Now()
	_, _, err := m.commitActiveUpdate(ctx, handle, func(snapshot Snapshot, now time.Time) (Snapshot, bool, error) {
		run := snapshot.CurrentRunView
		if !runMatchesHandle(run, handle) || !m.runOwnerMatches(run) || !isEventAcceptingRunStatus(run.Status) {
			return snapshot, false, nil
		}
		snapshot.Seq++
		snapshot.UpdatedAt = now
		run.UpdatedAt = now
		retryCleared = run.Retry != nil
		run.Retry = nil
		for _, message := range batch.messages {
			run.Messages = upsertUIMessage(run.Messages, message)
		}
		return snapshot, true, nil
	}, func(snapshot Snapshot) RuntimeDelta {
		delta := batch.delta()
		// Output is what clears a pending retry, so the patch rides the
		// output that did it.
		if retryCleared {
			delta.Run = runtimeRunPatch(snapshot, false, false, false).Run
			delta.Run.setRetry(nil)
		}
		return delta
	})
	out.lastFlush = time.Since(started)
	switch {
	case err == nil:
		out.err = nil
	case !errors.Is(err, ErrRunOwnershipLost):
		out.pending = batch
	}
	return err
}
