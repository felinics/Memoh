package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

// execComputerContext implements the computer_context tool: discovery and
// selection of applications and browsers, plus the machine-readable contract
// of the GUI tools. Selection only changes this session's defaults; nothing
// here navigates, clicks, or types.
func (p *BrowserProvider) execComputerContext(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	spec, err := computerContextContract.normalize(args)
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
	switch spec.Name {
	case "get_state":
		return p.contextState(ctx, botID, client, state), nil
	case "list_apps":
		apps, err := p.listApps(ctx, botID, client)
		if err != nil {
			return nil, err
		}
		return map[string]any{"apps": publicApps(apps.Apps), "helper_version": apps.HelperVersion}, nil
	case "get_app":
		launch, _, _ := BoolArg(args, "launch")
		return p.getApp(ctx, botID, client, state, StringArg(args, "app"), launch)
	case "list_browsers":
		browsers, err := p.listBrowsers(ctx, client)
		if err != nil {
			return nil, err
		}
		return map[string]any{"browsers": browsers}, nil
	case "get_browser":
		return p.getBrowser(ctx, client, state, StringArg(args, "browser_id"), StringArg(args, "url"))
	case "documentation":
		return p.contextDocumentation(ctx, botID, client, StringArg(args, "app_id"), StringArg(args, "browser_id"))
	default:
		return nil, fmt.Errorf("unknown computer_context action: %s", spec.Name)
	}
}

// listApps requires the desktop (and with it the accessibility bus) to be up.
func (p *BrowserProvider) listApps(ctx context.Context, botID string, client *bridge.Client) (*a11yAppsOutput, error) {
	if p.display == nil {
		return nil, errors.New("workspace display service is not configured")
	}
	if _, err := p.ensureComputerDisplay(ctx, botID); err != nil {
		return nil, err
	}
	apps, err := computerA11yApps(ctx, client)
	if err != nil {
		return nil, err
	}
	return apps, nil
}

func publicApps(apps []a11yAppInfo) []map[string]any {
	out := make([]map[string]any, 0, len(apps))
	for _, app := range apps {
		out = append(out, app.publicMap())
	}
	return out
}

// listBrowsers lists the workspace browsers together with their page tabs.
func (p *BrowserProvider) listBrowsers(ctx context.Context, client *bridge.Client) ([]map[string]any, error) {
	endpoints, err := p.discoverBrowsers(ctx, client)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(endpoints))
	for _, ep := range endpoints {
		entry := ep.publicMap()
		if ep.Status == "running" {
			targets, err := p.listTargets(ctx, client, ep)
			if err != nil {
				entry["tabs_error"] = err.Error()
			} else {
				entry["tabs"] = publicTargets(targets)
			}
		} else {
			entry["tabs"] = []map[string]any{}
		}
		out = append(out, entry)
	}
	return out, nil
}

// contextState reports every domain separately: a failure in one (say, the
// accessibility bus is down) is returned as that domain's error instead of
// being disguised as an empty list.
func (p *BrowserProvider) contextState(ctx context.Context, botID string, client *bridge.Client, state *guiSessionState) map[string]any {
	out := map[string]any{}
	if apps, err := p.listApps(ctx, botID, client); err != nil {
		out["apps_error"] = err.Error()
	} else {
		out["apps"] = publicApps(apps.Apps)
		out["helper_version"] = apps.HelperVersion
	}
	if browsers, err := p.listBrowsers(ctx, client); err != nil {
		out["browsers_error"] = err.Error()
	} else {
		out["browsers"] = browsers
	}
	browserID, tabID, appID := state.defaults()
	selected := map[string]any{}
	if browserID != "" {
		selected["browser_id"] = browserID
	}
	if tabID != "" {
		selected["tab_id"] = tabID
	}
	if appID != "" {
		selected["app_id"] = appID
	}
	if rec, ok := state.lastComputerSnapshot(); ok {
		selected["desktop_snapshot_id"] = rec.ID
	}
	out["session_selection"] = selected
	out["scope"] = "native workspace desktop and browsers of this bot; connected computers are not included"
	return out
}

