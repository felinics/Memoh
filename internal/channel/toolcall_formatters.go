package channel

import (
	"fmt"
	"strings"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/textutil"
)

// toolFormatter produces a structured presentation for a specific built-in
// tool. Status is already inferred by the caller (running / completed /
// failed); the formatter may choose to populate Header, Body, Footer,
// InputSummary, ResultSummary, or any subset. Emoji / ToolName / Status are
// filled by the caller if left empty.
type toolFormatter func(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation

// toolFormatters registers the per-tool renderers. Missing entries fall back
// to the generic SummarizeToolInput / SummarizeToolResult helpers.
var toolFormatters = map[string]toolFormatter{
	"list":  formatList,
	"read":  formatRead,
	"write": formatWrite,
	"edit":  formatEdit,

	"exec":                  formatExec,
	"bg_status":             formatBgStatus,
	"list_background":       formatListBackground,
	"get_background_status": formatGetBackgroundStatus,
	"kill_background":       formatKillBackground,

	"web_search": formatWebSearch,
	"web_fetch":  formatWebFetch,

	"search_memory":   formatSearchMemory,
	"search_messages": formatSearchMessages,
	"list_sessions":   formatListSessions,

	"list_schedule":   formatListSchedule,
	"get_schedule":    formatGetSchedule,
	"create_schedule": formatCreateSchedule,
	"update_schedule": formatUpdateSchedule,
	"delete_schedule": formatDeleteSchedule,

	"send":  formatSend,
	"react": formatReact,

	"get_contacts": formatGetContacts,

	"list_email_accounts": formatListEmailAccounts,
	"send_email":          formatSendEmail,
	"list_email":          formatListEmail,
	"read_email":          formatReadEmail,

	"spawn_agent":  formatAgentControl,
	"send_message": formatAgentControl,
	"wait":         formatWait,
	"wait_until":   formatWait,
	"list_agents":  formatAgentControl,
	"use_skill":    formatUseSkill,

	"generate_image":   formatGenerateImage,
	"speak":            formatSpeak,
	"transcribe_audio": formatTranscribeAudio,
	"ask_user":         formatAskUser,
}

func lookupToolFormatter(name string) toolFormatter {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil
	}
	return toolFormatters[key]
}

// --- helpers ------------------------------------------------------------

func inputMap(tc *StreamToolCall) map[string]any {
	if tc == nil {
		return nil
	}
	m, _ := normalizeToMap(tc.Input)
	return m
}

func resultMap(tc *StreamToolCall) map[string]any {
	if tc == nil {
		return nil
	}
	m, _ := normalizeToMap(tc.Result)
	return m
}

func errorPresentation(p ToolCallPresentation, status ToolCallStatus, tc *StreamToolCall) (ToolCallPresentation, bool) {
	if status != ToolCallStatusFailed {
		return p, false
	}
	errText := ""
	if res := resultMap(tc); res != nil {
		errText = pickStringField(res, "error", "message", "stderr")
	}
	if errText == "" {
		if tc != nil {
			if s, ok := tc.Result.(string); ok {
				errText = strings.TrimSpace(s)
			}
		}
	}
	if errText == "" {
		errText = "failed"
	}
	p.Footer = "error: " + truncateSummary(errText)
	return p, true
}

func truncLine(s string) string {
	return textutil.TruncateRunesWithSuffix(strings.TrimSpace(s), 200, toolCallSummaryTruncMark)
}

func asSliceOfMaps(v any) []map[string]any {
	if v == nil {
		return nil
	}
	switch arr := v.(type) {
	case []any:
		out := make([]map[string]any, 0, len(arr))
		for _, item := range arr {
			if m, ok := normalizeToMap(item); ok {
				out = append(out, m)
			}
		}
		return out
	case []map[string]any:
		return arr
	}
	return nil
}

