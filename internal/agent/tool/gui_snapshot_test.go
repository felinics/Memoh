package tools

import (
	"strings"
	"testing"
)

func testNodes(specs ...string) []guiNode {
	nodes := make([]guiNode, 0, len(specs))
	for i, spec := range specs {
		parts := strings.SplitN(spec, "|", 2)
		key, line := parts[0], parts[1]
		nodes = append(nodes, guiNode{Key: key, Ref: "e" + string(rune('1'+i)), Line: line, Fingerprint: line})
	}
	return nodes
}

func TestPresentSnapshotFullThenIncremental(t *testing.T) {
	t.Parallel()

	state := &guiSessionState{browserSnapshots: map[string]guiSnapshotRecord{}, heldKeys: map[string]struct{}{}, baselines: map[string]*guiSnapshotBaseline{}}
	opts := guiSnapshotOptions{Limit: 300}
	first := presentSnapshot(state, "browser:t1", "", "s1", testNodes("a|- button \"A\"", "b|- link \"B\""), map[string]string{"a": "e1", "b": "e2"}, 2, false, opts)
	if first.Mode != "full" || len(first.Lines) != 2 || first.BaselineReason == "" {
		t.Fatalf("first snapshot must be full: %#v", first)
	}
	second := presentSnapshot(state, "browser:t1", "", "s2", testNodes("a|- button \"A\"", "c|- textbox \"C\""), map[string]string{"a": "e1", "c": "e3"}, 3, false, opts)
	if second.Mode != "incremental" || second.Added != 1 || second.Updated != 0 || second.Unchanged != 1 {
		t.Fatalf("second snapshot must be incremental with one added: %#v", second)
	}
	if len(second.RemovedRefs) != 1 || second.RemovedRefs[0] != "e2" {
		t.Fatalf("removed refs: %#v", second.RemovedRefs)
	}
	if len(second.Lines) != 1 || !strings.HasPrefix(second.Lines[0], "+ textbox") {
		t.Fatalf("incremental lines: %#v", second.Lines)
	}
	third := presentSnapshot(state, "browser:t1", "", "s3", testNodes("a|- button \"A\" (disabled)", "c|- textbox \"C\""), map[string]string{"a": "e1", "c": "e3"}, 3, false, opts)
	if third.Updated != 1 || len(third.Lines) != 1 || !strings.HasPrefix(third.Lines[0], "~ button") {
		t.Fatalf("changed node must be reported as updated: %#v", third)
	}
	scoped := presentSnapshot(state, "browser:t1", "e1", "s4", testNodes("a|- button \"A\""), nil, 0, false, opts)
	if scoped.Mode != "full" || !strings.Contains(scoped.BaselineReason, "subtree") {
		t.Fatalf("first observation of a subtree must return a full listing: %#v", scoped)
	}
	scopedAgain := presentSnapshot(state, "browser:t1", "e1", "s5", testNodes("a|- button \"A\" (disabled)"), nil, 0, false, opts)
	if scopedAgain.Mode != "incremental" || scopedAgain.Updated != 1 {
		t.Fatalf("second observation of the same subtree must diff against the first: %#v", scopedAgain)
	}
	// The subtree observations never touched the whole-tab baseline.
	whole := presentSnapshot(state, "browser:t1", "", "s6", testNodes("a|- button \"A\" (disabled)", "c|- textbox \"C\""), map[string]string{"a": "e1", "c": "e3"}, 3, false, opts)
	if whole.Mode != "incremental" || whole.Added != 0 || whole.Updated != 0 || whole.Unchanged != 2 {
		t.Fatalf("whole-target snapshot after a subtree one must diff against the last whole-target one: %#v", whole)
	}
	disabled := presentSnapshot(state, "browser:t1", "e1", "s7", testNodes("a|- button \"A\""), nil, 0, false, guiSnapshotOptions{Limit: 300, DisableDiffing: true})
	if disabled.Mode != "full" || disabled.BaselineReason != "diffing disabled" {
		t.Fatalf("disable_diffing must return a full listing: %#v", disabled)
	}
	if baseline := state.snapshotBaseline("browser:t1", "e1"); baseline == nil || baseline.SnapshotID != "s7" {
		t.Fatalf("scoped baseline must track the latest subtree snapshot: %#v", baseline)
	}
	if baseline := state.snapshotBaseline("browser:t1", ""); baseline == nil || baseline.SnapshotID != "s6" {
		t.Fatalf("whole-target baseline must survive subtree snapshots: %#v", baseline)
	}
	if latest := state.latestBaseline("browser:t1"); latest == nil || latest.SnapshotID != "s7" {
		t.Fatalf("latest observation must be the most recent one at any scope: %#v", latest)
	}
	state.invalidateBrowserSnapshot("t1")
	if state.latestBaseline("browser:t1") != nil || state.snapshotBaseline("browser:t1", "") != nil {
		t.Fatal("navigation must drop every baseline of the tab")
	}
}

