package compaction

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/turn"
)

const entryMetadataMaxBytes = 256

var (
	dataURIRe = regexp.MustCompile(`data:[a-zA-Z0-9.+/-]+;base64,[A-Za-z0-9+/=]+`)
	// base64BlobRe matches long continuous base64-alphabet runs (data-URI-less
	// media such as MCP ImageContent's bare "data" field). The high threshold
	// avoids scrubbing ordinary tokens/words, which are broken by punctuation or
	// whitespace.
	base64BlobRe              = regexp.MustCompile(`[A-Za-z0-9+/_-]{256,}={0,2}`)
	entryMetadataValueEscaper = strings.NewReplacer("[", "(", "]", ")")
)

// entryPart is a minimal view of one stored content part, enough to render a
// summarizer-friendly entry without leaking raw JSON, media payloads, or
// oversized tool blobs.
type entryPart struct {
	Type       string          `json:"type"`
	ToolName   string          `json:"toolName"`
	ToolCallID string          `json:"toolCallId"`
	Input      json.RawMessage `json:"input"`
	Output     json.RawMessage `json:"output"`
	Result     json.RawMessage `json:"result"`
}

func isToolCallPartType(value string) bool {
	return strings.Contains(value, "tool-call") || strings.Contains(value, "tool_call")
}

func isToolResultPartType(value string) bool {
	return strings.Contains(value, "tool-result") || strings.Contains(value, "tool_result")
}

func isToolPartType(value string) bool {
	return isToolCallPartType(value) || isToolResultPartType(value)
}

// renderCandidateEntry renders a history row for the summarizer. Each tool
// result a stored row holds cites the ref get_messages reads it back by.
func renderCandidateEntry(record historyfrag.HistoryRecord) string {
	rowID := ""
	if record.SourceKind == historyfrag.SourceDBMessage {
		rowID = record.Ref.ID
	}
	content := strings.TrimSpace(entryRender{rowID: rowID}.content(record.ModelMessage))
	if content == "" {
		return ""
	}
	if header := renderEntryHeader(record); header != "" {
		return header + "\n" + content
	}
	return content
}

func renderEntryHeader(record historyfrag.HistoryRecord) string {
	var lines []string
	add := func(label, value string) {
		value = cleanEntryMetadataValue(value)
		if value == "" {
			return
		}
		lines = append(lines, "["+label+": "+value+"]")
	}

	add("message_id", record.ExternalMessageID)
	add("reply_to", record.SourceReplyToMessageID)
	add("sender", record.SenderDisplayName)
	add("platform", record.Platform)
	add("conversation_type", record.Scope.ConversationType)
	add("conversation_name", record.Scope.ConversationName)
	add("reply_target", record.Scope.ReplyTarget)
	add("workspace", entryExecutionLocation(record.Metadata))
	return strings.Join(lines, "\n")
}

// entryExecutionLocation renders the workspace a message ran in, so summaries
// keep the execution-location provenance that raw replay carries via
// transition markers.
func entryExecutionLocation(metadata map[string]any) string {
	raw, ok := metadata["execution_location"].(map[string]any)
	if !ok {
		return ""
	}
	targetID, _ := raw["target_id"].(string)
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return ""
	}
	name, _ := raw["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" || name == targetID {
		return targetID
	}
	return name + " (" + targetID + ")"
}

func cleanEntryMetadataValue(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return ""
	}
	value = entryMetadataValueEscaper.Replace(value)
	return truncateBytes(value, entryMetadataMaxBytes)
}

// renderEntryContent turns a stored history message into clean text for the
// summarizer prompt: plain text is surfaced directly, media is reduced to a
// marker, tool calls show their name and arguments, and tool results
// show their outcome text (falling back to a marker) instead of dumping the
// raw stored JSON.
func renderEntryContent(mm turn.ModelMessage) string {
	return entryRender{}.content(mm)
}

// entryRender names the row a message was stored in, so each tool result it
// renders cites ref=<row>:<part>, the part's position in the message history
// replays from that row.
type entryRender struct {
	rowID string
}

func (r entryRender) content(mm turn.ModelMessage) string {
	parts := parseEntryParts(mm.Content)
	refs := r.resultRefs(mm)

	// Legacy OpenAI-style tool-result envelope: ToolCallID lives on the
	// ModelMessage itself and Content IS the result payload directly, not a
	// content-part array (see timeline.nativeToolRoleContent). A content-part
	// tool-result, when present, still takes precedence below.
	if strings.TrimSpace(mm.ToolCallID) != "" && !hasToolResultPart(parts) {
		return refToolResult(mm.Name, refs, 0, mm.Content)
	}

	var segs []string
	if text := strings.TrimSpace(mm.TextContent()); text != "" {
		segs = append(segs, text)
	}

	sawToolCallPart := false
	results := 0
	for _, p := range parts {
		switch {
		case p.Type == "image":
			segs = append(segs, "[image]")
		case p.Type == "file":
			segs = append(segs, "[file]")
		case isToolCallPartType(p.Type):
			sawToolCallPart = true
			segs = append(segs, toolCallMarker(p.ToolName, p.Input))
		case isToolResultPartType(p.Type):
			segs = append(segs, refToolResult(p.ToolName, refs, results, p.Output, p.Result))
			results++
		}
	}

	if !sawToolCallPart {
		for _, tc := range mm.ToolCalls {
			segs = append(segs, toolCallMarker(tc.Function.Name, json.RawMessage(tc.Function.Arguments)))
		}
	}

	return strings.Join(segs, "\n")
}