func formatAskUser(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	p := ToolCallPresentation{HideToolHeader: true}
	loc := i18n.New(tc.Locale)
	if status == ToolCallStatusFailed {
		if failed, done := errorPresentation(p, status, tc); done {
			return failed
		}
	}
	// The interaction surface has already been replaced with the complete
	// question/answer summary before the deferred tool result resumes. A second
	// terminal card would repeat the first question and confuse the handoff.
	if status == ToolCallStatusCompleted {
		return p
	}

	in := inputMap(tc)
	payload, _ := normalizeToMap(in["payload"])
	questions := asSliceOfMaps(payload["questions"])
	if len(questions) == 0 {
		p.Header = loc.T("cmd.userInput.inputRequested")
	} else {
		p.Header = pickStringField(questions[0], "text")
		if len(questions) > 1 {
			p.Header = fmt.Sprintf("1/%d\n\n%s", len(questions), p.Header)
		}
		// Plain-text channels collect one durable page at a time. Native
		// adapters strip this body and render their own controls.
		appendAskUserOptions(&p, questions[0])
	}

	if status == ToolCallStatusRunning {
		p.Status = ToolCallStatusWaiting
		if len(questions) > 0 {
			switch pickStringField(questions[0], "kind") {
			case userinput.QuestionKindMultiSelect:
				p.Footer = loc.T("cmd.userInput.multiHint") + "\n" + loc.T("cmd.userInput.skipHint")
			case userinput.QuestionKindText:
				p.Footer = loc.T("cmd.userInput.textHint") + "\n" + loc.T("cmd.userInput.skipHint")
			default:
				p.Footer = loc.T("cmd.userInput.selectHint") + "\n" + loc.T("cmd.userInput.skipHint")
			}
		}
	}
	return p
}

func appendAskUserOptions(p *ToolCallPresentation, question map[string]any) {
	options := asSliceOfMaps(question["options"])
	for idx, option := range options {
		label := pickStringField(option, "label")
		if label == "" {
			continue
		}
		line := fmt.Sprintf("%d) %s", idx+1, label)
		if description := pickStringField(option, "description"); description != "" {
			line += " - " + description
		}
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: line})
	}
}

// --- file tools ---------------------------------------------------------

func formatList(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	path := ""
	if in != nil {
		path = pickStringField(in, "path")
		if path == "" {
			path = "."
		}
	}
	p := ToolCallPresentation{Header: path}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res == nil {
		return p
	}
	entries := asSliceOfMaps(res["entries"])
	total := 0
	if v, ok := numericField(res, "total_count"); ok {
		total = int(v)
	} else {
		total = len(entries)
	}
	if total > 0 {
		p.Header = fmt.Sprintf("%s · %d entries", path, total)
	}
	preview := 5
	shown := 0
	for _, e := range entries {
		if shown >= preview {
			break
		}
		name := pickStringField(e, "path")
		if name == "" {
			continue
		}
		isDir := false
		if b, ok := e["is_dir"].(bool); ok {
			isDir = b
		}
		kind := "file"
		if isDir {
			kind = "dir"
			name = strings.TrimRight(name, "/") + "/"
		}
		size := ""
		if sz, ok := numericField(e, "size"); ok && !isDir {
			size = humanSize(int64(sz))
		}
		var text string
		switch {
		case isDir:
			text = fmt.Sprintf("- %s (%s)", name, kind)
		case size != "":
			text = fmt.Sprintf("- %s (%s, %s)", name, kind, size)
		default:
			text = fmt.Sprintf("- %s (%s)", name, kind)
		}
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: text})
		shown++
	}
	if total > shown {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: fmt.Sprintf("…and %d more", total-shown)})
	}
	return p
}

func formatRead(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	path := pickStringField(in, "path", "file_path", "filepath")
	p := ToolCallPresentation{Header: path}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res == nil {
		return p
	}
	if lines, ok := numericField(res, "total_lines"); ok && path != "" {
		p.Header = fmt.Sprintf("%s · %d lines", path, int(lines))
	}
	return p
}

