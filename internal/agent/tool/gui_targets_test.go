package tools

import (
	"strings"
	"testing"
)

func TestBrowserIDsAreOpaquePortIdsNeverURLs(t *testing.T) {
	t.Parallel()

	if got := browserIDForPort(9222); got != "chrome-9222" {
		t.Fatalf("unexpected default browser id %q", got)
	}
	for _, id := range []string{"chrome-9222", " Chrome-9223 "} {
		if _, ok := portFromBrowserID(id); !ok {
			t.Fatalf("%q must parse as a browser id", id)
		}
	}
	for _, bad := range []string{"http://127.0.0.1:9222", "9222", "chrome-", "chrome-70000", "firefox-1"} {
		if _, ok := portFromBrowserID(bad); ok {
			t.Fatalf("%q must not parse as a browser id", bad)
		}
	}
}

func TestBrowserContractCommonTargetParams(t *testing.T) {
	t.Parallel()

	args := map[string]any{"action": "click", "ref": "e1", "browser_id": "chrome-9223", "tab_id": "ABC", "snapshot_id": "b1-2"}
	if _, err := browserActionContract.normalize(args); err != nil {
		t.Fatalf("target params must be accepted on every action: %v", err)
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "click", "ref": "e1", "tab_id": "ABC", "tab_index": 0}); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("tab_id and tab_index must be exclusive, got %v", err)
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "click", "ref": "e1", "browser_id": "http://localhost:9222"}); err == nil || !strings.Contains(err.Error(), "not a browser id") {
		t.Fatalf("a URL must not pass as browser_id, got %v", err)
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "tab_select", "tab_id": "ABC"}); err != nil {
		t.Fatalf("tab_select by tab_id: %v", err)
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "tab_select", "tab_index": 1}); err != nil {
		t.Fatalf("tab_select by legacy tab_index: %v", err)
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "tab_close"}); err != nil {
		t.Fatalf("tab_close without a tab closes the session tab: %v", err)
	}
	if _, err := browserObserveContract.normalize(map[string]any{"observe": "tab_list", "browser_id": "chrome-9223"}); err != nil {
		t.Fatalf("tab_list scoped by browser: %v", err)
	}
}

func TestBrowserContractNewInputActions(t *testing.T) {
	t.Parallel()

	ok := []map[string]any{
		{"action": "set_value", "ref": "e1", "value": ""},
		{"action": "paste", "text": "hi"},
		{"action": "paste", "ref": "e2", "text": "<b>x</b>", "format": "html"},
		{"action": "select_text", "ref": "e2", "text": "world", "prefix": "hello ", "selection_type": "cursor_after"},
		{"action": "secondary_action", "ref": "e2", "name": "expand"},
		{"action": "scroll", "pages": 1.5},
		{"action": "scroll", "ref": "e3", "amount": 200},
	}
	for _, args := range ok {
		if _, err := browserActionContract.normalize(args); err != nil {
			t.Fatalf("%v must be accepted: %v", args, err)
		}
	}
	bad := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "scroll", "pages": 1, "amount": 100}, "mutually exclusive"},
		{map[string]any{"action": "scroll", "pages": 0}, "pages must be at least"},
		{map[string]any{"action": "paste", "text": "x", "format": "rtf"}, "format must be one of"},
		{map[string]any{"action": "select_text", "ref": "e1", "text": "", "prefix": "a"}, "text is required"},
		{map[string]any{"action": "secondary_action", "ref": "e1"}, "name is required"},
		{map[string]any{"action": "set_value", "ref": "e1"}, "value is required"},
	}
	for _, tc := range bad {
		_, err := browserActionContract.normalize(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: expected %q, got %v", tc.args, tc.want, err)
		}
	}
}

