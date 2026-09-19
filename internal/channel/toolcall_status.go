package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/felinics/memoh/internal/textutil"
)

// With a bot's reuse_tool_call_message_in_im setting on, edit-capable adapters
// stop posting one message per tool call. The ordinary calls of an
// uninterrupted batch — no assistant text, attachment or interactive prompt in
// between — share one status message that is created for the first call and
// edited as calls start and finish. ToolCallStatusMessage owns one batch: its
// state, rendering, update coalescing and delivery ordering.
// ToolCallStatusTracker routes a stream's tool events across batches. Adapters
// only supply the platform call that creates or replaces a message.

const (
	// toolCallStatusHeaderRunes caps the call summary on a collapsed line.
	toolCallStatusHeaderRunes = 120
	// toolCallStatusRetries bounds self-scheduled retries of a failed publish.
	// The next tool event, keep-alive or Finish tries again regardless.
	toolCallStatusRetries = 3
)

// UsesToolCallStatusMessage reports whether a tool call can be shown in a
// shared status message. Approval requests and user-input prompts keep a
// message of their own: they carry buttons, and replies target that message.
func UsesToolCallStatusMessage(tc *StreamToolCall) bool {
	if tc == nil {
		return false
	}
	if strings.TrimSpace(tc.ApprovalID) != "" || hasUserInputAction(tc.Actions) {
		return false
	}
	return !BuildToolCallStart(tc).HideToolHeader
}

// EndsToolCallBatch reports whether an adapter handles the event by posting to
// the chat. That content must land below the current tool status message, so
// adapters finish the batch first.
func EndsToolCallBatch(event PreparedStreamEvent) bool {
	switch event.Type {
	case StreamEventDelta:
		return event.Delta != "" && event.Phase != StreamPhaseReasoning
	case StreamEventPhaseEnd:
		return event.Phase == StreamPhaseText
	case StreamEventStatus:
		return event.Status == StreamStatusStarted
	case StreamEventAttachment, StreamEventFinal, StreamEventError:
		return true
	default:
		return false
	}
}

// ToolCallStatusTracker keeps the tool status messages of one stream. New calls
// join the current batch. A call that outlives its batch — still running when
// text, an attachment or a prompt ended the batch — keeps updating the message
// that lists it, so no message is left showing a finished call as running.
//
// Like the stream that owns it, a tracker is used from one goroutine. Its
// methods are safe on a nil receiver.
type ToolCallStatusTracker struct {
	newMessage func() *ToolCallStatusMessage
	current    *ToolCallStatusMessage
	earlier    []*ToolCallStatusMessage
}

// NewToolCallStatusTracker returns a tracker that starts each batch with
// newMessage.
func NewToolCallStatusTracker(newMessage func() *ToolCallStatusMessage) *ToolCallStatusTracker {
	return &ToolCallStatusTracker{newMessage: newMessage}
}

// Active reports whether a batch is open for new calls.
func (t *ToolCallStatusTracker) Active() bool {
	return t != nil && t.current != nil
}

// Update applies the event to the status message that already lists the call,
// whether in the current batch or an earlier one, and reports whether one does.
func (t *ToolCallStatusTracker) Update(ctx context.Context, eventType StreamEventType, tc *StreamToolCall) bool {
	if t == nil || tc == nil {
		return false
	}
	callID := strings.TrimSpace(tc.CallID)
	if callID == "" {
		return false
	}
	if t.current.Tracks(callID) {
		t.current.Apply(ctx, eventType, tc)
		return true
	}
	for _, msg := range t.earlier {
		if msg.Tracks(callID) {
			msg.Apply(ctx, eventType, tc)
			return true
		}
	}
	return false
}

// Join adds the event's call to the current batch, starting a batch when none
// is open. Callers flush their pending text first when Active reports false.
func (t *ToolCallStatusTracker) Join(ctx context.Context, eventType StreamEventType, tc *StreamToolCall) {
	if t == nil || tc == nil || t.newMessage == nil {
		return
	}
	if t.current == nil {
		t.current = t.newMessage()
	}
	t.current.Apply(ctx, eventType, tc)
}

