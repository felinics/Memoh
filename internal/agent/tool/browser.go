package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/gorilla/websocket"

	displaypkg "github.com/felinics/memoh/internal/display"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	browserCDPAddress                   = "127.0.0.1:9222"
	browserCDPBaseURL                   = "http://" + browserCDPAddress
	browserToolTimeout                  = 45 * time.Second
	browserStartupTimeout               = 12 * time.Second
	computerDisplayStartupTimeout int32 = 1200
	screenshotSubdir                    = ".memoh/screenshots"
	defaultComputerWidth                = 1280
	defaultComputerHeight               = 800
	rfbButtonLeft                 byte  = 1
	rfbButtonMiddle               byte  = 2
	rfbButtonRight                byte  = 4
	rfbWheelUp                    byte  = 8
	rfbWheelDown                  byte  = 16
	rfbWheelLeft                  byte  = 32
	rfbWheelRight                 byte  = 64
)

type BrowserProvider struct {
	logger     *slog.Logger
	settings   *settings.Service
	containers bridge.Provider
	display    *displaypkg.Service
	dataRoot   string
}

func NewBrowserProvider(log *slog.Logger, settingsSvc *settings.Service, containers bridge.Provider, displayWorkspace displaypkg.Workspace, dataRoot string) *BrowserProvider {
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(dataRoot) == "" {
		dataRoot = "/data"
	}
	var displaySvc *displaypkg.Service
	if displayWorkspace != nil {
		displaySvc = displaypkg.NewService(log, displayWorkspace)
	}
	return &BrowserProvider{
		logger:     log.With(slog.String("tool", "browser")),
		settings:   settingsSvc,
		containers: containers,
		display:    displaySvc,
		dataRoot:   dataRoot,
	}
}

// Usage frames the workspace browser/desktop tool group. Injected only when
// these tools are registered (display-enabled sessions).
func (*BrowserProvider) Usage(_ context.Context, session SessionContext, available AvailableTools) string {
	var parts []string
	browserRefs := available.Refs(ToolBrowserObserve(), ToolBrowserAction())
	switch len(browserRefs) {
	case 2:
		parts = append(parts, "**Browser** ("+strings.Join(browserRefs, ", ")+"): Web pages in Chrome. Observe before acting; prefer element refs from snapshot over CSS selectors.")
	case 1:
		if ref, ok := available.Ref(ToolBrowserObserve()); ok {
			parts = append(parts, "**Browser** ("+ref+"): Web pages in Chrome. Observe before acting; prefer element refs from snapshot over CSS selectors.")
		} else {
			parts = append(parts, "**Browser** ("+browserRefs[0]+"): Web pages in Chrome.")
		}
	}
	desktopRefs := available.Refs(ToolComputerObserve(), ToolComputerAction())
	switch len(desktopRefs) {
	case 2:
		parts = append(parts, "**Computer** ("+strings.Join(desktopRefs, ", ")+"): Whole-desktop fallback for native dialogs, non-browser apps, or when the browser path fails. Start with a snapshot to get an accessibility tree with element refs, then drive actions with those refs; raw coordinates are a last-resort fallback.")
	case 1:
		if ref, ok := available.Ref(ToolComputerObserve()); ok {
			parts = append(parts, "**Computer** ("+ref+"): Whole-desktop fallback for native dialogs, non-browser apps, or when the browser path fails.")
		} else {
			parts = append(parts, "**Computer** ("+desktopRefs[0]+"): Whole-desktop fallback for native dialogs, non-browser apps, or when the browser path fails; raw coordinates are a last-resort fallback.")
		}
	}
	if ref, ok := available.Ref(ToolBrowserRemoteSession()); ok {
		if len(browserRefs) > 0 || len(desktopRefs) > 0 {
			parts = append(parts, ref+": Only when running Playwright or other CDP automation inside the workspace is clearly better than the GUI tools above.")
		} else {
			parts = append(parts, ref+": Use for code-driven Playwright or other CDP automation inside the workspace.")
		}
	}
	hasObserve := available.Has(ToolBrowserObserve()) || available.Has(ToolComputerObserve())
	if hasObserve {
		readHint := "the returned path can be used by later workspace actions when needed."
		if session.SupportsImageInput {
			if readRef, ok := available.Ref(ToolRead()); ok {
				readHint = "Read the returned path with " + readRef + " when you need the image."
			}
		}
		parts = append(parts, "**Screenshots**: Observe tools save screenshots to a workspace path; they are not attached to the conversation. "+readHint)
	}
	if len(parts) == 0 {
		return ""
	}
	return usageSection("Workspace browser & desktop", append([]string{
		"This bot has a headed workspace display (Chrome on a virtual desktop). Use GUI tools only when the task needs on-screen interaction.",
	}, parts...))
}

func (p *BrowserProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
	if p == nil || p.settings == nil {
		return nil, nil
	}
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, nil
	}
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil, nil
	}
	if !botSettings.DisplayEnabled {
		return nil, nil
	}
	sess := session
	return []sdk.Tool{
		{
			Name:        ToolBrowserAction().String(),
			Description: "Operate the current workspace browser tab. Prefer element refs from an observation result over CSS selectors; use selectors only as a fallback. Use fill to replace input values, type to append text, and press for shortcuts or submit keys. After navigation or UI-changing actions, observe again only when the next step depends on the changed state.",
			Parameters: browserObjectSchema(map[string]any{
				"action":          map[string]any{"type": "string", "enum": []string{"navigate", "click", "double_click", "focus", "type", "fill", "press", "hover", "select", "check", "uncheck", "scroll", "scroll_into_view", "drag", "upload", "wait", "go_back", "go_forward", "reload", "tab_new", "tab_select", "tab_close"}, "description": "Browser action to perform. Compatibility aliases dblclick, scrollintoview, keyboard_type, and keyboard_inserttext are also accepted; keydown and keyup dispatch a single raw key event."},
				"url":             map[string]any{"type": "string", "description": "URL to open for navigate or tab_new."},
				"ref":             map[string]any{"type": "string", "description": "Element ref such as e12 from a browser observation snapshot or screenshot annotation. Preferred over selector."},
				"selector":        map[string]any{"type": "string", "description": "CSS selector for the target element when no ref is available."},
				"text":            map[string]any{"type": "string", "description": "Text for type or fill."},
				"key":             map[string]any{"type": "string", "description": "Key or key chord for press, e.g. Enter, Tab, Escape, Control+a."},
				"value":           map[string]any{"type": "string", "description": "Option value for select."},
				"target_ref":      map[string]any{"type": "string", "description": "Drop target ref for drag, preferred over target_selector."},
				"target_selector": map[string]any{"type": "string", "description": "Drop target CSS selector for drag when no target_ref is available."},
				"files":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Workspace file paths to upload."},
				"tab_index":       map[string]any{"type": "integer", "minimum": 0, "description": "Tab index for tab_select or tab_close."},
				"direction":       map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}, "description": "Scroll direction. Defaults to down."},
				"amount":          map[string]any{"type": "integer", "minimum": 1, "maximum": 5000, "default": 500, "description": "Scroll amount in pixels."},
				"timeout":         map[string]any{"type": "integer", "minimum": 1, "maximum": 45000, "default": 1000, "description": "Timeout in milliseconds for wait or navigation readiness."},
			}, []string{"action"}),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execBrowserAction(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolBrowserObserve().String(),
			Description: "Inspect the current workspace browser without changing page state. Prefer snapshot for interactive elements and get_content for readable text. Use screenshot_annotate only when visual layout matters or you need rendered-page refs. Use evaluate only for small DOM queries or page-state checks. Screenshots are saved to a workspace path and are not attached automatically.",
			Parameters: browserObjectSchema(map[string]any{
				"observe":   map[string]any{"type": "string", "enum": []string{"snapshot", "get_content", "screenshot_annotate", "screenshot", "get_html", "evaluate", "get_url", "get_title", "pdf", "tab_list"}, "description": "What to observe from the page."},
				"ref":       map[string]any{"type": "string", "description": "Element ref from snapshot or screenshot_annotate. Scopes get_content/get_html and evaluate helper use."},
				"selector":  map[string]any{"type": "string", "description": "CSS selector to scope get_content or get_html when no ref is available."},
				"script":    map[string]any{"type": "string", "description": "JavaScript expression to evaluate. Keep it short and read-only unless the task requires otherwise."},
				"full_page": map[string]any{"type": "boolean", "default": false, "description": "Capture a full-page screenshot for screenshot."},
			}, []string{"observe"}),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execBrowserObserve(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolComputerObserve().String(),
			Description: "Inspect the workspace desktop without changing state. Use snapshot for an accessibility-tree listing of interactive UI elements with refs for later desktop actions. Use screenshot only when accessibility is unavailable or you need visual layout; the image is saved to a workspace path and is not attached automatically.",
			Parameters: browserObjectSchema(map[string]any{
				"observe": map[string]any{"type": "string", "enum": []string{"snapshot", "screenshot"}, "description": "What to observe from the desktop."},
			}, []string{"observe"}),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execComputerObserve(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolComputerAction().String(),
			Description: "Drive the workspace desktop. Prefer refs from a desktop observation snapshot for click/double_click/type/fill/scroll; coordinates (x, y) are only a fallback when no ref applies (native dialogs, raw drags, pointer hovers). For in-page browser targets, prefer browser-specific actions when they are available.",
			Parameters: browserObjectSchema(map[string]any{
				"action":      map[string]any{"type": "string", "enum": []string{"click", "double_click", "type", "fill", "key", "scroll", "drag", "wait", "mouse_move", "pointer"}, "description": "Desktop action to perform."},
				"ref":         map[string]any{"type": "string", "description": "Element ref such as e3 from a desktop observation snapshot. Preferred over coordinates for click/double_click/type/fill/scroll."},
				"x":           map[string]any{"type": "integer", "minimum": 0, "description": "X coordinate in desktop pixels (used when no ref is provided or as fallback)."},
				"y":           map[string]any{"type": "integer", "minimum": 0, "description": "Y coordinate in desktop pixels (used when no ref is provided or as fallback)."},
				"to_x":        map[string]any{"type": "integer", "minimum": 0, "description": "Destination X coordinate for drag."},
				"to_y":        map[string]any{"type": "integer", "minimum": 0, "description": "Destination Y coordinate for drag."},
				"button":      map[string]any{"type": "string", "enum": []string{"left", "middle", "right"}, "description": "Mouse button. Defaults to left."},
				"button_mask": map[string]any{"type": "integer", "minimum": 0, "maximum": 255, "description": "Raw RFB button mask for pointer actions."},
				"direction":   map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}, "description": "Scroll direction. Defaults to down."},
				"amount":      map[string]any{"type": "integer", "minimum": 1, "maximum": 10000, "default": 500, "description": "Scroll amount or wait duration in milliseconds."},
				"key":         map[string]any{"type": "string", "description": "Key or key chord, e.g. Enter, Escape, Control+a."},
				"text":        map[string]any{"type": "string", "description": "Text to type or fill into the target."},
			}, []string{"action"}),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execComputerAction(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolBrowserRemoteSession().String(),
			Description: "Advanced escape hatch for code-driven automation. Exposes the workspace Chrome CDP endpoint for chromium.connectOverCDP or other CDP clients.",
			Parameters: browserObjectSchema(map[string]any{
				"action":     map[string]any{"type": "string", "enum": []string{"create", "close", "status"}, "description": "Session action to perform."},
				"session_id": map[string]any{"type": "string", "description": "Target/session ID returned by create or status."},
				"url":        map[string]any{"type": "string", "description": "Optional URL to open when creating a target."},
			}, []string{"action"}),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execRemoteSession(ctx.Context, sess, inputAsMap(input))
			},
		},
	}, nil
}

func browserObjectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func sessionBotID(session SessionContext) (string, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return "", errors.New("bot_id is required")
	}
	return botID, nil
}

func (p *BrowserProvider) execBrowserAction(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	botID, err := sessionBotID(session)
	if err != nil {
		return nil, err
	}
	if err := p.ensureDisplayEnabled(ctx, botID); err != nil {
		return nil, err
	}
	action := StringArg(args, "action")
	if action == "" {
		return nil, errors.New("action is required")
	}
	runCtx, cancel := context.WithTimeout(ctx, browserToolTimeout)
	defer cancel()
	data, err := p.runCDPAction(runCtx, botID, args)
	if err != nil {
		return nil, err
	}
	return p.browserActionResult(ctx, botID, data), nil
}

func (p *BrowserProvider) execBrowserObserve(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	observe := StringArg(args, "observe")
	if observe == "" {
		return nil, errors.New("observe is required")
	}
	payload := map[string]any{"action": observe}
	for _, key := range []string{"ref", "selector", "script"} {
		if v := StringArg(args, key); v != "" {
			payload[key] = v
		}
	}
	if v, ok, _ := BoolArg(args, "full_page"); ok {
		payload["full_page"] = v
	}
	return p.execBrowserAction(ctx, session, payload)
}

func (p *BrowserProvider) execRemoteSession(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	botID, err := sessionBotID(session)
	if err != nil {
		return nil, err
	}
	if err := p.ensureDisplayEnabled(ctx, botID); err != nil {
		return nil, err
	}
	client, err := p.ensureCDP(ctx, botID)
	if err != nil {
		return nil, err
	}
	action := StringArg(args, "action")
	switch action {
	case "create":
		targetURL := StringArg(args, "url")
		target, err := p.createOrActiveTarget(ctx, client, targetURL)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"id":                      target.ID,
			"session_id":              target.ID,
			"status":                  "active",
			"cdp_url":                 browserCDPBaseURL,
			"ws_endpoint":             target.WebSocketDebuggerURL,
			"web_socket_debugger_url": target.WebSocketDebuggerURL,
			"connect_over_cdp":        browserCDPBaseURL,
			"target":                  target.publicMap(),
		}, nil
	case "status":
		targets, err := p.listTargets(ctx, client)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"status":           "active",
			"cdp_url":          browserCDPBaseURL,
			"connect_over_cdp": browserCDPBaseURL,
			"targets":          publicTargets(targets),
		}, nil
	case "close":
		sessionID := StringArg(args, "session_id")
		if sessionID == "" {
			return nil, errors.New("session_id is required for close")
		}
		return p.closeTarget(ctx, client, sessionID)
	default:
		return nil, fmt.Errorf("unknown session action: %s", action)
	}
}

func (p *BrowserProvider) execComputerObserve(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	observe := strings.TrimSpace(StringArg(args, "observe"))
	if observe == "" {
		return nil, errors.New("observe is required")
	}
	switch observe {
	case "screenshot":
		return p.execComputerScreenshot(ctx, session)
	case "snapshot":
		return p.execComputerSnapshot(ctx, session)
	default:
		return nil, fmt.Errorf("unknown computer observe: %s", observe)
	}
}

func (p *BrowserProvider) execComputerScreenshot(ctx context.Context, session SessionContext) (any, error) {
	botID, err := p.requireComputerDisplay(session)
	if err != nil {
		return nil, err
	}
	if _, err := p.ensureComputerDisplay(ctx, botID); err != nil {
		return nil, err
	}
	img, mime, err := p.display.Screenshot(ctx, botID)
	if err != nil {
		return nil, err
	}
	return p.buildScreenshotBytesResult(ctx, botID, img, mime, p.screenshotDir(), nil), nil
}

func (p *BrowserProvider) execComputerSnapshot(ctx context.Context, session SessionContext) (any, error) {
	botID, err := p.requireComputerDisplay(session)
	if err != nil {
		return nil, err
	}
	client, err := p.ensureComputerDisplay(ctx, botID)
	if err != nil {
		return nil, err
	}
	snapshot, err := computerA11ySnapshot(ctx, client)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"snapshot":  snapshot.Lines,
		"ref_count": len(snapshot.Items),
	}, nil
}

func (p *BrowserProvider) execComputerAction(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	botID, err := p.requireComputerDisplay(session)
	if err != nil {
		return nil, err
	}
	action := StringArg(args, "action")
	if action == "" {
		return nil, errors.New("action is required")
	}
	if _, err := p.ensureComputerDisplay(ctx, botID); err != nil {
		return nil, err
	}
	ref := normalizeBrowserRef(StringArg(args, "ref"))
	switch action {
	case "mouse_move", "pointer":
		x, y, err := requiredPoint(args)
		if err != nil {
			return nil, err
		}
		mask := byte(0)
		if value, ok, err := IntArg(args, "button_mask"); err != nil {
			return nil, err
		} else if ok {
			mask = clampByte(value)
		}
		if err := p.sendDisplayInputs(ctx, botID, displaypkg.ControlInput{Type: "pointer", X: x, Y: y, ButtonMask: mask}); err != nil {
			return nil, err
		}
		return map[string]any{"moved": true, "x": x, "y": y, "button_mask": mask}, nil
	case "click", "double_click":
		count := 1
		if action == "double_click" {
			count = 2
		}
		if ref != "" {
			result, err := p.clickByRef(ctx, botID, ref, count)
			if err != nil {
				return nil, err
			}
			if result != nil {
				return result, nil
			}
		}
		x, y, err := requiredPoint(args)
		if err != nil {
			return nil, fmt.Errorf("click requires ref or x/y: %w", err)
		}
		mask := mouseButtonMask(StringArg(args, "button"))
		for i := 0; i < count; i++ {
			if err := p.pointerClick(ctx, botID, x, y, mask); err != nil {
				return nil, err
			}
		}
		return map[string]any{"clicked": true, "x": x, "y": y, "button": buttonName(mask), "click_count": count}, nil
	case "type", "fill":
		text := StringArg(args, "text")
		if text == "" {
			return nil, errors.New("text is required")
		}
		if ref != "" {
			result, err := p.editByRef(ctx, botID, ref, text, action == "fill")
			if err != nil {
				return nil, err
			}
			if result != nil {
				return result, nil
			}
		}
		if err := p.typeText(ctx, botID, text); err != nil {
			return nil, err
		}
		out := map[string]any{"typed": text}
		if action == "fill" {
			out = map[string]any{"filled": text}
		}
		return out, nil
	case "drag":
		x, y, err := requiredPoint(args)
		if err != nil {
			return nil, err
		}
		toX, toY, err := requiredTargetPoint(args)
		if err != nil {
			return nil, err
		}
		if err := p.pointerDrag(ctx, botID, x, y, toX, toY, mouseButtonMask(StringArg(args, "button"))); err != nil {
			return nil, err
		}
		return map[string]any{"dragged": true, "x": x, "y": y, "to_x": toX, "to_y": toY}, nil
	case "scroll":
		x, y := optionalPoint(args, defaultComputerWidth/2, defaultComputerHeight/2)
		if ref != "" {
			if entry, err := lookupComputerRef(ctx, p.containers, botID, ref); err == nil && entry != nil {
				x, y = entry.CenterX, entry.CenterY
			}
		}
		direction := StringArg(args, "direction")
		if direction == "" {
			direction = "down"
		}
		amount, err := intArgOr(args, "amount", 500)
		if err != nil {
			return nil, err
		}
		if err := p.pointerScroll(ctx, botID, x, y, direction, amount); err != nil {
			return nil, err
		}
		return map[string]any{"scrolled": direction, "amount": amount, "x": x, "y": y}, nil
	case "key":
		key := StringArg(args, "key")
		if key == "" {
			return nil, errors.New("key is required")
		}
		if err := p.typeKeyChord(ctx, botID, key); err != nil {
			return nil, err
		}
		return map[string]any{"pressed": key}, nil
	case "wait":
		amount, err := intArgOr(args, "amount", 1000)
		if err != nil {
			return nil, err
		}
		if err := sleepContext(ctx, time.Duration(amount)*time.Millisecond); err != nil {
			return nil, err
		}
		return map[string]any{"waited_ms": amount}, nil
	default:
		return nil, fmt.Errorf("unknown computer action: %s", action)
	}
}

