package claudecode

import (
	"encoding/json"
	"strings"

	"github.com/felinics/memoh/internal/agent/toolresult"
)

// Claude Code's native tools Bash, Read, WebFetch and WebSearch are reported
// as exec, read, web_fetch and web_search, so their results are translated to
// those tools' formats. The structured tool_use_result on the user line is the
// source; when it is absent or not an object, the text of the tool_result
// content block is used.

// webFetchProvider names Claude Code as the fetcher in a web_fetch result.
const (
	webFetchProvider     = "claude_code"
	webFetchProviderName = "Claude Code"
)

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
		return toolresult.NewFailure(text).Map(), true
	}
	var object map[string]any
	if err := json.Unmarshal(structured, &object); err != nil {
		object = nil
	}
	switch toolName {
	case "exec":
		return execResult(object, text).Map(), false
	case "read":
		return readResult(object, text).Map(), false
	case "web_fetch":
		return webFetchResult(object, input, text).Map(), false
	default:
		return webSearchResult(object, input).Map(), false
	}
}

func execResult(object map[string]any, text string) toolresult.Exec {
	if object == nil {
		return toolresult.Exec{Stdout: text}
	}
	return toolresult.Exec{Stdout: stringField(object, "stdout"), Stderr: stringField(object, "stderr")}
}

func readResult(object map[string]any, text string) toolresult.Read {
	if file, ok := object["file"].(map[string]any); ok {
		if content, ok := file["content"].(string); ok {
			total := toolresult.CountLines(content)
			if n, ok := file["totalLines"].(float64); ok {
				total = int(n)
			}
			return toolresult.Read{Content: content, TotalLines: total}
		}
	}
	return toolresult.Read{Content: text, TotalLines: toolresult.CountLines(text)}
}

func webFetchResult(object map[string]any, input any, text string) toolresult.WebFetch {
	out := toolresult.WebFetch{
		Success: true, URL: stringField(inputObject(input), "url"),
		Provider: webFetchProvider, ProviderName: webFetchProviderName,
		Format: "text", Content: toolresult.Ptr(text),
	}
	if object == nil {
		return out
	}
	if url := stringField(object, "url"); url != "" {
		out.URL = url
	}
	if result, ok := object["result"].(string); ok {
		out.Content = &result
	}
	if code, ok := object["code"].(float64); ok {
		out.Success = code >= 200 && code < 300
	}
	return out
}

// webSearchResult reads the hits of the structured result: the search hit
// groups carry title and url; the model's own commentary between them is not a
// hit. Without a structured result only the query is known.
func webSearchResult(object map[string]any, input any) toolresult.WebSearch {
	out := toolresult.WebSearch{Query: stringField(inputObject(input), "query")}
	if object == nil {
		return out
	}
	if query := stringField(object, "query"); query != "" {
		out.Query = query
	}
	groups, _ := object["results"].([]any)
	for _, group := range groups {
		fields, _ := group.(map[string]any)
		hits, _ := fields["content"].([]any)
		for _, hit := range hits {
			fields, _ := hit.(map[string]any)
			if url := stringField(fields, "url"); url != "" {
				out.Results = append(out.Results, toolresult.WebSearchItem{Title: stringField(fields, "title"), URL: url})
			}
		}
	}
	return out
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
