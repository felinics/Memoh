package tools

import (
	"errors"
	"fmt"
)

const (
	browserDefaultReadyTimeoutMS = 30000
	browserDefaultWaitMS         = 1000
	browserMaxTimeoutMS          = 45000
	browserMaxWaitMS             = 10000
	browserMaxScrollAmount       = 5000
	browserDefaultScrollAmount   = 500
	browserMaxScrollPages        = 20
)

var browserElementKeys = []string{"ref", "selector"}

// browserTargetParams are accepted by every browser call: which browser and
// tab to act on, and which snapshot a ref belongs to.
var browserTargetParams = []string{"browser_id", "tab_id", "tab_index", "snapshot_id"}

func browserActionParams() map[string]map[string]any {
	return map[string]map[string]any{
		"browser_id":      {"type": "string", "description": "Browser instance id from computer_context list_browsers, e.g. chrome-9222. Defaults to the session's selected browser, then the workspace browser."},
		"tab_id":          {"type": "string", "description": "Tab id from tab_list, tab_new, or a previous result. Defaults to the session's selected tab. Mutually exclusive with tab_index."},
		"tab_index":       {"type": "integer", "minimum": 0, "description": "Zero-based page index within the browser (compatibility form). Mutually exclusive with tab_id."},
		"snapshot_id":     {"type": "string", "description": "Snapshot the ref was taken in. Defaults to the tab's latest snapshot in this session; a ref from another snapshot is refused."},
		"url":             {"type": "string", "description": "URL to open for navigate or tab_new."},
		"ref":             {"type": "string", "description": "Element ref such as e12 from a browser observation snapshot or screenshot annotation of this tab. Preferred over selector."},
		"selector":        {"type": "string", "description": "CSS selector for the target element when no ref is available."},
		"text":            {"type": "string", "description": "Text for type, fill, keyboard_type, paste, or select_text. fill accepts an empty string to clear the field."},
		"key":             {"type": "string", "description": "Key or key chord for press, keydown, or keyup, e.g. Enter, Tab, Escape, Control+a."},
		"value":           {"type": "string", "description": "Option value for select, or the value for set_value. An empty string is allowed."},
		"name":            {"type": "string", "description": "Name of the secondary action to run, as listed by an observation."},
		"format":          {"type": "string", "enum": []string{"text", "md", "html"}, "description": "Paste format. text and md insert the text as typed characters; html dispatches a rich paste. Defaults to text."},
		"prefix":          {"type": "string", "description": "Text that must immediately precede the select_text match, to disambiguate."},
		"suffix":          {"type": "string", "description": "Text that must immediately follow the select_text match, to disambiguate."},
		"selection_type":  {"type": "string", "enum": []string{"text", "cursor_before", "cursor_after"}, "description": "select_text result: select the text (default), or place the caret before or after it."},
		"target_ref":      {"type": "string", "description": "Drop target ref for drag, preferred over target_selector."},
		"target_selector": {"type": "string", "description": "Drop target CSS selector for drag when no target_ref is available."},
		"files":           {"type": "array", "items": map[string]any{"type": "string"}, "description": "Workspace file paths to upload."},
		"direction":       {"type": "string", "enum": []string{"up", "down", "left", "right"}, "description": "Scroll direction. Defaults to down."},
		"amount":          {"type": "integer", "minimum": 1, "maximum": browserMaxScrollAmount, "default": browserDefaultScrollAmount, "description": "Scroll amount in CSS pixels. Mutually exclusive with pages."},
		"pages":           {"type": "number", "minimum": 0.1, "maximum": browserMaxScrollPages, "description": "Scroll amount in visible-area heights (or widths) of the target. Mutually exclusive with amount."},
		"timeout":         {"type": "integer", "minimum": 1, "maximum": browserMaxTimeoutMS, "description": "Readiness timeout in milliseconds for navigate, reload, go_back, go_forward, and wait with a target. Defaults to 30000."},
		"duration_ms":     {"type": "integer", "minimum": 1, "maximum": browserMaxWaitMS, "default": browserDefaultWaitMS, "description": "Fixed pause in milliseconds for wait without a target."},
		"button":          {"type": "string", "enum": []string{"left", "middle", "right"}, "description": "Mouse button for click, double_click, and drag. Defaults to left."},
		"click_count":     {"type": "integer", "minimum": 1, "maximum": 3, "description": "Number of clicks for click. Defaults to 1; double_click is always 2."},
		"x":               {"type": "number", "minimum": 0, "description": "Viewport X in CSS pixels for click, double_click, hover, scroll, or a drag source when no element target is given."},
		"y":               {"type": "number", "minimum": 0, "description": "Viewport Y in CSS pixels; always paired with x."},
		"to_x":            {"type": "number", "minimum": 0, "description": "Drop X in viewport CSS pixels for drag when no target element is given."},
		"to_y":            {"type": "number", "minimum": 0, "description": "Drop Y in viewport CSS pixels; always paired with to_x."},
	}
}

