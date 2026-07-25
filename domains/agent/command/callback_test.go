package command

import (
	"strings"
	"testing"
)

func TestEncodeDecodeListCallbackRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		resource string
		action   string
		args     []string
		page     int
	}{
		{"no args page 0", "mcp", "list", nil, 0},
		{"no args page 3", "schedule", "list", nil, 3},
		{"single arg", "model", "list", []string{"openrouter"}, 2},
		{"multi arg", "model", "list", []string{"open", "router"}, 5},
		{"space-bearing arg", "mcp", "get", []string{"My Server"}, 0},
		{"two args one with space", "settings", "update", []string{"--acl_default_effect", "deny all"}, 1},
		{"high page", "memory", "list", nil, 9999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := EncodeListCallback(tt.resource, tt.action, tt.args, tt.page)
			if len(data) > telegramCallbackLimit {
				t.Fatalf("callback_data %q exceeds %d bytes (%d)", data, telegramCallbackLimit, len(data))
			}
			parsed, ok := DecodeCallback(data)
			if !ok {
				t.Fatalf("DecodeCallback(%q) returned ok=false", data)
			}
			if parsed.Kind != callbackKindListPage {
				t.Errorf("Kind = %q, want %q", parsed.Kind, callbackKindListPage)
			}
			if parsed.Resource != tt.resource {
				t.Errorf("Resource = %q, want %q", parsed.Resource, tt.resource)
			}
			if parsed.Action != tt.action {
				t.Errorf("Action = %q, want %q", parsed.Action, tt.action)
			}
			if parsed.Page != tt.page {
				t.Errorf("Page = %d, want %d", parsed.Page, tt.page)
			}
			if strings.Join(parsed.Args, " ") != strings.Join(tt.args, " ") {
				t.Errorf("Args = %v, want %v", parsed.Args, tt.args)
			}
		})
	}
}

func TestEncodeListCallbackLongArgsUsesToken(t *testing.T) {
	longArg := strings.Repeat("very-long-provider-name-", 5) // ~120 chars
	data := EncodeListCallback("model", "list", []string{longArg}, 1)
	if len(data) > telegramCallbackLimit {
		t.Fatalf("callback_data %q exceeds %d bytes (%d)", data, telegramCallbackLimit, len(data))
	}
	if !strings.Contains(data, "~#") {
		t.Fatalf("expected stashed token form (~#...), got %q", data)
	}
	parsed, ok := DecodeCallback(data)
	if !ok {
		t.Fatalf("DecodeCallback(%q) returned ok=false", data)
	}
	if strings.Join(parsed.Args, " ") != longArg {
		t.Errorf("Args = %v, want [%q]", parsed.Args, longArg)
	}
}

// TestEncodeListCallbackLongSpaceBearingArgsUsesToken guards the stash path:
// even when an arg is long enough to be stashed under a token, its internal
// spaces must survive as a single arg (not split on decode).
func TestEncodeListCallbackLongSpaceBearingArgsUsesToken(t *testing.T) {
	longName := strings.Repeat("My Long Server Name ", 5) // spaces + >64 bytes
	longName = strings.TrimSpace(longName)
	data := EncodeListCallback("mcp", "get", []string{longName}, 0)
	if len(data) > telegramCallbackLimit {
		t.Fatalf("callback_data %q exceeds %d bytes (%d)", data, telegramCallbackLimit, len(data))
	}
	if !strings.Contains(data, "~#") {
		t.Fatalf("expected stashed token form (~#...), got %q", data)
	}
	parsed, ok := DecodeCallback(data)
	if !ok {
		t.Fatalf("DecodeCallback(%q) returned ok=false", data)
	}
	if len(parsed.Args) != 1 || parsed.Args[0] != longName {
		t.Errorf("Args = %v, want [%q]", parsed.Args, longName)
	}
}

