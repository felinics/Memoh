package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	displaypkg "github.com/felinics/memoh/internal/display"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	browserCDPPort                      = 9222
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
	sessions   *guiSessionStore
	// clipboards serialises desktop clipboard use per bot: paste saves,
	// replaces, and restores the X clipboard, which only works one at a time.
	clipboards sync.Map
}

func NewBrowserProvider(log *slog.Logger, settingsSvc *settings.Service, containers bridge.Provider, displaySvc *displaypkg.Service, dataRoot string) *BrowserProvider {
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(dataRoot) == "" {
		dataRoot = "/data"
	}
	return &BrowserProvider{
		logger:     log.With(slog.String("tool", "browser")),
		settings:   settingsSvc,
		containers: containers,
		display:    displaySvc,
		dataRoot:   dataRoot,
		sessions:   newGUISessionStore(),
	}
}

// Usage frames the workspace browser/desktop tool group. Injected only when
// these tools are registered (display-enabled sessions).
func (*BrowserProvider) Usage(_ context.Context, session SessionContext, available AvailableTools) string {
	var parts []string
	if ref, ok := available.Ref(ToolComputerContext()); ok {
		parts = append(parts, "**Targets** ("+ref+"): list_apps/get_app select an application (app_id) and list_browsers/get_browser select a browser (browser_id) for this conversation; get_state shows both with the current selection, documentation returns the exact contract. Selection is per conversation and never navigates or launches unless get_app is called with launch=true.")
	}
	browserRefs := available.Refs(ToolBrowserObserve(), ToolBrowserAction())
	switch len(browserRefs) {
	case 2:
		parts = append(parts, "**Browser** ("+strings.Join(browserRefs, ", ")+"): Web pages in Chrome. Observe before acting; refs from a snapshot are bound to that snapshot_id and that tab, so observe again after navigation or when a ref is refused. Pass tab_id to address a specific tab; the conversation's selected tab is used otherwise.")
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
		parts = append(parts, "**Computer** ("+strings.Join(desktopRefs, ", ")+"): Native applications and dialogs on the workspace desktop. Select an application first when several are open, take a snapshot to get refs (with geometry, states, and actions=), then act with those refs; raw coordinates are a last-resort fallback.")
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
		if session.SupportsImageInput {
			readHint := ""
			if readRef, ok := available.Ref(ToolRead()); ok {
				readHint = " Read a saved path with " + readRef + " when you need the image again later."
			}
			parts = append(parts, "**Snapshots and screenshots**: snapshot returns the accessibility tree with refs and is incremental after the first observation of a target (added +, updated ~, removed refs); use cursor to keep reading a long one and scope_ref to zoom into a subtree. screenshot saves the image to a workspace path and also shows it to you on your next step (image_mode auto) unless image_mode is path; the result states the image size and coordinate space."+readHint)
		} else {
			readHint := "the returned path can be used by later workspace actions when needed."
			parts = append(parts, "**Snapshots and screenshots**: snapshot returns the accessibility tree with refs and is incremental after the first observation of a target; use cursor and scope_ref for long trees. Screenshots are saved to a workspace path; this model cannot view images, so rely on snapshot for page state and "+readHint)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return usageSection("Workspace browser & desktop", append([]string{
		"This bot has a headed workspace display (Chrome on a virtual desktop). Use GUI tools only when the task needs on-screen interaction. They always act on the bot's native workspace, never on a connected computer.",
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
			Name:        ToolComputerContext().String(),
			Description: "Discover and select the targets of the GUI tools: running applications on the workspace desktop (app_id) and workspace browsers with their tabs (browser_id, tab_id). get_app and get_browser make a target this conversation's default and return an initial observation; get_state reports everything with per-domain errors; documentation returns the exact action contracts and backend capabilities. Nothing here clicks, types, or navigates.",
			Parameters:  computerContextContract.schema(computerContextContract.actionDescription("Context action to perform:")),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execComputerContext(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolBrowserAction().String(),
			Description: "Operate a workspace browser tab. Prefer element refs from an observation of the same tab over CSS selectors; use selectors only as a fallback and viewport x/y only when no element target applies. Use fill to replace or clear a value, set_value to set it programmatically, type to insert at the caret, paste for clipboard-style input, select_text to select or place the caret, and press for shortcuts or submit keys. Each action accepts only the parameters listed for it; anything else is rejected before the browser is touched. After navigation or UI-changing actions, observe again only when the next step depends on the changed state.",
			Parameters:  browserActionContract.schema(browserActionContract.actionDescription("Browser action to perform:")),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execBrowserAction(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolBrowserObserve().String(),
			Description: "Inspect a workspace browser tab without changing page state. Prefer snapshot for interactive elements (it returns a snapshot_id that its refs belong to) and get_content for readable text. Use screenshot_annotate only when visual layout matters or you need rendered-page refs. Use evaluate only for small DOM queries or page-state checks. Screenshots are saved to a workspace path and are not attached automatically.",
			Parameters:  browserObserveContract.schema(browserObserveContract.actionDescription("What to observe from the page:")),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) { //nolint:contextcheck // the call context derives from ctx.Context with the tool call id attached
				return p.execBrowserObserve(withToolCallID(ctx.Context, ctx.ToolCallID), sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolComputerObserve().String(),
			Description: "Inspect the workspace desktop without changing state. Use snapshot for an accessibility listing of on-screen UI elements with refs, geometry, states, and available actions, bound to a snapshot_id; pass app_id to scope it to one application. Use screenshot only when accessibility is unavailable or you need visual layout; the image is saved to a workspace path and is not attached automatically.",
			Parameters:  computerObserveContract.schema(computerObserveContract.actionDescription("What to observe from the desktop:")),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) { //nolint:contextcheck // the call context derives from ctx.Context with the tool call id attached
				return p.execComputerObserve(withToolCallID(ctx.Context, ctx.ToolCallID), sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolComputerAction().String(),
			Description: "Drive the workspace desktop. Prefer refs from a desktop observation snapshot (they are bound to that snapshot_id and application); coordinates (x, y) are only a fallback when no ref applies (native dialogs, raw drags, pointer hovers). Each action accepts only the parameters listed for it. For in-page browser targets, prefer browser-specific actions when they are available.",
			Parameters:  computerActionContract.schema(computerActionContract.actionDescription("Desktop action to perform:")),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execComputerAction(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolBrowserRemoteSession().String(),
			Description: "Advanced escape hatch for code-driven automation. Exposes a workspace browser's CDP endpoint for chromium.connectOverCDP or other CDP clients running inside the workspace. The endpoint is browser-wide: it is not scoped to one tab and cannot be revoked independently of the browser.",
			Parameters:  browserRemoteSessionContract.schema(browserRemoteSessionContract.actionDescription("Session action to perform:")),
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

// execBrowserAction validates the call against the action contract before
// anything touches the browser, so a malformed request fails with no side
// effect.
func (p *BrowserProvider) execBrowserAction(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	if _, err := browserActionContract.normalize(args); err != nil {
		return nil, err
	}
	return p.runBrowser(ctx, session, args)
}

func (p *BrowserProvider) execBrowserObserve(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	spec, err := browserObserveContract.normalize(args)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"action": spec.Name}
	for _, key := range []string{"ref", "selector", "script", "browser_id", "tab_id", "snapshot_id", "scope_ref", "cursor", "image_mode"} {
		if v := StringArg(args, key); v != "" {
			payload[key] = v
		}
	}
	for _, key := range []string{"tab_index", "limit"} {
		if v, ok, _ := IntArg(args, key); ok {
			payload[key] = v
		}
	}
	for _, key := range []string{"full_page", "disable_diffing"} {
		if v, ok, _ := BoolArg(args, key); ok {
			payload[key] = v
		}
	}
	return p.runBrowser(ctx, session, payload)
}

// runBrowser executes an already-validated browser request.
func (p *BrowserProvider) runBrowser(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	botID, err := sessionBotID(session)
	if err != nil {
		return nil, err
	}
	if err := p.ensureDisplayEnabled(ctx, botID); err != nil {
		return nil, err
	}
	if p.containers == nil {
		return nil, errors.New("workspace runtime provider is not configured")
	}
	client, err := p.containers.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	state := p.sessions.get(session)
	runCtx, cancel := context.WithTimeout(ctx, browserToolTimeout)
	defer cancel()
	data, err := p.runCDPAction(runCtx, client, state, args)
	if err != nil {
		return nil, err
	}
	return p.browserActionResult(ctx, session, botID, data, args), nil
}

func (p *BrowserProvider) execRemoteSession(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	spec, err := browserRemoteSessionContract.normalize(args)
	if err != nil {
		return nil, err
	}
	botID, err := sessionBotID(session)
	if err != nil {
		return nil, err
	}
	if err := p.ensureDisplayEnabled(ctx, botID); err != nil {
		return nil, err
	}
	if p.containers == nil {
		return nil, errors.New("workspace runtime provider is not configured")
	}
	client, err := p.containers.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	state := p.sessions.get(session)
	browser, err := p.resolveBrowser(ctx, client, state, StringArg(args, "browser_id"))
	if err != nil {
		return nil, err
	}
	switch spec.Name {
	case "create":
		targetURL := StringArg(args, "url")
		var target cdpTarget
		created := false
		if strings.TrimSpace(targetURL) != "" {
			target, err = p.createTarget(ctx, client, browser, targetURL)
			created = true
		} else {
			var tab resolvedTab
			tab, err = p.resolveTab(ctx, client, state, map[string]any{"browser_id": browser.ID})
			target = tab.Target
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"id":                      target.ID,
			"session_id":              target.ID,
			"browser_id":              browser.ID,
			"tab_id":                  target.ID,
			"created_tab":             created,
			"status":                  "active",
			"scope":                   "browser-wide CDP endpoint; not revocable independently of the browser",
			"cdp_url":                 browser.baseURL(),
			"ws_endpoint":             target.WebSocketDebuggerURL,
			"web_socket_debugger_url": target.WebSocketDebuggerURL,
			"connect_over_cdp":        browser.baseURL(),
			"target":                  target.publicMap(),
		}, nil
	case "status":
		targets, err := p.listTargets(ctx, client, browser)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"status":           "active",
			"browser_id":       browser.ID,
			"cdp_url":          browser.baseURL(),
			"connect_over_cdp": browser.baseURL(),
			"targets":          publicTargets(targets),
		}, nil
	case "close":
		id := StringArg(args, "session_id")
		result, err := p.closeTarget(ctx, client, browser, id)
		if err != nil {
			return nil, err
		}
		state.forgetTab(id)
		return result, nil
	default:
		return nil, fmt.Errorf("unknown session action: %s", spec.Name)
	}
}

// runCDPAction dispatches one validated browser call. Tab-management
// actions resolve their own targets; page actions freeze one tab first.
func (p *BrowserProvider) runCDPAction(ctx context.Context, client *bridge.Client, state *guiSessionState, args map[string]any) (map[string]any, error) {
	action := normalizeBrowserAction(StringArg(args, "action"))
	if isCDPTabAction(action) {
		return p.runCDPTabAction(ctx, client, state, action, args)
	}
	tab, page, err := p.connectTab(ctx, client, state, args)
	if err != nil {
		return nil, err
	}
	defer func() { _ = page.conn.Close() }()
	result, err := p.runPageAction(ctx, state, tab, page, action, args)
	if err != nil {
		return nil, err
	}
	for k, v := range tab.identity() {
		if _, exists := result[k]; !exists {
			result[k] = v
		}
	}
	return result, nil
}

func (p *BrowserProvider) runPageAction(ctx context.Context, state *guiSessionState, tab resolvedTab, page *cdpPage, action string, args map[string]any) (map[string]any, error) {
	switch action {
	case "navigate":
		targetURL := StringArg(args, "url")
		timeout, err := browserReadyTimeout(args)
		if err != nil {
			return nil, err
		}
		result, err := page.conn.Call(ctx, "Page.navigate", map[string]any{"url": targetURL})
		if err != nil {
			return nil, err
		}
		state.invalidateBrowserSnapshot(tab.Target.ID)
		nav := map[string]any{}
		_ = jsonUnmarshalLoose(result, &nav)
		if errText, ok := nav["errorText"].(string); ok && strings.TrimSpace(errText) != "" {
			return nil, fmt.Errorf("navigate to %s failed: %s", targetURL, errText)
		}
		if err := page.waitReady(ctx, timeout); err != nil {
			return nil, fmt.Errorf("navigate to %s: %w", targetURL, err)
		}
		currentURL, _ := page.evaluateString(ctx, "location.href")
		nav["url"] = currentURL
		nav["timeout_ms"] = timeout
		return nav, nil
	case "click", "double_click":
		count, err := guiClickCount(args, action)
		if err != nil {
			return nil, err
		}
		button := browserButton(args)
		target, point, err := page.resolvePoint(ctx, args, "selector", "ref", "x", "y")
		if err != nil {
			return nil, err
		}
		if err := page.mouseClick(ctx, point.X, point.Y, button, count); err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"clicked": target.labelOr("point"), "x": point.X, "y": point.Y, "button": button, "click_count": count}), nil
	case "focus":
		target := browserTargetArg(args, "selector", "ref")
		var out struct {
			Focused bool   `json:"focused"`
			Active  string `json:"active"`
		}
		if err := page.evaluateObject(ctx, fmt.Sprintf(`memohFocus(%s)`, page.expr(target)), &out); err != nil {
			return nil, err
		}
		if !out.Focused {
			return nil, fmt.Errorf("%s did not receive focus; the active element is %s", target.label(), out.Active)
		}
		return target.withResult(map[string]any{"focused": target.label(), "active": out.Active}), nil
	case "type":
		target := browserTargetArg(args, "selector", "ref")
		text := rawStringArg(args, "text")
		if _, err := page.evaluate(ctx, fmt.Sprintf(`(() => { const el = %s; el.focus(); return true })()`, page.expr(target))); err != nil {
			return nil, err
		}
		if err := page.insertText(ctx, text); err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"typed": text}), nil
	case "fill":
		target := browserTargetArg(args, "selector", "ref")
		text := rawStringArg(args, "text")
		var selected struct {
			Kind   string `json:"kind"`
			Length int    `json:"length"`
		}
		if err := page.evaluateObject(ctx, fmt.Sprintf(`memohSelectAll(%s)`, page.expr(target)), &selected); err != nil {
			return nil, err
		}
		if text == "" {
			if _, err := page.evaluate(ctx, fmt.Sprintf(`memohDeleteSelection(%s)`, page.expr(target))); err != nil {
				return nil, err
			}
		} else if err := page.insertText(ctx, text); err != nil {
			return nil, err
		}
		value, err := page.evaluateString(ctx, fmt.Sprintf(`memohCurrentValue(%s)`, page.expr(target)))
		if err != nil {
			return nil, err
		}
		if value != text {
			return nil, fmt.Errorf("fill did not take: %s now holds %q instead of %q (the page may have rewritten it); try set_value", target.label(), value, text)
		}
		return target.withResult(map[string]any{"filled": text, "value": value, "kind": selected.Kind, "replaced_chars": selected.Length}), nil
	case "set_value":
		target := browserTargetArg(args, "selector", "ref")
		value := rawStringArg(args, "value")
		var out struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		}
		if err := page.evaluateObject(ctx, fmt.Sprintf(`memohSetValue(%s, %s)`, page.expr(target), jsQuote(value)), &out); err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"set_value": value, "value": out.Value, "kind": out.Kind}), nil
	case "paste":
		return p.browserPaste(ctx, page, args)
	case "select_text":
		target := browserTargetArg(args, "selector", "ref")
		mode := StringArg(args, "selection_type")
		if mode == "" {
			mode = "text"
		}
		var out map[string]any
		if err := page.evaluateObject(ctx, fmt.Sprintf(`memohSelectText(%s, %s, %s, %s, %s)`, page.expr(target), jsQuote(rawStringArg(args, "text")), jsQuote(rawStringArg(args, "prefix")), jsQuote(rawStringArg(args, "suffix")), jsQuote(mode)), &out); err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string]any{}
		}
		out["selection_type"] = mode
		return target.withResult(out), nil
	case "secondary_action":
		target := browserTargetArg(args, "selector", "ref")
		return nil, fmt.Errorf("the browser backend exposes no secondary actions for %s (name %q): the DOM carries no accessibility actions; use click, press, or the element's own controls", target.label(), StringArg(args, "name"))
	case "press":
		key := StringArg(args, "key")
		if err := page.pressKey(ctx, key, state.heldModifiers()); err != nil {
			return nil, err
		}
		return map[string]any{"pressed": key}, nil
	case "keyboard_type":
		text := rawStringArg(args, "text")
		if err := page.insertText(ctx, text); err != nil {
			return nil, err
		}
		return map[string]any{"inserted_text": text}, nil
	case "keydown":
		key := StringArg(args, "key")
		state.holdKey(key)
		if err := page.dispatchKeyWithModifiers(ctx, key, true, state.heldModifiers()); err != nil {
			state.releaseKey(key)
			return nil, err
		}
		return map[string]any{"keydown": key, "held": heldKeyList(state)}, nil
	case "keyup":
		key := StringArg(args, "key")
		state.releaseKey(key)
		if err := page.dispatchKeyWithModifiers(ctx, key, false, state.heldModifiers()); err != nil {
			return nil, err
		}
		return map[string]any{"keyup": key, "held": heldKeyList(state)}, nil
	case "hover":
		target, point, err := page.resolvePoint(ctx, args, "selector", "ref", "x", "y")
		if err != nil {
			return nil, err
		}
		if err := page.mouseMove(ctx, point.X, point.Y); err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"hovered": target.labelOr("point"), "x": point.X, "y": point.Y}), nil
	case "select":
		target := browserTargetArg(args, "selector", "ref")
		value := rawStringArg(args, "value")
		result, err := page.evaluate(ctx, fmt.Sprintf(`(() => {
const el = %s;
const value = %s;
if (!(el instanceof HTMLSelectElement)) throw new Error("element is not a select: " + %s);
const option = Array.from(el.options).find(o => o.value === value);
if (!option) throw new Error("select has no option with value " + JSON.stringify(value));
el.value = value;
el.dispatchEvent(new Event("input", { bubbles: true }));
el.dispatchEvent(new Event("change", { bubbles: true }));
return el.value;
})()`, page.expr(target), jsQuote(value), jsQuote(target.label())))
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"selected": result}), nil
	case "check", "uncheck":
		target := browserTargetArg(args, "selector", "ref")
		checked := action == "check"
		result, err := page.evaluate(ctx, fmt.Sprintf(`(() => {
const el = %s;
if (!(el instanceof HTMLInputElement) || (el.type !== "checkbox" && el.type !== "radio")) throw new Error("element is not a checkbox or radio: " + %s);
if (el.checked !== %t) {
  el.click();
}
if (el.checked !== %t) throw new Error("control did not change to checked=" + %t);
return el.checked;
})()`, page.expr(target), jsQuote(target.label()), checked, checked, checked))
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{action + "ed": target.label(), "checked": result}), nil
	case "screenshot":
		return p.browserScreenshot(ctx, page, args, nil)
	case "screenshot_annotate":
		opts, err := guiSnapshotOptionsFrom(args)
		if err != nil {
			return nil, err
		}
		opts.DisableDiffing = true
		snapshot, err := p.browserSnapshot(ctx, state, tab, page, args, opts)
		if err != nil {
			return nil, err
		}
		annotations, err := page.annotatePinned(ctx)
		if err != nil {
			return nil, err
		}
		result, captureErr := p.browserScreenshot(ctx, page, args, map[string]any{"annotations": annotations, "snapshot_id": snapshot["snapshot_id"], "ref_count": snapshot["ref_count"]})
		if removeErr := page.removeAnnotations(ctx); removeErr != nil {
			p.logger.Debug("remove browser annotations failed", slog.Any("error", removeErr))
		}
		return result, captureErr
	case "snapshot":
		opts, err := guiSnapshotOptionsFrom(args)
		if err != nil {
			return nil, err
		}
		return p.browserSnapshot(ctx, state, tab, page, args, opts)
	case "state_and_screenshot":
		opts, err := guiSnapshotOptionsFrom(args)
		if err != nil {
			return nil, err
		}
		if opts.Cursor != "" {
			return nil, errors.New("state_and_screenshot always takes a new snapshot; use snapshot with cursor to continue reading one")
		}
		before := page.pageGeneration(ctx)
		snapshot, err := p.browserSnapshot(ctx, state, tab, page, args, opts)
		if err != nil {
			return nil, err
		}
		extra := map[string]any{"snapshot_taken_at": time.Now().UTC().Format(time.RFC3339Nano)}
		for k, v := range snapshot {
			extra[k] = v
		}
		after := page.pageGeneration(ctx)
		extra["consistent"] = before != "" && before == after
		if before != after {
			extra["consistency_note"] = "the document changed between the snapshot and the screenshot; observe again before relying on refs"
		}
		return p.browserScreenshot(ctx, page, args, extra)
	case "probe":
		out := page.browserProbe(ctx)
		out["title"] = tab.Target.Title
		return out, nil
	case "get_content":
		target := browserTargetArg(args, "selector", "ref")
		expr := `document.body ? document.body.innerText : ""`
		if target.present() {
			expr = page.expr(target) + `.innerText`
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
			expr = page.expr(target) + `.innerHTML`
		}
		html, err := page.evaluateString(ctx, expr)
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"html": html}), nil
	case "evaluate":
		result, err := page.evaluate(ctx, StringArg(args, "script"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": result}, nil
	case "scroll":
		return page.scroll(ctx, args)
	case "scroll_into_view":
		target := browserTargetArg(args, "selector", "ref")
		_, err := page.evaluate(ctx, fmt.Sprintf(`(() => { %s.scrollIntoView({ block: "center", inline: "center" }); return true })()`, page.expr(target)))
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"scrolled_into_view": target.label()}), nil
	case "drag":
		sourceTarget, source, err := page.resolvePoint(ctx, args, "selector", "ref", "x", "y")
		if err != nil {
			return nil, err
		}
		dropTarget, drop, err := page.resolvePoint(ctx, args, "target_selector", "target_ref", "to_x", "to_y")
		if err != nil {
			return nil, err
		}
		button := browserButton(args)
		if err := page.mouseDrag(ctx, source.X, source.Y, drop.X, drop.Y, button); err != nil {
			return nil, err
		}
		return map[string]any{
			"dragged": sourceTarget.labelOr("point"), "target": dropTarget.labelOr("point"), "button": button,
			"ref": sourceTarget.Ref, "selector": sourceTarget.Selector, "x": source.X, "y": source.Y,
			"target_ref": dropTarget.Ref, "target_selector": dropTarget.Selector, "to_x": drop.X, "to_y": drop.Y,
		}, nil
	case "upload":
		target := browserTargetArg(args, "selector", "ref")
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
		if target.present() {
			timeout, err := browserReadyTimeout(args)
			if err != nil {
				return nil, err
			}
			if err := page.waitTarget(ctx, target, timeout); err != nil {
				return nil, err
			}
			return target.withResult(map[string]any{"waited_for": target.label(), "timeout_ms": timeout}), nil
		}
		ms, err := browserWaitDuration(args)
		if err != nil {
			return nil, err
		}
		if err := sleepContext(ctx, time.Duration(ms)*time.Millisecond); err != nil {
			return nil, err
		}
		return map[string]any{"waited_ms": ms}, nil
	case "go_back", "go_forward":
		timeout, err := browserReadyTimeout(args)
		if err != nil {
			return nil, err
		}
		if err := page.navigateHistory(ctx, action == "go_forward"); err != nil {
			return nil, err
		}
		state.invalidateBrowserSnapshot(tab.Target.ID)
		if err := page.waitReady(ctx, timeout); err != nil {
			return nil, fmt.Errorf("%s: %w", action, err)
		}
		currentURL, _ := page.evaluateString(ctx, "location.href")
		return map[string]any{"url": currentURL, "timeout_ms": timeout}, nil
	case "reload":
		timeout, err := browserReadyTimeout(args)
		if err != nil {
			return nil, err
		}
		if _, err := page.conn.Call(ctx, "Page.reload", nil); err != nil {
			return nil, err
		}
		state.invalidateBrowserSnapshot(tab.Target.ID)
		if err := page.waitReady(ctx, timeout); err != nil {
			return nil, fmt.Errorf("reload: %w", err)
		}
		currentURL, _ := page.evaluateString(ctx, "location.href")
		return map[string]any{"url": currentURL, "timeout_ms": timeout}, nil
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
		if err := jsonUnmarshalLoose(result, &out); err != nil {
			return nil, err
		}
		return map[string]any{"pdf": out.Data, "mimeType": "application/pdf"}, nil
	default:
		return nil, fmt.Errorf("unknown browser action: %s", action)
	}
}

