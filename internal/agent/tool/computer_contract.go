package tools

import "errors"

const (
	computerDefaultWaitMS       = 1000
	computerMaxWaitMS           = 10000
	computerMaxScrollAmount     = 10000
	computerDefaultScrollAmount = 500
)

var computerElementKeys = []string{"ref"}

func computerActionParams() map[string]map[string]any {
	return map[string]map[string]any{
		"ref":         {"type": "string", "description": "Element ref such as e3 from a desktop observation snapshot. Preferred over coordinates."},
		"x":           {"type": "integer", "minimum": 0, "description": "X in desktop pixels when no ref is given, or the drag start."},
		"y":           {"type": "integer", "minimum": 0, "description": "Y in desktop pixels; always paired with x."},
		"to_x":        {"type": "integer", "minimum": 0, "description": "Drag end X in desktop pixels."},
		"to_y":        {"type": "integer", "minimum": 0, "description": "Drag end Y in desktop pixels; always paired with to_x."},
		"button":      {"type": "string", "enum": []string{"left", "middle", "right"}, "description": "Mouse button for click, double_click, and drag. Defaults to left."},
		"click_count": {"type": "integer", "minimum": 1, "maximum": 3, "description": "Number of clicks for click. Defaults to 1; double_click is always 2."},
		"button_mask": {"type": "integer", "minimum": 0, "maximum": 255, "description": "Raw RFB button mask for mouse_move and pointer. 0 releases every button."},
		"direction":   {"type": "string", "enum": []string{"up", "down", "left", "right"}, "description": "Scroll direction. Defaults to down."},
		"amount":      {"type": "integer", "minimum": 1, "maximum": computerMaxScrollAmount, "default": computerDefaultScrollAmount, "description": "Scroll amount in pixels; delivered as discrete wheel steps of about 120 px."},
		"duration_ms": {"type": "integer", "minimum": 1, "maximum": computerMaxWaitMS, "default": computerDefaultWaitMS, "description": "Pause length in milliseconds for wait."},
		"key":         {"type": "string", "description": "Key or key chord for key, e.g. Enter, Escape, Control+a."},
		"text":        {"type": "string", "description": "Text for type or fill. fill accepts an empty string to clear the field."},
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

var computerActionContract = newGUIContract("action", computerElementKeys, computerActionParams(), []guiActionSpec{
	{Name: "click", Summary: "click an element ref or a desktop point", Locator: guiLocatorElementOrPoint, Optional: []string{"button", "click_count"}},
	{Name: "double_click", Summary: "double-click an element ref or a desktop point", Locator: guiLocatorElementOrPoint, Optional: []string{"button", "click_count"}, Validate: validateDoubleClickCount},
	{Name: "type", Summary: "insert text at the caret of a ref, or of the focused widget when no ref is given", Locator: guiLocatorElementOptional, Required: []string{"text"}},
	{Name: "fill", Summary: "replace the whole text of a ref, or of the focused widget when no ref is given; empty text clears it", Locator: guiLocatorElementOptional, Required: []string{"text"}, AllowEmpty: []string{"text"}},
	{Name: "key", Summary: "press a key or chord on the desktop", Required: []string{"key"}},
	{Name: "scroll", Summary: "scroll at a ref, a point, or the desktop centre", Locator: guiLocatorAnyOptional, Optional: []string{"direction", "amount"}},
	{Name: "drag", Summary: "press at x/y, move, and release at to_x/to_y", Locator: guiLocatorPoint, Optional: []string{"to_x", "to_y", "button"}, Validate: validateComputerDrag},
	{Name: "wait", Summary: "pause for duration_ms (amount is the legacy name)", Optional: []string{"duration_ms", "amount"}, Validate: validateComputerWait},
	{Name: "mouse_move", Summary: "move the pointer, keeping button_mask held", Locator: guiLocatorPoint, Optional: []string{"button_mask"}},
	{Name: "pointer", Summary: "send a raw pointer state at x/y with button_mask", Locator: guiLocatorPoint, Optional: []string{"button_mask"}},
})

func computerObserveParams() map[string]map[string]any {
	return map[string]map[string]any{
		"limit": {"type": "integer", "minimum": 1, "maximum": a11ySnapshotMaxLimit, "default": a11ySnapshotDefaultLimit, "description": "Maximum number of elements to return from snapshot."},
	}
}

var computerObserveContract = newGUIContract("observe", nil, computerObserveParams(), []guiActionSpec{
	{Name: "snapshot", Summary: "accessibility listing of on-screen elements with refs, geometry, and states", Optional: []string{"limit"}},
	{Name: "screenshot", Summary: "save a desktop screenshot to the workspace"},
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