func formatWrite(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	path := pickStringField(in, "path", "file_path", "filepath")
	p := ToolCallPresentation{Header: path}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	return p
}

func formatEdit(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	path := pickStringField(in, "path", "file_path", "filepath")
	p := ToolCallPresentation{Header: path}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res == nil {
		return p
	}
	if changes, ok := numericField(res, "changes"); ok && int(changes) > 0 && path != "" {
		p.Header = fmt.Sprintf("%s · %d changes", path, int(changes))
	}
	return p
}

// --- exec / background tasks -------------------------------------------

func formatExec(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	cmd := pickStringField(in, "command", "cmd")
	first := firstLine(cmd)
	p := ToolCallPresentation{Header: "$ " + first}
	if first == "" {
		p.Header = ""
	}
	if status == ToolCallStatusRunning {
		return p
	}
	res := resultMap(tc)
	if res == nil {
		if e, done := errorPresentation(p, status, tc); done {
			return e
		}
		return p
	}
	if st := pickStringField(res, "status"); st == "background_started" || st == "auto_backgrounded" {
		taskID := pickStringField(res, "task_id")
		out := pickStringField(res, "output_file")
		parts := []string{st}
		if taskID != "" {
			parts = append(parts, "task_id="+taskID)
		}
		if out != "" {
			parts = append(parts, out)
		}
		p.Footer = strings.Join(parts, " · ")
		return p
	}
	exit := 0
	if v, ok := numericField(res, "exit_code"); ok {
		exit = int(v)
	}
	stdout := pickStringField(res, "stdout")
	stderr := pickStringField(res, "stderr")
	if stdout != "" {
		text := truncLine(firstLine(stdout))
		if text != "" {
			p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "stdout: " + text})
		}
	}
	if stderr != "" {
		text := truncLine(firstLine(stderr))
		if text != "" {
			p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "stderr: " + text})
		}
	}
	if status == ToolCallStatusFailed {
		msg := firstLine(stderr)
		if msg == "" {
			msg = pickStringField(res, "error", "message")
		}
		if msg == "" {
			msg = fmt.Sprintf("exit=%d", exit)
		}
		p.Footer = "error: " + truncateSummary(msg)
		return p
	}
	p.Footer = fmt.Sprintf("exit=%d", exit)
	return p
}

func formatBgStatus(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	action := pickStringField(in, "action")
	taskID := pickStringField(in, "task_id")
	header := action
	if taskID != "" {
		header = fmt.Sprintf("%s · %s", action, taskID)
	}
	p := ToolCallPresentation{Header: header}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res == nil {
		return p
	}
	tasks := asSliceOfMaps(res["tasks"])
	if len(tasks) > 0 {
		p.Header = fmt.Sprintf("%s · %d tasks", action, len(tasks))
		preview := 5
		for i, t := range tasks {
			if i >= preview {
				break
			}
			id := pickStringField(t, "task_id", "id")
			desc := pickStringField(t, "description")
			st := pickStringField(t, "status")
			exit := ""
			if v, ok := numericField(t, "exit_code"); ok {
				exit = fmt.Sprintf(" exit=%d", int(v))
			}
			label := id
			if desc != "" {
				label = fmt.Sprintf("%s \"%s\"", id, desc)
			}
			text := fmt.Sprintf("- %s · %s%s", label, st, exit)
			p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: text})
		}
		if len(tasks) > preview {
			p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: fmt.Sprintf("…and %d more", len(tasks)-preview)})
		}
		return p
	}
	if msg := pickStringField(res, "message"); msg != "" {
		p.Footer = msg
	}
	return p
}