func (p *BrowserProvider) clickByRef(ctx context.Context, botID, ref string, count int) (map[string]any, error) {
	client, err := p.containers.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	result, err := computerA11yClick(ctx, client, ref)
	if err != nil {
		return nil, err
	}
	switch {
	case result.OK:
		out := map[string]any{"clicked": true, "ref": ref, "click_count": count, "via": "a11y"}
		if count == 2 {
			out["double_clicked"] = true
		}
		return out, nil
	case result.Fallback != nil:
		mask := rfbButtonLeft
		for i := 0; i < count; i++ {
			if err := p.pointerClick(ctx, botID, result.Fallback.X, result.Fallback.Y, mask); err != nil {
				return nil, err
			}
		}
		return map[string]any{"clicked": true, "x": result.Fallback.X, "y": result.Fallback.Y, "ref": ref, "click_count": count, "via": "rfb_fallback"}, nil
	default:
		if result.Error != "" {
			return nil, fmt.Errorf("a11y click %s failed: %s", ref, result.Error)
		}
		return nil, fmt.Errorf("a11y click %s failed without diagnostic", ref)
	}
}

func (p *BrowserProvider) editByRef(ctx context.Context, botID, ref, text string, replace bool) (map[string]any, error) {
	client, err := p.containers.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	result, err := computerA11yEdit(ctx, client, ref, text, replace)
	if err != nil {
		return nil, err
	}
	switch {
	case result.OK:
		key := "typed"
		if replace {
			key = "filled"
		}
		return map[string]any{key: text, "ref": ref, "via": "a11y"}, nil
	case result.Fallback != nil:
		if err := p.pointerClick(ctx, botID, result.Fallback.X, result.Fallback.Y, rfbButtonLeft); err != nil {
			return nil, err
		}
		if err := p.typeText(ctx, botID, text); err != nil {
			return nil, err
		}
		key := "typed"
		if replace {
			key = "filled"
		}
		return map[string]any{key: text, "ref": ref, "x": result.Fallback.X, "y": result.Fallback.Y, "via": "rfb_fallback"}, nil
	default:
		if result.Error != "" {
			return nil, fmt.Errorf("a11y edit %s failed: %s", ref, result.Error)
		}
		return nil, fmt.Errorf("a11y edit %s failed without diagnostic", ref)
	}
}

func (p *BrowserProvider) requireComputerDisplay(session SessionContext) (string, error) {
	botID, err := sessionBotID(session)
	if err != nil {
		return "", err
	}
	if p.display == nil {
		return "", errors.New("workspace display service is not configured")
	}
	return botID, nil
}

const computerDisplayReadyCommand = `# memoh-computer-display-ready-probe
test -S /tmp/.X11-unix/X99 &&
awk -v port="$(printf '%04X' 5999)" 'toupper($2) ~ ":" port "$" && $4 == "0A" { found = 1 } END { exit found ? 0 : 1 }' /proc/net/tcp /proc/net/tcp6 2>/dev/null`

func (p *BrowserProvider) ensureComputerDisplay(ctx context.Context, botID string) (*bridge.Client, error) {
	if p.containers == nil {
		return nil, errors.New("workspace runtime provider is not configured")
	}
	client, err := p.containers.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	if computerDisplayReady(ctx, client) {
		return client, nil
	}

	result, err := client.Exec(ctx, "/bin/sh /opt/memoh/scripts/display-prepare.sh", "/", computerDisplayStartupTimeout)
	if err != nil {
		return nil, fmt.Errorf("prepare workspace desktop: %w", err)
	}
	if result == nil || result.ExitCode != 0 {
		diagnostic := "display preparation failed"
		if result != nil {
			diagnostic = strings.TrimSpace(result.Stderr)
			if diagnostic == "" {
				diagnostic = strings.TrimSpace(result.Stdout)
			}
			if diagnostic == "" {
				diagnostic = fmt.Sprintf("display preparation exited with code %d", result.ExitCode)
			}
		}
		return nil, errors.New(diagnostic)
	}
	if !computerDisplayReady(ctx, client) {
		return nil, errors.New("workspace desktop did not become reachable after preparation")
	}
	return client, nil
}

func computerDisplayReady(ctx context.Context, client *bridge.Client) bool {
	if client == nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := client.Exec(probeCtx, computerDisplayReadyCommand, "/", 3)
	return err == nil && result != nil && result.ExitCode == 0
}

func (p *BrowserProvider) ensureDisplayEnabled(ctx context.Context, botID string) error {
	if p.settings == nil {
		return errors.New("settings service is not configured")
	}
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return err
	}
	if !botSettings.DisplayEnabled {
		return errors.New("workspace desktop is not enabled for this bot")
	}
	return nil
}

func (p *BrowserProvider) ensureCDP(ctx context.Context, botID string) (*bridge.Client, error) {
	if p.containers == nil {
		return nil, errors.New("workspace runtime provider is not configured")
	}
	client, err := p.containers.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	if p.cdpReachable(ctx, client) {
		return client, nil
	}
	if err := p.startDesktopBrowser(ctx, client); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(browserStartupTimeout)
	for time.Now().Before(deadline) {
		if p.cdpReachable(ctx, client) {
			return client, nil
		}
		if err := sleepContext(ctx, 300*time.Millisecond); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("workspace desktop browser CDP endpoint is not reachable")
}

func (p *BrowserProvider) cdpReachable(ctx context.Context, client *bridge.Client) bool {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	body, status, err := p.cdpHTTP(reqCtx, client, http.MethodGet, "/json/version", nil)
	if err != nil || status >= 400 || len(body) == 0 {
		return false
	}
	return true
}

func (*BrowserProvider) startDesktopBrowser(ctx context.Context, client *bridge.Client) error {
	const script = `set -eu
export DISPLAY=:99
if [ ! -S /tmp/.X11-unix/X99 ]; then
  echo "workspace desktop X socket is not ready; open or prepare the bot desktop first" >&2
  exit 2
fi
BROWSER=""
for candidate in google-chrome-stable google-chrome chromium chromium-browser; do
  if command -v "$candidate" >/dev/null 2>&1; then
    BROWSER="$(command -v "$candidate")"
    break
  fi
done
if [ -z "$BROWSER" ]; then
  echo "Chrome or Chromium is not installed in the workspace desktop" >&2
  exit 3
fi
BROWSER_PIDS=""
HAS_CDP=0
for proc_dir in /proc/[0-9]*; do
  [ -d "$proc_dir" ] || continue
  pid="${proc_dir#/proc/}"
  cmdline="$(tr '\000' '\n' <"$proc_dir/cmdline" 2>/dev/null || true)"
  printf '%s\n' "$cmdline" | grep -Eq '(^|/)(google-chrome-stable|google-chrome|chromium|chromium-browser|chrome)$' || continue
  BROWSER_PIDS="$BROWSER_PIDS $pid"
  if ! printf '%s\n' "$cmdline" | grep -Eq '^--type=' && printf '%s\n' "$cmdline" | grep -Fq -- '--remote-debugging-port=9222'; then
    HAS_CDP=1
  fi
done
if [ "$HAS_CDP" = "1" ]; then
  exit 0
fi
for pid in $BROWSER_PIDS; do
  kill "$pid" 2>/dev/null || true
done
sleep 1
for pid in $BROWSER_PIDS; do
  kill -9 "$pid" 2>/dev/null || true
done
rm -f /tmp/memoh-display-browser/SingletonLock /tmp/memoh-display-browser/SingletonSocket /tmp/memoh-display-browser/SingletonCookie
nohup "$BROWSER" \
  --no-sandbox \
  --disable-dev-shm-usage \
  --disable-gpu \
  --no-first-run \
  --no-default-browser-check \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 \
  --remote-allow-origins='*' \
  --user-data-dir=/tmp/memoh-display-browser \
  about:blank >/tmp/memoh-browser.log 2>&1 &
`
	result, err := client.Exec(ctx, script, "/", 20)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		msg := strings.TrimSpace(result.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(result.Stdout)
		}
		return fmt.Errorf("start workspace desktop browser failed: %s", msg)
	}
	return nil
}

func (*BrowserProvider) cdpHTTP(ctx context.Context, client *bridge.Client, method, path string, body io.Reader) ([]byte, int, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return client.DialContext(ctx, network, address)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, method, browserCDPBaseURL+path, body)
	if err != nil {
		return nil, 0, err
	}
	resp, err := httpClient.Do(req) //nolint:gosec // request is tunneled to the bot workspace loopback CDP endpoint.
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, readErr := io.ReadAll(resp.Body)
	return data, resp.StatusCode, readErr
}

type cdpTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	Title                string `json:"title"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func (t cdpTarget) publicMap() map[string]any {
	return map[string]any{
		"id":                      t.ID,
		"type":                    t.Type,
		"url":                     t.URL,
		"title":                   t.Title,
		"web_socket_debugger_url": t.WebSocketDebuggerURL,
	}
}

func publicTargets(targets []cdpTarget) []map[string]any {
	out := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		if target.Type == "page" {
			out = append(out, target.publicMap())
		}
	}
	return out
}

func (p *BrowserProvider) listTargets(ctx context.Context, client *bridge.Client) ([]cdpTarget, error) {
	body, status, err := p.cdpHTTP(ctx, client, http.MethodGet, "/json/list", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("list CDP targets failed (HTTP %d): %s", status, string(body))
	}
	var targets []cdpTarget
	if err := json.Unmarshal(body, &targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func (p *BrowserProvider) activeTarget(ctx context.Context, client *bridge.Client) (cdpTarget, error) {
	targets, err := p.listTargets(ctx, client)
	if err != nil {
		return cdpTarget{}, err
	}
	for _, target := range targets {
		if target.Type == "page" {
			return target, nil
		}
	}
	return p.createTarget(ctx, client, "about:blank")
}

func (p *BrowserProvider) createOrActiveTarget(ctx context.Context, client *bridge.Client, targetURL string) (cdpTarget, error) {
	if strings.TrimSpace(targetURL) != "" {
		return p.createTarget(ctx, client, targetURL)
	}
	return p.activeTarget(ctx, client)
}

func (p *BrowserProvider) createTarget(ctx context.Context, client *bridge.Client, targetURL string) (cdpTarget, error) {
	if strings.TrimSpace(targetURL) == "" {
		targetURL = "about:blank"
	}
	body, status, err := p.cdpHTTP(ctx, client, http.MethodPut, "/json/new?"+url.QueryEscape(targetURL), nil)
	if err != nil {
		return cdpTarget{}, err
	}
	if status >= 400 {
		return cdpTarget{}, fmt.Errorf("create CDP target failed (HTTP %d): %s", status, string(body))
	}
	var target cdpTarget
	if err := json.Unmarshal(body, &target); err != nil {
		return cdpTarget{}, err
	}
	return target, nil
}

func (p *BrowserProvider) activateTarget(ctx context.Context, client *bridge.Client, id string) error {
	body, status, err := p.cdpHTTP(ctx, client, http.MethodGet, "/json/activate/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("activate CDP target failed (HTTP %d): %s", status, string(body))
	}
	return nil
}

func (p *BrowserProvider) closeTarget(ctx context.Context, client *bridge.Client, id string) (any, error) {
	body, status, err := p.cdpHTTP(ctx, client, http.MethodGet, "/json/close/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("close CDP target failed (HTTP %d): %s", status, string(body))
	}
	return map[string]any{"success": true, "message": strings.TrimSpace(string(body))}, nil
}

type cdpConn struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	nextID int
}

type cdpResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (*BrowserProvider) dialCDP(ctx context.Context, client *bridge.Client, target cdpTarget) (*cdpConn, error) {
	if strings.TrimSpace(target.WebSocketDebuggerURL) == "" {
		return nil, errors.New("CDP target does not expose a websocket debugger URL")
	}
	dialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return client.DialContext(ctx, network, address)
		},
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, target.WebSocketDebuggerURL, nil) //nolint:bodyclose // gorilla websocket owns the response body.
	if err != nil {
		return nil, err
	}
	return &cdpConn{conn: conn}, nil
}

func (c *cdpConn) Close() error {
	return c.conn.Close()
}

func (c *cdpConn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	msg := map[string]any{
		"id":     id,
		"method": method,
	}
	if params != nil {
		msg["params"] = params
	}
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		_ = c.conn.SetReadDeadline(deadline)
		_ = c.conn.SetWriteDeadline(deadline)
	}
	if err := c.conn.WriteJSON(msg); err != nil {
		return nil, err
	}
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		var resp cdpResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, err
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("CDP %s failed: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

type cdpPage struct {
	conn *cdpConn
}

func (p *BrowserProvider) connectPage(ctx context.Context, botID string) (*bridge.Client, cdpTarget, *cdpPage, error) {
	client, err := p.ensureCDP(ctx, botID)
	if err != nil {
		return nil, cdpTarget{}, nil, err
	}
	target, err := p.activeTarget(ctx, client)
	if err != nil {
		return nil, cdpTarget{}, nil, err
	}
	if err := p.activateTarget(ctx, client, target.ID); err != nil {
		return nil, cdpTarget{}, nil, err
	}
	conn, err := p.dialCDP(ctx, client, target)
	if err != nil {
		return nil, cdpTarget{}, nil, err
	}
	page := &cdpPage{conn: conn}
	if _, err := page.conn.Call(ctx, "Page.enable", nil); err != nil {
		_ = conn.Close()
		return nil, cdpTarget{}, nil, err
	}
	if _, err := page.conn.Call(ctx, "Runtime.enable", nil); err != nil {
		_ = conn.Close()
		return nil, cdpTarget{}, nil, err
	}
	_, _ = page.conn.Call(ctx, "DOM.enable", nil)
	return client, target, page, nil
}

func (p *BrowserProvider) runCDPAction(ctx context.Context, botID string, args map[string]any) (map[string]any, error) {
	action := normalizeBrowserAction(StringArg(args, "action"))
	if isCDPTabAction(action) {
		client, err := p.ensureCDP(ctx, botID)
		if err != nil {
			return nil, err
		}
		return p.runCDPTabAction(ctx, client, action, args)
	}
	_, _, page, err := p.connectPage(ctx, botID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = page.conn.Close() }()

	switch action {
	case "navigate":
		targetURL := StringArg(args, "url")
		if targetURL == "" {
			return nil, errors.New("url is required for navigate")
		}
		result, err := page.conn.Call(ctx, "Page.navigate", map[string]any{"url": targetURL})
		if err != nil {
			return nil, err
		}
		_ = page.waitReady(ctx, timeoutArg(args, 30000))
		nav := map[string]any{}
		_ = json.Unmarshal(result, &nav)
		currentURL, _ := page.evaluateString(ctx, "location.href")
		nav["url"] = currentURL
		return nav, nil
	case "click", "double_click":
		target := browserTargetArg(args, "selector", "ref")
		if err := target.require(action); err != nil {
			return nil, err
		}
		point, err := page.elementPoint(ctx, target)
		if err != nil {
			return nil, err
		}
		count := 1
		if action == "double_click" {
			count = 2
		}
		if err := page.mouseClick(ctx, point.X, point.Y, "left", count); err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"clicked": target.label(), "x": point.X, "y": point.Y, "click_count": count}), nil
	case "focus":
		target := browserTargetArg(args, "selector", "ref")
		if err := target.require("focus"); err != nil {
			return nil, err
		}
		_, err := page.evaluate(ctx, fmt.Sprintf(`(() => { const el = mustTarget(%s, %s); el.focus(); return true })()`, jsQuote(target.Selector), jsQuote(target.Ref)))
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"focused": target.label()}), nil
	case "type":
		target := browserTargetArg(args, "selector", "ref")
		text := StringArg(args, "text")
		if err := target.require("type"); err != nil {
			return nil, err
		}
		if text == "" {
			return nil, errors.New("text is required for type")
		}
		if _, err := page.evaluate(ctx, fmt.Sprintf(`(() => { const el = mustTarget(%s, %s); el.focus(); return true })()`, jsQuote(target.Selector), jsQuote(target.Ref))); err != nil {
			return nil, err
		}
		if err := page.insertText(ctx, text); err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"typed": text}), nil
	case "fill":
		target := browserTargetArg(args, "selector", "ref")
		text := StringArg(args, "text")
		if err := target.require("fill"); err != nil {
			return nil, err
		}
		if text == "" {
			return nil, errors.New("text is required for fill")
		}
		_, err := page.evaluate(ctx, fmt.Sprintf(`(() => {
const el = mustTarget(%s, %s);
el.focus();
if ("value" in el) {
  el.value = %s;
  el.dispatchEvent(new InputEvent("input", { bubbles: true, data: %s, inputType: "insertText" }));
  el.dispatchEvent(new Event("change", { bubbles: true }));
} else {
  el.textContent = %s;
}
return true;
})()`, jsQuote(target.Selector), jsQuote(target.Ref), jsQuote(text), jsQuote(text), jsQuote(text)))
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"filled": text}), nil
	case "press":
		key := StringArg(args, "key")
		if key == "" {
			return nil, errors.New("key is required for press")
		}
		if err := page.pressKey(ctx, key); err != nil {
			return nil, err
		}
		return map[string]any{"pressed": key}, nil
	case "keyboard_type", "keyboard_inserttext":
		text := StringArg(args, "text")
		if text == "" {
			return nil, fmt.Errorf("text is required for %s", action)
		}
		if err := page.insertText(ctx, text); err != nil {
			return nil, err
		}
		return map[string]any{"inserted_text": text}, nil
	case "keydown", "keyup":
		key := StringArg(args, "key")
		if key == "" {
			return nil, fmt.Errorf("key is required for %s", action)
		}
		if err := page.dispatchKey(ctx, key, action == "keydown"); err != nil {
			return nil, err
		}
		return map[string]any{action: key}, nil
	case "hover":
		target := browserTargetArg(args, "selector", "ref")
		if err := target.require("hover"); err != nil {
			return nil, err
		}
		point, err := page.elementPoint(ctx, target)
		if err != nil {
			return nil, err
		}
		if err := page.mouseMove(ctx, point.X, point.Y); err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"hovered": target.label(), "x": point.X, "y": point.Y}), nil
	case "select":
		target := browserTargetArg(args, "selector", "ref")
		value := StringArg(args, "value")
		if err := target.require("select"); err != nil {
			return nil, err
		}
		if value == "" {
			return nil, errors.New("value is required for select")
		}
		result, err := page.evaluate(ctx, fmt.Sprintf(`(() => {
const el = mustTarget(%s, %s);
el.value = %s;
el.dispatchEvent(new Event("input", { bubbles: true }));
el.dispatchEvent(new Event("change", { bubbles: true }));
return el.value;
})()`, jsQuote(target.Selector), jsQuote(target.Ref), jsQuote(value)))
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"selected": result}), nil
	case "check", "uncheck":
		target := browserTargetArg(args, "selector", "ref")
		if err := target.require(action); err != nil {
			return nil, err
		}
		checked := action == "check"
		_, err := page.evaluate(ctx, fmt.Sprintf(`(() => {
const el = mustTarget(%s, %s);
el.checked = %t;
el.dispatchEvent(new Event("input", { bubbles: true }));
el.dispatchEvent(new Event("change", { bubbles: true }));
return true;
})()`, jsQuote(target.Selector), jsQuote(target.Ref), checked))
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{action + "ed": target.label()}), nil
	case "screenshot":
		fullPage, _, _ := BoolArg(args, "full_page")
		b64, err := page.captureScreenshot(ctx, fullPage)
		if err != nil {
			return nil, err
		}
		return map[string]any{"screenshot": b64, "mimeType": "image/png"}, nil
	case "screenshot_annotate":
		annotations, err := page.annotate(ctx)
		if err != nil {
			return nil, err
		}
		b64, captureErr := page.captureScreenshot(ctx, false)
		removeErr := page.removeAnnotations(ctx)
		if captureErr != nil {
			return nil, captureErr
		}
		if removeErr != nil {
			p.logger.Debug("remove browser annotations failed", slog.Any("error", removeErr))
		}
		return map[string]any{"screenshot": b64, "mimeType": "image/png", "annotations": annotations}, nil
	case "snapshot":
		snapshot, err := page.accessibilitySnapshot(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"snapshot": snapshot}, nil
	case "get_content":
		target := browserTargetArg(args, "selector", "ref")
		expr := `document.body ? document.body.innerText : ""`
		if target.present() {
			expr = fmt.Sprintf(`mustTarget(%s, %s).innerText`, jsQuote(target.Selector), jsQuote(target.Ref))
		}
		text, err := page.evaluateString(ctx, expr)
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"content": text}), nil
	case "get_html":
		target := browserTargetArg(args, "selector", "ref")
		expr := `document.documentElement ? document.documentElement.outerHTML : ""`
		if target.present() {
			expr = fmt.Sprintf(`mustTarget(%s, %s).innerHTML`, jsQuote(target.Selector), jsQuote(target.Ref))
		}
		html, err := page.evaluateString(ctx, expr)
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"html": html}), nil
	case "evaluate":
		script := StringArg(args, "script")
		if script == "" {
			return nil, errors.New("script is required for evaluate")
		}
		result, err := page.evaluate(ctx, script)
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": result}, nil
	case "scroll":
		direction := StringArg(args, "direction")
		if direction == "" {
			direction = "down"
		}
		amount, err := intArgOr(args, "amount", 500)
		if err != nil {
			return nil, err
		}
		target := browserTargetArg(args, "selector", "ref")
		if target.present() {
			_, err = page.evaluate(ctx, fmt.Sprintf(`(() => {
const el = mustTarget(%s, %s);
const dx = %d;
const dy = %d;
el.scrollBy(dx, dy);
return true;
})()`, jsQuote(target.Selector), jsQuote(target.Ref), scrollDeltaX(direction, amount), scrollDeltaY(direction, amount)))
		} else {
			err = page.mouseWheel(ctx, scrollDeltaX(direction, amount), scrollDeltaY(direction, amount))
		}
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"scrolled": direction, "amount": amount}), nil
	case "scroll_into_view":
		target := browserTargetArg(args, "selector", "ref")
		if err := target.require("scroll_into_view"); err != nil {
			return nil, err
		}
		_, err := page.evaluate(ctx, fmt.Sprintf(`(() => { mustTarget(%s, %s).scrollIntoView({ block: "center", inline: "center" }); return true })()`, jsQuote(target.Selector), jsQuote(target.Ref)))
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"scrolled_into_view": target.label()}), nil
	case "drag":
		sourceTarget := browserTargetArg(args, "selector", "ref")
		dropTarget := browserTargetArg(args, "target_selector", "target_ref")
		if err := sourceTarget.require("drag source"); err != nil {
			return nil, err
		}
		if err := dropTarget.require("drag target"); err != nil {
			return nil, err
		}
		source, err := page.elementPoint(ctx, sourceTarget)
		if err != nil {
			return nil, err
		}
		targetPoint, err := page.elementPoint(ctx, dropTarget)
		if err != nil {
			return nil, err
		}
		if err := page.mouseDrag(ctx, source.X, source.Y, targetPoint.X, targetPoint.Y); err != nil {
			return nil, err
		}
		return map[string]any{"dragged": sourceTarget.label(), "target": dropTarget.label(), "ref": sourceTarget.Ref, "selector": sourceTarget.Selector, "target_ref": dropTarget.Ref, "target_selector": dropTarget.Selector}, nil
	case "upload":
		target := browserTargetArg(args, "selector", "ref")
		if err := target.require("upload"); err != nil {
			return nil, err
		}
		files := stringSliceArg(args, "files")
		if len(files) == 0 {
			return nil, errors.New("files is required for upload")
		}
		if err := page.setInputFiles(ctx, target, files); err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"uploaded": files}), nil
	case "wait":
		target := browserTargetArg(args, "selector", "ref")
		timeout := timeoutArg(args, 1000)
		if target.present() {
			if err := page.waitTarget(ctx, target, timeout); err != nil {
				return nil, err
			}
			return target.withResult(map[string]any{"waited_for": target.label()}), nil
		}
		if err := sleepContext(ctx, time.Duration(timeout)*time.Millisecond); err != nil {
			return nil, err
		}
		return map[string]any{"waited_ms": timeout}, nil
	case "go_back", "go_forward":
		if err := page.navigateHistory(ctx, action == "go_forward"); err != nil {
			return nil, err
		}
		currentURL, _ := page.evaluateString(ctx, "location.href")
		return map[string]any{"url": currentURL}, nil
	case "reload":
		if _, err := page.conn.Call(ctx, "Page.reload", nil); err != nil {
			return nil, err
		}
		_ = page.waitReady(ctx, timeoutArg(args, 30000))
		currentURL, _ := page.evaluateString(ctx, "location.href")
		return map[string]any{"url": currentURL}, nil
	case "get_url":
		currentURL, err := page.evaluateString(ctx, "location.href")
		if err != nil {
			return nil, err
		}
		return map[string]any{"url": currentURL}, nil
	case "get_title":
		title, err := page.evaluateString(ctx, "document.title")
		if err != nil {
			return nil, err
		}
		return map[string]any{"title": title}, nil
	case "pdf":
		result, err := page.conn.Call(ctx, "Page.printToPDF", map[string]any{"printBackground": true})
		if err != nil {
			return nil, err
		}
		var out struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(result, &out); err != nil {
			return nil, err
		}
		return map[string]any{"pdf": out.Data, "mimeType": "application/pdf"}, nil
	default:
		return nil, fmt.Errorf("unknown browser action: %s", action)
	}
}

func isCDPTabAction(action string) bool {
	switch action {
	case "tab_new", "tab_select", "tab_close", "tab_list":
		return true
	default:
		return false
	}
}

func (p *BrowserProvider) runCDPTabAction(ctx context.Context, client *bridge.Client, action string, args map[string]any) (map[string]any, error) {
	switch action {
	case "tab_new":
		targetURL := StringArg(args, "url")
		newTarget, err := p.createTarget(ctx, client, targetURL)
		if err != nil {
			return nil, err
		}
		targets, _ := p.listTargets(ctx, client)
		return map[string]any{"tab_index": targetIndex(targets, newTarget.ID), "target": newTarget.publicMap(), "url": newTarget.URL}, nil
	case "tab_select":
		tabIndex, ok, err := IntArg(args, "tab_index")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("tab_index is required for tab_select")
		}
		targets, err := p.listTargets(ctx, client)
		if err != nil {
			return nil, err
		}
		target, err := pageTargetAt(targets, tabIndex)
		if err != nil {
			return nil, err
		}
		if err := p.activateTarget(ctx, client, target.ID); err != nil {
			return nil, err
		}
		return map[string]any{"tab_index": tabIndex, "target": target.publicMap(), "url": target.URL, "title": target.Title}, nil
	case "tab_close":
		targets, err := p.listTargets(ctx, client)
		if err != nil {
			return nil, err
		}
		tabIndex, ok, err := IntArg(args, "tab_index")
		if err != nil {
			return nil, err
		}
		var closeTarget cdpTarget
		if ok {
			closeTarget, err = pageTargetAt(targets, tabIndex)
			if err != nil {
				return nil, err
			}
		} else {
			closeTarget, err = p.activeTarget(ctx, client)
			if err != nil {
				return nil, err
			}
		}
		result, err := p.closeTarget(ctx, client, closeTarget.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"closed": targetIndex(targets, closeTarget.ID), "result": result}, nil
	case "tab_list":
		targets, err := p.listTargets(ctx, client)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tabs": publicTargets(targets)}, nil
	default:
		return nil, fmt.Errorf("unknown browser tab action: %s", action)
	}
}

func (p *BrowserProvider) browserActionResult(ctx context.Context, botID string, data map[string]any) any {
	if b64, ok := data["screenshot"].(string); ok && b64 != "" {
		return p.buildScreenshotResult(ctx, botID, b64, p.screenshotDir(), data)
	}
	return data
}

type remoteObject struct {
	Type                string          `json:"type"`
	Subtype             string          `json:"subtype"`
	Value               json.RawMessage `json:"value"`
	UnserializableValue string          `json:"unserializableValue"`
	Description         string          `json:"description"`
}

type runtimeEvaluateResponse struct {
	Result           remoteObject `json:"result"`
	ExceptionDetails *struct {
		Text      string `json:"text"`
		Exception struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

func (p *cdpPage) evaluate(ctx context.Context, expression string) (any, error) {
	wrapped := wrapRuntimeExpression(expression)
	result, err := p.conn.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    wrapped,
		"awaitPromise":  true,
		"returnByValue": true,
	})
	if err != nil {
		return nil, err
	}
	var out runtimeEvaluateResponse
	if err := json.Unmarshal(result, &out); err != nil {
		return nil, err
	}
	if out.ExceptionDetails != nil {
		msg := strings.TrimSpace(out.ExceptionDetails.Exception.Description)
		if msg == "" {
			msg = strings.TrimSpace(out.ExceptionDetails.Text)
		}
		return nil, errors.New(msg)
	}
	return remoteObjectValue(out.Result), nil
}

func wrapRuntimeExpression(expression string) string {
	expr := strings.TrimSpace(expression)
	for strings.HasSuffix(expr, ";") {
		expr = strings.TrimSpace(strings.TrimSuffix(expr, ";"))
	}
	return "(async () => {\n" + mustElementHelper + "\nreturn await (\n" + expr + "\n);\n})()"
}

func (p *cdpPage) evaluateString(ctx context.Context, expression string) (string, error) {
	value, err := p.evaluate(ctx, expression)
	if err != nil {
		return "", err
	}
	if value == nil {
		return "", nil
	}
	if s, ok := value.(string); ok {
		return s, nil
	}
	return fmt.Sprintf("%v", value), nil
}

func remoteObjectValue(obj remoteObject) any {
	if len(obj.Value) > 0 && string(obj.Value) != "null" {
		var value any
		if err := json.Unmarshal(obj.Value, &value); err == nil {
			return value
		}
		return string(obj.Value)
	}
	if obj.UnserializableValue != "" {
		return obj.UnserializableValue
	}
	if obj.Description != "" {
		return obj.Description
	}
	return nil
}

type browserTarget struct {
	Selector string
	Ref      string
}

func browserTargetArg(args map[string]any, selectorKey, refKey string) browserTarget {
	return browserTarget{
		Selector: StringArg(args, selectorKey),
		Ref:      normalizeBrowserRef(StringArg(args, refKey)),
	}
}

func normalizeBrowserRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	index, err := browserRefIndex(ref)
	if err != nil {
		return ref
	}
	return fmt.Sprintf("e%d", index)
}

func browserRefIndex(ref string) (int, error) {
	ref = strings.TrimSpace(strings.ToLower(ref))
	ref = strings.TrimPrefix(ref, "ref=")
	ref = strings.TrimPrefix(ref, "e")
	if ref == "" {
		return 0, errors.New("ref is empty")
	}
	index, err := strconv.Atoi(ref)
	if err != nil || index <= 0 {
		return 0, fmt.Errorf("invalid element ref %q", ref)
	}
	return index, nil
}

func (t browserTarget) present() bool {
	return strings.TrimSpace(t.Ref) != "" || strings.TrimSpace(t.Selector) != ""
}

func (t browserTarget) require(action string) error {
	if t.present() {
		return nil
	}
	return fmt.Errorf("ref or selector is required for %s", action)
}

func (t browserTarget) label() string {
	if t.Ref != "" {
		return t.Ref
	}
	return t.Selector
}

func (t browserTarget) withResult(result map[string]any) map[string]any {
	if result == nil {
		result = map[string]any{}
	}
	if t.Ref != "" {
		result["ref"] = t.Ref
	}
	if t.Selector != "" {
		result["selector"] = t.Selector
	}
	return result
}

type elementPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

func (p *cdpPage) elementPoint(ctx context.Context, target browserTarget) (elementPoint, error) {
	value, err := p.evaluate(ctx, fmt.Sprintf(`(() => {
const el = mustTarget(%s, %s);
el.scrollIntoView({ block: "center", inline: "center" });
const rect = el.getBoundingClientRect();
if (rect.width === 0 || rect.height === 0) throw new Error("element has no visible box: " + %s);
return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
})()`, jsQuote(target.Selector), jsQuote(target.Ref), jsQuote(target.label())))
	if err != nil {
		return elementPoint{}, err
	}
	var point elementPoint
	raw, _ := json.Marshal(value)
	if err := json.Unmarshal(raw, &point); err != nil {
		return elementPoint{}, err
	}
	return point, nil
}

func (p *cdpPage) mouseMove(ctx context.Context, x, y float64) error {
	_, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved",
		"x":    x,
		"y":    y,
	})
	return err
}

func (p *cdpPage) mouseClick(ctx context.Context, x, y float64, button string, clickCount int) error {
	if err := p.mouseMove(ctx, x, y); err != nil {
		return err
	}
	if _, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":       "mousePressed",
		"x":          x,
		"y":          y,
		"button":     button,
		"clickCount": clickCount,
	}); err != nil {
		return err
	}
	_, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":       "mouseReleased",
		"x":          x,
		"y":          y,
		"button":     button,
		"clickCount": clickCount,
	})
	return err
}

func (p *cdpPage) mouseDrag(ctx context.Context, fromX, fromY, toX, toY float64) error {
	if err := p.mouseMove(ctx, fromX, fromY); err != nil {
		return err
	}
	if _, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": fromX, "y": fromY, "button": "left", "clickCount": 1}); err != nil {
		return err
	}
	for i := 1; i <= 8; i++ {
		t := float64(i) / 8
		x := fromX + (toX-fromX)*t
		y := fromY + (toY-fromY)*t
		if _, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y, "button": "left", "buttons": 1}); err != nil {
			return err
		}
	}
	_, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": toX, "y": toY, "button": "left", "clickCount": 1})
	return err
}

func (p *cdpPage) mouseWheel(ctx context.Context, deltaX, deltaY int) error {
	_, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":   "mouseWheel",
		"x":      defaultComputerWidth / 2,
		"y":      defaultComputerHeight / 2,
		"deltaX": deltaX,
		"deltaY": deltaY,
	})
	return err
}

func (p *cdpPage) insertText(ctx context.Context, text string) error {
	_, err := p.conn.Call(ctx, "Input.insertText", map[string]any{"text": text})
	return err
}

func (p *cdpPage) pressKey(ctx context.Context, key string) error {
	parts := splitKeyChord(key)
	if len(parts) == 0 {
		return errors.New("key is required")
	}
	modifiers := 0
	for _, part := range parts[:len(parts)-1] {
		modifiers |= cdpModifier(part)
		if err := p.dispatchKeyWithModifiers(ctx, part, true, modifiers); err != nil {
			return err
		}
	}
	mainKey := parts[len(parts)-1]
	if err := p.dispatchKeyWithModifiers(ctx, mainKey, true, modifiers); err != nil {
		return err
	}
	if err := p.dispatchKeyWithModifiers(ctx, mainKey, false, modifiers); err != nil {
		return err
	}
	for i := len(parts) - 2; i >= 0; i-- {
		modifiers &^= cdpModifier(parts[i])
		if err := p.dispatchKeyWithModifiers(ctx, parts[i], false, modifiers); err != nil {
			return err
		}
	}
	return nil
}

func (p *cdpPage) dispatchKey(ctx context.Context, key string, down bool) error {
	return p.dispatchKeyWithModifiers(ctx, key, down, 0)
}

func (p *cdpPage) dispatchKeyWithModifiers(ctx context.Context, key string, down bool, modifiers int) error {
	info := keyInfoForCDP(key)
	eventType := "keyUp"
	if down {
		eventType = "keyDown"
	}
	params := map[string]any{
		"type":                  eventType,
		"key":                   info.Key,
		"code":                  info.Code,
		"windowsVirtualKeyCode": info.KeyCode,
		"nativeVirtualKeyCode":  info.KeyCode,
		"modifiers":             modifiers,
	}
	if len([]rune(info.Text)) == 1 && down {
		params["text"] = info.Text
		params["unmodifiedText"] = info.Text
	}
	_, err := p.conn.Call(ctx, "Input.dispatchKeyEvent", params)
	return err
}

func (p *cdpPage) captureScreenshot(ctx context.Context, fullPage bool) (string, error) {
	params := map[string]any{
		"format":      "png",
		"fromSurface": true,
	}
	if fullPage {
		metricsRaw, err := p.conn.Call(ctx, "Page.getLayoutMetrics", nil)
		if err == nil {
			var metrics struct {
				ContentSize struct {
					X      float64 `json:"x"`
					Y      float64 `json:"y"`
					Width  float64 `json:"width"`
					Height float64 `json:"height"`
				} `json:"contentSize"`
			}
			if json.Unmarshal(metricsRaw, &metrics) == nil && metrics.ContentSize.Width > 0 && metrics.ContentSize.Height > 0 {
				params["captureBeyondViewport"] = true
				params["clip"] = map[string]any{
					"x":      metrics.ContentSize.X,
					"y":      metrics.ContentSize.Y,
					"width":  metrics.ContentSize.Width,
					"height": metrics.ContentSize.Height,
					"scale":  1,
				}
			}
		}
	}
	raw, err := p.conn.Call(ctx, "Page.captureScreenshot", params)
	if err != nil {
		return "", err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.Data, nil
}

func (p *cdpPage) annotate(ctx context.Context) (any, error) {
	return p.evaluate(ctx, `(() => {
const result = [];
for (const item of memohInteractiveElements()) {
  const el = item.element;
  const rect = item.rect;
  result.push({ ref: item.ref, tag: item.tag, role: item.role, name: item.name });
  const label = document.createElement('div');
  label.className = '__memoh_annotation__';
  label.textContent = item.ref;
  label.style.cssText = 'position:fixed;left:' + rect.left + 'px;top:' + Math.max(0, rect.top - 18) + 'px;z-index:2147483647;background:#e63946;color:#fff;font:bold 11px/16px monospace;padding:0 4px;border-radius:3px;pointer-events:none;';
  document.body.appendChild(label);
}
return result;
})()`)
}

func (p *cdpPage) removeAnnotations(ctx context.Context) error {
	_, err := p.evaluate(ctx, `(() => { document.querySelectorAll('.__memoh_annotation__').forEach(el => el.remove()); return true })()`)
	return err
}

func (p *cdpPage) accessibilitySnapshot(ctx context.Context) (string, error) {
	value, err := p.evaluate(ctx, `(() => memohInteractiveElements().slice(0, 300).map(({ ref, role, name, tag, selector }) => ({ ref, role, name, tag, selector })))()`)
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(value)
	var items []struct {
		Ref      string `json:"ref"`
		Role     string `json:"role"`
		Name     string `json:"name"`
		Tag      string `json:"tag"`
		Selector string `json:"selector"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", err
	}
	var lines []string
	for _, item := range items {
		role := strings.TrimSpace(item.Role)
		name := strings.TrimSpace(item.Name)
		ref := strings.TrimSpace(item.Ref)
		line := "- " + role
		if name != "" {
			line += " " + jsQuote(name)
		}
		if ref != "" {
			line += " [ref=" + ref + "]"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "(empty page)", nil
	}
	if len(items) >= 300 {
		lines = append(lines, "- ...")
	}
	return strings.Join(lines, "\n"), nil
}