func TestSnapshotPagingWithCursor(t *testing.T) {
	t.Parallel()

	state := &guiSessionState{browserSnapshots: map[string]guiSnapshotRecord{}, heldKeys: map[string]struct{}{}, baselines: map[string]*guiSnapshotBaseline{}}
	nodes := testNodes("a|- one", "b|- two", "c|- three", "d|- four", "e|- five")
	page := presentSnapshot(state, "computer:", "", "s1", nodes, nil, 0, true, guiSnapshotOptions{Limit: 2, DisableDiffing: true})
	if len(page.Lines) != 2 || page.NextCursor != "2" || !page.Truncated {
		t.Fatalf("first page: %#v", page)
	}
	if !strings.Contains(page.TruncatedReason, "traversal budget") {
		t.Fatalf("walk truncation must be reported: %q", page.TruncatedReason)
	}
	next, baseline, err := continueSnapshot(state, "computer:", "", "2", 2)
	if err != nil || baseline.SnapshotID != "s1" {
		t.Fatalf("continue: %v %#v", err, baseline)
	}
	if len(next.Lines) != 2 || next.Lines[0] != "- three" || next.NextCursor != "4" || next.Mode != "continued" {
		t.Fatalf("second page: %#v", next)
	}
	last, _, err := continueSnapshot(state, "computer:", "s1", "4", 2)
	if err != nil || len(last.Lines) != 1 || last.NextCursor != "" || last.Truncated {
		t.Fatalf("last page: %v %#v", err, last)
	}
	if _, _, err := continueSnapshot(state, "computer:", "s0", "0", 2); err == nil || !strings.Contains(err.Error(), "belongs to snapshot s1") {
		t.Fatalf("foreign snapshot cursor must be refused: %v", err)
	}
	if _, _, err := continueSnapshot(state, "computer:", "", "99", 2); err == nil {
		t.Fatal("out-of-range cursor must be refused")
	}
	if _, _, err := continueSnapshot(state, "browser:none", "", "0", 2); err == nil {
		t.Fatal("cursor without a baseline must be refused")
	}
}

func TestPageLinesHonoursByteBudget(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", guiSnapshotMaxBytes/2)
	lines := []string{big, big, big}
	out, cursor, truncated, reason := pageLines(lines, 0, 10)
	if len(out) != 1 || cursor != "1" || !truncated || !strings.Contains(reason, "byte budget") {
		t.Fatalf("byte budget paging: n=%d cursor=%q truncated=%v reason=%q", len(out), cursor, truncated, reason)
	}
}

func TestSnapshotOptionsParsing(t *testing.T) {
	t.Parallel()

	opts, err := guiSnapshotOptionsFrom(map[string]any{"limit": 50, "scope_ref": "E7", "disable_diffing": true})
	if err != nil || opts.Limit != 50 || opts.ScopeRef != "e7" || !opts.DisableDiffing {
		t.Fatalf("options: %#v %v", opts, err)
	}
	if _, err := guiSnapshotOptionsFrom(map[string]any{"cursor": "3", "scope_ref": "e1"}); err == nil {
		t.Fatal("cursor with scope_ref must be refused")
	}
	if d, err := guiSnapshotOptionsFrom(map[string]any{}); err != nil || d.Limit != a11ySnapshotDefaultLimit {
		t.Fatalf("defaults: %#v %v", d, err)
	}
	if _, err := browserObserveContract.normalize(map[string]any{"observe": "snapshot", "cursor": "3", "limit": 20}); err != nil {
		t.Fatalf("snapshot paging params: %v", err)
	}
	if _, err := browserObserveContract.normalize(map[string]any{"observe": "state_and_screenshot", "full_page": true, "image_mode": "path"}); err != nil {
		t.Fatalf("state_and_screenshot params: %v", err)
	}
	if _, err := browserObserveContract.normalize(map[string]any{"observe": "screenshot", "image_mode": "inline"}); err == nil {
		t.Fatal("unknown image_mode must be refused")
	}
	if _, err := computerObserveContract.normalize(map[string]any{"observe": "probe", "app_id": "app:1"}); err != nil {
		t.Fatalf("probe params: %v", err)
	}
	if _, err := computerObserveContract.normalize(map[string]any{"observe": "screenshot", "limit": 3}); err == nil {
		t.Fatal("screenshot must not accept limit")
	}
}