func validateDoubleClickCount(args map[string]any) error {
	_, err := guiClickCount(args, "double_click")
	return err
}

func validateBrowserWait(args map[string]any) error {
	hasTarget := guiArgPresent(args, "ref", false) || guiArgPresent(args, "selector", false)
	if hasTarget {
		if guiArgPresent(args, "duration_ms", true) {
			return errors.New("wait with a target uses timeout; duration_ms is only for a fixed pause without a target")
		}
		return nil
	}
	return guiExclusive(args, "duration_ms", "timeout")
}

func validateBrowserDrag(args map[string]any) error {
	sourceElements := 0
	for _, key := range []string{"ref", "selector"} {
		if guiArgPresent(args, key, false) {
			sourceElements++
		}
	}
	sourcePoint, err := guiPointPresent(args, "x", "y")
	if err != nil {
		return err
	}
	switch {
	case sourceElements == 0 && !sourcePoint:
		return errors.New("drag requires a source: ref, selector, or x/y")
	case sourceElements > 0 && sourcePoint:
		return errors.New("drag source accepts either ref/selector or x/y, not both")
	}
	targetElements := 0
	for _, key := range []string{"target_ref", "target_selector"} {
		if guiArgPresent(args, key, false) {
			targetElements++
		}
	}
	if targetElements > 1 {
		return errors.New("drag accepts only one of target_ref, target_selector")
	}
	targetPoint, err := guiPointPresent(args, "to_x", "to_y")
	if err != nil {
		return err
	}
	switch {
	case targetElements == 0 && !targetPoint:
		return errors.New("drag requires a drop target: target_ref, target_selector, or to_x/to_y")
	case targetElements > 0 && targetPoint:
		return errors.New("drag target accepts either target_ref/target_selector or to_x/to_y, not both")
	}
	return nil
}

// validateScrollAmount enforces amount xor pages.
func validateScrollAmount(args map[string]any) error {
	return guiExclusive(args, "amount", "pages")
}

// validateTabPick requires exactly one of tab_id / tab_index for tab_select.
func validateTabPick(args map[string]any) error {
	hasID := guiArgPresent(args, "tab_id", false)
	hasIndex := guiArgPresent(args, "tab_index", true)
	if !hasID && !hasIndex {
		return errors.New("tab_id (or the compatibility tab_index) is required for tab_select")
	}
	return nil
}

// validateBrowserTargetParams applies to every browser call: tab_id and
// tab_index are two spellings of the same choice and never combine, and a
// browser id must be an id, never a URL.
func validateBrowserTargetParams(args map[string]any) error {
	if err := guiExclusive(args, "tab_id", "tab_index"); err != nil {
		return err
	}
	if id := StringArg(args, "browser_id"); id != "" {
		if _, ok := portFromBrowserID(id); !ok {
			return fmt.Errorf("browser_id %q is not a browser id; use computer_context list_browsers", id)
		}
	}
	return nil
}