func TestDecodeArgsTokenMiss(t *testing.T) {
	// A token that was never stashed should decode to nil (treated as unfiltered).
	if got := decodeArgsToken("#deadbeef"); got != nil {
		t.Errorf("decodeArgsToken(miss) = %v, want nil", got)
	}
}

// TestCallbackEncodersStayWithinLimit is a coverage gate for Telegram's 64-byte
// callback_data ceiling. EncodeListCallback stashes oversized args (so its base
// is the only concern), but EncodeRangeCallback and EncodeConfirmNewCallback
// have NO stash fallback — a future long resource/action/range key would
// silently breach the limit and Telegram would reject the button with no error
// surfaced to the user. Pin the invariant against the real command surface.
func TestCallbackEncodersStayWithinLimit(t *testing.T) {
	resources := make([]string, 0)
	for _, m := range MenuCommands(nil) {
		resources = append(resources, m.Command)
	}
	// Actions seen on interactive surfaces (drill-down, row taps, ranges).
	actions := []string{"list", "get", "set", "update", "show", "bindings", "providers", "outbox", "language"}
	for _, r := range resources {
		for _, a := range actions {
			// List-page base with worst-case page; a long arg would stash, so the
			// base ("m~lp~{resource}~{action}~{page}~") is what must fit.
			if got := EncodeListCallback(r, a, nil, 99999); len(got) > telegramCallbackLimit {
				t.Errorf("EncodeListCallback(%q,%q) base = %d bytes > %d", r, a, len(got), telegramCallbackLimit)
			}
		}
	}
	// Range presets actually emitted by /usage (no stash fallback).
	for _, key := range []string{"24h", "7d", "30d", "90d", "all", "by-model", "summary"} {
		if got := EncodeRangeCallback("usage", "summary", key); len(got) > telegramCallbackLimit {
			t.Errorf("EncodeRangeCallback range=%q = %d bytes > %d", key, len(got), telegramCallbackLimit)
		}
	}
	for _, mode := range []string{"chat", "discuss"} {
		if got := EncodeConfirmNewCallback(mode); len(got) > telegramCallbackLimit {
			t.Errorf("EncodeConfirmNewCallback(%q) = %d bytes > %d", mode, len(got), telegramCallbackLimit)
		}
	}
}

func TestDecodeCallbackDismissAndNoop(t *testing.T) {
	if p, ok := DecodeCallback(DismissCallback()); !ok || p.Kind != callbackKindDismiss {
		t.Errorf("dismiss decode = %+v ok=%v", p, ok)
	}
	if p, ok := DecodeCallback(NoopCallback()); !ok || p.Kind != callbackKindNoop {
		t.Errorf("noop decode = %+v ok=%v", p, ok)
	}
}

func TestDecodeCallbackRejectsNonInteractive(t *testing.T) {
	for _, data := range []string{"approve:abc123", "reject:xyz", "random", ""} {
		if _, ok := DecodeCallback(data); ok {
			t.Errorf("DecodeCallback(%q) ok=true, want false", data)
		}
		if IsInteractiveCallback(data) {
			t.Errorf("IsInteractiveCallback(%q) = true, want false", data)
		}
	}
}

func TestSyntheticCommand(t *testing.T) {
	p := ParsedCallback{Kind: callbackKindListPage, Resource: "model", Action: "list", Args: []string{"openrouter"}, Page: 2}
	if got, want := p.SyntheticCommand(), "/model list openrouter --page 2"; got != want {
		t.Errorf("SyntheticCommand = %q, want %q", got, want)
	}
	noArgs := ParsedCallback{Kind: callbackKindListPage, Resource: "mcp", Action: "list", Page: 0}
	if got, want := noArgs.SyntheticCommand(), "/mcp list --page 0"; got != want {
		t.Errorf("SyntheticCommand = %q, want %q", got, want)
	}
	if got := (ParsedCallback{Kind: callbackKindDismiss}).SyntheticCommand(); got != "" {
		t.Errorf("dismiss SyntheticCommand = %q, want empty", got)
	}
}