func TestComputerContractTargetParams(t *testing.T) {
	t.Parallel()

	if _, err := computerActionContract.normalize(map[string]any{"action": "click", "ref": "e1", "app_id": "app:1234", "snapshot_id": "sabc"}); err != nil {
		t.Fatalf("app_id/snapshot_id must be accepted: %v", err)
	}
	if _, err := computerActionContract.normalize(map[string]any{"action": "click", "ref": "e1", "app_id": "xfce4-terminal"}); err == nil || !strings.Contains(err.Error(), "not an application instance id") {
		t.Fatalf("a name must not pass as app_id, got %v", err)
	}
	if _, err := computerObserveContract.normalize(map[string]any{"observe": "snapshot", "app_id": "app:7"}); err != nil {
		t.Fatalf("snapshot scoped by app: %v", err)
	}
	for _, args := range []map[string]any{
		{"action": "set_value", "ref": "e1", "value": "42"},
		{"action": "paste", "text": "hello", "format": "html"},
		{"action": "select_text", "ref": "e1", "text": "abc", "suffix": "d"},
		{"action": "secondary_action", "ref": "e1", "name": "menu"},
	} {
		if _, err := computerActionContract.normalize(args); err != nil {
			t.Fatalf("%v must be accepted: %v", args, err)
		}
	}
	if _, err := computerActionContract.normalize(map[string]any{"action": "secondary_action", "name": "menu"}); err == nil {
		t.Fatal("secondary_action without ref must fail")
	}
}

