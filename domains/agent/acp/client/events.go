package client

import (
	"fmt"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"

	agentdomain "github.com/memohai/memoh/domains/agent"
	acpprofile "github.com/memohai/memoh/domains/agent/acp/profile"
)

const (
	maxCollectedStreamEvents = 4096
	maxTrackedACPToolStates  = 1024
)

type EventSink interface {
	EmitStreamEvent(agentdomain.StreamEvent)
}

type EventSinkFunc func(agentdomain.StreamEvent)

func (f EventSinkFunc) EmitStreamEvent(ev agentdomain.StreamEvent) {
	if f != nil {
		f(ev)
	}
}

type toolEventEmitter struct {
	mu        sync.RWMutex
	collector *eventCollector
	sink      EventSink
	limit     ToolOutputLimit
}

func (e *toolEventEmitter) setPromptState(collector *eventCollector, sink EventSink, limit ToolOutputLimit) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.collector = collector
	e.sink = sink
	e.limit = limit
	e.mu.Unlock()
}

func (e *toolEventEmitter) emit(ev agentdomain.StreamEvent) {
	if e == nil {
		return
	}
	e.mu.RLock()
	collector := e.collector
	sink := e.sink
	limit := e.limit
	e.mu.RUnlock()
	ev = LimitStreamEvent(ev, limit)
	if collector != nil {
		collector.record(ev)
	}
	if sink != nil {
		sink.EmitStreamEvent(ev)
	}
}

type eventCollector struct {
	mu     sync.Mutex
	text   strings.Builder
	events []agentdomain.StreamEvent
	// transcript is kept separately from the capped UI event buffer.
	transcript *TranscriptRecorder
	limit      ToolOutputLimit
}

func newEventCollector(limits ...ToolOutputLimit) *eventCollector {
	var limit ToolOutputLimit
	if len(limits) > 0 {
		limit = limits[0]
	}
	return &eventCollector{transcript: NewTranscriptRecorder(limit), limit: limit}
}

func (c *eventCollector) record(ev agentdomain.StreamEvent) {
	if c == nil {
		return
	}
	ev = LimitStreamEvent(ev, c.limit)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = appendBoundedStreamEvents(c.events, ev)
	c.transcript.Add(ev)
}

func (c *eventCollector) apply(n acp.SessionNotification, events []agentdomain.StreamEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	update := n.Update
	events = limitStreamEvents(events, c.limit)
	c.events = appendBoundedStreamEvents(c.events, events...)
	for _, ev := range events {
		c.transcript.Add(ev)
	}
	if update.AgentMessageChunk != nil {
		c.text.WriteString(contentText(update.AgentMessageChunk.Content))
	}
}

func limitStreamEvents(events []agentdomain.StreamEvent, limit ToolOutputLimit) []agentdomain.StreamEvent {
	if !hasToolOutputLimit(limit) || len(events) == 0 {
		return events
	}
	out := make([]agentdomain.StreamEvent, len(events))
	for i, ev := range events {
		out[i] = LimitStreamEvent(ev, limit)
	}
	return out
}

func (c *eventCollector) result() RunResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	events := append([]agentdomain.StreamEvent(nil), c.events...)
	text := strings.TrimSpace(c.text.String())
	return RunResult{
		Text:   text,
		Events: events,
		Output: c.transcript.Messages(text),
	}
}

func contentText(block acp.ContentBlock) string {
	if block.Text != nil {
		return block.Text.Text
	}
	if block.ResourceLink != nil {
		return block.ResourceLink.Uri
	}
	return ""
}

type acpToolEventMapper struct {
	mu       sync.Mutex
	tools    map[string]*acpToolState
	lastPlan string
	quirks   acpprofile.ToolQuirks
}

type acpToolState struct {
	id        string
	title     string
	kind      string
	status    string
	input     any
	output    any
	locations []acp.ToolCallLocation
	content   []acp.ToolCallContent
	name      string
	nativeIn  map[string]any
	started   bool
	done      bool
}