func (p *cdpPage) setInputFiles(ctx context.Context, target browserTarget, files []string) error {
	if target.Ref != "" {
		value, err := p.evaluate(ctx, fmt.Sprintf(`(() => {
const el = mustTarget("", %s);
return memohCssPath(el);
})()`, jsQuote(target.Ref)))
		if err != nil {
			return err
		}
		selector, ok := value.(string)
		if !ok || strings.TrimSpace(selector) == "" {
			return fmt.Errorf("could not resolve upload target ref %s to a selector", target.Ref)
		}
		target.Selector = selector
	}
	selector := strings.TrimSpace(target.Selector)
	if selector == "" {
		return errors.New("selector or ref is required for upload")
	}
	rawDoc, err := p.conn.Call(ctx, "DOM.getDocument", map[string]any{"depth": 1})
	if err != nil {
		return err
	}
	var doc struct {
		Root struct {
			NodeID int `json:"nodeId"`
		} `json:"root"`
	}
	if err := json.Unmarshal(rawDoc, &doc); err != nil {
		return err
	}
	rawNode, err := p.conn.Call(ctx, "DOM.querySelector", map[string]any{"nodeId": doc.Root.NodeID, "selector": selector})
	if err != nil {
		return err
	}
	var node struct {
		NodeID int `json:"nodeId"`
	}
	if err := json.Unmarshal(rawNode, &node); err != nil {
		return err
	}
	if node.NodeID == 0 {
		return fmt.Errorf("element not found: %s", selector)
	}
	_, err = p.conn.Call(ctx, "DOM.setFileInputFiles", map[string]any{"nodeId": node.NodeID, "files": files})
	return err
}

