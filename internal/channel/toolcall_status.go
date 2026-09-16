package channel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/felinics/memoh/internal/textutil"
)

// With a bot's reuse_tool_call_message_in_im setting on, IM channels that can
// edit messages stop posting one message per tool call. The ordinary calls of
// an uninterrupted batch — no assistant text, attachment or interactive prompt
// in between — share one status message that is created for the first call and
// edited as calls start and finish. ToolCallStatusMessage owns one batch,
// ToolCallStatusTracker routes a stream's tool events across batches, and
// StatusMessageEditor turns a publish into the platform's send-then-edit pair.
//
// Adapters drive all of this from the stream's own goroutine; only the timer
// that flushes a held back update runs elsewhere.

// toolCallStatusHeaderRunes caps the call summary on a collapsed line.
const toolCallStatusHeaderRunes = 120

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
// adapters end the batch first.
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

// ToolCallStatusHooks are the adapter's part of handling a tool event: text it
// has buffered must land before a new batch starts, and a call that keeps its
// own message falls back to one message per call.
type ToolCallStatusHooks struct {
	// FlushText posts buffered assistant text. Optional.
	FlushText func(ctx context.Context) error
	// PushCall delivers the call the way the bot does without this setting.
	PushCall func(ctx context.Context, eventType StreamEventType, tc *StreamToolCall) error
	// HasCard reports that the call already owns an interactive message, so its
	// result belongs there rather than in the batch. Optional.
	HasCard func(tc *StreamToolCall) bool
}

// ToolCallStatusTracker keeps the status messages of one stream. New calls join
// the current batch. A call that outlives its batch — still running when text,
// an attachment or a prompt ended the batch — keeps updating the message that
// lists it, so no message is left showing a finished call as running.
//
// Like the stream that owns it, a tracker is used from one goroutine. Its
// methods are safe on a nil receiver.
type ToolCallStatusTracker struct {
	newMessage func() *ToolCallStatusMessage
	logger     *slog.Logger
	current    *ToolCallStatusMessage
	earlier    []*ToolCallStatusMessage
}

// NewToolCallStatusTracker returns a tracker that starts each batch with
// newMessage.
func NewToolCallStatusTracker(logger *slog.Logger, newMessage func() *ToolCallStatusMessage) *ToolCallStatusTracker {
	return &ToolCallStatusTracker{newMessage: newMessage, logger: logger}
}

// Active reports whether a batch is open for new calls.
func (t *ToolCallStatusTracker) Active() bool {
	return t != nil && t.current != nil
}

// Route applies one tool event: a result for a call already listed updates the
// message listing it, a call that keeps its own message falls back to
// hooks.PushCall, and anything else joins the batch.
func (t *ToolCallStatusTracker) Route(ctx context.Context, hooks ToolCallStatusHooks, eventType StreamEventType, tc *StreamToolCall) error {
	if t == nil {
		return hooks.PushCall(ctx, eventType, tc)
	}
	listed := t.update(ctx, eventType, tc)
	switch {
	case !UsesToolCallStatusMessage(tc):
		t.EndBatch(ctx)
		return hooks.PushCall(ctx, eventType, tc)
	case hooks.HasCard != nil && hooks.HasCard(tc):
		return hooks.PushCall(ctx, eventType, tc)
	case listed:
		return nil
	}
	if t.current == nil {
		if hooks.FlushText != nil {
			if err := hooks.FlushText(ctx); err != nil {
				return err
			}
		}
		t.current = t.newMessage()
	}
	t.current.Apply(ctx, eventType, tc)
	return nil
}

// update applies the event to the status message that already lists the call,
// whether in the current batch or an earlier one, and reports whether one does.
func (t *ToolCallStatusTracker) update(ctx context.Context, eventType StreamEventType, tc *StreamToolCall) bool {
	if tc == nil {
		return false
	}
	callID := strings.TrimSpace(tc.CallID)
	if callID == "" {
		return false
	}
	for _, msg := range append([]*ToolCallStatusMessage{t.current}, t.earlier...) {
		if msg.Tracks(callID) {
			msg.Apply(ctx, eventType, tc)
			return true
		}
	}
	return false
}

