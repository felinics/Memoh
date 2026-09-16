package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// toolStatusRecorder stands in for an adapter's publish call. Publishing is
// synchronous, so the recorder needs no locking.
type toolStatusRecorder struct {
	snapshots []ToolCallStatusSnapshot
	errs      []error
}

func (r *toolStatusRecorder) publish(_ context.Context, snapshot ToolCallStatusSnapshot) error {
	r.snapshots = append(r.snapshots, snapshot)
	if len(r.errs) > 0 {
		var err error
		err, r.errs = r.errs[0], r.errs[1:]
		return err
	}
	return nil
}

func (r *toolStatusRecorder) last() ToolCallStatusSnapshot {
	return r.snapshots[len(r.snapshots)-1]
}

func toolStatusRead(id, path string) *StreamToolCall {
	return &StreamToolCall{Name: "read", CallID: id, Input: map[string]any{"path": path}}
}

func toolStatusReadEnd(id, path string, result map[string]any) *StreamToolCall {
	return &StreamToolCall{Name: "read", CallID: id, Input: map[string]any{"path": path}, Result: result}
}

func toolStatusStatuses(snapshot ToolCallStatusSnapshot) []ToolCallStatus {
	out := make([]ToolCallStatus, 0, len(snapshot.Calls))
	for _, call := range snapshot.Calls {
		out = append(out, call.Status)
	}
	return out
}

// newToolStatusMessage returns a message that publishes into the recorder. A
// long interval holds every update back until Finish.
func newToolStatusMessage(r *toolStatusRecorder, interval time.Duration, ephemeral bool) *ToolCallStatusMessage {
	return NewToolCallStatusMessage(ToolCallStatusOptions{Publish: r.publish, MinInterval: interval, Ephemeral: ephemeral})
}

func TestUsesToolCallStatusMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tc   *StreamToolCall
		want bool
	}{
		{name: "nil", tc: nil, want: false},
		{name: "ordinary call", tc: toolStatusRead("c1", "/data/a.md"), want: true},
		{name: "approval request", tc: &StreamToolCall{Name: "exec", CallID: "c2", ApprovalID: "ap-1"}, want: false},
		{name: "user input prompt", tc: &StreamToolCall{Name: "ask_user", CallID: "c3", Actions: []Action{{Type: "user_input", Value: "respond:1"}}}, want: false},
		{name: "ask_user without actions hides the tool", tc: &StreamToolCall{Name: "ask_user", CallID: "c4"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UsesToolCallStatusMessage(tt.tc); got != tt.want {
				t.Fatalf("UsesToolCallStatusMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEndsToolCallBatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event PreparedStreamEvent
		want  bool
	}{
		{name: "assistant text", event: PreparedStreamEvent{Type: StreamEventDelta, Delta: "hi"}, want: true},
		{name: "reasoning text", event: PreparedStreamEvent{Type: StreamEventDelta, Delta: "hmm", Phase: StreamPhaseReasoning}, want: false},
		{name: "text phase end", event: PreparedStreamEvent{Type: StreamEventPhaseEnd, Phase: StreamPhaseText}, want: true},
		{name: "turn start", event: PreparedStreamEvent{Type: StreamEventStatus, Status: StreamStatusStarted}, want: true},
		{name: "attachment", event: PreparedStreamEvent{Type: StreamEventAttachment}, want: true},
		{name: "final", event: PreparedStreamEvent{Type: StreamEventFinal}, want: true},
		{name: "tool call start", event: PreparedStreamEvent{Type: StreamEventToolCallStart}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EndsToolCallBatch(tt.event); got != tt.want {
				t.Fatalf("EndsToolCallBatch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToolCallStatusSnapshotRendersBatch(t *testing.T) {
	t.Parallel()

	snapshot := ToolCallStatusSnapshot{Calls: []ToolCallPresentation{
		BuildToolCallEnd(toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true})),
		BuildToolCallEnd(toolStatusReadEnd("c2", "/data/b.md", map[string]any{"error": "permission denied"})),
		BuildToolCallStart(toolStatusRead("c3", "/data/c.md")),
	}}
	// A finished call collapses to one line; a failed or running call keeps its
	// detail.
	want := "📖 read · completed · /data/a.md\n\n" +
		"📖 read · failed\n/data/b.md\nerror: permission denied\n\n" +
		"📖 read · running\n/data/c.md"
	if got := snapshot.RenderMarkdown(0); got != want {
		t.Fatalf("RenderMarkdown() =\n%s\nwant\n%s", got, want)
	}
	if got := snapshot.RenderPlain(0); got != want {
		t.Fatalf("RenderPlain() =\n%s\nwant\n%s", got, want)
	}
}

func TestToolCallStatusSnapshotFitsBudget(t *testing.T) {
	t.Parallel()

	calls := make([]ToolCallPresentation, 0, 6)
	for _, path := range []string{"/data/1.md", "/data/2.md", "/data/3.md", "/data/4.md", "/data/5.md"} {
		calls = append(calls, BuildToolCallEnd(toolStatusReadEnd("c"+path, path, map[string]any{"ok": true})))
	}
	calls = append(calls, BuildToolCallStart(toolStatusRead("last", "/data/6.md")))
	snapshot := ToolCallStatusSnapshot{Calls: calls}

	got := snapshot.RenderMarkdown(90)
	if utf8.RuneCountInString(got) > 90 {
		t.Fatalf("rendered %d runes over the 90 rune budget:\n%s", utf8.RuneCountInString(got), got)
	}
	if !strings.Contains(got, "earlier tool call") {
		t.Fatalf("expected a count of the hidden calls, got:\n%s", got)
	}
	// The newest call is the last to go.
	if !strings.Contains(got, "/data/6.md") {
		t.Fatalf("expected the newest call to survive, got:\n%s", got)
	}
}

func TestToolCallStatusMessageFoldsBatchIntoOneMessage(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	msg := newToolStatusMessage(recorder, time.Hour, false)
	ctx := context.Background()

	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c2", "/data/b.md"))
	msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true}))
	// The interval holds back everything after the first publish.
	if len(recorder.snapshots) != 1 {
		t.Fatalf("expected the burst to coalesce into one publish, got %d", len(recorder.snapshots))
	}
	if err := msg.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if len(recorder.snapshots) != 2 {
		t.Fatalf("expected Finish to publish the held back state, got %d publishes", len(recorder.snapshots))
	}
	final := recorder.last()
	if !final.Final {
		t.Fatal("expected the last publish to be marked final")
	}
	if got := toolStatusStatuses(final); len(got) != 2 || got[0] != ToolCallStatusCompleted || got[1] != ToolCallStatusRunning {
		t.Fatalf("unexpected final statuses: %v", got)
	}
	// Finish is idempotent and never publishes twice.
	if err := msg.Finish(ctx); err != nil || len(recorder.snapshots) != 2 {
		t.Fatalf("second Finish published again: %d publishes, err=%v", len(recorder.snapshots), err)
	}
}

func TestToolCallStatusMessageFinishKeepsPublishedStateQuiet(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	msg := newToolStatusMessage(recorder, 0, false)
	ctx := context.Background()

	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	if err := msg.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if len(recorder.snapshots) != 1 {
		t.Fatalf("expected no publish for an already current state, got %d", len(recorder.snapshots))
	}
}

func TestToolCallStatusMessageRevisesCallThatOutlivedItsBatch(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	msg := newToolStatusMessage(recorder, 0, false)
	ctx := context.Background()

	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	if err := msg.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if !msg.Tracks("c1") {
		t.Fatal("expected the message to keep listing its call")
	}
	if !msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true})) {
		t.Fatal("expected the finished message to take the late result")
	}
	if got := toolStatusStatuses(recorder.last()); len(got) != 1 || got[0] != ToolCallStatusCompleted {
		t.Fatalf("expected the late result to be published, got %v", got)
	}
	// A call it never listed belongs to a later batch.
	if msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c2", "/data/b.md", nil)) {
		t.Fatal("finished message took a call it never listed")
	}
}