// TestCallbackToCommandRoundTrip validates the full pagination round-trip:
// encode a button -> decode the tap -> build a synthetic command -> Parse it
// back into the same resource/action/args/page the renderer started from.
func TestCallbackToCommandRoundTrip(t *testing.T) {
	tests := []struct {
		resource, action string
		args             []string
		page             int
	}{
		{"mcp", "list", nil, 1},
		{"model", "list", []string{"openrouter"}, 4},
		{"schedule", "list", nil, 0},
		// A space-bearing name (MCP connection, schedule, memory/search target)
		// must survive encode -> decode -> synthetic command -> re-Parse as ONE
		// arg. Regression guard for the row-tap bug where "My Server" split into
		// ["My","Server"] and the handler read only "My".
		{"mcp", "get", []string{"My Server"}, 0},
		{"memory", "set", []string{"my provider"}, 2},
	}
	for _, tt := range tests {
		data := EncodeListCallback(tt.resource, tt.action, tt.args, tt.page)
		parsed, ok := DecodeCallback(data)
		if !ok {
			t.Fatalf("DecodeCallback(%q) ok=false", data)
		}
		cmd := parsed.SyntheticCommand()
		reparsed, err := Parse(cmd)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", cmd, err)
		}
		if reparsed.Resource != tt.resource || reparsed.Action != tt.action || reparsed.Page != tt.page {
			t.Errorf("round-trip %q -> %+v, want resource=%s action=%s page=%d",
				cmd, reparsed, tt.resource, tt.action, tt.page)
		}
		if strings.Join(reparsed.Args, " ") != strings.Join(tt.args, " ") {
			t.Errorf("round-trip args = %v, want %v", reparsed.Args, tt.args)
		}
	}
}

func TestModelProviderCallbackRoundTrip(t *testing.T) {
	for _, tc := range []struct{ prov, page int }{{0, 0}, {3, 2}, {12, 9}} {
		data := EncodeModelProviderCallback(tc.prov, tc.page)
		if len(data) > telegramCallbackLimit {
			t.Fatalf("callback %q exceeds limit", data)
		}
		parsed, ok := DecodeCallback(data)
		if !ok || parsed.Kind != callbackKindModelProvider {
			t.Fatalf("decode %q -> %+v ok=%v", data, parsed, ok)
		}
		if parsed.ProviderIndex != tc.prov || parsed.Page != tc.page {
			t.Errorf("decoded prov=%d page=%d, want %d/%d", parsed.ProviderIndex, parsed.Page, tc.prov, tc.page)
		}
		reparsed, err := Parse(parsed.SyntheticCommand())
		if err != nil {
			t.Fatalf("Parse(%q): %v", parsed.SyntheticCommand(), err)
		}
		if reparsed.Resource != "model" || reparsed.Action != "list" || reparsed.Prov != tc.prov || reparsed.Page != tc.page {
			t.Errorf("synthetic re-parse = %+v, want model/list prov=%d page=%d", reparsed, tc.prov, tc.page)
		}
	}
}

func TestModelSelectCallbackRoundTrip(t *testing.T) {
	const modelID = "1f2e3d4c-5b6a-7980-1234-56789abcdef0" // stable UUID, not a flat index
	data := EncodeModelSelectCallback(modelID)
	if len(data) > telegramCallbackLimit {
		t.Fatalf("callback %q exceeds %d-byte limit (%d)", data, telegramCallbackLimit, len(data))
	}
	parsed, ok := DecodeCallback(data)
	if !ok || parsed.Kind != callbackKindModelSelect || parsed.SelectID != modelID {
		t.Fatalf("decode %q -> %+v ok=%v", data, parsed, ok)
	}
	reparsed, err := Parse(parsed.SyntheticCommand())
	if err != nil {
		t.Fatalf("Parse(%q): %v", parsed.SyntheticCommand(), err)
	}
	if reparsed.Resource != "model" || reparsed.Action != "set" || reparsed.SelectID != modelID {
		t.Errorf("synthetic re-parse = %+v, want model/set id=%s", reparsed, modelID)
	}
}