func (p *cdpPage) waitTarget(ctx context.Context, target browserTarget, timeoutMS int) error {
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	for {
		value, err := p.evaluate(ctx, fmt.Sprintf(`(() => {
try {
  return Boolean(mustTarget(%s, %s));
} catch (_) {
  return false;
}
})()`, jsQuote(target.Selector), jsQuote(target.Ref)))
		if err == nil {
			if ok, _ := value.(bool); ok {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait for target timed out: %s", target.label())
		}
		if err := sleepContext(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
}

func (p *cdpPage) waitReady(ctx context.Context, timeoutMS int) error {
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	for {
		state, err := p.evaluateString(ctx, "document.readyState")
		if err == nil && (state == "interactive" || state == "complete") {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("page load timed out")
		}
		if err := sleepContext(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
}

func (p *cdpPage) navigateHistory(ctx context.Context, forward bool) error {
	raw, err := p.conn.Call(ctx, "Page.getNavigationHistory", nil)
	if err != nil {
		return err
	}
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &history); err != nil {
		return err
	}
	next := history.CurrentIndex - 1
	if forward {
		next = history.CurrentIndex + 1
	}
	if next < 0 || next >= len(history.Entries) {
		return errors.New("no navigation history entry in requested direction")
	}
	_, err = p.conn.Call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[next].ID})
	return err
}

func (p *BrowserProvider) buildScreenshotResult(ctx context.Context, botID, base64Data string, dir string, data map[string]any) any {
	imgBytes, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": "Screenshot captured (failed to decode image data)"}}}
	}
	return p.buildScreenshotBytesResult(ctx, botID, imgBytes, "image/png", dir, data)
}

func (p *BrowserProvider) buildScreenshotBytesResult(ctx context.Context, botID string, imgBytes []byte, mimeType string, dir string, data map[string]any) any {
	if mimeType == "" {
		mimeType = "image/png"
	}
	ext := screenshotExtension(mimeType)
	containerPath := fmt.Sprintf("%s/%d%s", dir, time.Now().UnixMilli(), ext)
	saveErr := p.saveBytes(ctx, botID, containerPath, imgBytes)
	text := fmt.Sprintf("Screenshot saved to %s", containerPath)
	if saveErr != nil {
		text = fmt.Sprintf("Screenshot captured (failed to save: %s)", saveErr.Error())
	}
	content := []map[string]any{{"type": "text", "text": text}}
	if data != nil {
		if annotations, ok := data["annotations"]; ok {
			content = append(content, map[string]any{"type": "text", "text": fmt.Sprintf("Annotations: %v", annotations)})
		}
	}
	result := map[string]any{"content": content, "path": containerPath, "mimeType": mimeType}
	if saveErr != nil {
		result["save_error"] = saveErr.Error()
	}
	return result
}

func (p *BrowserProvider) screenshotDir() string {
	root := strings.TrimRight(strings.TrimSpace(p.dataRoot), "/")
	if root == "" {
		root = "/data"
	}
	return root + "/" + screenshotSubdir
}

func (p *BrowserProvider) saveBytes(ctx context.Context, botID, path string, data []byte) error {
	if p.containers == nil {
		return errors.New("workspace runtime provider is not configured")
	}
	client, err := p.containers.MCPClient(ctx, botID)
	if err != nil {
		return err
	}
	dir := path[:strings.LastIndex(path, "/")]
	if _, err := client.Exec(ctx, "mkdir -p "+dir, "/", 5); err != nil {
		return err
	}
	return client.WriteFile(ctx, path, data)
}

func screenshotExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	default:
		return ".img"
	}
}