// resultRefs lists the ref of each tool result in mm, in order: its row and
// its part index in the message history replays from that row.
func (r entryRender) resultRefs(mm turn.ModelMessage) []string {
	if r.rowID == "" {
		return nil
	}
	var refs []string
	for i, part := range historyfrag.StoredModelMessageToSDKMessage(mm).Content {
		if _, ok := part.(sdk.ToolResultPart); ok {
			refs = append(refs, r.rowID+":"+strconv.Itoa(i))
		}
	}
	return refs
}

// refToolResult renders the index-th tool result of an entry with its ref,
// or plain when the entry has none.
func refToolResult(name string, refs []string, index int, candidates ...json.RawMessage) string {
	if index >= len(refs) {
		return renderToolResult(candidates...)
	}
	label := "[tool_result"
	if name = cleanEntryMetadataValue(name); name != "" {
		label += ": " + name
	}
	return label + " ref=" + refs[index] + "] " + renderToolResult(candidates...)
}

func hasToolResultPart(parts []entryPart) bool {
	for _, p := range parts {
		if isToolResultPartType(p.Type) {
			return true
		}
	}
	return false
}

func parseEntryParts(content json.RawMessage) []entryPart {
	if len(content) == 0 {
		return nil
	}
	var parts []entryPart
	if err := json.Unmarshal(content, &parts); err != nil {
		return nil
	}
	return parts
}

// toolCallMarker names the call and carries its arguments: for write/exec-style
// tools the result is often just a success marker, so the arguments are what
// tell the summarizer what was actually done. Arguments get the same media
// scrub and length bound as tool results.
func toolCallMarker(name string, input json.RawMessage) string {
	name = strings.TrimSpace(name)
	marker := "[tool_call]"
	if name != "" {
		marker = "[tool_call: " + name + "]"
	}
	args := strings.TrimSpace(string(input))
	if args == "" || args == "null" || args == "{}" {
		return marker
	}
	return marker + " " + sanitizeToolText(args)
}

// renderToolResult surfaces a tool result's outcome for the summarizer: a clean
// string when one can be extracted, otherwise a media-scrubbed
// serialization of the raw outcome so real output (stdout, search results,
// structured data) survives. Falls back to a marker only when there is nothing.
func renderToolResult(candidates ...json.RawMessage) string {
	if s := firstOutputText(candidates...); s != "" {
		return sanitizeToolText(s)
	}
	if s := rawToolOutput(candidates...); s != "" {
		return sanitizeToolText(s)
	}
	return "[tool result]"
}

// sanitizeToolText de-medias any tool outcome text before it reaches the
// summarizer, applied uniformly to both the clean-text and raw-JSON paths so
// neither can leak base64/data-URI payloads. The text itself reaches the
// summarizer whole: a summary covers only what its model read.
func sanitizeToolText(s string) string {
	s = dataURIRe.ReplaceAllString(s, "[media]")
	return base64BlobRe.ReplaceAllString(s, "[media]")
}

// firstOutputText returns the first cleanly extractable string from a tool
// result's output or result field. It only returns recognized string shapes so
// binary or structured payloads never leak through this path.
func firstOutputText(candidates ...json.RawMessage) string {
	for _, raw := range candidates {
		if s := outputText(raw); s != "" {
			return s
		}
	}
	return ""
}

func outputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Value   string `json:"value"`
		Text    string `json:"text"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if v := strings.TrimSpace(obj.Value); v != "" {
			return v
		}
		if v := strings.TrimSpace(obj.Text); v != "" {
			return v
		}
		var texts []string
		for _, c := range obj.Content {
			if t := strings.TrimSpace(c.Text); t != "" {
				texts = append(texts, t)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return ""
}

// rawToolOutput returns the first non-empty raw outcome payload so structured
// tool results (maps, arrays) still reach the summarizer (after sanitizing)
// instead of being dropped.
func rawToolOutput(candidates ...json.RawMessage) string {
	for _, raw := range candidates {
		s := strings.TrimSpace(string(raw))
		if s == "" || s == "null" {
			continue
		}
		return s
	}
	return ""
}

const truncationMarker = " …[truncated]"

func truncateBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + truncationMarker
}

// toolResultRefs names each tool result among items, in order, with the ref
// get_messages reads it back by. Rows that are not stored history have none.
func toolResultRefs(items []CompactionCandidate) []string {
	var refs []string
	for _, item := range items {
		if item.Record.SourceKind != historyfrag.SourceDBMessage {
			continue
		}
		for i, part := range historyfrag.StoredModelMessageToSDKMessage(item.Record.ModelMessage).Content {
			if result, ok := part.(sdk.ToolResultPart); ok {
				refs = append(refs, cleanEntryMetadataValue(result.ToolName)+" "+result.ToolCallID+" ref="+item.Record.Ref.ID+":"+strconv.Itoa(i))
			}
		}
	}
	return refs
}
