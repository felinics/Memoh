package claudecode

import (
	"encoding/json"
	"strings"
)

// Claude Code's native tools Bash, Read, WebFetch and WebSearch are reported
// as exec, read, web_fetch and web_search, so their results are translated to
// those tools' formats. The structured tool_use_result on the user line is the
// source; when it is absent or not an object, the text of the tool_result
// content block is used.

// translateToolResult returns the result of one finished call and whether the
// call failed. Tools without a native counterpart keep their content as sent.
func translateToolResult(toolName string, input any, content, structured json.RawMessage, isError bool) (any, bool) {
	switch toolName {
	case "exec", "read", "web_fetch", "web_search":
	default:
		var raw any
		if len(content) > 0 {
			_ = json.Unmarshal(content, &raw)
		}
		return raw, isError
	}
	text := contentText(content)
	if isError {
		// A failed Bash call has no structured exit code and the text is not
		// parsed for one.
		return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": text}}}, true
	}
	var object map[string]any
	if err := json.Unmarshal(structured, &object); err != nil {
		object = nil
	}
	switch toolName {
	case "exec":
		return execResult(object, text), false
	case "read":
		return readResult(object, text), false
	case "web_fetch":
		return webFetchResult(object, input, text), false
	default:
		return webSearchResult(object, input, text), false
	}
}

func execResult(object map[string]any, text string) map[string]any {
	stdout, hasStdout := object["stdout"].(string)
	stderr, hasStderr := object["stderr"].(string)
	if !hasStdout && !hasStderr {
		stdout = text
	}
	return map[string]any{"stdout": stdout, "stderr": stderr}
}

func readResult(object map[string]any, text string) map[string]any {
	if file, ok := object["file"].(map[string]any); ok {
		if content, ok := file["content"].(string); ok {
			text = content
		}
	}
	return map[string]any{"content": text}
}

func webFetchResult(object map[string]any, input any, text string) map[string]any {
	url := stringField(object, "url")
	if url == "" {
		url = stringField(inputObject(input), "url")
	}
	if result, ok := object["result"].(string); ok {
		text = result
	}
	success := true
	if code, ok := object["code"].(float64); ok {
		success = code >= 200 && code < 300
	}
	return map[string]any{"success": success, "url": url, "format": "text", "content": text}
}

func webSearchResult(object map[string]any, input any, text string) map[string]any {
	query := stringField(object, "query")
	if query == "" {
		query = stringField(inputObject(input), "query")
	}
	results := []map[string]any{}
	groups, _ := object["results"].([]any)
	for _, group := range groups {
		fields, _ := group.(map[string]any)
		hits, _ := fields["content"].([]any)
		for _, hit := range hits {
			fields, _ := hit.(map[string]any)
			if url := stringField(fields, "url"); url != "" {
				results = append(results, map[string]any{"title": stringField(fields, "title"), "url": url})
			}
		}
	}
	// Keep the tool's text when its search-hit schema is unavailable.
	return map[string]any{"query": query, "results": results, "content": text}
}

func inputObject(input any) map[string]any {
	object, _ := input.(map[string]any)
	return object
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

// contentText is the text of a tool_result content: a string, or a list of
// content blocks whose text blocks are joined.
func contentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