func formatListBackground(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	p := ToolCallPresentation{Header: "list"}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res == nil {
		return p
	}
	tasks := asSliceOfMaps(res["tasks"])
	p.Header = fmt.Sprintf("%d tasks", len(tasks))
	preview := 5
	for i, t := range tasks {
		if i >= preview {
			break
		}
		id := pickStringField(t, "task_id", "id")
		desc := pickStringField(t, "description")
		st := pickStringField(t, "status")
		exit := ""
		if v, ok := numericField(t, "exit_code"); ok {
			exit = fmt.Sprintf(" exit=%d", int(v))
		}
		label := id
		if desc != "" {
			label = fmt.Sprintf("%s \"%s\"", id, desc)
		}
		text := fmt.Sprintf("- %s · %s%s", label, st, exit)
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: text})
	}
	if len(tasks) > preview {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: fmt.Sprintf("…and %d more", len(tasks)-preview)})
	}
	return p
}

func formatGetBackgroundStatus(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	taskID := pickStringField(in, "task_id")
	p := ToolCallPresentation{Header: taskID}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res == nil {
		return p
	}
	if id := pickStringField(res, "task_id"); id != "" {
		taskID = id
	}
	kind := pickStringField(res, "kind")
	st := pickStringField(res, "status")
	if taskID != "" && st != "" {
		p.Header = fmt.Sprintf("%s · %s", taskID, st)
	}
	if kind != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "kind: " + kind})
	}
	if desc := pickStringField(res, "description"); desc != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "description: " + desc})
	}
	if out := pickStringField(res, "output_file"); out != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "output: " + out})
	}
	if branches := asSliceOfMaps(res["branches"]); len(branches) > 0 {
		p.Footer = fmt.Sprintf("%d branches", len(branches))
	} else if v, ok := numericField(res, "exit_code"); ok {
		p.Footer = fmt.Sprintf("exit=%d", int(v))
	}
	return p
}

func formatKillBackground(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	taskID := pickStringField(in, "task_id")
	p := ToolCallPresentation{Header: taskID}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res == nil {
		return p
	}
	if msg := pickStringField(res, "message"); msg != "" {
		p.Footer = msg
	}
	return p
}

// --- network tools ------------------------------------------------------

func formatWebSearch(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	query := pickStringField(in, "query")
	p := ToolCallPresentation{Header: fmt.Sprintf("%q", query)}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	results := asSliceOfMaps(res["results"])
	p.Header = fmt.Sprintf("%d results for %q", len(results), query)
	preview := 5
	for i, r := range results {
		if i >= preview {
			break
		}
		p.Body = append(p.Body, ToolCallBlock{
			Type:  ToolCallBlockLink,
			Title: pickStringField(r, "title", "name"),
			URL:   pickStringField(r, "url", "link"),
			Desc:  truncLine(pickStringField(r, "description", "snippet", "content", "text")),
		})
	}
	if len(results) > preview {
		p.Footer = fmt.Sprintf("…and %d more", len(results)-preview)
	}
	return p
}

func formatWebFetch(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	url := pickStringField(in, "url")
	p := ToolCallPresentation{Header: url}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res == nil {
		return p
	}
	fetched := pickStringField(res, "url")
	if fetched != "" {
		url = fetched
	}
	title := pickStringField(res, "title")
	if title != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockLink, Title: title, URL: url})
	} else if url != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockLink, URL: url})
	}
	format := pickStringField(res, "format")
	provider := pickStringField(res, "providerName", "provider")
	length := 0
	if v, ok := numericField(res, "length"); ok {
		length = int(v)
	}
	footer := format
	if provider != "" {
		if footer == "" {
			footer = provider
		} else {
			footer = fmt.Sprintf("%s · %s", provider, footer)
		}
	}
	if length > 0 {
		if footer == "" {
			footer = fmt.Sprintf("%d chars", length)
		} else {
			footer = fmt.Sprintf("%s · %d chars", footer, length)
		}
	}
	p.Footer = strings.TrimSpace(footer)
	return p
}

// --- memory / history --------------------------------------------------

