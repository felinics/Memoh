package gateway

import (
	"strings"
)

// ToolCallStatus is the lifecycle state of a single tool call as surfaced in IM.
type ToolCallStatus string

const (
	ToolCallStatusRunning          ToolCallStatus = "running"
	ToolCallStatusWaiting          ToolCallStatus = "waiting"
	ToolCallStatusApprovalRequired ToolCallStatus = "approval_required"
	ToolCallStatusCompleted        ToolCallStatus = "completed"
	ToolCallStatusFailed           ToolCallStatus = "failed"
)

// ExternalToolCallEmoji is the emoji used for any tool not in the built-in
// whitelist (including MCP and federation tools).
const ExternalToolCallEmoji = "⚙️"

// builtinToolCallEmoji maps built-in tool names to their display emoji.
// Names are matched case-insensitively after trimming whitespace.
var builtinToolCallEmoji = map[string]string{
	"list":                  "📂",
	"read":                  "📖",
	"write":                 "📝",
	"edit":                  "📝",
	"exec":                  "💻",
	"bg_status":             "💻",
	"list_background":       "💻",
	"get_background_status": "💻",
	"kill_background":       "💻",
	"web_search":            "🌐",
	"web_fetch":             "🌐",

	"search_memory":   "🧠",
	"search_messages": "🧠",
	"list_sessions":   "🧠",

	"list_schedule":   "📅",
	"get_schedule":    "📅",
	"create_schedule": "📅",
	"update_schedule": "📅",
	"delete_schedule": "📅",

	"send":  "💬",
	"react": "💬",

	"get_contacts": "👥",

	"list_email_accounts": "📧",
	"send_email":          "📧",
	"list_email":          "📧",
	"read_email":          "📧",

	"spawn_agent":  "🤖",
	"send_message": "🤖",
	"wait":         "⏱️",
	"wait_until":   "⏱️",
	"list_agents":  "🤖",
	"use_skill":    "🧩",

	"generate_image":   "🖼️",
	"speak":            "🔊",
	"transcribe_audio": "🎧",
	"ask_user":         "❓",
}

// ToolCallEmoji returns the emoji mapped for a tool name. Unknown / external
// tools fall back to ExternalToolCallEmoji.
func ToolCallEmoji(toolName string) string {
	key := strings.ToLower(strings.TrimSpace(toolName))
	if emoji, ok := builtinToolCallEmoji[key]; ok {
		return emoji
	}
	return ExternalToolCallEmoji
}

// ToolCallBlockType distinguishes body block rendering semantics.
type ToolCallBlockType string

const (
	ToolCallBlockText ToolCallBlockType = "text" // free-form line or paragraph
	ToolCallBlockLink ToolCallBlockType = "link" // titled hyperlink, optional description
	ToolCallBlockCode ToolCallBlockType = "code" // preformatted / code block
)

// ToolCallBlock is one rich element inside ToolCallPresentation.Body. Fields
// not applicable to the Type are ignored.
type ToolCallBlock struct {
	Type  ToolCallBlockType
	Title string
	URL   string
	Desc  string
	Text  string
}

// ToolCallPresentation is the rendered single-message view of one tool call
// state. Adapters call RenderToolCallMessage (or their own renderer) against
// this struct to produce the final IM text body.
//
// The preferred fields are Header / Body / Footer, populated either by
// per-tool formatters (see toolcall_formatters.go) or by the generic builder.
// InputSummary / ResultSummary are retained so existing callers that expect
// two flat strings keep working.
type ToolCallPresentation struct {
	Emoji    string
	ToolName string
	Status   ToolCallStatus
	// HideToolHeader is for user-facing interactions whose implementation as
	// a tool is not meaningful to the recipient, such as ask_user prompts.
	HideToolHeader bool

	Header string
	Body   []ToolCallBlock
	Footer string

	InputSummary  string
	ResultSummary string
}

// BuildToolCallStart builds a presentation for a tool_call_start event.
// Returns a zero-value presentation when the payload is nil.
func BuildToolCallStart(tc *StreamToolCall) ToolCallPresentation {
	if tc == nil {
		return ToolCallPresentation{}
	}
	name := strings.TrimSpace(tc.Name)
	if strings.TrimSpace(tc.ApprovalID) != "" {
		summary := SummarizeToolInput(name, tc.Input)
		return ToolCallPresentation{
			Emoji:    ToolCallEmoji(name),
			ToolName: name,
			Status:   ToolCallStatusApprovalRequired,
			Header:   summary,
			Body: []ToolCallBlock{
				{
					Type: ToolCallBlockText,
					Text: approvalHintText(tc),
				},
			},
			InputSummary: summary,
		}
	}
	if fn := lookupToolFormatter(name); fn != nil {
		p := fn(tc, ToolCallStatusRunning)
		fillBaseIdentity(&p, name, ToolCallStatusRunning)
		return p
	}
	summary := SummarizeToolInput(name, tc.Input)
	return ToolCallPresentation{
		Emoji:        ToolCallEmoji(name),
		ToolName:     name,
		Status:       ToolCallStatusRunning,
		Header:       summary,
		InputSummary: summary,
	}
}

// BuildToolCallEnd builds a presentation for a tool_call_end event. The
// completed / failed status is inferred from the tool result payload (ok=false,
// error fields, non-zero exit codes, etc.).
func BuildToolCallEnd(tc *StreamToolCall) ToolCallPresentation {
	if tc == nil {
		return ToolCallPresentation{}
	}
	name := strings.TrimSpace(tc.Name)
	status := ToolCallStatusCompleted
	if isToolResultFailure(tc.Result) {
		status = ToolCallStatusFailed
	}
	if fn := lookupToolFormatter(name); fn != nil {
		p := fn(tc, status)
		fillBaseIdentity(&p, name, status)
		return p
	}
	inputSummary := SummarizeToolInput(name, tc.Input)
	resultSummary := SummarizeToolResult(name, tc.Result)
	return ToolCallPresentation{
		Emoji:         ToolCallEmoji(name),
		ToolName:      name,
		Status:        status,
		Header:        inputSummary,
		Footer:        resultSummary,
		InputSummary:  inputSummary,
		ResultSummary: resultSummary,
	}
}

