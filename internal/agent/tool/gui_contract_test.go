package tools

import (
	"strings"
	"testing"
)

func TestBrowserContractRejectsBadCallsBeforeExecution(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "missing action", args: map[string]any{}, want: "action is required"},
		{name: "unknown action", args: map[string]any{"action": "teleport"}, want: `unknown action "teleport"`},
		{name: "click without target", args: map[string]any{"action": "click"}, want: "ref or selector or x/y is required for click"},
		{name: "click with ref and selector", args: map[string]any{"action": "click", "ref": "e1", "selector": "#a"}, want: "only one of ref, selector"},
		{name: "click with ref and point", args: map[string]any{"action": "click", "ref": "e1", "x": 1, "y": 2}, want: "either ref or selector or x/y"},
		{name: "click with half a point", args: map[string]any{"action": "click", "x": 10}, want: "x and y must be provided together"},
		{name: "click with bad button", args: map[string]any{"action": "click", "ref": "e1", "button": "quaternary"}, want: "button must be one of left, middle, right"},
		{name: "click_count out of range", args: map[string]any{"action": "click", "ref": "e1", "click_count": 9}, want: "click_count must be at most 3"},
		{name: "double_click conflicting count", args: map[string]any{"action": "double_click", "ref": "e1", "click_count": 3}, want: "double_click always uses click_count 2"},
		{name: "navigate without url", args: map[string]any{"action": "navigate"}, want: "url is required for navigate"},
		{name: "navigate with foreign param", args: map[string]any{"action": "navigate", "url": "https://a", "text": "x"}, want: `action "navigate" does not accept parameter(s): text`},
		{name: "type with empty text", args: map[string]any{"action": "type", "ref": "e1", "text": ""}, want: "text is required for type"},
		{name: "wait with both timings", args: map[string]any{"action": "wait", "timeout": 5, "duration_ms": 5}, want: "duration_ms and timeout are mutually exclusive"},
		{name: "wait target with duration", args: map[string]any{"action": "wait", "ref": "e1", "duration_ms": 5}, want: "duration_ms is only for a fixed pause"},
		{name: "timeout over max", args: map[string]any{"action": "navigate", "url": "https://a", "timeout": 90000}, want: "timeout must be at most 45000"},
		{name: "drag without target", args: map[string]any{"action": "drag", "ref": "e1"}, want: "drag requires a drop target"},
		{name: "drag with two targets", args: map[string]any{"action": "drag", "ref": "e1", "target_ref": "e2", "to_x": 1, "to_y": 2}, want: "either target_ref/target_selector or to_x/to_y"},
		{name: "tab_select without index", args: map[string]any{"action": "tab_select"}, want: "tab_id (or the compatibility tab_index) is required for tab_select"},
		{name: "wrong type", args: map[string]any{"action": "click", "ref": "e1", "click_count": "two"}, want: "click_count must be a number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := browserActionContract.normalize(tc.args)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %q", tc.want, err.Error())
			}
		})
	}
}

func TestBrowserContractAcceptsValidCallsAndNormalizes(t *testing.T) {
	t.Parallel()

	args := map[string]any{"action": "DblClick", "ref": "e3", "button": "Right"}
	spec, err := browserActionContract.normalize(args)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if spec.Name != "double_click" || args["action"] != "double_click" {
		t.Fatalf("alias not normalized: %#v", args)
	}
	if args["button"] != "right" {
		t.Fatalf("enum not normalized: %#v", args["button"])
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "fill", "selector": "#q", "text": ""}); err != nil {
		t.Fatalf("fill with empty text must be a valid clear: %v", err)
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "select", "ref": "e2", "value": ""}); err != nil {
		t.Fatalf("select with empty value must be accepted: %v", err)
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "keyboard_inserttext", "text": "hi"}); err != nil {
		t.Fatalf("keyboard_inserttext alias: %v", err)
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "click", "x": 12.0, "y": 40.0, "click_count": 3.0}); err != nil {
		t.Fatalf("JSON numbers must be accepted for integer params: %v", err)
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "wait", "timeout": 500}); err != nil {
		t.Fatalf("legacy targetless wait timeout must still be accepted alone: %v", err)
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "drag", "x": 1, "y": 2, "target_selector": "#drop"}); err != nil {
		t.Fatalf("drag from point to element: %v", err)
	}
	if _, err := browserActionContract.normalize(map[string]any{"action": "scroll"}); err != nil {
		t.Fatalf("scroll without locator: %v", err)
	}
}

func TestBrowserObserveContract(t *testing.T) {
	t.Parallel()

	if _, err := browserObserveContract.normalize(map[string]any{"observe": "evaluate"}); err == nil || !strings.Contains(err.Error(), "script is required") {
		t.Fatalf("evaluate without script: %v", err)
	}
	if _, err := browserObserveContract.normalize(map[string]any{"observe": "snapshot", "full_page": true}); err == nil || !strings.Contains(err.Error(), "does not accept") {
		t.Fatalf("snapshot with full_page: %v", err)
	}
	if _, err := browserObserveContract.normalize(map[string]any{"observe": "get_content", "ref": "e1", "selector": "#a"}); err == nil {
		t.Fatal("get_content with two locators must fail")
	}
	if _, err := browserObserveContract.normalize(map[string]any{"observe": "screenshot", "full_page": true}); err != nil {
		t.Fatalf("screenshot full_page: %v", err)
	}
}

func TestComputerContractValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "click without target", args: map[string]any{"action": "click"}, want: "ref or x/y is required for click"},
		{name: "fill without text", args: map[string]any{"action": "fill", "ref": "e1"}, want: "text is required for fill"},
		{name: "type empty text", args: map[string]any{"action": "type", "text": ""}, want: "text is required for type"},
		{name: "drag without end", args: map[string]any{"action": "drag", "x": 1, "y": 1}, want: "to_x and to_y are required for drag"},
		{name: "drag with ref", args: map[string]any{"action": "drag", "ref": "e1", "x": 1, "y": 1, "to_x": 2, "to_y": 2}, want: "does not accept parameter(s): ref"},
		{name: "wait with both", args: map[string]any{"action": "wait", "amount": 5, "duration_ms": 5}, want: "duration_ms and amount are mutually exclusive"},
		{name: "pointer mask range", args: map[string]any{"action": "pointer", "x": 1, "y": 1, "button_mask": 300}, want: "button_mask must be at most 255"},
		{name: "scroll direction", args: map[string]any{"action": "scroll", "direction": "sideways"}, want: "direction must be one of"},
		{name: "key without key", args: map[string]any{"action": "key"}, want: "key is required for key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := computerActionContract.normalize(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}

	if _, err := computerActionContract.normalize(map[string]any{"action": "fill", "ref": "e2", "text": ""}); err != nil {
		t.Fatalf("fill with empty text must clear: %v", err)
	}
	if _, err := computerActionContract.normalize(map[string]any{"action": "wait", "amount": 200}); err != nil {
		t.Fatalf("legacy wait amount: %v", err)
	}
	if ms, err := computerWaitDuration(map[string]any{"action": "wait", "amount": 200}); err != nil || ms != 200 {
		t.Fatalf("legacy amount should map to duration: %d %v", ms, err)
	}
	if ms, err := computerWaitDuration(map[string]any{"action": "wait"}); err != nil || ms != computerDefaultWaitMS {
		t.Fatalf("default wait: %d %v", ms, err)
	}
	if _, err := computerObserveContract.normalize(map[string]any{"observe": "snapshot", "limit": 5000}); err == nil {
		t.Fatal("limit above max must fail")
	}
	if _, err := computerObserveContract.normalize(map[string]any{"observe": "screenshot", "limit": 5}); err == nil {
		t.Fatal("screenshot must not accept limit")
	}
}

func TestGUIClickCount(t *testing.T) {
	t.Parallel()

	if n, err := guiClickCount(map[string]any{}, "click"); err != nil || n != 1 {
		t.Fatalf("default click count: %d %v", n, err)
	}
	if n, err := guiClickCount(map[string]any{"click_count": 3}, "click"); err != nil || n != 3 {
		t.Fatalf("explicit click count: %d %v", n, err)
	}
	if n, err := guiClickCount(map[string]any{}, "double_click"); err != nil || n != 2 {
		t.Fatalf("double click count: %d %v", n, err)
	}
	if n, err := guiClickCount(map[string]any{"click_count": 2}, "double_click"); err != nil || n != 2 {
		t.Fatalf("double click with matching count: %d %v", n, err)
	}
	if _, err := guiClickCount(map[string]any{"click_count": 1}, "double_click"); err == nil {
		t.Fatal("double click with conflicting count must fail")
	}
}

func TestBrowserTimingDefaults(t *testing.T) {
	t.Parallel()

	if ms, err := browserReadyTimeout(map[string]any{}); err != nil || ms != browserDefaultReadyTimeoutMS {
		t.Fatalf("ready timeout default: %d %v", ms, err)
	}
	if ms, err := browserReadyTimeout(map[string]any{"timeout": 1500}); err != nil || ms != 1500 {
		t.Fatalf("ready timeout explicit: %d %v", ms, err)
	}
	if ms, err := browserWaitDuration(map[string]any{}); err != nil || ms != browserDefaultWaitMS {
		t.Fatalf("wait default: %d %v", ms, err)
	}
	if ms, err := browserWaitDuration(map[string]any{"timeout": 700}); err != nil || ms != 700 {
		t.Fatalf("legacy targetless timeout maps to duration: %d %v", ms, err)
	}
	if _, err := browserWaitDuration(map[string]any{"timeout": 20000}); err == nil {
		t.Fatal("legacy timeout beyond the pause ceiling must fail")
	}
}

func TestGUIContractDescriptionsMatchSpecs(t *testing.T) {
	t.Parallel()

	doc := browserActionContract.actionDescription("Actions:")
	for _, want := range []string{
		"- double_click (alias dblclick): ",
		"- keyboard_type (alias keyboard_inserttext): ",
		"[target: ref|selector or x+y; optional button, click_count]",
		"- fill: ",
		"- wait: ",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("description missing %q:\n%s", want, doc)
		}
	}
}
