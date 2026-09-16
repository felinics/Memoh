package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type toolStatusRecorder struct {
	mu        sync.Mutex
	snapshots []ToolCallStatusSnapshot
	times     []time.Time
	errs      []error
	// release, when set, holds the first publish until it is closed.
	release chan struct{}
	entered chan struct{}
}

func (r *toolStatusRecorder) publish(_ context.Context, snapshot ToolCallStatusSnapshot) error {
	r.mu.Lock()
	r.snapshots = append(r.snapshots, snapshot)
	r.times = append(r.times, time.Now())
	first := len(r.snapshots) == 1
	var err error
	if len(r.errs) > 0 {
		err, r.errs = r.errs[0], r.errs[1:]
	}
	release, entered := r.release, r.entered
	r.mu.Unlock()
	if first && release != nil {
		close(entered)
		<-release
	}
	return err
}

func (r *toolStatusRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.snapshots)
}

func (r *toolStatusRecorder) at(i int) (ToolCallStatusSnapshot, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshots[i], r.times[i]
}

func waitForToolStatus(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
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

func TestToolCallStatusSnapshotRendersBatch(t *testing.T) {
	t.Parallel()

	snapshot := ToolCallStatusSnapshot{Calls: []ToolCallPresentation{
		BuildToolCallEnd(toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true})),
		BuildToolCallEnd(toolStatusReadEnd("c2", "/data/b.md", map[string]any{"error": "permission denied"})),
		BuildToolCallStart(toolStatusRead("c3", "/data/c.md")),
	}}
	want := "📖 read · completed · /data/a.md\n\n" +
		"📖 read · failed\n/data/b.md\nerror: permission denied\n\n" +
		"📖 read · running\n/data/c.md"
	if got := snapshot.RenderMarkdown(0); got != want {
		t.Fatalf("RenderMarkdown() =\n%s\nwant\n%s", got, want)
	}
	if got := snapshot.RenderPlain(0); got != want {
		t.Fatalf("RenderPlain() =\n%s\nwant\n%s", got, want)
	}
	if got := (ToolCallStatusSnapshot{}).RenderMarkdown(100); got != "" {
		t.Fatalf("empty snapshot rendered %q", got)
	}
}

func TestToolCallStatusSnapshotCollapsesOldestDetailFirst(t *testing.T) {
	t.Parallel()

	longError := strings.Repeat("x", 150)
	second := BuildToolCallEnd(toolStatusReadEnd("c2", "/data/b.md", map[string]any{"error": longError}))
	snapshot := ToolCallStatusSnapshot{Calls: []ToolCallPresentation{
		BuildToolCallEnd(toolStatusReadEnd("c1", "/data/a.md", map[string]any{"error": longError})),
		second,
	}}
	want := "📖 read · failed · /data/a.md\n\n" + RenderToolCallMessageMarkdown(second)
	budget := utf8.RuneCountInString(want)
	if full := snapshot.RenderMarkdown(0); utf8.RuneCountInString(full) <= budget {
		t.Fatalf("test batch must exceed the budget: %d <= %d", utf8.RuneCountInString(full), budget)
	}
	if got := snapshot.RenderMarkdown(budget); got != want {
		t.Fatalf("RenderMarkdown(%d) =\n%s\nwant\n%s", budget, got, want)
	}
}

func TestToolCallStatusSnapshotHidesOldestCallsOverBudget(t *testing.T) {
	t.Parallel()

	calls := make([]ToolCallPresentation, 0, 5)
	for i := 1; i <= 5; i++ {
		calls = append(calls, BuildToolCallEnd(toolStatusReadEnd(
			fmt.Sprintf("c%d", i), fmt.Sprintf("/data/file%d.md", i), map[string]any{"ok": true},
		)))
	}

	got := ToolCallStatusSnapshot{Calls: calls}.RenderMarkdown(100)
	want := "… 3 earlier tool calls\n📖 read · completed · /data/file4.md\n📖 read · completed · /data/file5.md"
	if got != want {
		t.Fatalf("RenderMarkdown(100) =\n%s\nwant\n%s", got, want)
	}

	got = ToolCallStatusSnapshot{Calls: calls[:2]}.RenderMarkdown(60)
	want = "… 1 earlier tool call\n📖 read · completed · /data/file2.md"
	if got != want {
		t.Fatalf("RenderMarkdown(60) =\n%s\nwant\n%s", got, want)
	}
}

