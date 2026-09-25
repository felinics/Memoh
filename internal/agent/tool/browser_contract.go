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
)

var browserElementKeys = []string{"ref", "selector"}

func browserActionParams() map[string]map[string]any {
	return map[string]map[string]any{
		"url":             {"type": "string", "description": "URL to open for navigate or tab_new."},
		"ref":             {"type": "string", "description": "Element ref such as e12 from a browser observation snapshot or screenshot annotation. Preferred over selector."},
		"selector":        {"type": "string", "description": "CSS selector for the target element when no ref is available."},
		"text":            {"type": "string", "description": "Text for type, fill, or keyboard_type. fill accepts an empty string to clear the field."},
		"key":             {"type": "string", "description": "Key or key chord for press, keydown, or keyup, e.g. Enter, Tab, Escape, Control+a."},
		"value":           {"type": "string", "description": "Option value for select. An empty string selects the empty option."},
		"target_ref":      {"type": "string", "description": "Drop target ref for drag, preferred over target_selector."},
		"target_selector": {"type": "string", "description": "Drop target CSS selector for drag when no target_ref is available."},
		"files":           {"type": "array", "items": map[string]any{"type": "string"}, "description": "Workspace file paths to upload."},
		"tab_index":       {"type": "integer", "minimum": 0, "description": "Zero-based page tab index for tab_select or tab_close."},
		"direction":       {"type": "string", "enum": []string{"up", "down", "left", "right"}, "description": "Scroll direction. Defaults to down."},
		"amount":          {"type": "integer", "minimum": 1, "maximum": browserMaxScrollAmount, "default": browserDefaultScrollAmount, "description": "Scroll amount in CSS pixels."},
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

var browserActionContract = newGUIContract("action", browserElementKeys, browserActionParams(), []guiActionSpec{
	{Name: "navigate", Summary: "open a URL in the current tab and wait until the document is ready", Required: []string{"url"}, Optional: []string{"timeout"}},
	{Name: "click", Summary: "click an element or a viewport point", Locator: guiLocatorElementOrPoint, Optional: []string{"button", "click_count"}},
	{Name: "double_click", Aliases: []string{"dblclick"}, Summary: "double-click an element or a viewport point", Locator: guiLocatorElementOrPoint, Optional: []string{"button", "click_count"}, Validate: validateDoubleClickCount},
	{Name: "focus", Summary: "focus an element", Locator: guiLocatorElement},
	{Name: "type", Summary: "focus an element and insert text at its caret", Locator: guiLocatorElement, Required: []string{"text"}},
	{Name: "fill", Summary: "replace the value of an input, textarea, select, or contenteditable; empty text clears it", Locator: guiLocatorElement, Required: []string{"text"}, AllowEmpty: []string{"text"}},
	{Name: "press", Summary: "press a key or chord in the page, e.g. Enter or Control+a", Required: []string{"key"}},
	{Name: "keyboard_type", Aliases: []string{"keyboard_inserttext"}, Summary: "insert text into whatever currently has focus", Required: []string{"text"}},
	{Name: "keydown", Summary: "send a single raw key-down event to the focused element", Required: []string{"key"}},
	{Name: "keyup", Summary: "send a single raw key-up event to the focused element", Required: []string{"key"}},
	{Name: "hover", Summary: "move the mouse over an element or a viewport point", Locator: guiLocatorElementOrPoint},
	{Name: "select", Summary: "choose a select option by value", Locator: guiLocatorElement, Required: []string{"value"}, AllowEmpty: []string{"value"}},
	{Name: "check", Summary: "set a checkbox or radio to checked", Locator: guiLocatorElement},
	{Name: "uncheck", Summary: "set a checkbox to unchecked", Locator: guiLocatorElement},
	{Name: "scroll", Summary: "scroll the page, a scrollable element, or at a viewport point", Locator: guiLocatorAnyOptional, Optional: []string{"direction", "amount"}},
	{Name: "scroll_into_view", Aliases: []string{"scrollintoview"}, Summary: "scroll an element into the viewport", Locator: guiLocatorElement},
	{Name: "drag", Summary: "press on a source, move, and release on a drop target", Optional: []string{"ref", "selector", "x", "y", "target_ref", "target_selector", "to_x", "to_y", "button"}, Validate: validateBrowserDrag},
	{Name: "upload", Summary: "set workspace files on a file input", Locator: guiLocatorElement, Required: []string{"files"}},
	{Name: "wait", Summary: "with a target, wait until it exists (timeout); without one, pause for duration_ms", Locator: guiLocatorElementOptional, Optional: []string{"timeout", "duration_ms"}, Validate: validateBrowserWait},
	{Name: "go_back", Summary: "go back one history entry in the current tab", Optional: []string{"timeout"}},
	{Name: "go_forward", Summary: "go forward one history entry in the current tab", Optional: []string{"timeout"}},
	{Name: "reload", Summary: "reload the current tab and wait until it is ready", Optional: []string{"timeout"}},
	{Name: "tab_new", Summary: "open a new tab, optionally at a URL", Optional: []string{"url"}},
	{Name: "tab_select", Summary: "make a tab the current tab by index", Required: []string{"tab_index"}},
	{Name: "tab_close", Summary: "close a tab by index, or the current tab", Optional: []string{"tab_index"}},
})

func browserObserveParams() map[string]map[string]any {
	return map[string]map[string]any{
		"ref":       {"type": "string", "description": "Element ref from snapshot or screenshot_annotate. Scopes get_content and get_html."},
		"selector":  {"type": "string", "description": "CSS selector to scope get_content or get_html when no ref is available."},
		"script":    {"type": "string", "description": "JavaScript expression for evaluate. Keep it short and read-only unless the task requires otherwise."},
		"full_page": {"type": "boolean", "default": false, "description": "Capture the whole document instead of the viewport for screenshot."},
	}
}

var browserObserveContract = newGUIContract("observe", browserElementKeys, browserObserveParams(), []guiActionSpec{
	{Name: "snapshot", Summary: "list interactive elements with refs"},
	{Name: "get_content", Summary: "readable text of the page or one element", Locator: guiLocatorElementOptional},
	{Name: "get_html", Summary: "outerHTML of the document or innerHTML of one element", Locator: guiLocatorElementOptional},
	{Name: "screenshot", Summary: "save a screenshot to the workspace", Optional: []string{"full_page"}},
	{Name: "screenshot_annotate", Summary: "save a screenshot with element refs drawn on it"},
	{Name: "evaluate", Summary: "evaluate a JavaScript expression in the page", Required: []string{"script"}},
	{Name: "get_url", Summary: "current URL"},
	{Name: "get_title", Summary: "current document title"},
	{Name: "pdf", Summary: "print the page to PDF"},
	{Name: "tab_list", Summary: "list open page tabs"},
})

func browserRemoteSessionParams() map[string]map[string]any {
	return map[string]map[string]any{
		"session_id": {"type": "string", "description": "Target/session ID returned by create or status."},
		"url":        {"type": "string", "description": "Optional URL to open when creating a target."},
	}
}

var browserRemoteSessionContract = newGUIContract("action", nil, browserRemoteSessionParams(), []guiActionSpec{
	{Name: "create", Summary: "expose a page target for CDP clients, creating one when url is given", Optional: []string{"url"}},
	{Name: "status", Summary: "list the CDP endpoint and open page targets"},
	{Name: "close", Summary: "close the target behind a session", Required: []string{"session_id"}},
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