func formatSearchMemory(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	query := pickStringField(in, "query")
	p := ToolCallPresentation{Header: fmt.Sprintf("%q", query)}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	results := asSliceOfMaps(res["results"])
	if len(results) == 0 {
		results = asSliceOfMaps(res["items"])
	}
	total := len(results)
	if v, ok := numericField(res, "total"); ok && int(v) > total {
		total = int(v)
	}
	p.Header = fmt.Sprintf("%d / %d results for %q", len(results), total, query)
	preview := 5
	for i, r := range results {
		if i >= preview {
			break
		}
		text := pickStringField(r, "text", "content", "memory")
		score := ""
		if v, ok := numericField(r, "score"); ok {
			score = fmt.Sprintf(" (%.2f)", v)
		}
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "- " + truncLine(text) + score})
	}
	if len(results) > preview {
		p.Footer = fmt.Sprintf("…and %d more", len(results)-preview)
	}
	return p
}

func formatSearchMessages(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	keyword := pickStringField(in, "keyword", "query", "q")
	header := ""
	if keyword != "" {
		header = fmt.Sprintf("keyword=%q", keyword)
	}
	p := ToolCallPresentation{Header: header}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	msgs := asSliceOfMaps(res["messages"])
	summary := fmt.Sprintf("%d messages", len(msgs))
	if keyword != "" {
		summary = fmt.Sprintf("%s · keyword=%q", summary, keyword)
	}
	p.Header = summary
	preview := 3
	for i, m := range msgs {
		if i >= preview {
			break
		}
		role := pickStringField(m, "role", "sender")
		text := pickStringField(m, "text", "content")
		when := pickStringField(m, "created_at", "timestamp")
		label := role
		if label == "" {
			label = "msg"
		}
		prefix := label + ": "
		if when != "" {
			prefix = fmt.Sprintf("[%s] %s: ", when, label)
		}
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: prefix + truncLine(text)})
	}
	if len(msgs) > preview {
		p.Footer = fmt.Sprintf("…and %d more", len(msgs)-preview)
	}
	return p
}

func formatListSessions(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	p := ToolCallPresentation{}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	sessions := asSliceOfMaps(res["sessions"])
	p.Header = fmt.Sprintf("%d sessions", len(sessions))
	preview := 5
	for i, s := range sessions {
		if i >= preview {
			break
		}
		id := pickStringField(s, "session_id", "id")
		title := pickStringField(s, "title", "conversation_name")
		platform := pickStringField(s, "platform")
		last := pickStringField(s, "last_active")
		parts := []string{fmt.Sprintf("- #%s", id)}
		if title != "" {
			parts = append(parts, fmt.Sprintf("\"%s\"", title))
		}
		if platform != "" {
			parts = append(parts, "· "+platform)
		}
		if last != "" {
			parts = append(parts, "· "+last)
		}
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: strings.Join(parts, " ")})
	}
	if len(sessions) > preview {
		p.Footer = fmt.Sprintf("…and %d more", len(sessions)-preview)
	}
	return p
}

// --- schedule ----------------------------------------------------------

func formatListSchedule(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	p := ToolCallPresentation{}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	items := asSliceOfMaps(res["items"])
	p.Header = fmt.Sprintf("%d schedules", len(items))
	preview := 5
	for i, item := range items {
		if i >= preview {
			break
		}
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "- " + summarizeScheduleEntry(item)})
	}
	if len(items) > preview {
		p.Footer = fmt.Sprintf("…and %d more", len(items)-preview)
	}
	return p
}