// Finish ends the current batch, if any.
func (t *ToolCallStatusTracker) Finish(ctx context.Context) error {
	if t == nil || t.current == nil {
		return nil
	}
	msg := t.current
	t.current = nil
	if !msg.opts.Ephemeral {
		// A finished message can still revise calls that outlive its batch. A
		// replaced draft is gone, so late updates for its calls start a new one.
		t.earlier = append(t.earlier, msg)
	}
	return msg.Finish(ctx)
}

// ToolCallStatusSnapshot is an immutable view of the calls in a status message,
// in the order they started.
type ToolCallStatusSnapshot struct {
	Calls []ToolCallPresentation
	// Final marks a publish after the batch ended. No update may follow it, so
	// a publisher should wait out rate limits rather than skip the edit.
	Final bool
}

// RenderMarkdown renders the snapshot as Markdown of at most maxRunes runes. A
// non-positive maxRunes disables the limit.
func (s ToolCallStatusSnapshot) RenderMarkdown(maxRunes int) string {
	return renderToolCallStatus(s.Calls, true, maxRunes)
}

// RenderPlain renders the snapshot as plain text of at most maxRunes runes. A
// non-positive maxRunes disables the limit.
func (s ToolCallStatusSnapshot) RenderPlain(maxRunes int) string {
	return renderToolCallStatus(s.Calls, false, maxRunes)
}

type toolCallStatusLine struct {
	// detailed is the full presentation; empty when it adds nothing to compact.
	detailed  string
	compact   string
	collapsed bool
}

func (l toolCallStatusLine) text() string {
	if l.collapsed || l.detailed == "" {
		return l.compact
	}
	return l.detailed
}

// renderToolCallStatus lays calls out oldest first. Calls still in flight keep
// their full presentation so the reader sees what is happening, and failures
// keep their error; completed calls collapse to one line so a long batch stays
// short. Over budget, detail collapses oldest first, then the oldest calls give
// way to a count of what was hidden, so the newest call is the last to go.
func renderToolCallStatus(calls []ToolCallPresentation, markdown bool, maxRunes int) string {
	if len(calls) == 0 {
		return ""
	}
	lines := make([]toolCallStatusLine, 0, len(calls))
	for _, p := range calls {
		line := toolCallStatusLine{compact: renderToolCallStatusCompact(p)}
		if toolCallStatusShowsDetail(p.Status) {
			if detailed := strings.TrimSpace(renderToolCall(p, markdown)); detailed != line.compact {
				line.detailed = detailed
			}
		}
		lines = append(lines, line)
	}
	text := joinToolCallStatusLines(lines)
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	for i := range lines {
		if lines[i].detailed == "" {
			continue
		}
		lines[i].collapsed = true
		if text = joinToolCallStatusLines(lines); utf8.RuneCountInString(text) <= maxRunes {
			return text
		}
	}
	kept := len(lines) - 1
	for start := kept - 1; start >= 1; start-- {
		if utf8.RuneCountInString(joinToolCallStatusLines(withHiddenToolCallCount(lines[start:], start))) > maxRunes {
			break
		}
		kept = start
	}
	text = joinToolCallStatusLines(withHiddenToolCallCount(lines[kept:], kept))
	// Only a single call longer than the whole budget gets here. Every line is
	// collapsed by now, so cutting it cannot split Markdown structure.
	return textutil.TruncateRunesWithSuffix(text, maxRunes, toolCallSummaryTruncMark)
}

func withHiddenToolCallCount(lines []toolCallStatusLine, hidden int) []toolCallStatusLine {
	if hidden <= 0 {
		return lines
	}
	marker := fmt.Sprintf("… %d earlier tool calls", hidden)
	if hidden == 1 {
		marker = "… 1 earlier tool call"
	}
	out := make([]toolCallStatusLine, 0, len(lines)+1)
	out = append(out, toolCallStatusLine{compact: marker})
	return append(out, lines...)
}