var browserActionContract = newGUIContractWithCommon("action", browserElementKeys, browserActionParams(), browserTargetParams, validateBrowserTargetParams, []guiActionSpec{
	{Name: "navigate", Summary: "open a URL in the tab and wait until the document is ready; refs of that tab become invalid", Required: []string{"url"}, Optional: []string{"timeout"}},
	{Name: "click", Summary: "click an element or a viewport point", Locator: guiLocatorElementOrPoint, Optional: []string{"button", "click_count"}},
	{Name: "double_click", Aliases: []string{"dblclick"}, Summary: "double-click an element or a viewport point", Locator: guiLocatorElementOrPoint, Optional: []string{"button", "click_count"}, Validate: validateDoubleClickCount},
	{Name: "focus", Summary: "focus an element and report what actually received focus", Locator: guiLocatorElement},
	{Name: "type", Summary: "focus an element and insert text at its caret", Locator: guiLocatorElement, Required: []string{"text"}},
	{Name: "fill", Summary: "replace the content of an input, textarea, or contenteditable by selecting everything and typing; empty text clears it", Locator: guiLocatorElement, Required: []string{"text"}, AllowEmpty: []string{"text"}},
	{Name: "set_value", Summary: "set the value of an input, textarea, select, or contenteditable directly and fire input/change; empty value is allowed", Locator: guiLocatorElement, Required: []string{"value"}, AllowEmpty: []string{"value"}},
	{Name: "paste", Summary: "paste text into an element or the focused element; html dispatches a rich paste event", Locator: guiLocatorElementOptional, Required: []string{"text"}, Optional: []string{"format"}},
	{Name: "select_text", Summary: "select an exact text inside an input, textarea, or contenteditable, or place the caret before/after it", Locator: guiLocatorElement, Required: []string{"text"}, Optional: []string{"prefix", "suffix", "selection_type"}},
	{Name: "secondary_action", Summary: "run a named secondary action an observation listed for the element (the browser backend exposes none)", Locator: guiLocatorElement, Required: []string{"name"}},
	{Name: "press", Summary: "press a key or chord in the page, e.g. Enter or Control+a", Required: []string{"key"}},
	{Name: "keyboard_type", Aliases: []string{"keyboard_inserttext"}, Summary: "insert text into whatever currently has focus", Required: []string{"text"}},
	{Name: "keydown", Summary: "hold a key down; it stays held for later calls until keyup", Required: []string{"key"}},
	{Name: "keyup", Summary: "release a key held by keydown", Required: []string{"key"}},
	{Name: "hover", Summary: "move the mouse over an element or a viewport point", Locator: guiLocatorElementOrPoint},
	{Name: "select", Summary: "choose a select option by value", Locator: guiLocatorElement, Required: []string{"value"}, AllowEmpty: []string{"value"}},
	{Name: "check", Summary: "set a checkbox or radio to checked", Locator: guiLocatorElement},
	{Name: "uncheck", Summary: "set a checkbox to unchecked", Locator: guiLocatorElement},
	{Name: "scroll", Summary: "scroll the page, a scrollable element, or at a viewport point by amount pixels or pages of the visible area", Locator: guiLocatorAnyOptional, Optional: []string{"direction", "amount", "pages"}, Validate: validateScrollAmount},
	{Name: "scroll_into_view", Aliases: []string{"scrollintoview"}, Summary: "scroll an element into the viewport", Locator: guiLocatorElement},
	{Name: "drag", Summary: "press on a source, move, and release on a drop target", Optional: []string{"ref", "selector", "x", "y", "target_ref", "target_selector", "to_x", "to_y", "button"}, Validate: validateBrowserDrag},
	{Name: "upload", Summary: "set workspace files on a file input", Locator: guiLocatorElement, Required: []string{"files"}},
	{Name: "wait", Summary: "with a target, wait until it exists (timeout); without one, pause for duration_ms", Locator: guiLocatorElementOptional, Optional: []string{"timeout", "duration_ms"}, Validate: validateBrowserWait},
	{Name: "go_back", Summary: "go back one history entry in the tab", Optional: []string{"timeout"}},
	{Name: "go_forward", Summary: "go forward one history entry in the tab", Optional: []string{"timeout"}},
	{Name: "reload", Summary: "reload the tab and wait until it is ready", Optional: []string{"timeout"}},
	{Name: "tab_new", Summary: "open a new tab in the browser (browser_id) and make it the session's tab; returns its tab_id", Optional: []string{"url"}},
	{Name: "tab_select", Summary: "make a tab the session's tab by tab_id (or tab_index) and bring it to front", Validate: validateTabPick},
	{Name: "tab_close", Summary: "close the tab given by tab_id or tab_index, or the session's tab"},
})