func summarizeScheduleEntry(item map[string]any) string {
	id := pickStringField(item, "id")
	name := pickStringField(item, "name")
	pattern := pickStringField(item, "pattern")
	enabled := true
	if b, ok := item["enabled"].(bool); ok {
		enabled = b
	}
	parts := []string{}
	if id != "" {
		parts = append(parts, fmt.Sprintf("[%s]", id))
	}
	if name != "" {
		parts = append(parts, fmt.Sprintf("\"%s\"", name))
	}
	if pattern != "" {
		parts = append(parts, "· "+pattern)
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	parts = append(parts, "· "+state)
	return strings.Join(parts, " ")
}

func formatGetSchedule(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	id := pickStringField(in, "id")
	p := ToolCallPresentation{Header: id}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	if res := resultMap(tc); res != nil {
		p.Header = summarizeScheduleEntry(res)
	}
	return p
}

func formatCreateSchedule(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	name := pickStringField(in, "name")
	pattern := pickStringField(in, "pattern")
	p := ToolCallPresentation{Header: fmt.Sprintf("\"%s\" · cron %s", name, pattern)}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	if res := resultMap(tc); res != nil {
		id := pickStringField(res, "id")
		if id != "" {
			p.Header = fmt.Sprintf("Created [%s] \"%s\" · cron %s", id, name, pattern)
		}
	}
	return p
}

func formatUpdateSchedule(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	id := pickStringField(in, "id")
	pattern := pickStringField(in, "pattern")
	header := fmt.Sprintf("[%s]", id)
	if pattern != "" {
		header += " · pattern " + pattern
	}
	p := ToolCallPresentation{Header: header}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	p.Header = "Updated " + header
	return p
}

func formatDeleteSchedule(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	id := pickStringField(in, "id")
	p := ToolCallPresentation{Header: fmt.Sprintf("[%s]", id)}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	p.Header = fmt.Sprintf("Deleted [%s]", id)
	return p
}

// --- messaging ---------------------------------------------------------

func formatSend(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	target := pickStringField(in, "target", "to", "recipient", "chat_id")
	platform := pickStringField(in, "platform")
	body := pickStringField(in, "body", "content", "message", "text")
	header := ""
	if target != "" {
		header = "→ " + target
		if platform != "" {
			header += " (" + platform + ")"
		}
	}
	p := ToolCallPresentation{Header: header}
	if body != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: fmt.Sprintf("%q", truncLine(body))})
	}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res != nil {
		parts := []string{}
		if delivered := pickStringField(res, "delivered"); delivered != "" {
			parts = append(parts, delivered)
		} else {
			parts = append(parts, "delivered")
		}
		if msgID := pickStringField(res, "message_id"); msgID != "" {
			parts = append(parts, "message_id="+msgID)
		}
		p.Footer = strings.Join(parts, " · ")
	}
	return p
}

func formatReact(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	emoji := pickStringField(in, "emoji")
	msgID := pickStringField(in, "message_id")
	remove := false
	if b, ok := in["remove"].(bool); ok {
		remove = b
	}
	action := "added"
	if remove {
		action = "removed"
	}
	p := ToolCallPresentation{Header: fmt.Sprintf("%s %s on %s", action, emoji, msgID)}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	return p
}

// --- contacts ----------------------------------------------------------

func formatGetContacts(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	p := ToolCallPresentation{}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	contacts := asSliceOfMaps(res["contacts"])
	if contacts == nil {
		contacts = asSliceOfMaps(res["items"])
	}
	p.Header = fmt.Sprintf("%d contacts", len(contacts))
	preview := 5
	for i, c := range contacts {
		if i >= preview {
			break
		}
		name := pickStringField(c, "display_name", "name")
		platform := pickStringField(c, "platform")
		handle := pickStringField(c, "username", "handle", "channel_id")
		last := pickStringField(c, "last_active")
		parts := []string{"- " + name}
		if platform != "" {
			parts = append(parts, "· "+platform)
		}
		if handle != "" {
			parts = append(parts, "· "+handle)
		} else if last != "" {
			parts = append(parts, "· "+last)
		}
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: strings.Join(parts, " ")})
	}
	if len(contacts) > preview {
		p.Footer = fmt.Sprintf("…and %d more", len(contacts)-preview)
	}
	return p
}

// --- email -------------------------------------------------------------

func formatListEmailAccounts(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	p := ToolCallPresentation{}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	accounts := asSliceOfMaps(res["accounts"])
	p.Header = fmt.Sprintf("%d accounts", len(accounts))
	preview := 5
	for i, a := range accounts {
		if i >= preview {
			break
		}
		addr := pickStringField(a, "address", "email")
		perms := pickStringField(a, "permissions")
		text := "- " + addr
		if perms != "" {
			text += " · " + perms
		}
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: text})
	}
	if len(accounts) > preview {
		p.Footer = fmt.Sprintf("…and %d more", len(accounts)-preview)
	}
	return p
}

