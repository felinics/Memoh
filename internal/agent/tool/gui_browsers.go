package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

// browserEndpoint is one Chromium-family instance in the workspace that
// exposes a CDP endpoint. The default workspace browser always appears (as
// "stopped" when it is not running yet); any other instance is discovered
// from its --remote-debugging-port flag and must already be running.
type browserEndpoint struct {
	ID              string `json:"browser_id"`
	Port            int    `json:"port"`
	PID             int    `json:"pid,omitempty"`
	Binary          string `json:"binary,omitempty"`
	Name            string `json:"name"`
	Family          string `json:"family"`
	Backend         string `json:"backend"`
	Status          string `json:"status"`
	Default         bool   `json:"default"`
	UserDataDir     string `json:"user_data_dir,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
	UserAgent       string `json:"user_agent,omitempty"`
}

func (e browserEndpoint) baseURL() string {
	return "http://127.0.0.1:" + strconv.Itoa(e.Port)
}

func (e browserEndpoint) publicMap() map[string]any {
	out := map[string]any{
		"browser_id": e.ID,
		"name":       e.Name,
		"family":     e.Family,
		"backend":    e.Backend,
		"status":     e.Status,
		"default":    e.Default,
		"cdp_port":   e.Port,
	}
	if e.PID > 0 {
		out["pid"] = e.PID
	}
	if e.UserDataDir != "" {
		out["user_data_dir"] = e.UserDataDir
	}
	if e.ProtocolVersion != "" {
		out["protocol_version"] = e.ProtocolVersion
	}
	out["capabilities"] = map[string]any{
		"tabs":             true,
		"auto_start":       e.Default,
		"secondary_action": false,
		"paste_html":       "synthetic paste event; insertHTML fallback for contenteditable",
	}
	return out
}

func browserIDForPort(port int) string {
	return fmt.Sprintf("chrome-%d", port)
}

// portFromBrowserID parses ids of the form chrome-<port>. Anything else,
// including URLs, is rejected so a URL can never be used as an identity.
func portFromBrowserID(id string) (int, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	rest, ok := strings.CutPrefix(id, "chrome-")
	if !ok {
		return 0, false
	}
	port, err := strconv.Atoi(rest)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// browserDiscoveryScript prints one line per Chromium-family main process
// that exposes a remote debugging port: pid, port, user-data-dir, binary.
const browserDiscoveryScript = `# memoh-browser-discovery
for proc_dir in /proc/[0-9]*; do
  [ -d "$proc_dir" ] || continue
  pid="${proc_dir#/proc/}"
  cmdline="$(tr '\000' '\n' <"$proc_dir/cmdline" 2>/dev/null || true)"
  [ -n "$cmdline" ] || continue
  exe="$(printf '%s\n' "$cmdline" | head -n1)"
  printf '%s\n' "$exe" | grep -Eq '(^|/)(google-chrome-stable|google-chrome|chromium|chromium-browser|chrome)$' || continue
  printf '%s\n' "$cmdline" | grep -Eq '^--type=' && continue
  port="$(printf '%s\n' "$cmdline" | sed -n 's/^--remote-debugging-port=\([0-9]*\)$/\1/p' | head -n1)"
  [ -n "$port" ] || continue
  dir="$(printf '%s\n' "$cmdline" | sed -n 's/^--user-data-dir=\(.*\)$/\1/p' | head -n1)"
  printf '%s\t%s\t%s\t%s\n' "$pid" "$port" "$dir" "$exe"
done
exit 0`

// discoverBrowsers lists the workspace browsers. The default endpoint is
// always present; discovered instances are probed for /json/version so a
// stale flag on a dying process is not reported as a usable browser.
func (p *BrowserProvider) discoverBrowsers(ctx context.Context, client *bridge.Client) ([]browserEndpoint, error) {
	if client == nil {
		return nil, errors.New("workspace bridge client is not configured")
	}
	result, err := client.Exec(ctx, browserDiscoveryScript, "/", 10)
	if err != nil {
		return nil, fmt.Errorf("discover workspace browsers: %w", err)
	}
	byPort := map[int]browserEndpoint{}
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) < 4 {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
		port, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil || port <= 0 {
			continue
		}
		ep := browserEndpoint{
			ID:          browserIDForPort(port),
			Port:        port,
			PID:         pid,
			UserDataDir: strings.TrimSpace(fields[2]),
			Binary:      strings.TrimSpace(fields[3]),
			Family:      "chromium",
			Backend:     "cdp",
			Default:     port == browserCDPPort,
		}
		byPort[port] = ep
	}
	if _, ok := byPort[browserCDPPort]; !ok {
		byPort[browserCDPPort] = browserEndpoint{
			ID:      browserIDForPort(browserCDPPort),
			Port:    browserCDPPort,
			Family:  "chromium",
			Backend: "cdp",
			Default: true,
		}
	}
	endpoints := make([]browserEndpoint, 0, len(byPort))
	for _, ep := range byPort {
		p.probeBrowser(ctx, client, &ep)
		endpoints = append(endpoints, ep)
	}
	sort.Slice(endpoints, func(i, j int) bool {
		if endpoints[i].Default != endpoints[j].Default {
			return endpoints[i].Default
		}
		return endpoints[i].Port < endpoints[j].Port
	})
	return endpoints, nil
}

// probeBrowser fills in the version fields from /json/version and marks the
// endpoint running or stopped.
func (p *BrowserProvider) probeBrowser(ctx context.Context, client *bridge.Client, ep *browserEndpoint) {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	body, status, err := p.cdpHTTP(reqCtx, client, ep.Port, http.MethodGet, "/json/version", nil)
	if err != nil || status >= 400 || len(body) == 0 {
		ep.Status = "stopped"
		if ep.Name == "" {
			ep.Name = "Workspace Chrome"
		}
		return
	}
	var version struct {
		Browser         string `json:"Browser"`
		ProtocolVersion string `json:"Protocol-Version"`
		UserAgent       string `json:"User-Agent"`
	}
	_ = json.Unmarshal(body, &version)
	ep.Status = "running"
	ep.Name = strings.TrimSpace(version.Browser)
	if ep.Name == "" {
		ep.Name = "Chromium"
	}
	ep.ProtocolVersion = strings.TrimSpace(version.ProtocolVersion)
	ep.UserAgent = strings.TrimSpace(version.UserAgent)
}

// resolveBrowser picks the browser a call addresses: the explicit browser_id,
// else the session's selected browser, else the default workspace browser.
// Only the default browser is started on demand; any other instance must be
// running, since the tool has no launch parameters for it.
func (p *BrowserProvider) resolveBrowser(ctx context.Context, client *bridge.Client, state *guiSessionState, explicitID string) (browserEndpoint, error) {
	wanted := strings.TrimSpace(explicitID)
	if wanted == "" && state != nil {
		wanted, _, _ = state.defaults()
	}
	if wanted == "" || (wanted == browserIDForPort(browserCDPPort)) {
		ep := browserEndpoint{ID: browserIDForPort(browserCDPPort), Port: browserCDPPort, Family: "chromium", Backend: "cdp", Default: true}
		if err := p.ensureCDPEndpoint(ctx, client, ep); err != nil {
			return browserEndpoint{}, err
		}
		p.probeBrowser(ctx, client, &ep)
		return ep, nil
	}
	if _, ok := portFromBrowserID(wanted); !ok {
		return browserEndpoint{}, fmt.Errorf("browser_id %q is not a browser id; use list_browsers to discover ids such as chrome-9222", wanted)
	}
	endpoints, err := p.discoverBrowsers(ctx, client)
	if err != nil {
		return browserEndpoint{}, err
	}
	for _, ep := range endpoints {
		if ep.ID == wanted {
			if ep.Status != "running" {
				return browserEndpoint{}, fmt.Errorf("browser %s is not running", wanted)
			}
			return ep, nil
		}
	}
	return browserEndpoint{}, fmt.Errorf("browser %s was not found in the workspace; use list_browsers", wanted)
}

// resolvedTab is the frozen target of one browser call.
type resolvedTab struct {
	Browser browserEndpoint
	Target  cdpTarget
	// Source says how the tab was chosen: explicit, session, or active.
	Source string
}

func (t resolvedTab) identity() map[string]any {
	return map[string]any{
		"browser_id": t.Browser.ID,
		"tab_id":     t.Target.ID,
		"tab_source": t.Source,
	}
}

// resolveTab freezes the tab a page action operates on. Explicit tab_id wins
// (and must belong to browser_id when both are given); tab_index is the
// zero-based compatibility form within the resolved browser; otherwise the
// session's selected tab is used while it still exists, else the browser's
// first page, which then becomes the session's selection.
func (p *BrowserProvider) resolveTab(ctx context.Context, client *bridge.Client, state *guiSessionState, args map[string]any) (resolvedTab, error) {
	explicitTab := strings.TrimSpace(StringArg(args, "tab_id"))
	explicitBrowser := strings.TrimSpace(StringArg(args, "browser_id"))
	tabIndex, hasIndex, err := IntArg(args, "tab_index")
	if err != nil {
		return resolvedTab{}, err
	}
	if explicitTab != "" {
		return p.resolveExplicitTab(ctx, client, explicitBrowser, explicitTab)
	}
	browser, err := p.resolveBrowser(ctx, client, state, explicitBrowser)
	if err != nil {
		return resolvedTab{}, err
	}
	targets, err := p.listTargets(ctx, client, browser)
	if err != nil {
		return resolvedTab{}, err
	}
	if hasIndex {
		target, err := pageTargetAt(targets, tabIndex)
		if err != nil {
			return resolvedTab{}, err
		}
		return resolvedTab{Browser: browser, Target: target, Source: "explicit"}, nil
	}
	if state != nil {
		sessionBrowser, sessionTab, _ := state.defaults()
		if sessionTab != "" && (sessionBrowser == "" || sessionBrowser == browser.ID) {
			for _, target := range targets {
				if target.Type == "page" && target.ID == sessionTab {
					return resolvedTab{Browser: browser, Target: target, Source: "session"}, nil
				}
			}
			state.forgetTab(sessionTab)
		}
	}
	for _, target := range targets {
		if target.Type == "page" {
			if state != nil {
				state.selectTab(browser.ID, target.ID)
			}
			return resolvedTab{Browser: browser, Target: target, Source: "active"}, nil
		}
	}
	target, err := p.createTarget(ctx, client, browser, "about:blank")
	if err != nil {
		return resolvedTab{}, err
	}
	if state != nil {
		state.selectTab(browser.ID, target.ID)
	}
	return resolvedTab{Browser: browser, Target: target, Source: "created"}, nil
}

func (p *BrowserProvider) resolveExplicitTab(ctx context.Context, client *bridge.Client, browserID, tabID string) (resolvedTab, error) {
	endpoints, err := p.discoverBrowsers(ctx, client)
	if err != nil {
		return resolvedTab{}, err
	}
	var matches []resolvedTab
	for _, ep := range endpoints {
		if ep.Status != "running" {
			continue
		}
		if browserID != "" && ep.ID != browserID {
			continue
		}
		targets, err := p.listTargets(ctx, client, ep)
		if err != nil {
			continue
		}
		for _, target := range targets {
			if target.Type == "page" && target.ID == tabID {
				matches = append(matches, resolvedTab{Browser: ep, Target: target, Source: "explicit"})
			}
		}
	}
	switch len(matches) {
	case 0:
		if browserID != "" {
			return resolvedTab{}, fmt.Errorf("tab %s does not exist in browser %s (it may have been closed, or the browser restarted)", tabID, browserID)
		}
		return resolvedTab{}, fmt.Errorf("tab %s does not exist in any running workspace browser (it may have been closed, or the browser restarted)", tabID)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.Browser.ID)
		}
		return resolvedTab{}, fmt.Errorf("tab %s exists in several browsers (%s); pass browser_id", tabID, strings.Join(ids, ", "))
	}
}