func TestToolCallStatusSnapshotTruncatesSingleOversizedCall(t *testing.T) {
	t.Parallel()

	snapshot := ToolCallStatusSnapshot{Calls: []ToolCallPresentation{
		BuildToolCallStart(toolStatusRead("c1", "/data/"+strings.Repeat("deep/", 40)+"file.md")),
	}}
	got := snapshot.RenderMarkdown(30)
	if n := utf8.RuneCountInString(got); n > 30 {
		t.Fatalf("rendered %d runes over a 30 rune budget: %q", n, got)
	}
	if !strings.HasPrefix(got, "📖 read · running") || !strings.HasSuffix(got, toolCallSummaryTruncMark) {
		t.Fatalf("unexpected truncated render %q", got)
	}
}

func TestToolCallStatusMessageCoalescesUntilFinish(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	msg := NewToolCallStatusMessage(ToolCallStatusOptions{Publish: recorder.publish, MinInterval: time.Hour})
	ctx := context.Background()

	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	waitForToolStatus(t, "first publish", func() bool { return recorder.count() == 1 })

	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c2", "/data/b.md"))
	msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true}))
	msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c2", "/data/b.md", map[string]any{"ok": true}))
	time.Sleep(30 * time.Millisecond)
	if got := recorder.count(); got != 1 {
		t.Fatalf("updates inside MinInterval must coalesce, got %d publishes", got)
	}

	if err := msg.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if got := recorder.count(); got != 2 {
		t.Fatalf("Finish must publish the pending state once, got %d publishes", got)
	}
	first, _ := recorder.at(0)
	if first.Final || fmt.Sprint(toolStatusStatuses(first)) != "[running]" {
		t.Fatalf("unexpected first snapshot: final=%v statuses=%v", first.Final, toolStatusStatuses(first))
	}
	final, _ := recorder.at(1)
	if !final.Final || fmt.Sprint(toolStatusStatuses(final)) != "[completed completed]" {
		t.Fatalf("unexpected final snapshot: final=%v statuses=%v", final.Final, toolStatusStatuses(final))
	}
	if final.Calls[0].Header != "/data/a.md" || final.Calls[1].Header != "/data/b.md" {
		t.Fatalf("calls must keep start order, got %q then %q", final.Calls[0].Header, final.Calls[1].Header)
	}
	if msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c3", "/data/c.md")) {
		t.Fatalf("Apply after Finish must be rejected")
	}
}

func TestToolCallStatusMessageFinishSkipsDeliveredState(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	msg := NewToolCallStatusMessage(ToolCallStatusOptions{Publish: recorder.publish})
	ctx := context.Background()

	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	waitForToolStatus(t, "publish", func() bool { return recorder.count() == 1 })
	if err := msg.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := msg.Finish(ctx); err != nil {
		t.Fatalf("second Finish: %v", err)
	}
	if got := recorder.count(); got != 1 {
		t.Fatalf("Finish must not republish a delivered state, got %d publishes", got)
	}
}

func TestToolCallStatusMessageEphemeralKeepAlive(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	msg := NewToolCallStatusMessage(ToolCallStatusOptions{
		Publish:   recorder.publish,
		KeepAlive: 10 * time.Millisecond,
		Ephemeral: true,
	})
	ctx := context.Background()

	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	waitForToolStatus(t, "keep-alive refreshes", func() bool { return recorder.count() >= 3 })
	if err := msg.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	stopped := recorder.count()
	time.Sleep(40 * time.Millisecond)
	if got := recorder.count(); got != stopped {
		t.Fatalf("publishing must stop at Finish: %d publishes, then %d", stopped, got)
	}
	for i := range stopped {
		if snapshot, _ := recorder.at(i); snapshot.Final {
			t.Fatalf("ephemeral surfaces never get a final publish (publish %d)", i)
		}
	}
}

func TestToolCallStatusMessageMatchesCallsWithoutIDs(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	msg := NewToolCallStatusMessage(ToolCallStatusOptions{Publish: recorder.publish, MinInterval: time.Hour})
	ctx := context.Background()

	msg.Apply(ctx, StreamEventToolCallStart, &StreamToolCall{Name: "read", Input: map[string]any{"path": "/data/a.md"}})
	msg.Apply(ctx, StreamEventToolCallStart, &StreamToolCall{Name: "read", Input: map[string]any{"path": "/data/b.md"}})
	msg.Apply(ctx, StreamEventToolCallEnd, &StreamToolCall{Name: "read", Input: map[string]any{"path": "/data/a.md"}, Result: map[string]any{"ok": true}})
	// A repeated start must not reopen the finished call.
	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c3", "/data/c.md"))
	msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c3", "/data/c.md", map[string]any{"ok": true}))
	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c3", "/data/c.md"))

	if !msg.Tracks("c3") || msg.Tracks("missing") || msg.Tracks(" ") {
		t.Fatalf("Tracks must report only calls in the batch")
	}
	if err := msg.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	final, _ := recorder.at(recorder.count() - 1)
	if got := fmt.Sprint(toolStatusStatuses(final)); got != "[completed running completed]" {
		t.Fatalf("unexpected statuses %s", got)
	}
}