func browserObserveParams() map[string]map[string]any {
	return map[string]map[string]any{
		"browser_id":  {"type": "string", "description": "Browser instance id, e.g. chrome-9222. Defaults to the session's selected browser."},
		"tab_id":      {"type": "string", "description": "Tab id to observe. Defaults to the session's selected tab. Mutually exclusive with tab_index."},
		"tab_index":   {"type": "integer", "minimum": 0, "description": "Zero-based page index within the browser (compatibility form)."},
		"snapshot_id": {"type": "string", "description": "Snapshot the ref was taken in; defaults to the tab's latest snapshot in this session."},
		"ref":         {"type": "string", "description": "Element ref from snapshot or screenshot_annotate. Scopes get_content and get_html."},
		"selector":    {"type": "string", "description": "CSS selector to scope get_content or get_html when no ref is available."},
		"script":      {"type": "string", "description": "JavaScript expression for evaluate. Keep it short and read-only unless the task requires otherwise."},
		"full_page":   {"type": "boolean", "default": false, "description": "Capture the whole document instead of the viewport for screenshot."},
	}
}

var browserObserveContract = newGUIContractWithCommon("observe", browserElementKeys, browserObserveParams(), browserTargetParams, validateBrowserTargetParams, []guiActionSpec{
	{Name: "snapshot", Summary: "list interactive elements with refs bound to a new snapshot_id"},
	{Name: "get_content", Summary: "readable text of the page or one element", Locator: guiLocatorElementOptional},
	{Name: "get_html", Summary: "outerHTML of the document or innerHTML of one element", Locator: guiLocatorElementOptional},
	{Name: "screenshot", Summary: "save a screenshot to the workspace", Optional: []string{"full_page"}},
	{Name: "screenshot_annotate", Summary: "save a screenshot with element refs drawn on it (also a new snapshot_id)"},
	{Name: "evaluate", Summary: "evaluate a JavaScript expression in the page", Required: []string{"script"}},
	{Name: "get_url", Summary: "the tab's URL"},
	{Name: "get_title", Summary: "the tab's document title"},
	{Name: "pdf", Summary: "print the page to PDF"},
	{Name: "tab_list", Summary: "list the page tabs of the browser with their tab_id"},
})

func browserRemoteSessionParams() map[string]map[string]any {
	return map[string]map[string]any{
		"session_id": {"type": "string", "description": "Target/session ID returned by create or status."},
		"url":        {"type": "string", "description": "Optional URL to open when creating a target."},
		"browser_id": {"type": "string", "description": "Browser instance id, e.g. chrome-9222. Defaults to the workspace browser."},
	}
}

var browserRemoteSessionContract = newGUIContract("action", nil, browserRemoteSessionParams(), []guiActionSpec{
	{Name: "create", Summary: "expose a page target for CDP clients, creating one when url is given", Optional: []string{"url", "browser_id"}},
	{Name: "status", Summary: "list the CDP endpoint and open page targets", Optional: []string{"browser_id"}},
	{Name: "close", Summary: "close the target behind a session", Required: []string{"session_id"}, Optional: []string{"browser_id"}},
})

// normalizeBrowserAction maps compatibility aliases to canonical action names.
func normalizeBrowserAction(action string) string {
	return browserActionContract.canonical(action)
}

// browserReadyTimeout resolves the readiness timeout for navigation-like
// actions. Validation already bounded the value; this only applies the
// default.
func browserReadyTimeout(args map[string]any) (int, error) {
	return guiTimeoutMS(args, browserDefaultReadyTimeoutMS)
}

func browserWaitDuration(args map[string]any) (int, error) {
	value, err := guiDurationMS(args, "timeout", browserDefaultWaitMS)
	if err != nil {
		return 0, err
	}
	if value > browserMaxWaitMS {
		return 0, fmt.Errorf("wait without a target pauses at most %d ms", browserMaxWaitMS)
	}
	return value, nil
}