func heldKeyList(state *guiSessionState) []string {
	state.mu.Lock()
	defer state.mu.Unlock()
	keys := make([]string, 0, len(state.heldKeys))
	for key := range state.heldKeys {
		keys = append(keys, key)
	}
	return keys
}

// scroll scrolls the page, an element, or at a point by pixels or by pages
// of the target's visible area.
func (p *cdpPage) scroll(ctx context.Context, args map[string]any) (map[string]any, error) {
	direction := StringArg(args, "direction")
	if direction == "" {
		direction = "down"
	}
	target := browserTargetArg(args, "selector", "ref")
	pages, hasPages, err := floatArg(args, "pages")
	if err != nil {
		return nil, err
	}
	amount, err := intArgOr(args, "amount", browserDefaultScrollAmount)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"direction": direction}
	if hasPages {
		// Convert pages into pixels using the target's own visible size.
		sizeExpr := `({ w: window.innerWidth, h: window.innerHeight })`
		if target.present() {
			sizeExpr = fmt.Sprintf(`(() => { const el = %s; return { w: el.clientWidth, h: el.clientHeight }; })()`, p.expr(target))
		}
		var size struct {
			W float64 `json:"w"`
			H float64 `json:"h"`
		}
		if err := p.evaluateObject(ctx, sizeExpr, &size); err != nil {
			return nil, err
		}
		extent := size.H
		if direction == "left" || direction == "right" {
			extent = size.W
		}
		if extent <= 0 {
			return nil, errors.New("the scroll target has no visible area to measure pages against")
		}
		amount = int(math.Round(pages * extent))
		if amount < 1 {
			amount = 1
		}
		result["pages"] = pages
		result["page_extent"] = extent
	}
	result["amount"] = amount
	if target.present() {
		var out struct {
			Before float64 `json:"before"`
			After  float64 `json:"after"`
		}
		if err := p.evaluateObject(ctx, fmt.Sprintf(`(() => {
const el = %s;
const dx = %d, dy = %d;
const before = dx !== 0 ? el.scrollLeft : el.scrollTop;
el.scrollBy(dx, dy);
const after = dx !== 0 ? el.scrollLeft : el.scrollTop;
return { before, after };
})()`, p.expr(target), scrollDeltaX(direction, amount), scrollDeltaY(direction, amount)), &out); err != nil {
			return nil, err
		}
		result["scrolled"] = target.label()
		result["scroll_before"] = out.Before
		result["scroll_after"] = out.After
		return target.withResult(result), nil
	}
	var point elementPoint
	if x, y, ok := optionalFloatPoint(args, "x", "y"); ok {
		point = elementPoint{X: x, Y: y}
		result["scrolled"] = "point"
	} else {
		point, err = p.viewportCenter(ctx)
		if err != nil {
			return nil, err
		}
		result["scrolled"] = "page"
	}
	if err := p.mouseWheel(ctx, point.X, point.Y, scrollDeltaX(direction, amount), scrollDeltaY(direction, amount)); err != nil {
		return nil, err
	}
	result["x"], result["y"] = point.X, point.Y
	return result, nil
}