func approvalHintText(tc *StreamToolCall) string {
	id := tc.ShortID
	if id > 0 {
		return strings.TrimSpace(
			"Approval required. Use /approve " + intString(id) +
				" to allow, or /reject " + intString(id) + " [reason] to reject. You can also reply to this message with /approve or /reject.",
		)
	}
	return "Approval required. Reply to this message with /approve or /reject."
}

func intString(v int) string {
	var b strings.Builder
	if v == 0 {
		return "0"
	}
	if v < 0 {
		b.WriteByte('-')
		v = -v
	}
	var digits [20]byte
	i := len(digits)
	for v > 0 {
		i--
		digits[i] = byte('0' + v%10)
		v /= 10
	}
	b.Write(digits[i:])
	return b.String()
}

// fillBaseIdentity fills emoji / tool name / status after a per-tool
// formatter runs, without clobbering values set by the formatter itself.
// InputSummary / ResultSummary are intentionally NOT populated here: when a
// formatter is used, its Header / Body / Footer output is authoritative and
// we must not append raw JSON summaries as a fallback.
func fillBaseIdentity(p *ToolCallPresentation, name string, status ToolCallStatus) {
	if p.Emoji == "" {
		p.Emoji = ToolCallEmoji(name)
	}
	if p.ToolName == "" {
		p.ToolName = name
	}
	if p.Status == "" {
		p.Status = status
	}
}

// RenderToolCallMessage renders a plain-text single-message view of a tool
// call state. Links are rendered as two lines: the title on one line and the
// URL on the next. Adapters that want Markdown link syntax should use
// RenderToolCallMessageMarkdown instead.
func RenderToolCallMessage(p ToolCallPresentation) string {
	return renderToolCall(p, false)
}

// RenderToolCallMessageMarkdown renders a Markdown version of the tool call
// presentation. Links become [title](url), code blocks are fenced with triple
// backticks, and plain-text blocks are unchanged.
func RenderToolCallMessageMarkdown(p ToolCallPresentation) string {
	return renderToolCall(p, true)
}

func renderToolCall(p ToolCallPresentation, markdown bool) string {
	if !presentationHasContent(p) {
		return ""
	}
	var b strings.Builder

	if !p.HideToolHeader {
		emoji := p.Emoji
		if emoji == "" {
			emoji = ExternalToolCallEmoji
		}
		b.WriteString(emoji)
		b.WriteString(" ")
		if p.ToolName != "" {
			b.WriteString(p.ToolName)
		} else {
			b.WriteString("tool")
		}
		if p.Status != "" {
			b.WriteString(" · ")
			b.WriteString(string(p.Status))
		}
	}

	header := strings.TrimSpace(p.Header)
	if header == "" {
		header = strings.TrimSpace(p.InputSummary)
	}
	if header != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(header)
	}

	for _, block := range p.Body {
		rendered := renderToolCallBlock(block, markdown)
		if rendered == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(rendered)
	}

	footer := strings.TrimSpace(p.Footer)
	if footer == "" {
		footer = strings.TrimSpace(p.ResultSummary)
	}
	if footer != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(footer)
	}

	return b.String()
}

func presentationHasContent(p ToolCallPresentation) bool {
	if p.ToolName != "" || p.Emoji != "" {
		return true
	}
	if strings.TrimSpace(p.Header) != "" {
		return true
	}
	if strings.TrimSpace(p.Footer) != "" {
		return true
	}
	if strings.TrimSpace(p.InputSummary) != "" {
		return true
	}
	if strings.TrimSpace(p.ResultSummary) != "" {
		return true
	}
	return len(p.Body) > 0
}

func renderToolCallBlock(block ToolCallBlock, markdown bool) string {
	switch block.Type {
	case ToolCallBlockLink:
		return renderLinkBlock(block, markdown)
	case ToolCallBlockCode:
		return renderCodeBlock(block, markdown)
	case ToolCallBlockText:
		return strings.TrimRight(block.Text, "\n")
	default:
		// Unknown types: fall back to Text for resilience.
		return strings.TrimRight(block.Text, "\n")
	}
}

func renderLinkBlock(block ToolCallBlock, markdown bool) string {
	title := strings.TrimSpace(block.Title)
	url := strings.TrimSpace(block.URL)
	desc := strings.TrimSpace(block.Desc)

	var b strings.Builder
	switch {
	case markdown && url != "":
		label := title
		if label == "" {
			label = url
		}
		b.WriteString("[")
		b.WriteString(label)
		b.WriteString("](")
		b.WriteString(url)
		b.WriteString(")")
	case url != "" && title != "":
		b.WriteString(title)
		b.WriteString("\n")
		b.WriteString(url)
	case url != "":
		b.WriteString(url)
	case title != "":
		b.WriteString(title)
	}

	if desc != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(desc)
	}
	return b.String()
}

func renderCodeBlock(block ToolCallBlock, markdown bool) string {
	text := strings.TrimRight(block.Text, "\n")
	if text == "" {
		return ""
	}
	if !markdown {
		return text
	}
	var b strings.Builder
	b.WriteString("```")
	b.WriteString("\n")
	b.WriteString(text)
	b.WriteString("\n```")
	return b.String()
}