func TestRangeCallbackRoundTrip(t *testing.T) {
	for _, tc := range []struct{ action, key string }{
		{"summary", "30d"}, {"by-model", "all"}, {"summary", "24h"},
	} {
		data := EncodeRangeCallback("usage", tc.action, tc.key)
		if len(data) > telegramCallbackLimit {
			t.Fatalf("callback %q exceeds limit", data)
		}
		parsed, ok := DecodeCallback(data)
		if !ok || parsed.Kind != callbackKindRange {
			t.Fatalf("decode %q -> %+v ok=%v", data, parsed, ok)
		}
		if parsed.Resource != "usage" || parsed.Action != tc.action || parsed.Range != tc.key {
			t.Errorf("decoded = %+v, want usage/%s/%s", parsed, tc.action, tc.key)
		}
		reparsed, err := Parse(parsed.SyntheticCommand())
		if err != nil {
			t.Fatalf("Parse(%q): %v", parsed.SyntheticCommand(), err)
		}
		if reparsed.Resource != "usage" || reparsed.Action != tc.action || reparsed.Range != tc.key {
			t.Errorf("synthetic re-parse = %+v, want usage/%s range=%s", reparsed, tc.action, tc.key)
		}
	}
}

// TestSkillActivateCallbackRoundTrip pins the tap-to-activate encoding: a
// /skill list row button decodes back to the exact canonical skill name, and
// the synthetic command is the bare "/<name>" skill slash (no --page artifact,
// no resource/action wrapper) so it re-enters the normal skill-intent pipeline.
func TestSkillActivateCallbackRoundTrip(t *testing.T) {
	name := "flutter-adding-home-screen-widgets"
	data := EncodeSkillActivateCallback(name)
	if len(data) > telegramCallbackLimit {
		t.Fatalf("callback %q = %d bytes > %d", data, len(data), telegramCallbackLimit)
	}
	if !IsInteractiveCallback(data) {
		t.Fatalf("IsInteractiveCallback(%q) = false", data)
	}
	parsed, ok := DecodeCallback(data)
	if !ok || !parsed.IsSkillActivation() {
		t.Fatalf("decode %q -> %+v ok=%v", data, parsed, ok)
	}
	if parsed.SelectID != name {
		t.Errorf("SelectID = %q, want %q", parsed.SelectID, name)
	}
	if got, want := parsed.SyntheticCommand(), "/"+name; got != want {
		t.Errorf("SyntheticCommand = %q, want %q", got, want)
	}
}

// TestSkillActivateCallbackLongNameStashes covers names that would breach
// Telegram's 64-byte callback_data ceiling: the name goes through the bounded
// stash and still decodes to the original.
func TestSkillActivateCallbackLongNameStashes(t *testing.T) {
	name := strings.Repeat("very-long-skill-", 6) + "name" // 100 bytes
	data := EncodeSkillActivateCallback(name)
	if len(data) > telegramCallbackLimit {
		t.Fatalf("stashed callback %q = %d bytes > %d", data, len(data), telegramCallbackLimit)
	}
	parsed, ok := DecodeCallback(data)
	if !ok || !parsed.IsSkillActivation() || parsed.SelectID != name {
		t.Fatalf("decode %q -> %+v ok=%v, want SelectID %q", data, parsed, ok, name)
	}
}

// TestSkillActivateCallbackStashMissInvalidates: a stale stash token (process
// restart, FIFO rollover) must invalidate the button rather than dispatch a
// broken or empty activation.
func TestSkillActivateCallbackStashMissInvalidates(t *testing.T) {
	for _, data := range []string{"m~sk~#nosuchhash", "m~sk~", "m~sk~%zz"} {
		if parsed, ok := DecodeCallback(data); ok {
			t.Errorf("DecodeCallback(%q) = %+v ok=true, want invalid", data, parsed)
		}
	}
}