func (p *BrowserProvider) pointerClick(ctx context.Context, botID string, x, y int, mask byte) error {
	return p.sendDisplayInputs(ctx, botID,
		displaypkg.ControlInput{Type: "pointer", X: x, Y: y, ButtonMask: mask},
		displaypkg.ControlInput{Type: "pointer", X: x, Y: y, ButtonMask: 0},
	)
}

func (p *BrowserProvider) pointerDrag(ctx context.Context, botID string, fromX, fromY, toX, toY int, mask byte) error {
	events := []displaypkg.ControlInput{{Type: "pointer", X: fromX, Y: fromY, ButtonMask: mask}}
	for i := 1; i <= 12; i++ {
		t := float64(i) / 12
		x := int(math.Round(float64(fromX) + float64(toX-fromX)*t))
		y := int(math.Round(float64(fromY) + float64(toY-fromY)*t))
		events = append(events, displaypkg.ControlInput{Type: "pointer", X: x, Y: y, ButtonMask: mask})
	}
	events = append(events, displaypkg.ControlInput{Type: "pointer", X: toX, Y: toY, ButtonMask: 0})
	return p.sendDisplayInputs(ctx, botID, events...)
}

func (p *BrowserProvider) pointerScroll(ctx context.Context, botID string, x, y int, direction string, amount int) error {
	var mask byte
	switch direction {
	case "up":
		mask = rfbWheelUp
	case "left":
		mask = rfbWheelLeft
	case "right":
		mask = rfbWheelRight
	case "down", "":
		mask = rfbWheelDown
	default:
		return fmt.Errorf("unsupported scroll direction %q", direction)
	}
	steps := clamp(int(math.Ceil(float64(amount)/120.0)), 1, 20)
	for i := 0; i < steps; i++ {
		if err := p.pointerClick(ctx, botID, x, y, mask); err != nil {
			return err
		}
	}
	return nil
}

func (p *BrowserProvider) typeText(ctx context.Context, botID, text string) error {
	events := make([]displaypkg.ControlInput, 0, len(text)*2)
	for _, r := range text {
		ks := keysymForRune(r)
		events = append(events,
			displaypkg.ControlInput{Type: "key", Keysym: ks, Down: true},
			displaypkg.ControlInput{Type: "key", Keysym: ks, Down: false},
		)
	}
	return p.sendDisplayInputs(ctx, botID, events...)
}

func (p *BrowserProvider) typeKeyChord(ctx context.Context, botID, chord string) error {
	parts := splitKeyChord(chord)
	if len(parts) == 0 {
		return errors.New("key is required")
	}
	var modifiers []uint32
	events := make([]displaypkg.ControlInput, 0, len(parts)*2)
	for _, part := range parts[:len(parts)-1] {
		ks := namedKeysym(part)
		if ks == 0 {
			return fmt.Errorf("unsupported modifier key %q", part)
		}
		modifiers = append(modifiers, ks)
		events = append(events, displaypkg.ControlInput{Type: "key", Keysym: ks, Down: true})
	}
	main := namedKeysym(parts[len(parts)-1])
	if main == 0 {
		rs := []rune(parts[len(parts)-1])
		if len(rs) == 1 {
			main = keysymForRune(rs[0])
		}
	}
	if main == 0 {
		return fmt.Errorf("unsupported key %q", parts[len(parts)-1])
	}
	events = append(events,
		displaypkg.ControlInput{Type: "key", Keysym: main, Down: true},
		displaypkg.ControlInput{Type: "key", Keysym: main, Down: false},
	)
	for i := len(modifiers) - 1; i >= 0; i-- {
		events = append(events, displaypkg.ControlInput{Type: "key", Keysym: modifiers[i], Down: false})
	}
	return p.sendDisplayInputs(ctx, botID, events...)
}

func (p *BrowserProvider) sendDisplayInputs(ctx context.Context, botID string, events ...displaypkg.ControlInput) error {
	return p.display.ControlInputs(ctx, botID, events)
}

func requiredPoint(args map[string]any) (int, int, error) {
	x, okX, err := IntArg(args, "x")
	if err != nil {
		return 0, 0, err
	}
	y, okY, err := IntArg(args, "y")
	if err != nil {
		return 0, 0, err
	}
	if !okX || !okY {
		return 0, 0, errors.New("x and y are required")
	}
	return x, y, nil
}

func requiredTargetPoint(args map[string]any) (int, int, error) {
	x, okX, err := IntArg(args, "to_x")
	if err != nil {
		return 0, 0, err
	}
	y, okY, err := IntArg(args, "to_y")
	if err != nil {
		return 0, 0, err
	}
	if !okX || !okY {
		return 0, 0, errors.New("to_x and to_y are required")
	}
	return x, y, nil
}

func optionalPoint(args map[string]any, fallbackX, fallbackY int) (int, int) {
	x, okX, _ := IntArg(args, "x")
	y, okY, _ := IntArg(args, "y")
	if !okX {
		x = fallbackX
	}
	if !okY {
		y = fallbackY
	}
	return x, y
}

func mouseButtonMask(button string) byte {
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "middle":
		return rfbButtonMiddle
	case "right":
		return rfbButtonRight
	default:
		return rfbButtonLeft
	}
}

func buttonName(mask byte) string {
	switch mask {
	case rfbButtonMiddle:
		return "middle"
	case rfbButtonRight:
		return "right"
	default:
		return "left"
	}
}

func keysymForRune(r rune) uint32 {
	if r >= 0x20 && r <= 0x7e {
		return uint32(r)
	}
	return 0x01000000 | uint32(r) //nolint:gosec // runes are Unicode code points and this branch uses the X11 UCS keysym encoding.
}