// joinToolCallStatusLines separates one-line calls with a newline and sets
// multi-line calls apart with a blank line.
func joinToolCallStatusLines(lines []toolCallStatusLine) string {
	var b strings.Builder
	previousMultiline := false
	for i, line := range lines {
		text := line.text()
		multiline := strings.Contains(text, "\n")
		if i > 0 {
			b.WriteString("\n")
			if multiline || previousMultiline {
				b.WriteString("\n")
			}
		}
		b.WriteString(text)
		previousMultiline = multiline
	}
	return b.String()
}

func toolCallStatusShowsDetail(status ToolCallStatus) bool {
	switch status {
	case ToolCallStatusRunning, ToolCallStatusWaiting, ToolCallStatusFailed:
		return true
	default:
		return false
	}
}

func isFinishedToolCallStatus(status ToolCallStatus) bool {
	return status == ToolCallStatusCompleted || status == ToolCallStatusFailed
}

// renderToolCallStatusCompact renders a call as "emoji tool · status · summary".
func renderToolCallStatusCompact(p ToolCallPresentation) string {
	emoji := p.Emoji
	if emoji == "" {
		emoji = ExternalToolCallEmoji
	}
	name := p.ToolName
	if name == "" {
		name = "tool"
	}
	var b strings.Builder
	b.WriteString(emoji)
	b.WriteString(" ")
	b.WriteString(name)
	if p.Status != "" {
		b.WriteString(" · ")
		b.WriteString(string(p.Status))
	}
	header := strings.TrimSpace(p.Header)
	if header == "" {
		header = strings.TrimSpace(p.InputSummary)
	}
	if header = firstLine(header); header != "" {
		b.WriteString(" · ")
		b.WriteString(textutil.TruncateRunesWithSuffix(header, toolCallStatusHeaderRunes, toolCallSummaryTruncMark))
	}
	return b.String()
}

// RetryAfterError is a rate-limit failure carrying the delay the platform asked
// for before the next request.
type RetryAfterError struct {
	Err   error
	Delay time.Duration
}

func (e *RetryAfterError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("rate limited; retry after %s", e.Delay)
	}
	return e.Err.Error()
}

func (e *RetryAfterError) Unwrap() error {
	return e.Err
}

// RetryAfterDelay returns the delay of the first RetryAfterError in err's chain.
func RetryAfterDelay(err error) (time.Duration, bool) {
	var retryErr *RetryAfterError
	if errors.As(err, &retryErr) && retryErr.Delay > 0 {
		return retryErr.Delay, true
	}
	return 0, false
}

// ToolCallStatusOptions configures a ToolCallStatusMessage.
type ToolCallStatusOptions struct {
	// Publish creates the status message on its first call and replaces the
	// message content on later calls. Calls never overlap.
	Publish func(ctx context.Context, snapshot ToolCallStatusSnapshot) error
	// MinInterval spaces publishes so a burst of tool events lands as one
	// platform edit. Publishes after Finish are never delayed.
	MinInterval time.Duration
	// KeepAlive republishes the current state after this long without a
	// publish, for surfaces that expire unless refreshed. Zero disables it.
	KeepAlive time.Duration
	// Ephemeral marks a surface that the next outbound message replaces, such
	// as a draft preview. Finish then only stops publishing, and the message
	// takes no updates afterwards.
	Ephemeral bool
	// Logger receives failed publishes that no caller sees. Optional.
	Logger *slog.Logger
}

// ToolCallStatusMessage folds the ordinary tool calls of one batch into a
// single live message. Apply records an event and returns at once while a
// background goroutine publishes the newest state; Finish ends the batch and
// returns once the message shows it, so whatever the caller posts next lands
// below the status message.
//
// Apply, Tracks and Finish must be called from one goroutine, like the stream
// methods that drive them.
type ToolCallStatusMessage struct {
	opts ToolCallStatusOptions

	wake chan struct{}
	stop chan struct{}
	done chan struct{}

	mu        sync.Mutex
	calls     []toolCallStatusEntry
	version   uint64
	published uint64
	started   bool
	finished  bool
}