func formatSendEmail(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	to := pickStringField(in, "to", "recipient", "target")
	subject := pickStringField(in, "subject")
	header := ""
	if to != "" {
		header = "→ " + to
	}
	p := ToolCallPresentation{Header: header}
	if subject != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "Subject: " + truncLine(subject)})
	}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res != nil {
		parts := []string{}
		if st := pickStringField(res, "status"); st != "" {
			parts = append(parts, "status="+st)
		}
		if msgID := pickStringField(res, "message_id"); msgID != "" {
			parts = append(parts, "message_id="+msgID)
		}
		p.Footer = strings.Join(parts, " · ")
	}
	return p
}

func formatListEmail(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	p := ToolCallPresentation{}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	emails := asSliceOfMaps(res["emails"])
	total := 0
	if v, ok := numericField(res, "total"); ok {
		total = int(v)
	}
	page := 0
	if v, ok := numericField(res, "page"); ok {
		page = int(v)
	}
	if total > 0 {
		p.Header = fmt.Sprintf("page %d · %d of %d", page, len(emails), total)
	} else {
		p.Header = fmt.Sprintf("%d emails", len(emails))
	}
	preview := 5
	for i, em := range emails {
		if i >= preview {
			break
		}
		uid := pickStringField(em, "uid", "id")
		from := pickStringField(em, "from", "sender")
		subject := pickStringField(em, "subject")
		when := pickStringField(em, "date", "received_at", "created_at")
		parts := []string{"- #" + uid}
		if from != "" {
			parts = append(parts, "· "+from)
		}
		if subject != "" {
			parts = append(parts, "· \""+truncLine(subject)+"\"")
		}
		if when != "" {
			parts = append(parts, "· "+when)
		}
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: strings.Join(parts, " ")})
	}
	if len(emails) > preview {
		p.Footer = fmt.Sprintf("…and %d more", len(emails)-preview)
	}
	return p
}

func formatReadEmail(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	uid := pickStringField(in, "uid", "id", "message_id")
	p := ToolCallPresentation{Header: "#" + uid}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res == nil {
		return p
	}
	from := pickStringField(res, "from", "sender")
	subject := pickStringField(res, "subject")
	received := pickStringField(res, "date", "received_at", "received")
	if from != "" {
		p.Header = "From: " + from
	}
	if subject != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "Subject: " + truncLine(subject)})
	}
	if received != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "Received: " + received})
	}
	p.Footer = "(body hidden)"
	return p
}

// --- subagent / skills / media -----------------------------------------

func formatAgentControl(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	p := ToolCallPresentation{}
	if status == ToolCallStatusRunning {
		in := inputMap(tc)
		agentID := pickStringField(in, "id", "agent_id")
		task := pickStringField(in, "task", "message")
		switch strings.ToLower(strings.TrimSpace(tc.Name)) {
		case "list_agents":
			p.Header = "list agents"
		default:
			if agentID != "" {
				p.Header = agentID + " · " + truncLine(task)
			} else {
				p.Header = truncLine(task)
			}
		}
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if strings.EqualFold(strings.TrimSpace(tc.Name), "list_agents") {
		agents := asSliceOfMaps(res["agents"])
		p.Header = fmt.Sprintf("%d agents", len(agents))
		for _, a := range agents {
			id := pickStringField(a, "agent_id")
			st := pickStringField(a, "status")
			if id != "" {
				p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "- " + id + " · " + st})
			}
		}
		return p
	}
	agentID := pickStringField(res, "agent_id")
	statusText := pickStringField(res, "status")
	switch {
	case agentID != "" && statusText != "":
		p.Header = agentID + " · " + statusText
	case agentID != "":
		p.Header = agentID
	default:
		p.Header = statusText
	}
	taskID := pickStringField(res, "task_id")
	sessionID := pickStringField(res, "session_id")
	if taskID != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "task " + taskID})
	}
	if sessionID != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "session " + sessionID})
	}
	if text := pickStringField(res, "text", "report"); text != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: truncLine(text)})
	}
	if errText := pickStringField(res, "error"); errText != "" {
		p.Footer = "error: " + truncateSummary(errText)
	}
	return p
}