// browserPaste pastes text into a page element or the focused element. It
// dispatches a real paste event first (so editors that handle paste get the
// rich payload); when nothing consumes it, html is inserted through
// insertHTML for contenteditable targets and plain text is inserted through
// the input pipeline otherwise.
func (*BrowserProvider) browserPaste(ctx context.Context, page *cdpPage, args map[string]any) (map[string]any, error) {
	target := browserTargetArg(args, "selector", "ref")
	text := rawStringArg(args, "text")
	format := StringArg(args, "format")
	if format == "" {
		format = "text"
	}
	html := ""
	if format == "html" {
		html = text
	}
	targetExpr := "null"
	if target.present() {
		targetExpr = page.expr(target)
	}
	var pasted struct {
		Consumed bool   `json:"consumed"`
		Kind     string `json:"kind"`
		Target   string `json:"target"`
	}
	plain := text
	if format == "html" {
		plain = htmlToText(text)
	}
	if err := page.evaluateObject(ctx, fmt.Sprintf(`memohPaste(%s, %s, %s)`, targetExpr, jsQuote(plain), jsQuote(html)), &pasted); err != nil {
		return nil, err
	}
	result := target.withResult(map[string]any{"pasted": text, "format": format, "target": pasted.Target})
	switch {
	case pasted.Consumed:
		result["via"] = "paste_event"
	case format == "html" && pasted.Kind == "contenteditable":
		var inserted struct {
			Inserted bool `json:"inserted"`
		}
		if err := page.evaluateObject(ctx, fmt.Sprintf(`memohInsertHTML(%s, %s)`, targetExpr, jsQuote(html)), &inserted); err != nil {
			return nil, err
		}
		if !inserted.Inserted {
			return nil, errors.New("the editable target did not accept rich text (insertHTML refused) and no paste handler consumed it")
		}
		result["via"] = "insert_html"
	case pasted.Kind == "":
		return nil, fmt.Errorf("%s is not editable and nothing on the page consumed the paste event", pasted.Target)
	default:
		if format == "html" {
			result["rich_text"] = "not supported by this target; pasted as plain text"
		}
		if err := page.insertText(ctx, plain); err != nil {
			return nil, err
		}
		result["via"] = "insert_text"
	}
	return result, nil
}

