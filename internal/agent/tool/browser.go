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
			Description: "Operate the current workspace browser tab. Prefer element refs from an observation result over CSS selectors; use selectors only as a fallback and viewport x/y only when no element target applies. Use fill to replace or clear a value, type to insert at the caret, and press for shortcuts or submit keys. Each action accepts only the parameters listed for it; anything else is rejected before the browser is touched. After navigation or UI-changing actions, observe again only when the next step depends on the changed state.",
			Parameters:  browserActionContract.schema(browserActionContract.actionDescription("Browser action to perform:")),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execBrowserAction(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolBrowserObserve().String(),
			Description: "Inspect the current workspace browser without changing page state. Prefer snapshot for interactive elements and get_content for readable text. Use screenshot_annotate only when visual layout matters or you need rendered-page refs. Use evaluate only for small DOM queries or page-state checks. Screenshots are saved to a workspace path and are not attached automatically.",
			Parameters:  browserObserveContract.schema(browserObserveContract.actionDescription("What to observe from the page:")),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execBrowserObserve(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolComputerObserve().String(),
			Description: "Inspect the workspace desktop without changing state. Use snapshot for an accessibility listing of on-screen UI elements with refs, geometry, and states for later desktop actions. Use screenshot only when accessibility is unavailable or you need visual layout; the image is saved to a workspace path and is not attached automatically.",
			Parameters:  computerObserveContract.schema(computerObserveContract.actionDescription("What to observe from the desktop:")),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execComputerObserve(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolComputerAction().String(),
			Description: "Drive the workspace desktop. Prefer refs from a desktop observation snapshot for click/double_click/type/fill/scroll; coordinates (x, y) are only a fallback when no ref applies (native dialogs, raw drags, pointer hovers). Each action accepts only the parameters listed for it. For in-page browser targets, prefer browser-specific actions when they are available.",
			Parameters:  computerActionContract.schema(computerActionContract.actionDescription("Desktop action to perform:")),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execComputerAction(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolBrowserRemoteSession().String(),
			Description: "Advanced escape hatch for code-driven automation. Exposes the workspace Chrome CDP endpoint for chromium.connectOverCDP or other CDP clients.",
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
	for _, key := range []string{"ref", "selector", "script"} {
		if v := StringArg(args, key); v != "" {
			payload[key] = v
		}
	}
	if v, ok, _ := BoolArg(args, "full_page"); ok {
		payload["full_page"] = v
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
	runCtx, cancel := context.WithTimeout(ctx, browserToolTimeout)
	defer cancel()
	data, err := p.runCDPAction(runCtx, botID, args)
	if err != nil {
		return nil, err
	}
	return p.browserActionResult(ctx, botID, data), nil
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
	client, err := p.ensureCDP(ctx, botID)
	if err != nil {
		return nil, err
	}
	switch spec.Name {
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
		return p.closeTarget(ctx, client, StringArg(args, "session_id"))
	default:
		return nil, fmt.Errorf("unknown session action: %s", spec.Name)
	}
}

func (p *BrowserProvider) execComputerObserve(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	spec, err := computerObserveContract.normalize(args)
	if err != nil {
		return nil, err
	}
	switch spec.Name {
	case "screenshot":
		return p.execComputerScreenshot(ctx, session)
	case "snapshot":
		limit, _, err := IntArg(args, "limit")
		if err != nil {
			return nil, err
		}
		return p.execComputerSnapshot(ctx, session, limit)
	default:
		return nil, fmt.Errorf("unknown computer observe: %s", spec.Name)
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

func (p *BrowserProvider) execComputerSnapshot(ctx context.Context, session SessionContext, limit int) (any, error) {
	botID, err := p.requireComputerDisplay(session)
	if err != nil {
		return nil, err
	}
	client, err := p.ensureComputerDisplay(ctx, botID)
	if err != nil {
		return nil, err
	}
	snapshot, err := computerA11ySnapshot(ctx, client, limit)
	if err != nil {
		return nil, err
	}
	p.logger.Debug("computer snapshot",
		slog.String("bot_id", botID),
		slog.String("helper_version", snapshot.HelperVersion),
		slog.String("bus_address", snapshot.Diagnostics.BusAddress),
		slog.String("display", snapshot.Diagnostics.Display),
		slog.Int("accepted", snapshot.Diagnostics.Accepted),
		slog.Bool("truncated", snapshot.Truncated),
	)
	return map[string]any{
		"snapshot":       snapshot.text(),
		"ref_count":      len(snapshot.Items),
		"limit":          snapshot.Limit,
		"truncated":      snapshot.Truncated,
		"helper_version": snapshot.HelperVersion,
		"diagnostics":    snapshot.Diagnostics.public(),
	}, nil
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
		count, err := guiClickCount(args, action)
		if err != nil {
			return nil, err
		}
		mask := mouseButtonMask(StringArg(args, "button"))
		if ref != "" {
			return p.clickByRef(ctx, botID, ref, mask, count)
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
			return p.editByRef(ctx, botID, ref, text, replace)
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
			client, err := p.containers.MCPClient(ctx, botID)
			if err != nil {
				return nil, err
			}
			located, err := computerA11yLocate(ctx, client, ref)
			if err != nil {
				return nil, err
			}
			if located.Center == nil {
				return nil, fmt.Errorf("ref %s has no on-screen box to scroll at; observe again or pass x/y", ref)
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
func (p *BrowserProvider) clickByRef(ctx context.Context, botID, ref string, mask byte, count int) (map[string]any, error) {
	client, err := p.containers.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	if mask == rfbButtonLeft && count == 1 {
		result, err := computerA11yClick(ctx, client, ref)
		if err != nil {
			return nil, err
		}
		switch {
		case result.OK:
			out := map[string]any{"clicked": true, "ref": ref, "button": "left", "click_count": 1, "via": "a11y"}
			if result.Detail != "" {
				out["a11y_action"] = result.Detail
			}
			return out, nil
		case result.Fallback != nil && a11yPointerCoordValid(*result.Fallback):
			if err := p.pointerMultiClick(ctx, botID, result.Fallback.X, result.Fallback.Y, mask, 1); err != nil {
				return nil, err
			}
			return map[string]any{"clicked": true, "ref": ref, "x": result.Fallback.X, "y": result.Fallback.Y, "button": "left", "click_count": 1, "via": "rfb_fallback"}, nil
		default:
			if result.Error != "" {
				return nil, fmt.Errorf("a11y click %s failed: %s", ref, result.Error)
			}
			return nil, fmt.Errorf("a11y click %s failed without diagnostic", ref)
		}
	}
	located, err := computerA11yLocate(ctx, client, ref)
	if err != nil {
		return nil, err
	}
	if located.Center == nil {
		return nil, fmt.Errorf("ref %s has no on-screen box, and a %s %s click cannot be expressed as an accessibility action; observe again or pass x/y", ref, clickCountName(count), buttonName(mask))
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
func (p *BrowserProvider) editByRef(ctx context.Context, botID, ref, text string, replace bool) (map[string]any, error) {
	client, err := p.containers.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	result, err := computerA11yEdit(ctx, client, ref, text, replace)
	if err != nil {
		return nil, err
	}
	key := "typed"
	if replace {
		key = "filled"
	}
	switch {
	case result.OK:
		return map[string]any{key: text, "ref": ref, "via": "a11y"}, nil
	case result.Fallback != nil && a11yPointerCoordValid(*result.Fallback):
		if err := p.pointerMultiClick(ctx, botID, result.Fallback.X, result.Fallback.Y, rfbButtonLeft, 1); err != nil {
			return nil, err
		}
		out := map[string]any{key: text, "ref": ref, "x": result.Fallback.X, "y": result.Fallback.Y, "via": "rfb_fallback"}
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
		timeout, err := browserReadyTimeout(args)
		if err != nil {
			return nil, err
		}
		result, err := page.conn.Call(ctx, "Page.navigate", map[string]any{"url": targetURL})
		if err != nil {
			return nil, err
		}
		nav := map[string]any{}
		_ = json.Unmarshal(result, &nav)
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
		text := rawStringArg(args, "text")
		if _, err := page.evaluate(ctx, fmt.Sprintf(`(() => { const el = mustTarget(%s, %s); el.focus(); return true })()`, jsQuote(target.Selector), jsQuote(target.Ref))); err != nil {
			return nil, err
		}
		if err := page.insertText(ctx, text); err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"typed": text}), nil
	case "fill":
		target := browserTargetArg(args, "selector", "ref")
		text := rawStringArg(args, "text")
		result, err := page.evaluate(ctx, fmt.Sprintf(`(() => {
const el = mustTarget(%s, %s);
const text = %s;
el.focus();
if ("value" in el && !(el instanceof HTMLButtonElement)) {
  el.value = text;
  el.dispatchEvent(new InputEvent("input", { bubbles: true, data: text, inputType: text === "" ? "deleteContentBackward" : "insertText" }));
  el.dispatchEvent(new Event("change", { bubbles: true }));
  return { value: el.value, kind: "value" };
}
if (el.isContentEditable) {
  el.textContent = text;
  el.dispatchEvent(new InputEvent("input", { bubbles: true, data: text, inputType: text === "" ? "deleteContentBackward" : "insertText" }));
  return { value: el.textContent, kind: "contenteditable" };
}
throw new Error("element is not editable: " + %s);
})()`, jsQuote(target.Selector), jsQuote(target.Ref), jsQuote(text), jsQuote(target.label())))
		if err != nil {
			return nil, err
		}
		out := map[string]any{"filled": text}
		if fields, ok := result.(map[string]any); ok {
			out["value"] = fields["value"]
			out["kind"] = fields["kind"]
		}
		return target.withResult(out), nil
	case "press":
		key := StringArg(args, "key")
		if err := page.pressKey(ctx, key); err != nil {
			return nil, err
		}
		return map[string]any{"pressed": key}, nil
	case "keyboard_type":
		text := rawStringArg(args, "text")
		if err := page.insertText(ctx, text); err != nil {
			return nil, err
		}
		return map[string]any{"inserted_text": text}, nil
	case "keydown", "keyup":
		key := StringArg(args, "key")
		if err := page.dispatchKey(ctx, key, action == "keydown"); err != nil {
			return nil, err
		}
		return map[string]any{action: key}, nil
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
const el = mustTarget(%s, %s);
const value = %s;
if (!(el instanceof HTMLSelectElement)) throw new Error("element is not a select: " + %s);
const option = Array.from(el.options).find(o => o.value === value);
if (!option) throw new Error("select has no option with value " + JSON.stringify(value));
el.value = value;
el.dispatchEvent(new Event("input", { bubbles: true }));
el.dispatchEvent(new Event("change", { bubbles: true }));
return el.value;
})()`, jsQuote(target.Selector), jsQuote(target.Ref), jsQuote(value), jsQuote(target.label())))
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
		amount, err := intArgOr(args, "amount", browserDefaultScrollAmount)
		if err != nil {
			return nil, err
		}
		target := browserTargetArg(args, "selector", "ref")
		scrolled := "page"
		if target.present() {
			_, err = page.evaluate(ctx, fmt.Sprintf(`(() => {
const el = mustTarget(%s, %s);
const dx = %d;
const dy = %d;
el.scrollBy(dx, dy);
return true;
})()`, jsQuote(target.Selector), jsQuote(target.Ref), scrollDeltaX(direction, amount), scrollDeltaY(direction, amount)))
			scrolled = target.label()
		} else {
			var point elementPoint
			if x, y, ok := optionalFloatPoint(args, "x", "y"); ok {
				point = elementPoint{X: x, Y: y}
				scrolled = "point"
			} else {
				point, err = page.viewportCenter(ctx)
				if err != nil {
					return nil, err
				}
			}
			err = page.mouseWheel(ctx, point.X, point.Y, scrollDeltaX(direction, amount), scrollDeltaY(direction, amount))
		}
		if err != nil {
			return nil, err
		}
		return target.withResult(map[string]any{"scrolled": scrolled, "direction": direction, "amount": amount}), nil
	case "scroll_into_view":
		target := browserTargetArg(args, "selector", "ref")
		_, err := page.evaluate(ctx, fmt.Sprintf(`(() => { mustTarget(%s, %s).scrollIntoView({ block: "center", inline: "center" }); return true })()`, jsQuote(target.Selector), jsQuote(target.Ref)))
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

// labelOr returns the target label, or fallback when the action was
// addressed by coordinates instead of an element.
func (t browserTarget) labelOr(fallback string) string {
	if t.present() {
		return t.label()
	}
	return fallback
}

// browserButton returns the CDP mouse button name for the call (validation
// has already restricted it to left, middle, or right).
func browserButton(args map[string]any) string {
	button := strings.ToLower(strings.TrimSpace(StringArg(args, "button")))
	if button == "" {
		return "left"
	}
	return button
}

// cdpButtonsMask maps a CDP button name to the Input.dispatchMouseEvent
// "buttons" bitmask reported while it is held.
func cdpButtonsMask(button string) int {
	switch button {
	case "right":
		return 2
	case "middle":
		return 4
	default:
		return 1
	}
}

// optionalFloatPoint reads a coordinate pair when both halves are present.
func optionalFloatPoint(args map[string]any, xKey, yKey string) (float64, float64, bool) {
	x, okX, errX := IntArg(args, xKey)
	y, okY, errY := IntArg(args, yKey)
	if errX != nil || errY != nil || !okX || !okY {
		return 0, 0, false
	}
	return float64(x), float64(y), true
}

// resolvePoint resolves an element target (ref/selector keys) or an explicit
// coordinate pair to a viewport point. The contract guarantees exactly one of
// them is present for actions that call this.
func (p *cdpPage) resolvePoint(ctx context.Context, args map[string]any, selectorKey, refKey, xKey, yKey string) (browserTarget, elementPoint, error) {
	target := browserTargetArg(args, selectorKey, refKey)
	if target.present() {
		point, err := p.elementPoint(ctx, target)
		return target, point, err
	}
	if x, y, ok := optionalFloatPoint(args, xKey, yKey); ok {
		return target, elementPoint{X: x, Y: y}, nil
	}
	return target, elementPoint{}, fmt.Errorf("%s/%s or %s/%s is required", refKey, selectorKey, xKey, yKey)
}

func (p *cdpPage) viewportCenter(ctx context.Context) (elementPoint, error) {
	value, err := p.evaluate(ctx, `({ x: window.innerWidth / 2, y: window.innerHeight / 2 })`)
	if err != nil {
		return elementPoint{}, err
	}
	var point elementPoint
	raw, _ := json.Marshal(value)
	if err := json.Unmarshal(raw, &point); err != nil {
		return elementPoint{}, err
	}
	if point.X <= 0 || point.Y <= 0 {
		point = elementPoint{X: defaultComputerWidth / 2, Y: defaultComputerHeight / 2}
	}
	return point, nil
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

// mouseDrag presses, moves in steps, and releases. The release is attempted
// even when an intermediate move fails so the page is not left with a button
// held down.
func (p *cdpPage) mouseDrag(ctx context.Context, fromX, fromY, toX, toY float64, button string) (err error) {
	if err := p.mouseMove(ctx, fromX, fromY); err != nil {
		return err
	}
	if _, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": fromX, "y": fromY, "button": button, "clickCount": 1}); err != nil {
		return err
	}
	lastX, lastY := fromX, fromY
	defer func() {
		if _, releaseErr := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": lastX, "y": lastY, "button": button, "clickCount": 1}); releaseErr != nil && err == nil {
			err = releaseErr
		}
	}()
	buttons := cdpButtonsMask(button)
	for i := 1; i <= 8; i++ {
		t := float64(i) / 8
		x := fromX + (toX-fromX)*t
		y := fromY + (toY-fromY)*t
		if _, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y, "button": button, "buttons": buttons}); err != nil {
			return err
		}
		lastX, lastY = x, y
	}
	return nil
}

func (p *cdpPage) mouseWheel(ctx context.Context, x, y float64, deltaX, deltaY int) error {
	_, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":   "mouseWheel",
		"x":      x,
		"y":      y,
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
			return fmt.Errorf("page did not become ready within %d ms", timeoutMS)
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