func formatUseSkill(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	name := pickStringField(in, "name", "skill", "skill_name")
	p := ToolCallPresentation{Header: name}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res != nil {
		desc := pickStringField(res, "description", "summary")
		if desc != "" {
			p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: truncLine(desc)})
		}
	}
	return p
}

func formatWait(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	p := ToolCallPresentation{}
	if status != ToolCallStatusRunning {
		return p
	}
	in := inputMap(tc)
	switch strings.ToLower(strings.TrimSpace(tc.Name)) {
	case "wait_until":
		if taskID := pickStringField(in, "task_id"); taskID != "" {
			p.Header = "wait until " + taskID
		} else {
			p.Header = "wait until task"
		}
	default:
		if duration := pickStringField(in, "duration"); duration != "" {
			p.Header = "wait " + duration + "s"
		} else {
			p.Header = "wait"
		}
	}
	return p
}

func formatGenerateImage(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	prompt := pickStringField(in, "prompt", "description")
	p := ToolCallPresentation{Header: fmt.Sprintf("prompt: %q", truncLine(prompt))}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res == nil {
		return p
	}
	path := pickStringField(res, "path", "file", "url")
	mime := pickStringField(res, "mime", "content_type")
	size := ""
	if v, ok := numericField(res, "size"); ok {
		size = humanSize(int64(v))
	}
	headerParts := []string{}
	if path != "" {
		headerParts = append(headerParts, path)
	}
	if size != "" {
		headerParts = append(headerParts, size)
	}
	if mime != "" {
		headerParts = append(headerParts, mime)
	}
	if len(headerParts) > 0 {
		p.Header = strings.Join(headerParts, " · ")
	}
	if prompt != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: "prompt: " + fmt.Sprintf("%q", truncLine(prompt))})
	}
	return p
}

func formatSpeak(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	in := inputMap(tc)
	text := pickStringField(in, "text", "content")
	p := ToolCallPresentation{Header: fmt.Sprintf("%q", truncLine(text))}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	p.Header = "delivered · " + fmt.Sprintf("%q", truncLine(text))
	return p
}

func formatTranscribeAudio(tc *StreamToolCall, status ToolCallStatus) ToolCallPresentation {
	p := ToolCallPresentation{}
	if status == ToolCallStatusRunning {
		return p
	}
	if e, done := errorPresentation(p, status, tc); done {
		return e
	}
	res := resultMap(tc)
	if res == nil {
		return p
	}
	lang := pickStringField(res, "language", "lang")
	durSec := 0
	if v, ok := numericField(res, "duration"); ok {
		durSec = int(v)
	} else if v, ok := numericField(res, "duration_seconds"); ok {
		durSec = int(v)
	}
	text := pickStringField(res, "text", "transcription")
	headerParts := []string{}
	if lang != "" {
		headerParts = append(headerParts, lang)
	}
	if durSec > 0 {
		headerParts = append(headerParts, fmt.Sprintf("%ds", durSec))
	}
	if len(headerParts) > 0 {
		p.Header = strings.Join(headerParts, " · ")
	}
	if text != "" {
		p.Body = append(p.Body, ToolCallBlock{Type: ToolCallBlockText, Text: fmt.Sprintf("%q", truncLine(text))})
	}
	return p
}

// humanSize formats a byte count into a short, human-readable string.
func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB"}
	if exp >= len(units) {
		exp = len(units) - 1
	}
	return fmt.Sprintf("%.1f %s", float64(bytes)/float64(div), units[exp])
}