// htmlToText is the plain-text companion of an html paste: tags dropped,
// block boundaries kept as newlines.
func htmlToText(html string) string {
	replacer := strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p>", "\n", "</div>", "\n", "</li>", "\n", "</h1>", "\n", "</h2>", "\n", "</h3>", "\n")
	text := replacer.Replace(html)
	var b strings.Builder
	inTag := false
	for _, r := range text {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	out := strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(b.String())
	return strings.TrimSpace(out)
}

func isCDPTabAction(action string) bool {
	switch action {
	case "tab_new", "tab_select", "tab_close", "tab_list":
		return true
	default:
		return false
	}
}

func (p *BrowserProvider) runCDPTabAction(ctx context.Context, client *bridge.Client, state *guiSessionState, action string, args map[string]any) (map[string]any, error) {
	switch action {
	case "tab_new":
		browser, err := p.resolveBrowser(ctx, client, state, StringArg(args, "browser_id"))
		if err != nil {
			return nil, err
		}
		newTarget, err := p.createTarget(ctx, client, browser, StringArg(args, "url"))
		if err != nil {
			return nil, err
		}
		state.selectTab(browser.ID, newTarget.ID)
		targets, _ := p.listTargets(ctx, client, browser)
		return map[string]any{"browser_id": browser.ID, "tab_id": newTarget.ID, "tab_index": targetIndex(targets, newTarget.ID), "target": newTarget.publicMap(), "url": newTarget.URL, "selected": true}, nil
	case "tab_select":
		tab, err := p.resolveTab(ctx, client, state, args)
		if err != nil {
			return nil, err
		}
		if err := p.activateTarget(ctx, client, tab.Browser, tab.Target.ID); err != nil {
			return nil, err
		}
		state.selectTab(tab.Browser.ID, tab.Target.ID)
		targets, _ := p.listTargets(ctx, client, tab.Browser)
		return map[string]any{"browser_id": tab.Browser.ID, "tab_id": tab.Target.ID, "tab_index": targetIndex(targets, tab.Target.ID), "target": tab.Target.publicMap(), "url": tab.Target.URL, "title": tab.Target.Title, "activated": true}, nil
	case "tab_close":
		tab, err := p.resolveTab(ctx, client, state, args)
		if err != nil {
			return nil, err
		}
		targets, _ := p.listTargets(ctx, client, tab.Browser)
		result, err := p.closeTarget(ctx, client, tab.Browser, tab.Target.ID)
		if err != nil {
			return nil, err
		}
		state.forgetTab(tab.Target.ID)
		return map[string]any{"browser_id": tab.Browser.ID, "tab_id": tab.Target.ID, "closed": targetIndex(targets, tab.Target.ID), "result": result}, nil
	case "tab_list":
		browser, err := p.resolveBrowser(ctx, client, state, StringArg(args, "browser_id"))
		if err != nil {
			return nil, err
		}
		targets, err := p.listTargets(ctx, client, browser)
		if err != nil {
			return nil, err
		}
		_, selectedTab, _ := state.defaults()
		return map[string]any{"browser_id": browser.ID, "tabs": publicTargets(targets), "selected_tab_id": selectedTab}, nil
	default:
		return nil, fmt.Errorf("unknown browser tab action: %s", action)
	}
}

func (p *BrowserProvider) browserActionResult(ctx context.Context, session SessionContext, botID string, data map[string]any, args map[string]any) any {
	b64, ok := data["screenshot"].(string)
	if !ok || b64 == "" {
		return data
	}
	imgBytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		delete(data, "screenshot")
		data["content"] = []map[string]any{{"type": "text", "text": "Screenshot captured (failed to decode image data)"}}
		return data
	}
	return p.screenshotResult(ctx, session, botID, imgBytes, "image/png", data, args)
}

