package tools

import (
	"errors"
	"fmt"
	"strings"
)

const (
	computerDefaultWaitMS       = 1000
	computerMaxWaitMS           = 10000
	computerMaxScrollAmount     = 10000
	computerDefaultScrollAmount = 500
)

var computerElementKeys = []string{"ref"}

// computerTargetParams are accepted by every desktop call.
var computerTargetParams = []string{"app_id", "snapshot_id"}

func computerActionParams() map[string]map[string]any {
	return map[string]map[string]any{
		"app_id":         {"type": "string", "description": "Application instance id from computer_context (app:<pid>). Defaults to the session's selected application; refs must belong to it."},
		"snapshot_id":    {"type": "string", "description": "Snapshot the ref was taken in. Defaults to the session's latest desktop snapshot; a ref from another snapshot is refused."},
		"ref":            {"type": "string", "description": "Element ref such as e3 from a desktop observation snapshot. Preferred over coordinates."},
		"x":              {"type": "integer", "minimum": 0, "description": "X in desktop pixels when no ref is given, or the drag start."},
		"y":              {"type": "integer", "minimum": 0, "description": "Y in desktop pixels; always paired with x."},
		"to_x":           {"type": "integer", "minimum": 0, "description": "Drag end X in desktop pixels."},
		"to_y":           {"type": "integer", "minimum": 0, "description": "Drag end Y in desktop pixels; always paired with to_x."},
		"button":         {"type": "string", "enum": []string{"left", "middle", "right"}, "description": "Mouse button for click, double_click, and drag. Defaults to left."},
		"click_count":    {"type": "integer", "minimum": 1, "maximum": 3, "description": "Number of clicks for click. Defaults to 1; double_click is always 2."},
		"button_mask":    {"type": "integer", "minimum": 0, "maximum": 255, "description": "Raw RFB button mask for mouse_move and pointer. 0 releases every button."},
		"direction":      {"type": "string", "enum": []string{"up", "down", "left", "right"}, "description": "Scroll direction. Defaults to down."},
		"amount":         {"type": "integer", "minimum": 1, "maximum": computerMaxScrollAmount, "default": computerDefaultScrollAmount, "description": "Scroll amount in pixels; delivered as discrete wheel steps of about 120 px."},
		"duration_ms":    {"type": "integer", "minimum": 1, "maximum": computerMaxWaitMS, "default": computerDefaultWaitMS, "description": "Pause length in milliseconds for wait."},
		"key":            {"type": "string", "description": "Key or key chord for key, e.g. Enter, Escape, Control+a."},
		"text":           {"type": "string", "description": "Text for type, fill, paste, or select_text. fill accepts an empty string to clear the field."},
		"value":          {"type": "string", "description": "Value for set_value: text for editable widgets, a number for sliders and spin buttons. An empty string is allowed."},
		"name":           {"type": "string", "description": "Secondary action name exactly as listed in the element's actions= list of the snapshot."},
		"format":         {"type": "string", "enum": []string{"text", "md", "html"}, "description": "Paste format. text and md set a plain-text clipboard; html sets a text/html clipboard. Defaults to text."},
		"prefix":         {"type": "string", "description": "Text that must immediately precede the select_text match, to disambiguate."},
		"suffix":         {"type": "string", "description": "Text that must immediately follow the select_text match, to disambiguate."},
		"selection_type": {"type": "string", "enum": []string{"text", "cursor_before", "cursor_after"}, "description": "select_text result: select the text (default), or place the caret before or after it."},
	}
}

func validateComputerDrag(args map[string]any) error {
	ok, err := guiPointPresent(args, "to_x", "to_y")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("to_x and to_y are required for drag")
	}
	return nil
}

func validateComputerWait(args map[string]any) error {
	return guiExclusive(args, "duration_ms", "amount")
}

// validateComputerTargetParams applies to every desktop call: an app id must
// be an application instance id from discovery, never a command or a name.
func validateComputerTargetParams(args map[string]any) error {
	if id := StringArg(args, "app_id"); id != "" && !isComputerAppID(id) {
		return fmt.Errorf("app_id %q is not an application instance id; use computer_context list_apps or get_app (ids look like app:1234)", id)
	}
	return nil
}

func isComputerAppID(id string) bool {
	id = strings.TrimSpace(id)
	return strings.HasPrefix(id, "app:") || strings.HasPrefix(id, "bus::")
}

var computerActionContract = newGUIContractWithCommon("action", computerElementKeys, computerActionParams(), computerTargetParams, validateComputerTargetParams, []guiActionSpec{
	{Name: "click", Summary: "click an element ref or a desktop point", Locator: guiLocatorElementOrPoint, Optional: []string{"button", "click_count"}},
	{Name: "double_click", Summary: "double-click an element ref or a desktop point", Locator: guiLocatorElementOrPoint, Optional: []string{"button", "click_count"}, Validate: validateDoubleClickCount},
	{Name: "type", Summary: "insert text at the caret of a ref, or of the focused widget when no ref is given", Locator: guiLocatorElementOptional, Required: []string{"text"}},
	{Name: "fill", Summary: "replace the whole text of a ref, or of the focused widget when no ref is given; empty text clears it", Locator: guiLocatorElementOptional, Required: []string{"text"}, AllowEmpty: []string{"text"}},
	{Name: "set_value", Summary: "set an editable widget's text or a slider/spin button's number directly; unsupported widgets return an explicit error", Locator: guiLocatorElement, Required: []string{"value"}, AllowEmpty: []string{"value"}},
	{Name: "paste", Summary: "paste text through the desktop clipboard into a ref or the focused widget, restoring the previous clipboard afterwards", Locator: guiLocatorElementOptional, Required: []string{"text"}, Optional: []string{"format"}},
	{Name: "select_text", Summary: "select an exact text inside a text widget, or place the caret before/after it", Locator: guiLocatorElement, Required: []string{"text"}, Optional: []string{"prefix", "suffix", "selection_type"}},
	{Name: "secondary_action", Summary: "run one of the actions the snapshot listed for the element (actions=...)", Locator: guiLocatorElement, Required: []string{"name"}},
	{Name: "key", Summary: "press a key or chord on the desktop", Required: []string{"key"}},
	{Name: "scroll", Summary: "scroll at a ref, a point, or the desktop centre", Locator: guiLocatorAnyOptional, Optional: []string{"direction", "amount"}},
	{Name: "drag", Summary: "press at x/y, move, and release at to_x/to_y", Locator: guiLocatorPoint, Optional: []string{"to_x", "to_y", "button"}, Validate: validateComputerDrag},
	{Name: "wait", Summary: "pause for duration_ms (amount is the legacy name)", Optional: []string{"duration_ms", "amount"}, Validate: validateComputerWait},
	{Name: "mouse_move", Summary: "move the pointer, keeping button_mask held", Locator: guiLocatorPoint, Optional: []string{"button_mask"}},
	{Name: "pointer", Summary: "send a raw pointer state at x/y with button_mask", Locator: guiLocatorPoint, Optional: []string{"button_mask"}},
})