func newACPToolEventMapper(quirks acpprofile.ToolQuirks) *acpToolEventMapper {
	return &acpToolEventMapper{tools: map[string]*acpToolState{}, quirks: quirks}
}

func (m *acpToolEventMapper) eventsFromNotification(n acp.SessionNotification) []agentdomain.StreamEvent {
	update := n.Update
	switch {
	case update.AgentMessageChunk != nil:
		text := contentText(update.AgentMessageChunk.Content)
		if text == "" {
			return nil
		}
		return []agentdomain.StreamEvent{{
			Type:  agentdomain.TextDelta,
			Delta: text,
		}}
	case update.AgentThoughtChunk != nil:
		text := contentText(update.AgentThoughtChunk.Content)
		if text == "" {
			return nil
		}
		return []agentdomain.StreamEvent{{
			Type:  agentdomain.ReasoningDelta,
			Delta: text,
		}}
	case update.Plan != nil:
		return m.applyPlan(*update.Plan)
	case update.ToolCall != nil:
		return m.applyToolCall(*update.ToolCall)
	case update.ToolCallUpdate != nil:
		return m.applyToolUpdate(*update.ToolCallUpdate)
	default:
		return nil
	}
}

func (m *acpToolEventMapper) applyPlan(plan acp.SessionUpdatePlan) []agentdomain.StreamEvent {
	text := formatPlanEntries(plan.Entries)
	if text == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if text == m.lastPlan {
		return nil
	}
	prefix := "Plan:\n"
	if m.lastPlan != "" {
		prefix = "\nPlan updated:\n"
	}
	m.lastPlan = text
	return []agentdomain.StreamEvent{{
		Type:  agentdomain.ReasoningDelta,
		Delta: prefix + text,
	}}
}