// browserScreenshot captures the viewport or the full page and describes the
// coordinate space of the image: viewport captures map CSS pixels through
// the device pixel ratio with the origin at the viewport's top-left; full
// page captures use document coordinates.
func (*BrowserProvider) browserScreenshot(ctx context.Context, page *cdpPage, args map[string]any, extra map[string]any) (map[string]any, error) {
	fullPage, _, _ := BoolArg(args, "full_page")
	metrics, metricsErr := page.viewportMetrics(ctx)
	b64, err := page.captureScreenshot(ctx, fullPage)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"screenshot": b64, "mimeType": "image/png", "full_page": fullPage}
	if metricsErr == nil {
		data["viewport"] = metrics
		if fullPage {
			data["coordinate_space"] = "document CSS pixels: x/y for browser_action are viewport coordinates, so subtract the viewport scroll offset"
			data["origin"] = "top-left corner of the document"
		} else {
			data["coordinate_space"] = "viewport CSS pixels: divide image pixels by device_pixel_ratio to get x/y for browser_action"
			data["origin"] = "top-left corner of the viewport"
		}
	}
	for k, v := range extra {
		data[k] = v
	}
	return data, nil
}

// browserSnapshot observes the tab through the accessibility tree, keeps
// refs stable across observations of the same tab, and presents the result
// as a full or incremental page.
func (*BrowserProvider) browserSnapshot(ctx context.Context, state *guiSessionState, tab resolvedTab, page *cdpPage, args map[string]any, opts guiSnapshotOptions) (map[string]any, error) {
	key := "browser:" + tab.Target.ID
	if opts.Cursor != "" {
		pg, baseline, err := continueSnapshot(state, key, StringArg(args, "snapshot_id"), opts.Cursor, opts.Limit)
		if err != nil {
			return nil, err
		}
		out := pg.result()
		out["snapshot_id"] = baseline.SnapshotID
		return out, nil
	}
	// Refs are assigned page-wide whatever the scope, so reuse starts from
	// the latest observation of the tab at any scope.
	baseline := state.latestBaseline(key)
	scopeBackend := 0
	if opts.ScopeRef != "" {
		id, err := page.scopeBackendID(ctx, opts.ScopeRef)
		if err != nil {
			return nil, fmt.Errorf("scope_ref: %w", err)
		}
		scopeBackend = id
	}
	snapshotID := newBrowserSnapshotID()
	res, err := page.takeAXSnapshot(ctx, snapshotID, baseline, scopeBackend)
	if err != nil {
		return nil, err
	}
	state.recordBrowserSnapshot(guiSnapshotRecord{ID: snapshotID, BrowserID: tab.Browser.ID, TabID: tab.Target.ID, Taken: time.Now()})
	pg := presentSnapshot(state, key, opts.ScopeRef, snapshotID, res.Nodes, res.RefsByKey, res.MaxRef, res.Truncated, opts)
	out := pg.result()
	out["snapshot_id"] = snapshotID
	out["source"] = res.Source
	out["ref_count"] = res.Interactive
	out["reused_refs"] = res.Reused
	out["limit"] = opts.Limit
	out["taken_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	if res.Source == "cdp_accessibility" {
		out["ax_nodes"] = res.AXNodes
	} else {
		out["degraded"] = "the accessibility tree was unavailable; this is a flat DOM scan without hierarchy or states"
	}
	if opts.ScopeRef != "" {
		out["scope_ref"] = opts.ScopeRef
	}
	return out, nil
}