func computerObserveParams() map[string]map[string]any {
	return map[string]map[string]any{
		"limit":           {"type": "integer", "minimum": 1, "maximum": a11ySnapshotMaxLimit, "default": a11ySnapshotDefaultLimit, "description": "Maximum number of lines returned per page of a snapshot."},
		"app_id":          {"type": "string", "description": "Restrict snapshot to one application instance (app:<pid>). Defaults to the session's selected application, or the whole desktop when none is selected."},
		"scope_ref":       {"type": "string", "description": "Ref from the latest desktop snapshot; the new snapshot lists only that element's subtree."},
		"cursor":          {"type": "string", "description": "next_cursor from the previous snapshot result: continue reading that same snapshot instead of taking a new one. Not combined with scope_ref or disable_diffing."},
		"disable_diffing": {"type": "boolean", "default": false, "description": "Return the full listing even when a previous snapshot of the same target exists (by default only added, updated, and removed elements are listed)."},
		"image_mode":      {"type": "string", "enum": []string{"auto", "path"}, "default": "auto", "description": "auto: the screenshot is saved and, when the model accepts images, also sent to the model as its next input; path: saved only."},
	}
}

var computerSnapshotParams = []string{"limit", "app_id", "scope_ref", "cursor", "disable_diffing"}

var computerObserveContract = newGUIContractWithCommon("observe", nil, computerObserveParams(), nil, validateComputerTargetParams, []guiActionSpec{
	{Name: "snapshot", Summary: "accessibility tree of the desktop or one application (roles, names, values, states, actions, hierarchy) with refs bound to a new snapshot_id; incremental against the previous snapshot of the same target unless disable_diffing", Optional: computerSnapshotParams},
	{Name: "screenshot", Summary: "capture the desktop to the workspace and, by default, into the model's next input; the result states the pixel size and coordinate space", Optional: []string{"image_mode"}},
	{Name: "state_and_screenshot", Summary: "snapshot and screenshot of the desktop or application in one call, each with its capture time and a consistency check", Optional: append(append([]string{}, computerSnapshotParams...), "image_mode")},
	{Name: "probe", Summary: "what the desktop backends can do right now: accessibility bus, display, screenshot, input, clipboard", Optional: []string{"app_id"}},
})

func computerWaitDuration(args map[string]any) (int, error) {
	value, err := guiDurationMS(args, "amount", computerDefaultWaitMS)
	if err != nil {
		return 0, err
	}
	if value > computerMaxWaitMS {
		return 0, errors.New("wait pauses at most 10000 ms")
	}
	return value, nil
}

func computerContextParams() map[string]map[string]any {
	return map[string]map[string]any{
		"app":        {"type": "string", "description": "Application to select for get_app: an instance id (app:1234), an application name, a desktop entry or executable name such as xfce4-terminal, or an absolute executable path."},
		"launch":     {"type": "boolean", "default": false, "description": "For get_app: start the application when it is installed but not running. Governed like exec by the tool approval policy."},
		"app_id":     {"type": "string", "description": "Application instance id for documentation."},
		"browser_id": {"type": "string", "description": "Browser instance id for get_browser or documentation, e.g. chrome-9222."},
		"url":        {"type": "string", "description": "For get_browser: pick the running browser that has a tab at this URL. Never navigates or opens tabs."},
	}
}

func validateGetBrowser(args map[string]any) error {
	return guiExclusive(args, "browser_id", "url")
}

var computerContextContract = newGUIContract("action", nil, computerContextParams(), []guiActionSpec{
	{Name: "get_state", Summary: "applications, browsers, and tabs visible to this bot, with per-domain discovery errors"},
	{Name: "list_apps", Summary: "running applications on the workspace desktop with app_id, name, and windows"},
	{Name: "get_app", Summary: "select an application as this session's default and return its app_id and an initial snapshot; launch=true starts an installed application that is not running", Required: []string{"app"}, Optional: []string{"launch"}},
	{Name: "list_browsers", Summary: "workspace browsers with browser_id, status, and capabilities"},
	{Name: "get_browser", Summary: "select a running browser (by browser_id, or the one showing url) as this session's default; ambiguity returns candidates", Optional: []string{"browser_id", "url"}, Validate: validateGetBrowser},
	{Name: "documentation", Summary: "the action contracts and backend capabilities of the GUI tools, for an app_id or browser_id or in general", Optional: []string{"app_id", "browser_id"}},
})