func TestToolCallStatusMessageEphemeralEndsCurrentAndTakesNoLateUpdates(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	msg := newToolStatusMessage(recorder, time.Hour, true)
	ctx := context.Background()

	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true}))
	if err := msg.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	// A preview must not be left showing a finished call as running.
	if got := toolStatusStatuses(recorder.last()); len(got) != 1 || got[0] != ToolCallStatusCompleted {
		t.Fatalf("expected the preview to end on the finished state, got %v", got)
	}
	// The next message replaces the preview, so it is gone for later updates.
	if msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/a.md", nil)) {
		t.Fatal("a replaced draft must not take late updates")
	}
}

func TestToolCallStatusMessageRecordsRepeatsAndMissingIDs(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	msg := newToolStatusMessage(recorder, 0, false)
	ctx := context.Background()

	// A result matches the oldest unfinished call of the same tool when the
	// runtime sends no call ID.
	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("", "/data/a.md"))
	msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("", "/data/a.md", map[string]any{"ok": true}))
	if got := toolStatusStatuses(recorder.last()); len(got) != 1 || got[0] != ToolCallStatusCompleted {
		t.Fatalf("expected one completed call, got %v", got)
	}
	// A repeated start must not reopen a call that already finished.
	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/b.md"))
	msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/b.md", map[string]any{"ok": true}))
	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/b.md"))
	if got := toolStatusStatuses(recorder.last()); len(got) != 2 || got[1] != ToolCallStatusCompleted {
		t.Fatalf("a late start reopened a finished call: %v", got)
	}
}

// trackerHooks records what the adapter was asked to do.
type trackerHooks struct {
	flushes int
	pushed  []string
}

func (h *trackerHooks) hooks(hasCard func(*StreamToolCall) bool) ToolCallStatusHooks {
	return ToolCallStatusHooks{
		FlushText: func(context.Context) error {
			h.flushes++
			return nil
		},
		PushCall: func(_ context.Context, _ StreamEventType, tc *StreamToolCall) error {
			h.pushed = append(h.pushed, tc.CallID)
			return nil
		},
		HasCard: hasCard,
	}
}

