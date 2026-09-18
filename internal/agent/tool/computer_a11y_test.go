package tools

import (
	"errors"
	"strings"
	"testing"
)

// snapshotFixture mirrors the JSON emitted by crates/a11y-cli `snapshot`
// (protocol 2): lines is an array, geometry is x/y/width/height, and the
// diagnostics carry the private bus address.
const snapshotFixture = `{"ok":true,"protocol_version":2,"helper_version":"0.1.0","limit":300,"truncated":false,` +
	`"items":[{"ref":"e1","role":"frame","name":"Application Finder","x":0,"y":0,"width":1280,"height":960},` +
	`{"ref":"e2","role":"text","name":"","x":40,"y":72,"width":600,"height":34,"states":["focused","editable"]},` +
	`{"ref":"e3","role":"link","name":"Help","x":0,"y":0,"width":0,"height":0}],` +
	`"lines":["- frame \"Application Finder\" [ref=e1] @0,0 1280x960","- text [ref=e2] @40,72 600x34 (focused, editable)","- link \"Help\" [ref=e3]"],` +
	`"refs_path":"/tmp/a11y-cli-refs.json",` +
	`"diagnostics":{"apps":3,"visited":120,"accepted":3,"skipped_state":80,"skipped_role":30,"skipped_geometry":7,"errors":0,"bus_address":"unix:path=/run/user/1000/at-spi/bus_0","display":":99"}}`

func TestDecodeA11ySnapshotProtocol2(t *testing.T) {
	t.Parallel()

	var out a11ySnapshotOutput
	if err := decodeA11y([]byte(snapshotFixture), "snapshot", &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.ProtocolVersion != a11yProtocolVersion || out.HelperVersion != "0.1.0" || out.Limit != 300 {
		t.Fatalf("unexpected header: %#v", out)
	}
	if len(out.Items) != 3 || len(out.Lines) != 3 {
		t.Fatalf("expected 3 items and lines, got %d/%d", len(out.Items), len(out.Lines))
	}
	center, ok := out.Items[1].center()
	if !ok || center.X != 340 || center.Y != 89 {
		t.Fatalf("expected center 340,89 for e2, got %#v ok=%v", center, ok)
	}
	if _, ok := out.Items[2].center(); ok {
		t.Fatal("e3 has no box and must not yield a center")
	}
	if got := out.Items[1].States; len(got) != 2 || got[0] != "focused" {
		t.Fatalf("states not decoded: %#v", got)
	}
	text := out.text()
	if !strings.Contains(text, "[ref=e2] @40,72 600x34 (focused, editable)") {
		t.Fatalf("lines not joined: %q", text)
	}
	public := out.Diagnostics.public()
	if _, leaked := public["bus_address"]; leaked {
		t.Fatalf("bus address must stay out of the public diagnostics: %#v", public)
	}
	if public["accepted"] != 3 || public["skipped_geometry"] != 7 {
		t.Fatalf("unexpected public diagnostics: %#v", public)
	}
}

func TestDecodeA11yRefusesOtherProtocols(t *testing.T) {
	t.Parallel()

	legacy := `{"ok":true,"items":[],"lines":[],"refs_path":"/tmp/a11y-cli-refs.json","diagnostics":{}}`
	var out a11ySnapshotOutput
	err := decodeA11y([]byte(legacy), "snapshot", &out)
	if !errors.Is(err, errA11yHelperOutdated) {
		t.Fatalf("legacy output must be reported as an outdated helper, got %v", err)
	}
	future := `{"ok":true,"protocol_version":3,"items":[],"lines":[]}`
	err = decodeA11y([]byte(future), "snapshot", &out)
	if !errors.Is(err, errA11yHelperOutdated) || !strings.Contains(err.Error(), "helper protocol 3") {
		t.Fatalf("mismatched protocol must be refused with both versions, got %v", err)
	}
	if err := decodeA11y([]byte("not json"), "snapshot", &out); err == nil || !strings.Contains(err.Error(), "parse a11y-cli snapshot output") {
		t.Fatalf("garbage must fail to parse, got %v", err)
	}
}

func TestDecodeA11yActionAndLocate(t *testing.T) {
	t.Parallel()

	var action a11yActionOutput
	if err := decodeA11y([]byte(`{"ok":false,"protocol_version":2,"action":"click","ref":"e2","error":"no actions","fallback":{"x":340,"y":89}}`), "click", &action); err != nil {
		t.Fatalf("decode action: %v", err)
	}
	if action.OK || action.Fallback == nil || action.Fallback.X != 340 || action.Error != "no actions" {
		t.Fatalf("unexpected action output: %#v", action)
	}
	var located a11yLocateOutput
	if err := decodeA11y([]byte(`{"ok":true,"protocol_version":2,"action":"locate","ref":"e3","role":"link","name":"Help","x":0,"y":0,"width":0,"height":0}`), "locate", &located); err != nil {
		t.Fatalf("decode locate: %v", err)
	}
	if located.Center != nil {
		t.Fatalf("locate without geometry must not carry a center: %#v", located)
	}
}

func TestA11yUsageErrorDetectsOutdatedHelper(t *testing.T) {
	t.Parallel()

	if !a11yUsageError("error: unrecognized subcommand 'locate'\n\nUsage: a11y-cli <COMMAND>") {
		t.Fatal("clap subcommand rejection must be detected")
	}
	if !a11yUsageError("error: unexpected argument '--limit' found") {
		t.Fatal("clap argument rejection must be detected")
	}
	if a11yUsageError("could not connect to the accessibility bus") {
		t.Fatal("runtime failures must not be mistaken for an outdated helper")
	}
}