func TestBuildAXLinesShowsInteractiveAndNamedStructure(t *testing.T) {
	t.Parallel()

	tree := []axNode{
		{NodeID: "1", Role: axValue{"RootWebArea"}, Name: axValue{"Form"}, ChildIDs: []string{"2", "6"}},
		{NodeID: "2", ParentID: "1", Role: axValue{"form"}, Name: axValue{"Sign in"}, ChildIDs: []string{"3", "4", "5"}},
		{NodeID: "3", ParentID: "2", Role: axValue{"textbox"}, Name: axValue{"Email"}, Value: axValue{"a@b.c"}, BackendDOMNodeID: 10, Properties: []axProperty{{Name: "focused", Value: axValue{true}}}},
		{NodeID: "4", ParentID: "2", Role: axValue{"textbox"}, Name: axValue{"Password"}, Value: axValue{"secret"}, BackendDOMNodeID: 11},
		{NodeID: "5", ParentID: "2", Role: axValue{"button"}, Name: axValue{"Submit"}, BackendDOMNodeID: 12, Properties: []axProperty{{Name: "disabled", Value: axValue{true}}}},
		{NodeID: "6", ParentID: "1", Role: axValue{"generic"}, ChildIDs: []string{"7"}},
		{NodeID: "7", ParentID: "6", Role: axValue{"StaticText"}, Name: axValue{"footer"}},
		{NodeID: "8", ParentID: "1", Role: axValue{"heading"}, Name: axValue{"Hidden"}, Properties: []axProperty{{Name: "hidden", Value: axValue{true}}}},
	}
	byBackend := map[int]*browserInteractive{
		10: {backendID: 10, ref: "e1", Left: 10, Top: 20, Width: 100, Height: 30},
		11: {backendID: 11, ref: "e2", Left: 10, Top: 60, Width: 100, Height: 30, Password: true},
		12: {backendID: 12, ref: "e3", Left: 10, Top: 100, Width: 80, Height: 30},
	}
	nodes := buildAXLines(tree, byBackend, 0)
	lines := make([]string, 0, len(nodes))
	for _, n := range nodes {
		lines = append(lines, n.Line)
	}
	joined := strings.Join(lines, "\n")
	want := []string{
		`- form "Sign in"`,
		`  - textbox "Email" [ref=e1] @10,20 100x30 value="a@b.c" (focused)`,
		`  - textbox "Password" [ref=e2] @10,60 100x30 value="•••"`,
		`  - button "Submit" [ref=e3] @10,100 80x30 (disabled)`,
	}
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Fatalf("missing %q in:\n%s", w, joined)
		}
	}
	if strings.Contains(joined, "secret") || strings.Contains(joined, "footer") || strings.Contains(joined, "Hidden") {
		t.Fatalf("password value, plain text, and hidden nodes must not appear:\n%s", joined)
	}
	if nodes[1].Key != "dom:10" || nodes[1].Ref != "e1" || strings.Contains(nodes[1].Fingerprint, "[ref=") {
		t.Fatalf("interactive node bookkeeping: %#v", nodes[1])
	}
	scoped := buildAXLines(tree, byBackend, 12)
	if len(scoped) != 1 || !strings.HasPrefix(scoped[0].Line, "- button") {
		t.Fatalf("scope must restrict to the subtree: %#v", scoped)
	}
}

func TestAXStatesRendering(t *testing.T) {
	t.Parallel()

	n := axNode{Role: axValue{"heading"}, Properties: []axProperty{
		{Name: "level", Value: axValue{float64(2)}},
		{Name: "expanded", Value: axValue{false}},
		{Name: "checked", Value: axValue{"mixed"}},
		{Name: "invalid", Value: axValue{"grammar"}},
		{Name: "hasPopup", Value: axValue{"menu"}},
		{Name: "required", Value: axValue{false}},
	}}
	got := strings.Join(n.states(), ",")
	if got != "h2,collapsed,checked=mixed,invalid,haspopup=menu" {
		t.Fatalf("states: %q", got)
	}
}
