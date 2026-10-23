// Package conformance checks that an external runtime reports the native
// tools' result formats, and pins the translated results as golden files the
// web client tests render.
//
// Each runtime's adapter test feeds recorded protocol samples through the
// adapter and passes the events it emitted to Record. Record fails the test
// when a call labelled with a native tool name carries a result that does not
// decode into that tool's record, then compares the results with
// testdata/golden/<kind>.json (rewritten by -update-golden).
package conformance

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/toolresult"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite internal/agent/toolresult/testdata/golden from the recorded samples")

// Entry is one finished call under a native tool name.
type Entry struct {
	Tool   string `json:"tool"`
	Input  any    `json:"input,omitempty"`
	Result any    `json:"result"`
	Status string `json:"status,omitempty"`
}

// Options relax the checks for samples that legitimately differ.
type Options struct {
	// EmptyOutput lists tool call ids whose result may carry no output.
	EmptyOutput []string
	// AnyOutput skips the output check, for entries re-read from a golden
	// without their call ids.
	AnyOutput bool
	// Extras allows result fields beyond the native record (ACP keeps what
	// the agent reported).
	Extras bool
}

// GoldenDir is the directory of the golden files.
func GoldenDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata", "golden")
}

// Entries collects the finished calls under native tool names, pairing each
// with the input announced when it started.
func Entries(events []event.StreamEvent) (entries []Entry, ids []string) {
	inputs := map[string]any{}
	for _, ev := range events {
		switch ev.Type {
		case event.ToolCallStart:
			inputs[ev.ToolCallID] = ev.Input
		case event.ToolCallEnd:
			if !isNative(ev.ToolName) {
				continue
			}
			input := ev.Input
			if input == nil {
				input = inputs[ev.ToolCallID]
			}
			entries = append(entries, Entry{Tool: ev.ToolName, Input: input, Result: ev.Result, Status: ev.Status})
			ids = append(ids, ev.ToolCallID)
		}
	}
	return entries, ids
}

func isNative(tool string) bool {
	switch tool {
	case "exec", "read", "web_fetch", "web_search", "apply_patch":
		return true
	}
	return false
}

// Check reports every entry whose result is not its tool's record.
func Check(t *testing.T, entries []Entry, ids []string, opts Options) {
	t.Helper()
	for i, entry := range entries {
		id := ""
		if i < len(ids) {
			id = ids[i]
		}
		emptyOK := opts.AnyOutput
		for _, want := range opts.EmptyOutput {
			emptyOK = emptyOK || want == id
		}
		if msg := checkEntry(entry, emptyOK, opts.Extras); msg != "" {
			t.Errorf("%s call %s: %s\nresult: %#v", entry.Tool, id, msg, entry.Result)
		}
	}
}

func checkEntry(entry Entry, emptyOK, extras bool) string {
	decode := func(v any, into any) error { return decodeInto(v, into, !extras) }
	if entry.Status == "failed" {
		var failure toolresult.Failure
		if err := decode(entry.Result, &failure); err != nil {
			return "a failed call must carry a failure record: " + err.Error()
		}
		if !failure.IsError || failure.Text() == "" {
			return "a failed call must carry isError and an explanation"
		}
		return ""
	}
	if missing := missingFields(entry); missing != "" {
		return "result lacks the native field " + missing
	}
	empty := ""
	switch entry.Tool {
	case "exec":
		var r toolresult.Exec
		if err := decode(entry.Result, &r); err != nil {
			return err.Error()
		}
		if r.Stdout == "" && r.Stderr == "" {
			empty = "output"
		}
	case "read":
		var r toolresult.Read
		if err := decode(entry.Result, &r); err != nil {
			return err.Error()
		}
		if r.Content == "" {
			empty = "content"
		}
	case "web_fetch":
		var r toolresult.WebFetch
		if err := decode(entry.Result, &r); err != nil {
			return err.Error()
		}
		if r.Content == nil || *r.Content == "" {
			empty = "content"
		}
	case "web_search":
		var r toolresult.WebSearch
		if err := decode(entry.Result, &r); err != nil {
			return err.Error()
		}
		if len(r.Results) == 0 {
			empty = "results"
		}
	case "apply_patch":
		var r toolresult.ApplyPatch
		if err := decode(entry.Result, &r); err != nil {
			return err.Error()
		}
		if len(r.Files) == 0 {
			empty = "files"
		}
	}
	if empty != "" && !emptyOK {
		return "no " + empty + " in a sample that has output"
	}
	return ""
}

// requiredFields are the fields a successful result of each native tool always
// carries (exec's exit_code is present only when the runtime reported one). Decoding alone cannot tell a result in another shape from an empty
// one, because unknown fields are ignored.
var requiredFields = map[string][]string{
	"exec":        {"stdout", "stderr"},
	"read":        {"content", "total_lines"},
	"web_fetch":   {"success", "url", "format", "content"},
	"web_search":  {"query", "results"},
	"apply_patch": {"ok", "summary", "files"},
}

func missingFields(entry Entry) string {
	fields, _ := entry.Result.(map[string]any)
	for _, name := range requiredFields[entry.Tool] {
		if _, ok := fields[name]; !ok {
			return name
		}
	}
	return ""
}

func decodeInto(v, into any, strict bool) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	return dec.Decode(into)
}

// Record checks the events of one runtime and pins their native-name results
// to the runtime's golden file.
func Record(t *testing.T, kind string, events []event.StreamEvent, opts Options) {
	t.Helper()
	entries, ids := Entries(events)
	if len(entries) == 0 {
		t.Fatalf("%s samples produced no call under a native tool name", kind)
	}
	Check(t, entries, ids, opts)

	path := filepath.Join(GoldenDir(), kind+".json")
	got, err := json.MarshalIndent(roundTrip(entries), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed test fixture path
	if err != nil {
		t.Fatalf("read %s: %v (run with -update-golden to create it)", path, err)
	}
	var gotAny, wantAny any
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantAny); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		t.Errorf("translated results differ from %s (run with -update-golden to accept):\n%s", path, got)
	}
}

// roundTrip normalizes entries to their JSON value so numbers and slices
// compare the way the golden stores them.
func roundTrip(entries []Entry) []Entry {
	raw, err := json.Marshal(entries)
	if err != nil {
		return entries
	}
	var out []Entry
	if err := json.Unmarshal(raw, &out); err != nil {
		return entries
	}
	return out
}