func TestToolCallStatusTrackerRoutesEvents(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	hooks := &trackerHooks{}
	tracker := NewToolCallStatusTracker(nil, func() *ToolCallStatusMessage {
		return newToolStatusMessage(recorder, 0, false)
	})
	ctx := context.Background()

	// The first ordinary call flushes buffered text and opens a batch.
	if err := tracker.Route(ctx, hooks.hooks(nil), StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md")); err != nil {
		t.Fatal(err)
	}
	if hooks.flushes != 1 || !tracker.Active() {
		t.Fatalf("expected one text flush and an open batch, got flushes=%d active=%v", hooks.flushes, tracker.Active())
	}
	// A second call joins the same batch without flushing again.
	if err := tracker.Route(ctx, hooks.hooks(nil), StreamEventToolCallStart, toolStatusRead("c2", "/data/b.md")); err != nil {
		t.Fatal(err)
	}
	if hooks.flushes != 1 || len(recorder.last().Calls) != 2 {
		t.Fatalf("expected both calls in one message, got flushes=%d calls=%d", hooks.flushes, len(recorder.last().Calls))
	}
	// An approval request ends the batch and keeps its own message.
	approval := &StreamToolCall{Name: "exec", CallID: "c3", ApprovalID: "ap-1"}
	if err := tracker.Route(ctx, hooks.hooks(nil), StreamEventToolCallStart, approval); err != nil {
		t.Fatal(err)
	}
	if tracker.Active() || len(hooks.pushed) != 1 || hooks.pushed[0] != "c3" {
		t.Fatalf("expected the approval to end the batch and be pushed on its own, got active=%v pushed=%v", tracker.Active(), hooks.pushed)
	}
	// A result for a call listed in the closed batch still updates that message.
	if err := tracker.Route(ctx, hooks.hooks(nil), StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true})); err != nil {
		t.Fatal(err)
	}
	if got := toolStatusStatuses(recorder.last()); len(hooks.pushed) != 1 || got[0] != ToolCallStatusCompleted {
		t.Fatalf("late result was not folded back into its message: pushed=%v statuses=%v", hooks.pushed, got)
	}
	// A call that owns an interactive card keeps its result there, and the batch
	// stays open.
	if err := tracker.Route(ctx, hooks.hooks(nil), StreamEventToolCallStart, toolStatusRead("c4", "/data/d.md")); err != nil {
		t.Fatal(err)
	}
	hasCard := func(tc *StreamToolCall) bool { return tc.CallID == "c5" }
	if err := tracker.Route(ctx, hooks.hooks(hasCard), StreamEventToolCallEnd, toolStatusReadEnd("c5", "/data/e.md", nil)); err != nil {
		t.Fatal(err)
	}
	if !tracker.Active() || len(hooks.pushed) != 2 || hooks.pushed[1] != "c5" {
		t.Fatalf("a card result must go to its card without ending the batch: active=%v pushed=%v", tracker.Active(), hooks.pushed)
	}
}

func TestStatusMessageEditorSendsThenEdits(t *testing.T) {
	t.Parallel()

	var sends, edits []string
	gone := errors.New("message gone")
	editor := &StatusMessageEditor{
		Send: func(_ context.Context, text string) (string, error) {
			sends = append(sends, text)
			return "m1", nil
		},
		Edit: func(_ context.Context, id, text string, _ bool) error {
			edits = append(edits, id+":"+text)
			if text == "gone" {
				return gone
			}
			return nil
		},
		Gone: func(err error) bool { return errors.Is(err, gone) },
	}
	ctx := context.Background()

	if err := editor.Publish(ctx, "first", false); err != nil {
		t.Fatal(err)
	}
	if err := editor.Publish(ctx, "second", false); err != nil {
		t.Fatal(err)
	}
	// Publishing the same text again changes nothing.
	if err := editor.Publish(ctx, "second", true); err != nil {
		t.Fatal(err)
	}
	if len(sends) != 1 || len(edits) != 1 || edits[0] != "m1:second" {
		t.Fatalf("expected one send and one edit, got sends=%v edits=%v", sends, edits)
	}
	// A message that went away is replaced instead of losing the batch.
	if err := editor.Publish(ctx, "gone", false); err != nil {
		t.Fatal(err)
	}
	if len(sends) != 2 || sends[1] != "gone" {
		t.Fatalf("expected the batch to continue in a new message, got sends=%v", sends)
	}
}

func TestStatusMessageEditorReturnsEditFailures(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	editor := &StatusMessageEditor{
		Send: func(context.Context, string) (string, error) { return "m1", nil },
		Edit: func(context.Context, string, string, bool) error { return boom },
	}
	ctx := context.Background()

	if err := editor.Publish(ctx, "first", false); err != nil {
		t.Fatal(err)
	}
	if err := editor.Publish(ctx, "second", false); !errors.Is(err, boom) {
		t.Fatalf("expected the edit failure to reach the caller, got %v", err)
	}
}