func namedKeysym(key string) uint32 {
	switch normalizeKeyName(key) {
	case "backspace":
		return 0xff08
	case "tab":
		return 0xff09
	case "enter", "return":
		return 0xff0d
	case "escape", "esc":
		return 0xff1b
	case "delete":
		return 0xffff
	case "home":
		return 0xff50
	case "left", "arrowleft":
		return 0xff51
	case "up", "arrowup":
		return 0xff52
	case "right", "arrowright":
		return 0xff53
	case "down", "arrowdown":
		return 0xff54
	case "pageup":
		return 0xff55
	case "pagedown":
		return 0xff56
	case "end":
		return 0xff57
	case "shift":
		return 0xffe1
	case "control", "ctrl":
		return 0xffe3
	case "alt", "option":
		return 0xffe9
	case "meta", "cmd", "command", "super":
		return 0xffeb
	case "space":
		return 0x20
	default:
		rs := []rune(key)
		if len(rs) == 1 {
			return keysymForRune(rs[0])
		}
		return 0
	}
}

type cdpKeyInfo struct {
	Key     string
	Code    string
	KeyCode int
	Text    string
}

func keyInfoForCDP(key string) cdpKeyInfo {
	normalized := normalizeKeyName(key)
	switch normalized {
	case "enter", "return":
		return cdpKeyInfo{Key: "Enter", Code: "Enter", KeyCode: 13}
	case "tab":
		return cdpKeyInfo{Key: "Tab", Code: "Tab", KeyCode: 9}
	case "escape", "esc":
		return cdpKeyInfo{Key: "Escape", Code: "Escape", KeyCode: 27}
	case "backspace":
		return cdpKeyInfo{Key: "Backspace", Code: "Backspace", KeyCode: 8}
	case "delete":
		return cdpKeyInfo{Key: "Delete", Code: "Delete", KeyCode: 46}
	case "arrowleft", "left":
		return cdpKeyInfo{Key: "ArrowLeft", Code: "ArrowLeft", KeyCode: 37}
	case "arrowup", "up":
		return cdpKeyInfo{Key: "ArrowUp", Code: "ArrowUp", KeyCode: 38}
	case "arrowright", "right":
		return cdpKeyInfo{Key: "ArrowRight", Code: "ArrowRight", KeyCode: 39}
	case "arrowdown", "down":
		return cdpKeyInfo{Key: "ArrowDown", Code: "ArrowDown", KeyCode: 40}
	case "control", "ctrl":
		return cdpKeyInfo{Key: "Control", Code: "ControlLeft", KeyCode: 17}
	case "shift":
		return cdpKeyInfo{Key: "Shift", Code: "ShiftLeft", KeyCode: 16}
	case "alt", "option":
		return cdpKeyInfo{Key: "Alt", Code: "AltLeft", KeyCode: 18}
	case "meta", "cmd", "command", "super":
		return cdpKeyInfo{Key: "Meta", Code: "MetaLeft", KeyCode: 91}
	case "space":
		return cdpKeyInfo{Key: " ", Code: "Space", KeyCode: 32, Text: " "}
	default:
		rs := []rune(key)
		if len(rs) == 1 {
			r := rs[0]
			upper := strings.ToUpper(string(r))
			code := "Key" + upper
			keyCode := int([]rune(upper)[0])
			if r >= '0' && r <= '9' {
				code = "Digit" + string(r)
				keyCode = int(r)
			}
			return cdpKeyInfo{Key: string(r), Code: code, KeyCode: keyCode, Text: string(r)}
		}
		return cdpKeyInfo{Key: key, Code: key, KeyCode: 0}
	}
}

func cdpModifier(key string) int {
	switch normalizeKeyName(key) {
	case "alt", "option":
		return 1
	case "control", "ctrl":
		return 2
	case "meta", "cmd", "command", "super":
		return 4
	case "shift":
		return 8
	default:
		return 0
	}
}

func normalizeKeyName(key string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), " ", ""))
}

func splitKeyChord(key string) []string {
	parts := strings.FieldsFunc(key, func(r rune) bool { return r == '+' })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func normalizeBrowserAction(action string) string {
	normalized := strings.ToLower(strings.TrimSpace(action))
	switch normalized {
	case "dblclick":
		return "double_click"
	case "scrollintoview":
		return "scroll_into_view"
	default:
		return normalized
	}
}

func timeoutArg(args map[string]any, fallback int) int {
	value, ok, err := IntArg(args, "timeout")
	if err != nil || !ok || value <= 0 {
		return fallback
	}
	return value
}

func intArgOr(args map[string]any, key string, fallback int) (int, error) {
	value, _, err := IntArg(args, key)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return fallback, nil
	}
	return value, nil
}

func scrollDeltaX(direction string, amount int) int {
	switch direction {
	case "left":
		return -amount
	case "right":
		return amount
	default:
		return 0
	}
}

func scrollDeltaY(direction string, amount int) int {
	switch direction {
	case "up":
		return -amount
	case "down", "":
		return amount
	default:
		return 0
	}
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch values := raw.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if s := strings.TrimSpace(fmt.Sprintf("%v", value)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func pageTargetAt(targets []cdpTarget, index int) (cdpTarget, error) {
	if index < 0 {
		return cdpTarget{}, fmt.Errorf("tab index %d out of range", index)
	}
	i := 0
	for _, target := range targets {
		if target.Type != "page" {
			continue
		}
		if i == index {
			return target, nil
		}
		i++
	}
	return cdpTarget{}, fmt.Errorf("tab index %d out of range (%d tabs)", index, i)
}

func targetIndex(targets []cdpTarget, id string) int {
	i := 0
	for _, target := range targets {
		if target.Type != "page" {
			continue
		}
		if target.ID == id {
			return i
		}
		i++
	}
	return -1
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func clampByte(value int) byte {
	if value <= 0 {
		return 0
	}
	if value >= 255 {
		return 255
	}
	return byte(value) //nolint:gosec // value is manually bounded to the uint8 range above.
}

func jsQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

const mustElementHelper = `
function mustElement(selector) {
  const el = document.querySelector(selector);
  if (!el) throw new Error("element not found: " + selector);
  return el;
}

const memohInteractiveSelector = [
  'a[href]',
  'button',
  'input',
  'select',
  'textarea',
  'summary',
  '[contenteditable="true"]',
  '[role="button"]',
  '[role="link"]',
  '[role="tab"]',
  '[role="menuitem"]',
  '[role="checkbox"]',
  '[role="radio"]',
  '[onclick]',
  '[tabindex]:not([tabindex="-1"])'
].join(',');

function memohVisible(el) {
  const rect = el.getBoundingClientRect();
  const style = getComputedStyle(el);
  if (rect.width === 0 || rect.height === 0) return null;
  if (style.visibility === 'hidden' || style.display === 'none' || Number(style.opacity) === 0) return null;
  return rect;
}

function memohRole(el) {
  const explicit = (el.getAttribute('role') || '').trim();
  if (explicit) return explicit;
  const tag = el.tagName.toLowerCase();
  if (tag === 'a') return 'link';
  if (tag === 'button') return 'button';
  if (tag === 'select') return 'combobox';
  if (tag === 'textarea') return 'textbox';
  if (tag === 'summary') return 'button';
  if (tag === 'input') {
    const type = (el.getAttribute('type') || 'text').toLowerCase();
    if (type === 'checkbox') return 'checkbox';
    if (type === 'radio') return 'radio';
    if (type === 'submit' || type === 'button' || type === 'reset') return 'button';
    return 'textbox';
  }
  return 'element';
}

function memohElementName(el) {
  const tag = el.tagName.toLowerCase();
  const type = (el.getAttribute('type') || '').toLowerCase();
  const candidates = [
    el.getAttribute('aria-label'),
    el.getAttribute('alt'),
    el.getAttribute('title'),
    el.getAttribute('placeholder')
  ];
  if (tag === 'input' && ['button', 'submit', 'reset'].includes(type)) {
    candidates.push(el.value);
  }
  candidates.push(el.innerText, el.textContent);
  for (const candidate of candidates) {
    const text = String(candidate || '').replace(/\s+/g, ' ').trim();
    if (text) return text.slice(0, 80);
  }
  return '';
}

function memohCssEscape(value) {
  if (globalThis.CSS && typeof CSS.escape === 'function') return CSS.escape(value);
  return String(value).replace(/[^a-zA-Z0-9_-]/g, '\\$&');
}

function memohCssPath(el) {
  if (el.id) return '#' + memohCssEscape(el.id);
  const parts = [];
  let node = el;
  while (node && node.nodeType === Node.ELEMENT_NODE && node !== document.body && node !== document.documentElement) {
    let part = node.tagName.toLowerCase();
    const parent = node.parentElement;
    if (!parent) break;
    const sameTag = Array.from(parent.children).filter(child => child.tagName === node.tagName);
    if (sameTag.length > 1) {
      part += ':nth-of-type(' + (sameTag.indexOf(node) + 1) + ')';
    }
    parts.unshift(part);
    node = parent;
  }
  return parts.length ? parts.join(' > ') : el.tagName.toLowerCase();
}

function memohInteractiveElements() {
  const result = [];
  const seen = new Set();
  for (const el of document.querySelectorAll(memohInteractiveSelector)) {
    if (seen.has(el)) continue;
    seen.add(el);
    const rect = memohVisible(el);
    if (!rect) continue;
    const ref = 'e' + (result.length + 1);
    result.push({
      ref,
      element: el,
      rect,
      tag: el.tagName.toLowerCase(),
      role: memohRole(el),
      name: memohElementName(el),
      selector: memohCssPath(el)
    });
  }
  return result;
}

function elementByRef(ref) {
  const value = String(ref || '').trim().toLowerCase().replace(/^ref=/, '').replace(/^e/, '');
  const index = Number.parseInt(value, 10);
  if (!Number.isInteger(index) || index < 1) throw new Error('invalid element ref: ' + ref);
  const item = memohInteractiveElements()[index - 1];
  if (!item) throw new Error('element ref not found: ' + ref + ' (observe again; the page may have changed)');
  return item.element;
}

function mustTarget(selector, ref) {
  if (String(ref || '').trim()) return elementByRef(ref);
  return mustElement(selector);
}
`