// EndBatch ends the current batch before the stream posts anything else, so the
// status message keeps its place above what follows. A failed last update only
// costs the status message its final state, so it is logged, not returned.
func (t *ToolCallStatusTracker) EndBatch(ctx context.Context) {
	if err := t.Finish(ctx); err != nil && t.logger != nil {
		t.logger.WarnContext(ctx, "tool call status update failed", slog.Any("error", err))
	}
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

// ToolCallStatusOptions configures a ToolCallStatusMessage.
type ToolCallStatusOptions struct {
	// Publish creates the status message on its first call and replaces the
	// message content on later calls.
	Publish func(ctx context.Context, snapshot ToolCallStatusSnapshot) error
	// MinInterval spaces publishes so a burst of tool events lands as one
	// platform edit. State held back is published once the interval is up, or
	// earlier by the next event or by Finish, which is never delayed.
	MinInterval time.Duration
	// KeepAlive republishes the current state after this long without a
	// publish, for a surface that expires unless it is refreshed, such as a
	// Telegram draft. Zero disables it.
	KeepAlive time.Duration
	// Ephemeral marks a surface that the next outbound message replaces, such
	// as a draft preview. Such a message is gone once its batch ends, so it
	// takes no late updates.
	Ephemeral bool
	// Logger receives failed publishes that no caller sees. Optional.
	Logger *slog.Logger
}

// ToolCallStatusMessage folds the ordinary tool calls of one batch into a
// single live message. Publishing is spaced by MinInterval; state held back
// reaches the chat on a timer, so a batch never sits on a stale state while the
// model thinks. Publishes never overlap, and the lock is what keeps the timer
// out of the stream's way.
type ToolCallStatusMessage struct {
	opts ToolCallStatusOptions

	mu          sync.Mutex
	calls       []toolCallStatusEntry
	pending     bool
	lastPublish time.Time
	finished    bool
	flush       *time.Timer
}

type toolCallStatusEntry struct {
	callID       string
	presentation ToolCallPresentation
}

// NewToolCallStatusMessage returns an empty status message. Nothing is
// published before the first Apply.
func NewToolCallStatusMessage(opts ToolCallStatusOptions) *ToolCallStatusMessage {
	return &ToolCallStatusMessage{opts: opts}
}

// Apply records a tool_call_start or tool_call_end event and publishes the new
// state unless a publish just happened. It reports whether the message took the
// event. After Finish it only takes updates for listed calls that have not
// finished, and publishes them at once.
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
	defer m.mu.Unlock()
	if m.finished {
		return m.revise(ctx, eventType, callID, p)
	}
	if !m.record(eventType, callID, p) {
		return true
	}
	m.pending = true
	if wait := m.opts.MinInterval - time.Since(m.lastPublish); wait > 0 {
		m.scheduleFlush(ctx, wait, false)
		return true
	}
	m.publishLogged(ctx, false)
	return true
}

// scheduleFlush publishes the state once wait is up, unless another event or
// Finish publishes it first. A refresh publishes even with nothing pending, to
// hold a surface that expires on its own.
func (m *ToolCallStatusMessage) scheduleFlush(ctx context.Context, wait time.Duration, refresh bool) {
	if m.flush != nil {
		m.flush.Stop()
	}
	m.flush = time.AfterFunc(wait, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.flush = nil
		if m.finished || ctx.Err() != nil || (!m.pending && !refresh) {
			return
		}
		m.publishLogged(ctx, false)
	})
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

// Finish ends the batch and publishes the state the throttle held back, so a
// batch never ends on a stale state. It is idempotent and safe on a nil
// receiver.
func (m *ToolCallStatusMessage) Finish(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished {
		return nil
	}
	m.finished = true
	if m.flush != nil {
		m.flush.Stop()
		m.flush = nil
	}
	if !m.pending {
		return nil
	}
	return m.publish(ctx, true)
}