var browserSnapshotCounter struct {
	mu sync.Mutex
	n  uint64
}

// newBrowserSnapshotID mints a per-process unique snapshot id for a browser
// observation: time-based so ids from different server runs never collide.
func newBrowserSnapshotID() string {
	browserSnapshotCounter.mu.Lock()
	defer browserSnapshotCounter.mu.Unlock()
	browserSnapshotCounter.n++
	return fmt.Sprintf("b%x-%x", time.Now().UnixMilli(), browserSnapshotCounter.n)
}

// computerActionTarget resolves the application and snapshot a ref-based
// desktop action is bound to. Refs are only accepted together with the
// snapshot they came from: the explicit snapshot_id, else the session's
// latest desktop snapshot, which must have covered the requested application.
func (*BrowserProvider) computerActionTarget(state *guiSessionState, args map[string]any, needsRef bool) (appID, snapshotID string, err error) {
	appID = strings.TrimSpace(StringArg(args, "app_id"))
	_, _, sessionApp := state.defaults()
	if appID == "" {
		appID = sessionApp
	}
	snapshotID = strings.TrimSpace(StringArg(args, "snapshot_id"))
	if !needsRef {
		return appID, snapshotID, nil
	}
	if snapshotID != "" {
		return appID, snapshotID, nil
	}
	rec, ok := state.lastComputerSnapshot()
	if !ok {
		return "", "", errors.New("no desktop snapshot has been taken in this conversation; observe with computer_observe snapshot (or computer_context get_app) before using refs")
	}
	if appID != "" && rec.AppID != "" && rec.AppID != appID {
		return "", "", fmt.Errorf("the latest snapshot in this conversation covered %s, not %s; observe %s before using its refs", rec.AppID, appID, appID)
	}
	if appID != "" && rec.AppID == "" {
		// A whole-desktop snapshot covers every application; refs from it
		// stay valid for the selected app.
		return appID, rec.ID, nil
	}
	return appID, rec.ID, nil
}

func (p *BrowserProvider) execComputerAction(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	botID, err := p.requireComputerDisplay(session)
	if err != nil {
		return nil, err
	}
	spec, err := computerActionContract.normalize(args)
	if err != nil {
		return nil, err
	}
	action := spec.Name
	client, err := p.ensureComputerDisplay(ctx, botID)
	if err != nil {
		return nil, err
	}
	state := p.sessions.get(session)
	ref := normalizeBrowserRef(StringArg(args, "ref"))
	appID, snapshotID, err := p.computerActionTarget(state, args, ref != "")
	if err != nil {
		return nil, err
	}
	result, err := p.runComputerAction(ctx, botID, client, state, action, args, ref, snapshotID)
	if err != nil {
		p.releaseHeldPointer(ctx, botID, state)
		return nil, err
	}
	if appID != "" {
		if _, exists := result["app_id"]; !exists {
			result["app_id"] = appID
		}
	}
	if ref != "" {
		result["snapshot_id"] = snapshotID
	}
	return result, nil
}

// releaseHeldPointer sends a release for any pointer button the session left
// held (mouse_move / pointer with a button mask) after an action failed, so a
// timeout never leaves the desktop dragging.
func (p *BrowserProvider) releaseHeldPointer(ctx context.Context, botID string, state *guiSessionState) {
	mask := state.takeHeldButtons()
	if mask == 0 {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := p.sendDisplayInputs(releaseCtx, botID, displaypkg.ControlInput{Type: "pointer", X: defaultComputerWidth / 2, Y: defaultComputerHeight / 2, ButtonMask: 0}); err != nil {
		p.logger.Debug("release held pointer failed", slog.String("bot_id", botID), slog.Any("error", err))
	}
}

func (p *BrowserProvider) runComputerAction(ctx context.Context, botID string, client *bridge.Client, state *guiSessionState, action string, args map[string]any, ref, snapshotID string) (map[string]any, error) {
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
		state.setHeldButtons(mask)
		return map[string]any{"moved": true, "x": x, "y": y, "button_mask": mask, "held": mask != 0}, nil
	case "click", "double_click":
		count, err := guiClickCount(args, action)
		if err != nil {
			return nil, err
		}
		mask := mouseButtonMask(StringArg(args, "button"))
		if ref != "" {
			return p.clickByRef(ctx, botID, client, ref, snapshotID, mask, count)
		}
		x, y, err := requiredPoint(args)
		if err != nil {
			return nil, err
		}
		if err := p.pointerMultiClick(ctx, botID, x, y, mask, count); err != nil {
			return nil, err
		}
		return map[string]any{"clicked": true, "x": x, "y": y, "button": buttonName(mask), "click_count": count, "via": "rfb"}, nil
	case "type", "fill":
		text := rawStringArg(args, "text")
		replace := action == "fill"
		if ref != "" {
			return p.editByRef(ctx, botID, client, ref, snapshotID, text, replace)
		}
		if replace {
			if err := p.replaceFocusedText(ctx, botID, text); err != nil {
				return nil, err
			}
			return map[string]any{"filled": text, "target": "focus", "via": "rfb", "cleared_with": "select_all"}, nil
		}
		if err := p.typeText(ctx, botID, text); err != nil {
			return nil, err
		}
		return map[string]any{"typed": text, "target": "focus", "via": "rfb"}, nil
	case "set_value":
		result, err := computerA11ySetValue(ctx, client, ref, rawStringArg(args, "value"), snapshotID)
		if err != nil {
			return nil, err
		}
		if !result.OK {
			if result.Unsupported {
				return nil, fmt.Errorf("set_value is not supported for %s: %s", ref, result.Error)
			}
			return nil, result.failure("set_value", ref)
		}
		return map[string]any{"set_value": rawStringArg(args, "value"), "ref": ref, "via": "a11y", "detail": result.Detail}, nil
	case "paste":
		return p.computerPaste(ctx, botID, client, args, ref, snapshotID)
	case "select_text":
		mode := StringArg(args, "selection_type")
		if mode == "" {
			mode = "text"
		}
		result, err := computerA11ySelectText(ctx, client, ref, rawStringArg(args, "text"), rawStringArg(args, "prefix"), rawStringArg(args, "suffix"), mode, snapshotID)
		if err != nil {
			return nil, err
		}
		if !result.OK {
			if result.Unsupported {
				return nil, fmt.Errorf("select_text is not supported for %s: %s", ref, result.Error)
			}
			return nil, result.failure("select_text", ref)
		}
		out := map[string]any{"ref": ref, "via": "a11y", "selection_type": mode, "detail": result.Detail}
		if result.Selection != nil {
			out["start"] = result.Selection.Start
			out["end"] = result.Selection.End
		}
		return out, nil
	case "secondary_action":
		name := StringArg(args, "name")
		result, err := computerA11yNamedAction(ctx, client, ref, name, snapshotID)
		if err != nil {
			return nil, err
		}
		if !result.OK {
			if result.Unsupported {
				return nil, fmt.Errorf("secondary action %q is not available on %s: %s", name, ref, result.Error)
			}
			return nil, result.failure("action", ref)
		}
		return map[string]any{"ran": result.Detail, "ref": ref, "via": "a11y"}, nil
	case "drag":
		x, y, err := requiredPoint(args)
		if err != nil {
			return nil, err
		}
		toX, toY, err := requiredTargetPoint(args)
		if err != nil {
			return nil, err
		}
		mask := mouseButtonMask(StringArg(args, "button"))
		if err := p.pointerDrag(ctx, botID, x, y, toX, toY, mask); err != nil {
			return nil, err
		}
		return map[string]any{"dragged": true, "x": x, "y": y, "to_x": toX, "to_y": toY, "button": buttonName(mask)}, nil
	case "scroll":
		x, y := optionalPoint(args, defaultComputerWidth/2, defaultComputerHeight/2)
		target := "point"
		if ref != "" {
			located, err := computerA11yLocate(ctx, client, ref, snapshotID)
			if err != nil {
				return nil, err
			}
			if located.Center == nil {
				return nil, located.noCenterError(ref, "scroll at the element")
			}
			x, y = located.Center.X, located.Center.Y
			target = ref
		} else if _, hasX, _ := IntArg(args, "x"); !hasX {
			target = "desktop_center"
		}
		direction := StringArg(args, "direction")
		if direction == "" {
			direction = "down"
		}
		amount, err := intArgOr(args, "amount", computerDefaultScrollAmount)
		if err != nil {
			return nil, err
		}
		steps, err := p.pointerScroll(ctx, botID, x, y, direction, amount)
		if err != nil {
			return nil, err
		}
		return map[string]any{"scrolled": direction, "amount": amount, "wheel_steps": steps, "x": x, "y": y, "target": target, "via": "rfb"}, nil
	case "key":
		key := StringArg(args, "key")
		if err := p.typeKeyChord(ctx, botID, key); err != nil {
			return nil, err
		}
		return map[string]any{"pressed": key}, nil
	case "wait":
		ms, err := computerWaitDuration(args)
		if err != nil {
			return nil, err
		}
		if err := sleepContext(ctx, time.Duration(ms)*time.Millisecond); err != nil {
			return nil, err
		}
		return map[string]any{"waited_ms": ms}, nil
	default:
		return nil, fmt.Errorf("unknown computer action: %s", action)
	}
}