func TestToolCallStatusMessageRetriesFailedPublish(t *testing.T) {
	t.Parallel()

	const backoff = 40 * time.Millisecond
	recorder := &toolStatusRecorder{errs: []error{
		fmt.Errorf("send: %w", &RetryAfterError{Err: errors.New("flood"), Delay: backoff}),
	}}
	msg := NewToolCallStatusMessage(ToolCallStatusOptions{Publish: recorder.publish})
	ctx := context.Background()

	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	waitForToolStatus(t, "retried publish", func() bool { return recorder.count() == 2 })
	_, failedAt := recorder.at(0)
	_, retriedAt := recorder.at(1)
	if gap := retriedAt.Sub(failedAt); gap < backoff {
		t.Fatalf("retry must wait for the platform backoff: retried after %s, want >= %s", gap, backoff)
	}
	if err := msg.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if got := recorder.count(); got != 2 {
		t.Fatalf("a delivered retry must not be published again, got %d publishes", got)
	}
}

func TestToolCallStatusMessageFinishReturnsWhenContextEnds(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{release: make(chan struct{}), entered: make(chan struct{})}
	msg := NewToolCallStatusMessage(ToolCallStatusOptions{Publish: recorder.publish})

	msg.Apply(context.Background(), StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	<-recorder.entered
	msg.Apply(context.Background(), StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := msg.Finish(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Finish with a canceled context = %v, want context.Canceled", err)
	}
	close(recorder.release)
	waitForToolStatus(t, "publisher exit", func() bool {
		select {
		case <-msg.done:
			return true
		default:
			return false
		}
	})
	if got := recorder.count(); got != 1 {
		t.Fatalf("no publish may follow an abandoned Finish, got %d publishes", got)
	}
}

func TestToolCallStatusMessageIgnoresUnusableInput(t *testing.T) {
	t.Parallel()

	var nilMessage *ToolCallStatusMessage
	if err := nilMessage.Finish(context.Background()); err != nil {
		t.Fatalf("nil Finish: %v", err)
	}
	if nilMessage.Apply(context.Background(), StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md")) {
		t.Fatalf("nil Apply must be rejected")
	}

	recorder := &toolStatusRecorder{}
	msg := NewToolCallStatusMessage(ToolCallStatusOptions{Publish: recorder.publish})
	if msg.Apply(context.Background(), StreamEventDelta, toolStatusRead("c1", "/data/a.md")) {
		t.Fatalf("non tool-call events must be rejected")
	}
	if msg.Apply(context.Background(), StreamEventToolCallStart, nil) {
		t.Fatalf("nil tool calls must be rejected")
	}
	if err := msg.Finish(context.Background()); err != nil {
		t.Fatalf("Finish of an empty batch: %v", err)
	}
	if got := recorder.count(); got != 0 {
		t.Fatalf("an empty batch must never publish, got %d publishes", got)
	}
}

func TestRetryAfterDelay(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("edit: %w", &RetryAfterError{Err: errors.New("429"), Delay: 3 * time.Second})
	if delay, ok := RetryAfterDelay(wrapped); !ok || delay != 3*time.Second {
		t.Fatalf("RetryAfterDelay() = %s, %v", delay, ok)
	}
	if _, ok := RetryAfterDelay(errors.New("boom")); ok {
		t.Fatalf("plain errors carry no delay")
	}
	if !strings.Contains(wrapped.Error(), "429") {
		t.Fatalf("RetryAfterError must keep the platform error text, got %q", wrapped.Error())
	}
}

func TestToolCallStatusMessageRevisesCallsThatOutliveTheBatch(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	msg := NewToolCallStatusMessage(ToolCallStatusOptions{Publish: recorder.publish, MinInterval: time.Hour})
	ctx := context.Background()

	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c2", "/data/b.md"))
	msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c2", "/data/b.md", map[string]any{"ok": true}))
	if err := msg.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	published := recorder.count()

	if msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c3", "/data/c.md")) {
		t.Fatalf("a finished batch must not take new calls")
	}
	if msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c2", "/data/b.md", map[string]any{"ok": true})) {
		t.Fatalf("a finished call must not be revised")
	}
	if !msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true})) {
		t.Fatalf("a call that outlived its batch must be revised")
	}
	if got := recorder.count(); got != published+1 {
		t.Fatalf("a revision must publish at once: %d publishes, want %d", got, published+1)
	}
	revised, _ := recorder.at(recorder.count() - 1)
	if !revised.Final || fmt.Sprint(toolStatusStatuses(revised)) != "[completed completed]" {
		t.Fatalf("unexpected revision: final=%v statuses=%v", revised.Final, toolStatusStatuses(revised))
	}
}