// revise applies a late event for a call that outlived the batch: still running
// when the batch ended. Nothing is published in the background anymore, so the
// message is updated at once.
func (m *ToolCallStatusMessage) revise(ctx context.Context, eventType StreamEventType, callID string, p ToolCallPresentation) bool {
	idx := m.indexOf(callID)
	if m.opts.Ephemeral || callID == "" || idx < 0 || isFinishedToolCallStatus(m.calls[idx].presentation.Status) {
		return false
	}
	if m.record(eventType, callID, p) {
		m.publishLogged(ctx, true)
	}
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

func (m *ToolCallStatusMessage) publishLogged(ctx context.Context, final bool) {
	if err := m.publish(ctx, final); err != nil && m.opts.Logger != nil {
		m.opts.Logger.WarnContext(ctx, "tool call status publish failed", slog.Any("error", err))
	}
}

func (m *ToolCallStatusMessage) publish(ctx context.Context, final bool) error {
	if len(m.calls) == 0 {
		return nil
	}
	calls := make([]ToolCallPresentation, len(m.calls))
	for i, entry := range m.calls {
		calls[i] = entry.presentation
	}
	m.pending = false
	m.lastPublish = time.Now()
	if !final && m.opts.KeepAlive > 0 {
		m.scheduleFlush(ctx, m.opts.KeepAlive, true)
	}
	return m.opts.Publish(ctx, ToolCallStatusSnapshot{Calls: calls, Final: final})
}

// StatusMessageEditor posts a status message on its first publish and edits it
// afterwards. A message that went away is replaced rather than losing the batch.
type StatusMessageEditor struct {
	// Send posts the first version and returns the platform's message ID.
	Send func(ctx context.Context, text string) (string, error)
	// Edit replaces the text of the message with the given ID. final marks the
	// last update of a batch, which a platform may want to retry harder.
	Edit func(ctx context.Context, id, text string, final bool) error
	// Gone reports that an edit failed because the message no longer exists.
	// Optional; without it a failed edit is returned as is.
	Gone func(err error) bool

	id   string
	last string
}

// Publish shows text as the status message, skipping an edit that would change
// nothing.
func (e *StatusMessageEditor) Publish(ctx context.Context, text string, final bool) error {
	if text == "" || (e.id != "" && text == e.last) {
		return nil
	}
	if e.id != "" {
		err := e.Edit(ctx, e.id, text, final)
		if err == nil {
			e.last = text
			return nil
		}
		if e.Gone == nil || !e.Gone(err) {
			return err
		}
		e.id = ""
	}
	id, err := e.Send(ctx, text)
	if err != nil {
		return err
	}
	e.id, e.last = id, text
	return nil
}

// ToolCallStatusSnapshot is an immutable view of the calls in a status message,
// in the order they started.
type ToolCallStatusSnapshot struct {
	Calls []ToolCallPresentation
	// Final marks the last publish of a batch.
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

// renderToolCallStatus lays calls out oldest first. A call still in flight or
// failed keeps its full presentation so the reader sees what is happening;
// completed calls collapse to one line. Over budget every call collapses, then
// the oldest give way to a count of what was hidden, so the newest call is the
// last to go.
func renderToolCallStatus(calls []ToolCallPresentation, markdown bool, maxRunes int) string {
	if len(calls) == 0 {
		return ""
	}
	compact := make([]string, len(calls))
	lines := make([]string, len(calls))
	for i, p := range calls {
		compact[i] = renderToolCallStatusCompact(p)
		lines[i] = compact[i]
		if toolCallStatusShowsDetail(p.Status) {
			if detailed := strings.TrimSpace(renderToolCall(p, markdown)); detailed != "" {
				lines[i] = detailed
			}
		}
	}
	if text := joinToolCallStatusLines(lines); fitsToolCallStatus(text, maxRunes) {
		return text
	}
	text := joinToolCallStatusLines(compact)
	for hidden := 1; hidden < len(compact) && !fitsToolCallStatus(text, maxRunes); hidden++ {
		marker := fmt.Sprintf("… %d earlier tool calls", hidden)
		if hidden == 1 {
			marker = "… 1 earlier tool call"
		}
		text = joinToolCallStatusLines(append([]string{marker}, compact[hidden:]...))
	}
	// Only a single call longer than the whole budget gets past the loop. Every
	// line is collapsed by now, so cutting it cannot split Markdown structure.
	return textutil.TruncateRunesWithSuffix(text, maxRunes, toolCallSummaryTruncMark)
}

func fitsToolCallStatus(text string, maxRunes int) bool {
	return maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes
}

// joinToolCallStatusLines separates one-line calls with a newline and sets
// multi-line calls apart with a blank line.
func joinToolCallStatusLines(lines []string) string {
	var b strings.Builder
	previousMultiline := false
	for i, text := range lines {
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