type toolCallStatusEntry struct {
	callID       string
	presentation ToolCallPresentation
}

// NewToolCallStatusMessage returns an empty status message. Nothing is
// published before the first Apply.
func NewToolCallStatusMessage(opts ToolCallStatusOptions) *ToolCallStatusMessage {
	return &ToolCallStatusMessage{
		opts: opts,
		wake: make(chan struct{}, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// Apply records a tool_call_start or tool_call_end event and schedules a
// publish. ctx bounds background publishing and should be the stream's context.
// It reports whether the message took the event. After Finish the message only
// takes updates for listed calls that have not finished, and publishes them at
// once.
func (m *ToolCallStatusMessage) Apply(ctx context.Context, eventType StreamEventType, tc *StreamToolCall) bool {
	if m == nil || tc == nil {
		return false
	}
	var p ToolCallPresentation
	switch eventType {
	case StreamEventToolCallStart:
		p = BuildToolCallStart(tc)
	case StreamEventToolCallEnd:
		p = BuildToolCallEnd(tc)
	default:
		return false
	}
	callID := strings.TrimSpace(tc.CallID)
	m.mu.Lock()
	if m.finished {
		m.mu.Unlock()
		return m.revise(ctx, eventType, callID, p)
	}
	changed := m.record(eventType, callID, p)
	launch := changed && !m.started
	if changed {
		m.version++
		m.started = true
	}
	m.mu.Unlock()
	if launch {
		go m.run(ctx)
	}
	if changed {
		m.notify()
	}
	return true
}

// Tracks reports whether the message lists the call with the given ID.
func (m *ToolCallStatusMessage) Tracks(callID string) bool {
	callID = strings.TrimSpace(callID)
	if m == nil || callID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.indexOf(callID) >= 0
}

// Finish ends the batch. It stops background publishing, waits for a publish
// in flight and then, unless the surface is ephemeral, publishes the final
// state if it has not been delivered yet. Finish is idempotent; it is safe on a
// nil receiver and on a message that never received a call.
func (m *ToolCallStatusMessage) Finish(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.finished {
		m.mu.Unlock()
		return nil
	}
	m.finished = true
	started := m.started
	m.mu.Unlock()
	if !started {
		return nil
	}
	close(m.stop)
	select {
	case <-m.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if m.opts.Ephemeral {
		return nil
	}
	m.mu.Lock()
	if m.version == m.published {
		m.mu.Unlock()
		return nil
	}
	snapshot := m.snapshotLocked(true)
	version := m.version
	m.mu.Unlock()
	if err := m.opts.Publish(ctx, snapshot); err != nil {
		return err
	}
	m.markPublished(version)
	return nil
}

// revise applies a late event for a call that outlived the batch: still
// running when the batch ended. Nothing publishes in the background anymore,
// so the message is updated at once.
func (m *ToolCallStatusMessage) revise(ctx context.Context, eventType StreamEventType, callID string, p ToolCallPresentation) bool {
	if m.opts.Ephemeral || callID == "" {
		return false
	}
	select {
	case <-m.done:
	default:
		// A Finish abandoned on context end left a publish in flight.
		return false
	}
	m.mu.Lock()
	idx := m.indexOf(callID)
	if idx < 0 || isFinishedToolCallStatus(m.calls[idx].presentation.Status) {
		m.mu.Unlock()
		return false
	}
	m.record(eventType, callID, p)
	m.version++
	snapshot := m.snapshotLocked(true)
	version := m.version
	m.mu.Unlock()
	if err := m.opts.Publish(ctx, snapshot); err != nil {
		if m.opts.Logger != nil {
			m.opts.Logger.Warn("tool call status revision failed", slog.Any("error", err))
		}
		return true
	}
	m.markPublished(version)
	return true
}

// record applies one presentation to the batch and reports whether it changed.
// Calls without an ID are matched on end by tool name, oldest unfinished first,
// which is the order runtimes execute them in.
func (m *ToolCallStatusMessage) record(eventType StreamEventType, callID string, p ToolCallPresentation) bool {
	idx := -1
	switch {
	case callID != "":
		idx = m.indexOf(callID)
	case eventType == StreamEventToolCallEnd:
		for i, entry := range m.calls {
			if entry.callID == "" && entry.presentation.ToolName == p.ToolName && !isFinishedToolCallStatus(entry.presentation.Status) {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		m.calls = append(m.calls, toolCallStatusEntry{callID: callID, presentation: p})
		return true
	}
	// A late or repeated start must not reopen a call that already finished.
	if eventType == StreamEventToolCallStart && isFinishedToolCallStatus(m.calls[idx].presentation.Status) {
		return false
	}
	m.calls[idx].presentation = p
	return true
}

func (m *ToolCallStatusMessage) indexOf(callID string) int {
	for i, entry := range m.calls {
		if entry.callID == callID {
			return i
		}
	}
	return -1
}

func (m *ToolCallStatusMessage) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *ToolCallStatusMessage) run(ctx context.Context) {
	defer close(m.done)
	var (
		refreshTimer *time.Timer
		refresh      <-chan time.Time
		notBefore    time.Time
		failures     int
	)
	if m.opts.KeepAlive > 0 {
		refreshTimer = time.NewTimer(m.opts.KeepAlive)
		defer refreshTimer.Stop()
		refresh = refreshTimer.C
	}
	for {
		force := false
		select {
		case <-ctx.Done():
			return
		case <-m.stop:
			return
		case <-m.wake:
		case <-refresh:
			force = true
			refreshTimer.Reset(m.opts.KeepAlive)
		}
		// select picks randomly among ready cases, so a pending wake can win
		// over stop. Never start a publish once Finish has begun.
		if !m.waitUntil(ctx, notBefore) || m.stopping() || ctx.Err() != nil {
			return
		}
		snapshot, version, ok := m.pending(force)
		if !ok {
			continue
		}
		err := m.opts.Publish(ctx, snapshot)
		notBefore = time.Now().Add(m.opts.MinInterval)
		if refreshTimer != nil {
			refreshTimer.Reset(m.opts.KeepAlive)
		}
		if err == nil {
			failures = 0
			m.markPublished(version)
			continue
		}
		failures++
		if delay, ok := RetryAfterDelay(err); ok {
			if retryAt := time.Now().Add(delay); retryAt.After(notBefore) {
				notBefore = retryAt
			}
		}
		if m.opts.Logger != nil {
			m.opts.Logger.Warn("tool call status publish failed",
				slog.Int("attempt", failures),
				slog.Any("error", err),
			)
		}
		if failures < toolCallStatusRetries {
			m.notify()
		}
	}
}

// waitUntil sleeps until t and reports false when the batch stops first.
func (m *ToolCallStatusMessage) waitUntil(ctx context.Context, t time.Time) bool {
	delay := time.Until(t)
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-m.stop:
		return false
	case <-timer.C:
		return true
	}
}

// stopping reports whether Finish has begun.
func (m *ToolCallStatusMessage) stopping() bool {
	select {
	case <-m.stop:
		return true
	default:
		return false
	}
}

// pending returns the state to publish: the newest one when it has not been
// published yet, or the current one regardless when force is set.
func (m *ToolCallStatusMessage) pending(force bool) (ToolCallStatusSnapshot, uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 || (!force && m.version == m.published) {
		return ToolCallStatusSnapshot{}, 0, false
	}
	return m.snapshotLocked(false), m.version, true
}

func (m *ToolCallStatusMessage) snapshotLocked(final bool) ToolCallStatusSnapshot {
	calls := make([]ToolCallPresentation, len(m.calls))
	for i, entry := range m.calls {
		calls[i] = entry.presentation
	}
	return ToolCallStatusSnapshot{Calls: calls, Final: final}
}

func (m *ToolCallStatusMessage) markPublished(version uint64) {
	m.mu.Lock()
	if version > m.published {
		m.published = version
	}
	m.mu.Unlock()
}