// clickByRef clicks an accessibility ref. A plain left single click goes
// through the element's AT-SPI action with a pointer fallback. Anything else
// (double or triple click, middle or right button) has no AT-SPI equivalent:
// the default action fires once and carries no button, so the ref is resolved
// to its on-screen centre and real pointer events are replayed instead.
func (p *BrowserProvider) clickByRef(ctx context.Context, botID string, client *bridge.Client, ref, snapshotID string, mask byte, count int) (map[string]any, error) {
	if mask == rfbButtonLeft && count == 1 {
		result, err := computerA11yClick(ctx, client, ref, snapshotID)
		if err != nil {
			return nil, err
		}
		if result.OK {
			out := map[string]any{"clicked": true, "ref": ref, "button": "left", "click_count": 1, "via": "a11y"}
			if result.Detail != "" {
				out["a11y_action"] = result.Detail
			}
			return out, nil
		}
		if point, ok := result.fallbackPoint(); ok {
			if err := p.pointerMultiClick(ctx, botID, point.X, point.Y, mask, 1); err != nil {
				return nil, err
			}
			return map[string]any{"clicked": true, "ref": ref, "x": point.X, "y": point.Y, "button": "left", "click_count": 1, "via": "rfb_fallback"}, nil
		}
		return nil, result.failure("click", ref)
	}
	located, err := computerA11yLocate(ctx, client, ref, snapshotID)
	if err != nil {
		return nil, err
	}
	if located.Center == nil {
		return nil, located.noCenterError(ref, clickCountName(count)+" "+buttonName(mask)+" click")
	}
	if err := p.pointerMultiClick(ctx, botID, located.Center.X, located.Center.Y, mask, count); err != nil {
		return nil, err
	}
	return map[string]any{"clicked": true, "ref": ref, "x": located.Center.X, "y": located.Center.Y, "button": buttonName(mask), "click_count": count, "via": "rfb"}, nil
}

func clickCountName(count int) string {
	switch count {
	case 2:
		return "double"
	case 3:
		return "triple"
	default:
		return "single"
	}
}

// editByRef types into or replaces the text of an accessibility ref. When
// AT-SPI cannot edit the widget and reports a pointer fallback, the widget is
// focused by clicking it; a replace then clears it before typing, so fill
// keeps its "replace everything" meaning on the fallback path too.
func (p *BrowserProvider) editByRef(ctx context.Context, botID string, client *bridge.Client, ref, snapshotID, text string, replace bool) (map[string]any, error) {
	result, err := computerA11yEdit(ctx, client, ref, text, replace, snapshotID)
	if err != nil {
		return nil, err
	}
	key := "typed"
	if replace {
		key = "filled"
	}
	if result.OK {
		return map[string]any{key: text, "ref": ref, "via": "a11y"}, nil
	}
	point, ok := result.fallbackPoint()
	if !ok {
		return nil, result.failure("edit", ref)
	}
	if err := p.pointerMultiClick(ctx, botID, point.X, point.Y, rfbButtonLeft, 1); err != nil {
		return nil, err
	}
	out := map[string]any{key: text, "ref": ref, "x": point.X, "y": point.Y, "via": "rfb_fallback"}
	if replace {
		if err := p.replaceFocusedText(ctx, botID, text); err != nil {
			return nil, err
		}
		out["cleared_with"] = "select_all"
		return out, nil
	}
	if err := p.typeText(ctx, botID, text); err != nil {
		return nil, err
	}
	return out, nil
}

