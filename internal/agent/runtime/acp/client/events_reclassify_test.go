package client

import (
	"testing"

	acp "github.com/coder/acp-go-sdk"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
)

// TestEditToolWithWriteTitleReclassifiesConsistently locks the edit->write name
// fix: a write-titled edit must surface as "write" in BOTH the approval path
// and the streamed tool events. Both resolve through nativeToolFromACPState, so
// one action can never show two names (the prior bug streamed "edit" while the
// approval said "write").
func TestEditToolWithWriteTitleReclassifiesConsistently(t *testing.T) {
	writeTitled := &acpToolState{
		id:    "tc-1",
		kind:  string(acp.ToolKindEdit),
		title: "Write config.yaml",
		input: map[string]any{
			"file_path":  "config.yaml",
			"old_string": "a",
			"new_string": "b",
		},
	}
	if name, _, ok := nativeToolFromACPState(writeTitled, acpprofile.DefaultToolQuirks()); !ok || name != "write" {
		t.Fatalf("nativeToolFromACPState name=%q ok=%v, want write/true", name, ok)
	}
	// The streamed tool event must agree with the approval name.
	events := newACPToolEventMapper(acpprofile.DefaultToolQuirks()).eventsForState(writeTitled)
	if len(events) == 0 || events[0].ToolName != "write" {
		t.Fatalf("eventsForState first event = %+v, want ToolName=write", events)
	}

	// A plain edit (no write/create/new-file title) must stay "edit" - the
	// reclassification must not fire on every edit.
	plainEdit := &acpToolState{
		id:    "tc-2",
		kind:  string(acp.ToolKindEdit),
		title: "Edit config.yaml",
		input: map[string]any{
			"file_path":  "config.yaml",
			"old_string": "a",
			"new_string": "b",
		},
	}
	if name, _, ok := nativeToolFromACPState(plainEdit, acpprofile.DefaultToolQuirks()); !ok || name != "edit" {
		t.Fatalf("plain edit name=%q ok=%v, want edit/true", name, ok)
	}
}

// TestPerAgentQuirksReachToolMapping proves the profile seam works
// end-to-end: an agent profile that overrides its title heuristics changes
// how BOTH the event mapper and the permission mapper classify a tool call,
// without touching shared mapping code. When an agent update changes its
// wording, the fix belongs in that agent's profile quirks.
func TestPerAgentQuirksReachToolMapping(t *testing.T) {
	state := func() *acpToolState {
		return &acpToolState{
			id:    "tc-quirk",
			kind:  string(acp.ToolKindEdit),
			title: "Replace config.yaml",
			input: map[string]any{
				"file_path":  "config.yaml",
				"old_string": "a",
				"new_string": "b",
			},
		}
	}

	// Default quirks: "Replace" is not a write keyword -> stays edit.
	if name, _, _ := nativeToolFromACPState(state(), acpprofile.DefaultToolQuirks()); name != "edit" {
		t.Fatalf("default quirks name = %q, want edit", name)
	}

	custom := acpprofile.ToolQuirks{WriteTitleKeywords: []string{"replace"}}
	if name, _, _ := nativeToolFromACPState(state(), custom); name != "write" {
		t.Fatalf("custom quirks name = %q, want write", name)
	}
	// The same quirks drive the streamed events through the mapper.
	events := newACPToolEventMapper(custom).eventsForState(state())
	if len(events) == 0 || events[0].ToolName != "write" {
		t.Fatalf("mapper with custom quirks first event = %+v, want ToolName=write", events)
	}
	// And the permission path follows the same per-agent quirks.
	if _, name, _, ok := permissionNativeToolState(state(), custom); !ok || name != "write" {
		t.Fatalf("permissionNativeToolState with custom quirks = %q/%v, want write/true", name, ok)
	}
}
