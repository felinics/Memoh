package claudecode

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/toolresult"
	"github.com/felinics/memoh/internal/agent/toolresult/conformance"
	"github.com/felinics/memoh/internal/runtimekind"
)

// protocolref/tool-results.json holds one call per tool as the pinned CLI
// sent it, tool_use_result included.
func claudeToolSamples(t *testing.T) map[string][]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("protocolref/tool-results.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples map[string][]json.RawMessage
	if err := json.Unmarshal(raw, &samples); err != nil {
		t.Fatal(err)
	}
	return samples
}

func claudeSampleEvents(t *testing.T, samples map[string][]json.RawMessage, names ...string) []event.StreamEvent {
	t.Helper()
	sink := &recordingSink{}
	r := newTestRunner(sink)
	for _, name := range names {
		lines := samples[name]
		if len(lines) == 0 {
			t.Fatalf("no recorded %s sample", name)
		}
		for _, line := range lines {
			feed(t, r, string(line))
		}
	}
	return sink.snapshot()
}

func TestClaudeToolResultsAreNativeFormat(t *testing.T) {
	samples := claudeToolSamples(t)
	events := claudeSampleEvents(t, samples, "bashOk", "bashFail", "read", "webFetch", "webSearch")
	conformance.Record(t, string(runtimekind.ClaudeCode), events, conformance.Options{})
}

func endOf(t *testing.T, events []event.StreamEvent, tool string) event.StreamEvent {
	t.Helper()
	for _, ev := range events {
		if ev.Type == event.ToolCallEnd && ev.ToolName == tool {
			return ev
		}
	}
	t.Fatalf("no %s end among %#v", tool, events)
	return event.StreamEvent{}
}

func TestClaudeBashResultSplitsStdoutAndStderr(t *testing.T) {
	events := claudeSampleEvents(t, claudeToolSamples(t), "bashOk")
	end := endOf(t, events, "exec")
	if end.Status == "failed" {
		t.Fatalf("bash success marked failed: %#v", end)
	}
	got, err := toolresult.DecodeStrict[toolresult.Exec](end.Result)
	if err != nil || got.Stdout != "out\nerr" || got.ExitCode != nil {
		t.Fatalf("result = %#v, %v", end.Result, err)
	}
}

// A failed Bash call has no structured exit code; its text is the failure.
func TestClaudeFailedBashCarriesTextWithoutExitCode(t *testing.T) {
	events := claudeSampleEvents(t, claudeToolSamples(t), "bashFail")
	end := endOf(t, events, "exec")
	if end.Status != "failed" {
		t.Fatalf("failed bash not marked failed: %#v", end)
	}
	failure, err := toolresult.DecodeStrict[toolresult.Failure](end.Result)
	if err != nil || !strings.Contains(failure.Text(), "Exit code 3") || !strings.Contains(failure.Text(), "out\nerr") {
		t.Fatalf("result = %#v, %v", end.Result, err)
	}
}

// Read content comes from the structured file, without the line-number prefix
// the text form carries.
func TestClaudeReadResultHasNoLineNumbers(t *testing.T) {
	events := claudeSampleEvents(t, claudeToolSamples(t), "read")
	got, err := toolresult.DecodeStrict[toolresult.Read](endOf(t, events, "read").Result)
	if err != nil || got.Content != "hello file\nline2\n" || got.TotalLines != 3 {
		t.Fatalf("result = %#v, %v", got, err)
	}
}

func TestClaudeWebToolsResults(t *testing.T) {
	samples := claudeToolSamples(t)
	events := claudeSampleEvents(t, samples, "webFetch", "webSearch")

	fetched, err := toolresult.DecodeStrict[toolresult.WebFetch](endOf(t, events, "web_fetch").Result)
	if err != nil || fetched.URL != "https://example.com" || fetched.Content == nil || !strings.Contains(*fetched.Content, "Example Domain") || !fetched.Success {
		t.Fatalf("web_fetch result = %#v, %v", fetched, err)
	}

	searched, err := toolresult.DecodeStrict[toolresult.WebSearch](endOf(t, events, "web_search").Result)
	if err != nil || searched.Query != "example domain iana" || len(searched.Results) != 9 || searched.Results[0].URL != "https://www.iana.org/help/example-domains" {
		t.Fatalf("web_search result = %#v, %v", searched, err)
	}
}

// Without a structured result the text content stands in.
func TestClaudeToolResultFallsBackToText(t *testing.T) {
	sink := &recordingSink{}
	r := newTestRunner(sink)
	feed(t, r, `{"type":"assistant","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_b","name":"Bash","input":{"command":"echo hi"}},{"type":"tool_use","id":"tu_r","name":"Read","input":{"file_path":"/data/a.txt"}}]}}`)
	feed(t, r, `{"type":"user","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_b","content":"hi\n","is_error":false},{"type":"tool_result","tool_use_id":"tu_r","content":[{"type":"text","text":"1\tx"}]}]},"tool_use_result":"not an object"}`)
	events := sink.snapshot()
	exec, err := toolresult.DecodeStrict[toolresult.Exec](endOf(t, events, "exec").Result)
	if err != nil || exec.Stdout != "hi\n" {
		t.Fatalf("exec = %#v, %v", exec, err)
	}
	read, err := toolresult.DecodeStrict[toolresult.Read](endOf(t, events, "read").Result)
	if err != nil || read.Content != "1\tx" {
		t.Fatalf("read = %#v, %v", read, err)
	}
}

// tool_use_result belongs to the whole user line, so a line with two results
// gives each its own content, and a finished call is forgotten.
func TestClaudeStructuredResultIsNotSharedAcrossResultsOnALine(t *testing.T) {
	sink := &recordingSink{}
	r := newTestRunner(sink)
	feed(t, r, `{"type":"assistant","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_a","name":"Bash","input":{"command":"echo a"}},{"type":"tool_use","id":"tu_b","name":"Bash","input":{"command":"echo b"}}]}}`)
	feed(t, r, `{"type":"user","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_a","content":"a\n"},{"type":"tool_result","tool_use_id":"tu_b","content":"b\n"}]},"tool_use_result":{"stdout":"shared","stderr":""}}`)
	var stdouts []string
	for _, ev := range sink.snapshot() {
		if ev.Type != event.ToolCallEnd {
			continue
		}
		exec, err := toolresult.DecodeStrict[toolresult.Exec](ev.Result)
		if err != nil {
			t.Fatal(err)
		}
		stdouts = append(stdouts, exec.Stdout)
	}
	if len(stdouts) != 2 || stdouts[0] != "a\n" || stdouts[1] != "b\n" {
		t.Fatalf("stdouts = %q, want each result's own text", stdouts)
	}
	r.mu.Lock()
	remaining := len(r.toolCalls)
	r.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d finished calls still remembered", remaining)
	}
}