// getApp selects an application. Selection is by instance id, exact name,
// desktop entry / executable name, or absolute path. A name that matches
// several running instances is ambiguous and returns the candidates. When
// nothing is running and launch is set, an installed application is started
// as argv (never through a shell) and observed once it registers on the
// accessibility bus.
func (p *BrowserProvider) getApp(ctx context.Context, botID string, client *bridge.Client, state *guiSessionState, selector string, launch bool) (any, error) {
	selector = strings.TrimSpace(selector)
	apps, err := p.listApps(ctx, botID, client)
	if err != nil {
		return nil, err
	}
	matches := matchApps(apps.Apps, selector)
	switch {
	case len(matches) > 1:
		return nil, fmt.Errorf("%d running applications match %q; pass one of their app_id values: %s", len(matches), selector, strings.Join(appIDs(matches), ", "))
	case len(matches) == 1:
		return p.selectApp(ctx, client, state, matches[0], false)
	}
	if isComputerAppID(selector) {
		return nil, fmt.Errorf("application %s is not running (instance ids expire with the process); use list_apps", selector)
	}
	if !launch {
		return nil, fmt.Errorf("no running application matches %q; pass launch=true to start an installed application, or use list_apps", selector)
	}
	launched, err := p.launchApp(ctx, client, selector)
	if err != nil {
		return nil, err
	}
	app, err := p.waitForApp(ctx, client, launched, apps.Apps)
	if errors.Is(err, errAppNotAccessible) {
		// The process runs but exposes no accessibility tree (xterm and
		// other non-toolkit apps). That is a capability gap, not a failed
		// launch: report it plainly instead of pretending it can be driven
		// by refs.
		return map[string]any{
			"launched":   map[string]any{"argv": launched.argv, "pid": launched.pid, "source": launched.source},
			"selected":   false,
			"accessible": false,
			"note":       err.Error() + "; it cannot be observed or driven by refs, only by computer_observe screenshot and coordinates",
		}, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := p.selectApp(ctx, client, state, app, true)
	if err != nil {
		return nil, err
	}
	if out, ok := result.(map[string]any); ok {
		out["launched"] = map[string]any{"argv": launched.argv, "pid": launched.pid, "source": launched.source}
	}
	return result, nil
}

func appIDs(apps []a11yAppInfo) []string {
	ids := make([]string, 0, len(apps))
	for _, app := range apps {
		ids = append(ids, app.AppID)
	}
	return ids
}

// matchApps resolves a selector against running applications: instance id,
// then exact case-insensitive name, then the binary name behind the pid.
func matchApps(apps []a11yAppInfo, selector string) []a11yAppInfo {
	wanted := strings.ToLower(strings.TrimSpace(selector))
	if wanted == "" {
		return nil
	}
	var out []a11yAppInfo
	for _, app := range apps {
		if strings.EqualFold(app.AppID, wanted) {
			return []a11yAppInfo{app}
		}
	}
	for _, app := range apps {
		if strings.ToLower(strings.TrimSpace(app.Name)) == wanted {
			out = append(out, app)
		}
	}
	if len(out) > 0 {
		return out
	}
	base := path.Base(wanted)
	for _, app := range apps {
		name := strings.ToLower(strings.TrimSpace(app.Name))
		if name == base || strings.TrimSuffix(name, ".desktop") == base {
			out = append(out, app)
		}
	}
	return out
}

// selectApp records the application as the session default and returns it
// with an initial, application-scoped snapshot.
func (p *BrowserProvider) selectApp(ctx context.Context, client *bridge.Client, state *guiSessionState, app a11yAppInfo, launched bool) (any, error) {
	state.selectApp(app.AppID)
	opts := guiSnapshotOptions{Limit: a11ySnapshotDefaultLimit, DisableDiffing: true}
	snapshot, err := p.computerSnapshot(ctx, "", client, state, map[string]any{"app_id": app.AppID}, opts)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"app":         app.publicMap(),
		"app_id":      app.AppID,
		"selected":    true,
		"was_running": !launched,
	}
	for k, v := range snapshot {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out, nil
}

// errAppNotAccessible marks a launched process that never registered on the
// accessibility bus: it runs, but the desktop tools cannot address it by ref.
var errAppNotAccessible = errors.New("application did not register on the accessibility bus")

// processAlive reports whether pid still exists in the workspace.
func processAlive(ctx context.Context, client *bridge.Client, pid int) bool {
	result, err := client.Exec(ctx, fmt.Sprintf("kill -0 %d 2>/dev/null", pid), "/", 5)
	return err == nil && result != nil && result.ExitCode == 0
}

type launchedApp struct {
	argv   []string
	pid    int
	source string
}

// launchAppScript resolves what to start. Absolute paths must exist and be
// executable; otherwise a desktop entry's Exec line or a PATH lookup decides.
// The argv is printed as NUL-separated fields for the Go side to quote.
const launchAppScript = `# memoh-launch-resolve
target="$1"
case "$target" in
  /*)
    if [ -x "$target" ] && [ ! -d "$target" ]; then printf 'path\n%s\n' "$target"; exit 0; fi
    echo "not executable: $target" >&2; exit 4;;
esac
for dir in /usr/share/applications /usr/local/share/applications "$HOME/.local/share/applications"; do
  [ -d "$dir" ] || continue
  for f in "$dir"/*.desktop; do
    [ -f "$f" ] || continue
    stem="$(basename "$f" .desktop)"
    name="$(sed -n 's/^Name=//p' "$f" | head -n1)"
    if [ "$(printf '%s' "$stem" | tr 'A-Z' 'a-z')" = "$(printf '%s' "$target" | tr 'A-Z' 'a-z')" ] || [ "$(printf '%s' "$name" | tr 'A-Z' 'a-z')" = "$(printf '%s' "$target" | tr 'A-Z' 'a-z')" ]; then
      exec_line="$(sed -n 's/^Exec=//p' "$f" | head -n1 | sed 's/ %[a-zA-Z]//g')"
      [ -n "$exec_line" ] || continue
      printf 'desktop\n%s\n' "$exec_line"; exit 0
    fi
  done
done
if bin="$(command -v "$target" 2>/dev/null)"; then printf 'path\n%s\n' "$bin"; exit 0; fi
echo "no installed application, desktop entry, or executable matches $target" >&2
exit 5`

func (*BrowserProvider) launchApp(ctx context.Context, client *bridge.Client, selector string) (launchedApp, error) {
	if strings.ContainsAny(selector, "\n\x00") {
		return launchedApp{}, errors.New("application selector contains invalid characters")
	}
	resolve, err := client.Exec(ctx, "sh -c "+shellQuote(launchAppScript)+" memoh-launch "+shellQuote(selector), "/", 10)
	if err != nil {
		return launchedApp{}, err
	}
	if resolve.ExitCode != 0 {
		msg := strings.TrimSpace(resolve.Stderr)
		if msg == "" {
			msg = "could not resolve application " + selector
		}
		return launchedApp{}, errors.New(msg)
	}
	lines := strings.Split(strings.TrimSpace(resolve.Stdout), "\n")
	if len(lines) < 2 {
		return launchedApp{}, errors.New("application resolution returned no command")
	}
	source := strings.TrimSpace(lines[0])
	argv := strings.Fields(strings.TrimSpace(lines[1]))
	if len(argv) == 0 {
		return launchedApp{}, errors.New("application resolution returned an empty command")
	}
	// Each argv element is quoted separately: the user's selector never
	// reaches a shell unquoted, and desktop Exec lines are split on
	// whitespace only.
	cmd := "DISPLAY=:99 nohup " + shellQuoteArgs(argv) + " >/dev/null 2>&1 & echo $!"
	run, err := client.Exec(ctx, cmd, "/", 10)
	if err != nil {
		return launchedApp{}, err
	}
	if run.ExitCode != 0 {
		return launchedApp{}, fmt.Errorf("launch %s failed: %s", argv[0], strings.TrimSpace(run.Stderr))
	}
	pid := 0
	_, _ = fmt.Sscanf(strings.TrimSpace(run.Stdout), "%d", &pid)
	return launchedApp{argv: argv, pid: pid, source: source}, nil
}

// waitForApp polls the accessibility bus until the launched process (or a
// new application that was not running before) registers.
func (*BrowserProvider) waitForApp(ctx context.Context, client *bridge.Client, launched launchedApp, before []a11yAppInfo) (a11yAppInfo, error) {
	known := make(map[string]struct{}, len(before))
	for _, app := range before {
		known[app.AppID] = struct{}{}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		apps, err := computerA11yApps(ctx, client)
		if err == nil {
			var fresh []a11yAppInfo
			for _, app := range apps.Apps {
				if launched.pid > 0 && app.PID == launched.pid {
					return app, nil
				}
				if _, seen := known[app.AppID]; !seen {
					fresh = append(fresh, app)
				}
			}
			if len(fresh) == 1 {
				return fresh[0], nil
			}
		}
		if time.Now().After(deadline) {
			if launched.pid > 0 && processAlive(ctx, client, launched.pid) {
				return a11yAppInfo{}, fmt.Errorf("%w: %s (pid %d) is running but exposed no AT-SPI tree within 10 s", errAppNotAccessible, launched.argv[0], launched.pid)
			}
			return a11yAppInfo{}, fmt.Errorf("%s was started (pid %d) but exited or handed off to an existing instance before registering on the accessibility bus", launched.argv[0], launched.pid)
		}
		if err := sleepContext(ctx, 500*time.Millisecond); err != nil {
			return a11yAppInfo{}, err
		}
	}
}

// getBrowser selects a running browser by id or by a URL one of its tabs
// shows. It never navigates or opens tabs; ambiguity returns candidates.
func (p *BrowserProvider) getBrowser(ctx context.Context, client *bridge.Client, state *guiSessionState, browserID, matchURL string) (any, error) {
	endpoints, err := p.discoverBrowsers(ctx, client)
	if err != nil {
		return nil, err
	}
	browserID = strings.TrimSpace(browserID)
	matchURL = strings.TrimSpace(matchURL)
	if browserID == "" && matchURL == "" {
		// No selector: the default workspace browser, started if needed.
		ep, err := p.resolveBrowser(ctx, client, nil, "")
		if err != nil {
			return nil, err
		}
		state.selectBrowser(ep.ID)
		return p.browserSelection(ctx, client, ep, "", "default"), nil
	}
	if browserID != "" {
		for _, ep := range endpoints {
			if ep.ID != browserID {
				continue
			}
			if ep.Status != "running" {
				return nil, fmt.Errorf("browser %s is not running", browserID)
			}
			state.selectBrowser(ep.ID)
			return p.browserSelection(ctx, client, ep, "", "browser_id"), nil
		}
		return nil, fmt.Errorf("browser %s was not found; use list_browsers", browserID)
	}
	type hit struct {
		ep  browserEndpoint
		tab cdpTarget
	}
	var hits []hit
	for _, ep := range endpoints {
		if ep.Status != "running" {
			continue
		}
		targets, err := p.listTargets(ctx, client, ep)
		if err != nil {
			continue
		}
		for _, target := range targets {
			if target.Type == "page" && urlMatches(target.URL, matchURL) {
				hits = append(hits, hit{ep: ep, tab: target})
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no running browser has a tab at %s; get_browser does not navigate", matchURL)
	case 1:
		state.selectTab(hits[0].ep.ID, hits[0].tab.ID)
		return p.browserSelection(ctx, client, hits[0].ep, hits[0].tab.ID, "url"), nil
	default:
		candidates := make([]map[string]any, 0, len(hits))
		for _, h := range hits {
			entry := h.tab.publicMap()
			entry["browser_id"] = h.ep.ID
			candidates = append(candidates, entry)
		}
		return map[string]any{
			"selected":   false,
			"error":      fmt.Sprintf("%d tabs match %s; pass browser_id (and tab_id) explicitly", len(hits), matchURL),
			"candidates": candidates,
		}, nil
	}
}

func urlMatches(actual, wanted string) bool {
	actual = strings.TrimSuffix(strings.TrimSpace(actual), "/")
	wanted = strings.TrimSuffix(strings.TrimSpace(wanted), "/")
	if actual == "" || wanted == "" {
		return false
	}
	return actual == wanted || strings.HasPrefix(actual, wanted+"/") || strings.HasPrefix(actual, wanted+"?") || strings.HasPrefix(actual, wanted+"#")
}

func (p *BrowserProvider) browserSelection(ctx context.Context, client *bridge.Client, ep browserEndpoint, tabID, source string) map[string]any {
	out := ep.publicMap()
	out["selected"] = true
	out["selection_source"] = source
	if targets, err := p.listTargets(ctx, client, ep); err == nil {
		out["tabs"] = publicTargets(targets)
	} else {
		out["tabs_error"] = err.Error()
	}
	if tabID != "" {
		out["tab_id"] = tabID
	}
	return out
}

// contextDocumentation returns the contracts the schemas and validation are
// generated from, plus what each backend can and cannot do.
func (p *BrowserProvider) contextDocumentation(ctx context.Context, botID string, client *bridge.Client, appID, browserID string) (any, error) {
	doc := map[string]any{
		"tools": map[string]any{
			ToolBrowserAction().String():        browserActionContract.documentation(),
			ToolBrowserObserve().String():       browserObserveContract.documentation(),
			ToolComputerAction().String():       computerActionContract.documentation(),
			ToolComputerObserve().String():      computerObserveContract.documentation(),
			ToolBrowserRemoteSession().String(): browserRemoteSessionContract.documentation(),
			ToolComputerContext().String():      computerContextContract.documentation(),
		},
		"identity": map[string]any{
			"app_id":      "app:<pid> from list_apps / get_app; expires with the process",
			"browser_id":  "chrome-<cdp port> from list_browsers; the workspace browser is chrome-9222",
			"tab_id":      "CDP target id from tab_list / tab_new; invalid after the browser restarts",
			"snapshot_id": "returned by every snapshot; refs are only valid with the snapshot they came from and are refused otherwise",
			"ref":         "e<N>; browser refs are pinned to the elements listed by that snapshot and never re-numbered",
		},
		"coordinates": map[string]any{
			"browser":  "viewport CSS pixels of the tab",
			"computer": "desktop pixels; pointer targets must lie within 0..32767",
		},
		"timing": map[string]any{
			"timeout":     "readiness bound in ms (navigate/reload/history/wait-for-element), default 30000, max 45000",
			"duration_ms": "fixed pause in ms for wait without a target, default 1000, max 10000; never combined with timeout",
		},
		"capabilities": map[string]any{
			"browser": map[string]any{
				"secondary_action": "unsupported: the DOM backend exposes no accessibility actions; use click/press instead",
				"paste":            "text/md insert the characters; html dispatches a paste event with text/html and falls back to insertHTML for contenteditable or plain insertion for fields",
				"select_text":      "input, textarea, and contenteditable; offsets are UTF-16 code units",
				"set_value":        "input, textarea, select (option must exist), contenteditable",
				"scroll_pages":     "pages are multiples of the target's visible height/width",
				"held_keys":        "keydown holds a key for later calls in this session until keyup; navigation releases nothing on its own",
			},
			"computer": map[string]any{
				"secondary_action": "AT-SPI actions listed as actions=... in the snapshot; unlisted names are refused",
				"paste":            "desktop clipboard via xclip; previous clipboard is restored unless it changed during the paste; html sets text/html only",
				"select_text":      "AT-SPI Text interface; offsets are characters",
				"set_value":        "EditableText contents or Value-interface numbers (sliders, spin buttons)",
				"scroll":           "discrete wheel steps of about 120 px (reported as wheel_steps)",
				"double_click":     "pointer replay at the element centre; elements without an on-screen box are refused",
			},
		},
	}
	helper := map[string]any{"protocol_version": a11yProtocolVersion}
	if p.display != nil {
		if _, err := p.ensureComputerDisplay(ctx, botID); err == nil {
			if apps, err := computerA11yApps(ctx, client); err == nil {
				helper["helper_version"] = apps.HelperVersion
				helper["reachable"] = true
			} else {
				helper["reachable"] = false
				helper["error"] = err.Error()
			}
		}
		helper["clipboard"] = p.clipboardCapability(ctx, client)
	}
	doc["helper"] = helper
	if appID = strings.TrimSpace(appID); appID != "" {
		apps, err := computerA11yApps(ctx, client)
		if err != nil {
			return nil, err
		}
		found := false
		for _, app := range apps.Apps {
			if app.AppID == appID {
				doc["app"] = app.publicMap()
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("application %s is not running", appID)
		}
	}
	if browserID = strings.TrimSpace(browserID); browserID != "" {
		endpoints, err := p.discoverBrowsers(ctx, client)
		if err != nil {
			return nil, err
		}
		found := false
		for _, ep := range endpoints {
			if ep.ID == browserID {
				doc["browser"] = ep.publicMap()
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("browser %s was not found", browserID)
		}
	}
	return doc, nil
}

// clipboardCapability reports whether desktop paste is available.
func (*BrowserProvider) clipboardCapability(ctx context.Context, client *bridge.Client) map[string]any {
	result, err := client.Exec(ctx, "command -v xclip >/dev/null 2>&1 && echo yes || echo no", "/", 5)
	if err != nil || result == nil {
		return map[string]any{"available": false, "reason": "could not probe the workspace"}
	}
	if strings.TrimSpace(result.Stdout) == "yes" {
		return map[string]any{"available": true, "backend": "xclip"}
	}
	return map[string]any{"available": false, "reason": "xclip is not installed in the workspace image"}
}