// computerPaste pastes through the desktop clipboard: the current clipboard
// is saved, replaced with the text (as text/plain or text/html), the target
// is focused, Ctrl+V is sent, and the previous clipboard is restored unless
// something else changed it meanwhile. Clipboard use is serialised per bot.
func (p *BrowserProvider) computerPaste(ctx context.Context, botID string, client *bridge.Client, args map[string]any, ref, snapshotID string) (map[string]any, error) {
	text := rawStringArg(args, "text")
	format := StringArg(args, "format")
	if format == "" {
		format = "text"
	}
	mimeTarget := "UTF8_STRING"
	if format == "html" {
		mimeTarget = "text/html"
	}
	if capability := p.clipboardCapability(ctx, client); capability["available"] != true {
		return nil, fmt.Errorf("desktop paste is unavailable: %v", capability["reason"])
	}
	lockAny, _ := p.clipboards.LoadOrStore(botID, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	saved, savedTarget := p.readClipboard(ctx, client)
	if err := p.writeClipboard(ctx, client, mimeTarget, text); err != nil {
		return nil, fmt.Errorf("set desktop clipboard: %w", err)
	}
	// Restore (or clear) the previous clipboard on every exit path, but only
	// if it still holds what this paste put there.
	restore := func() (string, string) {
		current, _ := p.readClipboard(ctx, client)
		if current != text {
			return "kept", "the clipboard changed during the paste, so it was left as is"
		}
		if saved == "" && savedTarget == "" {
			if err := p.writeClipboard(ctx, client, "UTF8_STRING", ""); err != nil {
				return "failed", err.Error()
			}
			return "cleared", "the clipboard was empty before the paste"
		}
		if err := p.writeClipboard(ctx, client, savedTarget, saved); err != nil {
			return "failed", err.Error()
		}
		return "restored", ""
	}
	result := map[string]any{"pasted": text, "format": format, "via": "clipboard"}
	target := "focus"
	if ref != "" {
		located, err := computerA11yLocate(ctx, client, ref, snapshotID)
		if err != nil {
			status, note := restore()
			result["clipboard"] = status
			_ = note
			return nil, err
		}
		if located.Center == nil {
			restore()
			return nil, located.noCenterError(ref, "click to focus it for pasting")
		}
		if err := p.pointerMultiClick(ctx, botID, located.Center.X, located.Center.Y, rfbButtonLeft, 1); err != nil {
			restore()
			return nil, err
		}
		target = ref
		result["ref"] = ref
	}
	if err := p.typeKeyChord(ctx, botID, "Control+v"); err != nil {
		restore()
		return nil, err
	}
	if err := sleepContext(ctx, 250*time.Millisecond); err != nil {
		restore()
		return nil, err
	}
	status, note := restore()
	result["target"] = target
	result["clipboard"] = status
	if note != "" {
		result["clipboard_note"] = note
	}
	if format == "html" {
		result["rich_text"] = "clipboard offered text/html only; targets that read plain text get nothing"
	}
	return result, nil
}

// readClipboard returns the clipboard text and the target it was read with
// (text/html when the owner offers it, else UTF8_STRING). Empty when there
// is no clipboard owner.
func (*BrowserProvider) readClipboard(ctx context.Context, client *bridge.Client) (string, string) {
	const script = `export DISPLAY=:99
targets="$(timeout 2 xclip -o -selection clipboard -t TARGETS 2>/dev/null || true)"
[ -n "$targets" ] || exit 0
if printf '%s\n' "$targets" | grep -qx 'text/html'; then
  printf 'text/html\n'; timeout 2 xclip -o -selection clipboard -t text/html 2>/dev/null; exit 0
fi
printf 'UTF8_STRING\n'; timeout 2 xclip -o -selection clipboard -t UTF8_STRING 2>/dev/null || true`
	result, err := client.Exec(ctx, script, "/", 6)
	if err != nil || result == nil {
		return "", ""
	}
	out := result.Stdout
	target, rest, ok := strings.Cut(out, "\n")
	if !ok {
		return "", ""
	}
	return rest, strings.TrimSpace(target)
}

func (*BrowserProvider) writeClipboard(ctx context.Context, client *bridge.Client, target, text string) error {
	if target == "" {
		target = "UTF8_STRING"
	}
	// xclip forks a background owner; its stdio must be detached or the
	// bridge exec waits on it forever.
	cmd := "export DISPLAY=:99; printf '%s' " + shellQuote(text) + " | xclip -i -selection clipboard -t " + shellQuote(target) + " >/dev/null 2>&1 </dev/stdin"
	cmd = strings.Replace(cmd, "printf '%s' ", "printf %s ", 1)
	result, err := client.Exec(ctx, cmd, "/", 6)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("xclip exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
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

// pointerMultiClick sends count press/release pairs in one RFB batch. Sending
// them together keeps the clicks inside the toolkit's double-click interval,
// which separate round-trips do not guarantee.
func (p *BrowserProvider) pointerMultiClick(ctx context.Context, botID string, x, y int, mask byte, count int) error {
	if count < 1 {
		count = 1
	}
	events := make([]displaypkg.ControlInput, 0, count*2)
	for i := 0; i < count; i++ {
		events = append(events,
			displaypkg.ControlInput{Type: "pointer", X: x, Y: y, ButtonMask: mask},
			displaypkg.ControlInput{Type: "pointer", X: x, Y: y, ButtonMask: 0},
		)
	}
	return p.sendDisplayInputs(ctx, botID, events...)
}

// replaceFocusedText clears the focused widget with Select All + BackSpace
// and then types text, so a pointer-driven fill replaces instead of appending.
// An empty text leaves the widget cleared.
func (p *BrowserProvider) replaceFocusedText(ctx context.Context, botID, text string) error {
	const (
		keysymControl   uint32 = 0xffe3
		keysymA         uint32 = 'a'
		keysymBackSpace uint32 = 0xff08
	)
	events := []displaypkg.ControlInput{
		{Type: "key", Keysym: keysymControl, Down: true},
		{Type: "key", Keysym: keysymA, Down: true},
		{Type: "key", Keysym: keysymA, Down: false},
		{Type: "key", Keysym: keysymControl, Down: false},
		{Type: "key", Keysym: keysymBackSpace, Down: true},
		{Type: "key", Keysym: keysymBackSpace, Down: false},
	}
	for _, r := range text {
		ks := keysymForRune(r)
		events = append(events,
			displaypkg.ControlInput{Type: "key", Keysym: ks, Down: true},
			displaypkg.ControlInput{Type: "key", Keysym: ks, Down: false},
		)
	}
	return p.sendDisplayInputs(ctx, botID, events...)
}

// pointerDrag presses, moves in steps, and releases in one batch; if the batch
// fails the button is released with a second best-effort batch so the
// desktop is never left dragging.
func (p *BrowserProvider) pointerDrag(ctx context.Context, botID string, fromX, fromY, toX, toY int, mask byte) error {
	events := []displaypkg.ControlInput{{Type: "pointer", X: fromX, Y: fromY, ButtonMask: mask}}
	for i := 1; i <= 12; i++ {
		t := float64(i) / 12
		x := int(math.Round(float64(fromX) + float64(toX-fromX)*t))
		y := int(math.Round(float64(fromY) + float64(toY-fromY)*t))
		events = append(events, displaypkg.ControlInput{Type: "pointer", X: x, Y: y, ButtonMask: mask})
	}
	events = append(events, displaypkg.ControlInput{Type: "pointer", X: toX, Y: toY, ButtonMask: 0})
	if err := p.sendDisplayInputs(ctx, botID, events...); err != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = p.sendDisplayInputs(releaseCtx, botID, displaypkg.ControlInput{Type: "pointer", X: toX, Y: toY, ButtonMask: 0})
		return err
	}
	return nil
}

// pointerScroll turns a pixel amount into discrete RFB wheel steps (about
// 120 px each, capped at 20) and reports how many were sent, since the
// desktop cannot scroll by an exact pixel count.
func (p *BrowserProvider) pointerScroll(ctx context.Context, botID string, x, y int, direction string, amount int) (int, error) {
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
		return 0, fmt.Errorf("unsupported scroll direction %q", direction)
	}
	steps := clamp(int(math.Ceil(float64(amount)/120.0)), 1, 20)
	events := make([]displaypkg.ControlInput, 0, steps*2)
	for i := 0; i < steps; i++ {
		events = append(events,
			displaypkg.ControlInput{Type: "pointer", X: x, Y: y, ButtonMask: mask},
			displaypkg.ControlInput{Type: "pointer", X: x, Y: y, ButtonMask: 0},
		)
	}
	if err := p.sendDisplayInputs(ctx, botID, events...); err != nil {
		return 0, err
	}
	return steps, nil
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