func TestComputerContextContract(t *testing.T) {
	t.Parallel()

	if _, err := computerContextContract.normalize(map[string]any{"action": "get_app"}); err == nil || !strings.Contains(err.Error(), "app is required") {
		t.Fatalf("get_app without app: %v", err)
	}
	if _, err := computerContextContract.normalize(map[string]any{"action": "get_browser", "browser_id": "chrome-9222", "url": "https://a"}); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("get_browser with both selectors: %v", err)
	}
	if _, err := computerContextContract.normalize(map[string]any{"action": "get_app", "app": "xterm", "launch": "yes"}); err == nil || !strings.Contains(err.Error(), "launch must be a boolean") {
		t.Fatalf("launch must be boolean: %v", err)
	}
	for _, args := range []map[string]any{
		{"action": "get_state"},
		{"action": "list_apps"},
		{"action": "list_browsers"},
		{"action": "get_browser"},
		{"action": "documentation", "app_id": "app:1"},
	} {
		if _, err := computerContextContract.normalize(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	doc := browserActionContract.documentation()
	if doc["action_key"] != "action" {
		t.Fatalf("documentation must expose the action key: %#v", doc["action_key"])
	}
	common, _ := doc["common_parameters"].([]string)
	if len(common) == 0 || common[0] != "browser_id" {
		t.Fatalf("documentation must list common target params: %#v", doc["common_parameters"])
	}
}

func TestMatchAppsResolvesIdsNamesAndAmbiguity(t *testing.T) {
	t.Parallel()

	apps := []a11yAppInfo{
		{AppID: "app:10", PID: 10, Name: "xfce4-appfinder"},
		{AppID: "app:11", PID: 11, Name: "Terminal"},
		{AppID: "app:12", PID: 12, Name: "Terminal"},
	}
	if got := matchApps(apps, "APP:10"); len(got) != 1 || got[0].PID != 10 {
		t.Fatalf("id match: %#v", got)
	}
	if got := matchApps(apps, "terminal"); len(got) != 2 {
		t.Fatalf("ambiguous name must return both candidates: %#v", got)
	}
	if got := matchApps(apps, "/usr/bin/xfce4-appfinder"); len(got) != 1 || got[0].PID != 10 {
		t.Fatalf("path base match: %#v", got)
	}
	if got := matchApps(apps, "gedit"); len(got) != 0 {
		t.Fatalf("unknown app must not match: %#v", got)
	}
}

func TestURLMatchesTabURLs(t *testing.T) {
	t.Parallel()

	if !urlMatches("https://example.com/", "https://example.com") || !urlMatches("https://example.com/a?b=1", "https://example.com/a") {
		t.Fatal("exact and prefix matches must succeed")
	}
	if urlMatches("https://example.com/about", "https://example.com/ab") || urlMatches("", "https://x") {
		t.Fatal("partial path segments must not match")
	}
}

func TestGUISessionDefaultsAreIsolatedPerSession(t *testing.T) {
	t.Parallel()

	store := newGUISessionStore()
	a := store.get(SessionContext{BotID: "bot", SessionID: "thread-a"})
	b := store.get(SessionContext{BotID: "bot", SessionID: "thread-b"})
	a.selectTab("chrome-9222", "TAB-A")
	a.selectApp("app:1")
	if browser, tab, app := b.defaults(); browser != "" || tab != "" || app != "" {
		t.Fatalf("session b must not see session a's defaults: %q %q %q", browser, tab, app)
	}
	a.recordBrowserSnapshot(guiSnapshotRecord{ID: "b1", TabID: "TAB-A"})
	if _, ok := b.browserSnapshot("TAB-A"); ok {
		t.Fatal("snapshots must be per session")
	}
	a.invalidateBrowserSnapshot("TAB-A")
	if _, ok := a.browserSnapshot("TAB-A"); ok {
		t.Fatal("navigation must invalidate the tab's snapshot")
	}
	a.selectBrowser("chrome-9223")
	if _, tab, _ := a.defaults(); tab != "" {
		t.Fatal("selecting another browser must drop the tab selection")
	}
	a.holdKey("Shift")
	a.holdKey("Control")
	if got := a.heldModifiers(); got != 10 {
		t.Fatalf("held modifiers: %d", got)
	}
	a.releaseKey("shift")
	if got := a.heldModifiers(); got != 2 {
		t.Fatalf("held modifiers after release: %d", got)
	}
	if same := store.get(SessionContext{BotID: "bot", SessionID: "thread-a"}); same != a {
		t.Fatal("same session must map to the same state")
	}
}

func TestComputerActionTargetRequiresMatchingSnapshot(t *testing.T) {
	t.Parallel()

	p := &BrowserProvider{}
	state := &guiSessionState{browserSnapshots: map[string]guiSnapshotRecord{}, heldKeys: map[string]struct{}{}}
	if _, _, err := p.computerActionTarget(state, map[string]any{}, true); err == nil || !strings.Contains(err.Error(), "no desktop snapshot") {
		t.Fatalf("refs without any snapshot must be refused: %v", err)
	}
	if _, snap, err := p.computerActionTarget(state, map[string]any{}, false); err != nil || snap != "" {
		t.Fatalf("coordinate actions need no snapshot: %q %v", snap, err)
	}
	state.recordComputerSnapshot(guiSnapshotRecord{ID: "s1", AppID: "app:1"})
	if _, snap, err := p.computerActionTarget(state, map[string]any{"app_id": "app:1"}, true); err != nil || snap != "s1" {
		t.Fatalf("matching app snapshot: %q %v", snap, err)
	}
	if _, _, err := p.computerActionTarget(state, map[string]any{"app_id": "app:2"}, true); err == nil || !strings.Contains(err.Error(), "covered app:1") {
		t.Fatalf("snapshot of another app must be refused: %v", err)
	}
	if _, snap, err := p.computerActionTarget(state, map[string]any{"snapshot_id": "explicit"}, true); err != nil || snap != "explicit" {
		t.Fatalf("explicit snapshot wins: %q %v", snap, err)
	}
	state.recordComputerSnapshot(guiSnapshotRecord{ID: "s2"})
	if _, snap, err := p.computerActionTarget(state, map[string]any{"app_id": "app:9"}, true); err != nil || snap != "s2" {
		t.Fatalf("a desktop-wide snapshot covers every app: %q %v", snap, err)
	}
}

func TestHTMLToText(t *testing.T) {
	t.Parallel()

	got := htmlToText("<p>Hello <b>world</b></p><ul><li>one</li><li>two &amp; three</li></ul>")
	if got != "Hello world\none\ntwo & three" {
		t.Fatalf("unexpected text: %q", got)
	}
}