func formatPlanEntries(entries []acp.PlanEntry) string {
	var sb strings.Builder
	for _, entry := range entries {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		status := strings.TrimSpace(string(entry.Status))
		if status == "" {
			status = "pending"
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("- [")
		sb.WriteString(status)
		sb.WriteString("] ")
		sb.WriteString(content)
	}
	return strings.TrimSpace(sb.String())
}

func (m *acpToolEventMapper) applyToolCall(tc acp.SessionUpdateToolCall) []agentdomain.StreamEvent {
	id := strings.TrimSpace(string(tc.ToolCallId))
	if id == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.ensureTool(id)
	state.title = strings.TrimSpace(tc.Title)
	state.kind = strings.TrimSpace(string(tc.Kind))
	state.status = strings.TrimSpace(string(tc.Status))
	state.input = tc.RawInput
	state.output = tc.RawOutput
	state.locations = append([]acp.ToolCallLocation(nil), tc.Locations...)
	state.content = append([]acp.ToolCallContent(nil), tc.Content...)
	return m.eventsForState(state)
}

func (m *acpToolEventMapper) applyToolUpdate(tc acp.SessionToolCallUpdate) []agentdomain.StreamEvent {
	id := strings.TrimSpace(string(tc.ToolCallId))
	if id == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.ensureTool(id)
	if tc.Title != nil {
		state.title = strings.TrimSpace(*tc.Title)
	}
	if tc.Kind != nil {
		state.kind = strings.TrimSpace(string(*tc.Kind))
	}
	if tc.Status != nil {
		state.status = strings.TrimSpace(string(*tc.Status))
	}
	if tc.RawInput != nil {
		state.input = tc.RawInput
	}
	if tc.RawOutput != nil {
		state.output = tc.RawOutput
	}
	if len(tc.Locations) > 0 {
		state.locations = append([]acp.ToolCallLocation(nil), tc.Locations...)
	}
	if len(tc.Content) > 0 {
		state.content = append([]acp.ToolCallContent(nil), tc.Content...)
	}
	return m.eventsForState(state)
}

func (m *acpToolEventMapper) ensureTool(id string) *acpToolState {
	state := m.tools[id]
	if state == nil {
		if len(m.tools) >= maxTrackedACPToolStates {
			for staleID := range m.tools {
				delete(m.tools, staleID)
				break
			}
		}
		state = &acpToolState{id: id}
		m.tools[id] = state
	}
	return state
}

func appendBoundedStreamEvents(events []agentdomain.StreamEvent, incoming ...agentdomain.StreamEvent) []agentdomain.StreamEvent {
	if len(incoming) == 0 {
		return events
	}
	events = append(events, incoming...)
	if len(events) <= maxCollectedStreamEvents {
		return events
	}
	return append([]agentdomain.StreamEvent(nil), events[len(events)-maxCollectedStreamEvents:]...)
}

func (m *acpToolEventMapper) eventsForState(state *acpToolState) []agentdomain.StreamEvent {
	name, input, ok := nativeToolFromACPState(state, m.quirks)
	if !ok {
		return nil
	}
	state.name = name
	state.nativeIn = input

	events := make([]agentdomain.StreamEvent, 0, 2)
	if !state.started {
		state.started = true
		events = append(events, agentdomain.StreamEvent{
			Type:       agentdomain.ToolCallStart,
			ToolCallID: state.id,
			ToolName:   state.name,
			Input:      state.nativeIn,
		})
	}
	if isTerminalACPToolStatus(state.status) && !state.done {
		state.done = true
		ev := agentdomain.StreamEvent{
			Type:       agentdomain.ToolCallEnd,
			ToolCallID: state.id,
			ToolName:   state.name,
			Input:      state.nativeIn,
			Result:     nativeToolResultFromACPState(state),
		}
		if isFailedACPToolStatus(state.status) {
			ev.Error = state.status
		}
		events = append(events, ev)
		delete(m.tools, state.id)
	}
	return events
}

// nativeToolFromACPState maps an agent-reported tool call onto a canonical
// native tool name and input. All agent-wording knowledge (title heuristics)
// comes from quirks, which profile owns per agent - never inline keyword
// checks here.
func nativeToolFromACPState(state *acpToolState, quirks acpprofile.ToolQuirks) (string, map[string]any, bool) {
	if state == nil {
		return "", nil, false
	}
	switch strings.ToLower(strings.TrimSpace(state.kind)) {
	case string(acp.ToolKindExecute):
		command := commandFromACPInput(state.input)
		if command == "" {
			command = quirks.CommandFromTitle(state.title)
		}
		if command == "" {
			return "", nil, false
		}
		return "exec", map[string]any{"command": command}, true
	case string(acp.ToolKindRead):
		path := pathFromACPInput(state.input)
		if path == "" {
			path = pathFromACPLocations(state.locations)
		}
		if path == "" {
			return "", nil, false
		}
		return "read", map[string]any{"path": path}, true
	case string(acp.ToolKindEdit):
		name, input, ok := editToolFromACPState(state)
		// A title like "Write file X" overrides the structural edit shape. Apply
		// it HERE so the streamed tool-event name and the approval name (both
		// resolve through this function) agree - one action must not surface as
		// "edit" on the stream while its approval says "write".
		if ok && name == "edit" && quirks.TitleIndicatesWrite(state.title) {
			name = "write"
		}
		return name, input, ok
	default:
		return "", nil, false
	}
}

func editToolFromACPState(state *acpToolState) (string, map[string]any, bool) {
	path := pathFromACPInput(state.input)
	if path == "" {
		path = pathFromACPLocations(state.locations)
	}
	diff := firstACPToolDiff(state.content)
	if path == "" && diff != nil {
		path = strings.TrimSpace(diff.Path)
	}
	if path == "" {
		return "", nil, false
	}

	if m, ok := state.input.(map[string]any); ok {
		if content, ok := rawStringFromMap(m, "content", "text"); ok {
			return "write", writeToolInput(path, content), true
		}
		oldText, hasOld := rawStringFromMap(m, "old_string", "oldString", "old_text", "oldText")
		newText, hasNew := rawStringFromMap(m, "new_string", "newString", "new_text", "newText")
		if hasOld || hasNew {
			return "edit", map[string]any{
				"path":     path,
				"old_text": oldText,
				"new_text": newText,
			}, true
		}
	}
	if diff != nil {
		if diff.OldText == nil {
			return "write", writeToolInput(path, diff.NewText), true
		}
		return "edit", map[string]any{
			"path":     path,
			"old_text": *diff.OldText,
			"new_text": diff.NewText,
		}, true
	}
	return "edit", map[string]any{"path": path}, true
}

func nativeToolResultFromACPState(state *acpToolState) any {
	if state == nil {
		return nil
	}
	result := normalizeACPToolOutput(state.output)
	if result == nil {
		if text := toolContentText(state.content); text != "" {
			result = map[string]any{"stdout": text}
		}
	}
	if result == nil {
		result = map[string]any{}
	}
	if isFailedACPToolStatus(state.status) {
		if m, ok := result.(map[string]any); ok {
			m["isError"] = true
			if _, ok := m["content"]; !ok {
				text := firstNonEmptyString(
					stringFromAny(m["stderr"]),
					stringFromAny(m["stdout"]),
					strings.TrimSpace(state.status),
				)
				m["content"] = []map[string]any{{"type": "text", "text": text}}
			}
		}
	}
	return result
}

func normalizeACPToolOutput(value any) any {
	switch v := value.(type) {
	case nil:
		return nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = val
		}
		if code, ok := numberFromAny(firstPresent(v, "exit_code", "exitCode", "code")); ok {
			out["exit_code"] = code
		}
		if stdout := firstNonEmptyRawString(
			rawStringFromAny(firstPresent(v, "stdout", "output", "text")),
			toolTextFromContentValue(v["content"]),
		); stdout != "" {
			out["stdout"] = stdout
		}
		if stderr := rawStringFromAny(firstPresent(v, "stderr", "error")); strings.TrimSpace(stderr) != "" {
			out["stderr"] = stderr
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return map[string]any{"stdout": v}
	default:
		return value
	}
}

func commandFromACPInput(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s := stringFromAny(item); s != "" {
				parts = append(parts, shellQuoteIfNeeded(s))
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		if cmd := firstNonEmptyString(
			stringFromAny(firstPresent(v, "command", "cmd", "shell_command", "shellCommand", "script")),
			commandFromACPInput(v["argv"]),
			commandFromACPInput(v["args"]),
		); cmd != "" {
			return cmd
		}
	}
	return ""
}

func pathFromACPInput(value any) string {
	if m, ok := value.(map[string]any); ok {
		return stringFromAny(firstPresent(m, "path", "file_path", "filePath", "file", "filename"))
	}
	return ""
}

func pathFromACPLocations(locations []acp.ToolCallLocation) string {
	for _, location := range locations {
		if path := strings.TrimSpace(location.Path); path != "" {
			return path
		}
	}
	return ""
}

func firstACPToolDiff(contents []acp.ToolCallContent) *acp.ToolCallContentDiff {
	for i := range contents {
		if contents[i].Diff != nil {
			return contents[i].Diff
		}
	}
	return nil
}

func toolContentText(contents []acp.ToolCallContent) string {
	if len(contents) == 0 {
		return ""
	}
	lines := make([]string, 0, len(contents))
	for _, item := range contents {
		if item.Content != nil {
			if text := contentText(item.Content.Content); text != "" {
				lines = append(lines, text)
			}
		}
		if item.Diff != nil {
			if item.Diff.Path != "" {
				lines = append(lines, item.Diff.Path)
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func toolTextFromContentValue(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	lines := make([]string, 0, len(items))
	for _, raw := range items {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(stringFromAny(m["type"]), "text") {
			if text := stringFromAny(m["text"]); text != "" {
				lines = append(lines, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func isTerminalACPToolStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "done", "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func isFailedACPToolStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func firstPresent(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmptyRawString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func rawStringFromAny(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return stringFromAny(value)
}

func rawStringFromMap(m map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		value, ok := m[key]
		if !ok {
			continue
		}
		if s, ok := value.(string); ok {
			return s, true
		}
		text := rawStringFromAny(value)
		if text != "" {
			return text, true
		}
	}
	return "", false
}

func numberFromAny(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	default:
		return 0, false
	}
}

func shellQuoteIfNeeded(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, " \t\n'\"$`\\") {
		return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return value
}