func TestToolCallStatusMessageEphemeralBatchTakesNoLateUpdates(t *testing.T) {
	t.Parallel()

	recorder := &toolStatusRecorder{}
	msg := NewToolCallStatusMessage(ToolCallStatusOptions{Publish: recorder.publish, Ephemeral: true})
	ctx := context.Background()

	msg.Apply(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	waitForToolStatus(t, "publish", func() bool { return recorder.count() == 1 })
	if err := msg.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if msg.Apply(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true})) {
		t.Fatalf("a replaced draft cannot be revised")
	}
	if got := recorder.count(); got != 1 {
		t.Fatalf("an ephemeral batch must not publish after Finish, got %d publishes", got)
	}
}

func TestToolCallStatusTrackerRoutesAcrossBatches(t *testing.T) {
	t.Parallel()

	var recorders []*toolStatusRecorder
	tracker := NewToolCallStatusTracker(func() *ToolCallStatusMessage {
		recorder := &toolStatusRecorder{}
		recorders = append(recorders, recorder)
		return NewToolCallStatusMessage(ToolCallStatusOptions{Publish: recorder.publish, MinInterval: time.Hour})
	})
	ctx := context.Background()

	if tracker.Active() || tracker.Update(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md")) {
		t.Fatalf("an empty tracker lists no calls")
	}
	tracker.Join(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	tracker.Join(ctx, StreamEventToolCallStart, toolStatusRead("c2", "/data/b.md"))
	if !tracker.Active() || len(recorders) != 1 {
		t.Fatalf("calls of one batch must share a message, got %d batches", len(recorders))
	}
	if !tracker.Update(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c2", "/data/b.md", map[string]any{"ok": true})) {
		t.Fatalf("the current batch lists c2")
	}
	if err := tracker.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if tracker.Active() {
		t.Fatalf("Finish must close the batch")
	}

	// c1 outlived the first batch, so its end revises that message in place.
	before := recorders[0].count()
	if !tracker.Update(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true})) {
		t.Fatalf("the earlier batch lists c1")
	}
	if got := recorders[0].count(); got != before+1 {
		t.Fatalf("the earlier batch must be revised: %d publishes, want %d", got, before+1)
	}
	// A repeated end stays with the batch that lists the call.
	if !tracker.Update(ctx, StreamEventToolCallEnd, toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true})) {
		t.Fatalf("a finished call stays listed by its batch")
	}

	tracker.Join(ctx, StreamEventToolCallStart, toolStatusRead("c3", "/data/c.md"))
	if len(recorders) != 2 {
		t.Fatalf("a call after Finish must start a new batch, got %d batches", len(recorders))
	}
	if err := tracker.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	var nilTracker *ToolCallStatusTracker
	nilTracker.Join(ctx, StreamEventToolCallStart, toolStatusRead("c4", "/data/d.md"))
	if nilTracker.Active() || nilTracker.Update(ctx, StreamEventToolCallStart, toolStatusRead("c4", "/data/d.md")) || nilTracker.Finish(ctx) != nil {
		t.Fatalf("a nil tracker must be inert")
	}
}

func TestToolCallStatusTrackerStartsNewDraftForLateUpdates(t *testing.T) {
	t.Parallel()

	batches := 0
	tracker := NewToolCallStatusTracker(func() *ToolCallStatusMessage {
		batches++
		recorder := &toolStatusRecorder{}
		return NewToolCallStatusMessage(ToolCallStatusOptions{Publish: recorder.publish, Ephemeral: true})
	})
	ctx := context.Background()

	tracker.Join(ctx, StreamEventToolCallStart, toolStatusRead("c1", "/data/a.md"))
	if err := tracker.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	end := toolStatusReadEnd("c1", "/data/a.md", map[string]any{"ok": true})
	if tracker.Update(ctx, StreamEventToolCallEnd, end) {
		t.Fatalf("a replaced draft cannot list calls anymore")
	}
	tracker.Join(ctx, StreamEventToolCallEnd, end)
	if batches != 2 {
		t.Fatalf("a late update after a draft must start a new draft, got %d batches", batches)
	}
	if err := tracker.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}
