package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

// cdpHTTP performs an HTTP request against the CDP endpoint of one browser
// instance inside the workspace, tunnelled through the bridge.
func (*BrowserProvider) cdpHTTP(ctx context.Context, client *bridge.Client, port int, method, path string, body io.Reader) ([]byte, int, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return client.DialContext(ctx, network, address)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(port)+path, body)
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

func (p *BrowserProvider) cdpReachable(ctx context.Context, client *bridge.Client, port int) bool {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	body, status, err := p.cdpHTTP(reqCtx, client, port, http.MethodGet, "/json/version", nil)
	if err != nil || status >= 400 || len(body) == 0 {
		return false
	}
	return true
}

// ensureCDPEndpoint makes sure the browser behind ep answers CDP. Only the
// default workspace browser is started on demand.
func (p *BrowserProvider) ensureCDPEndpoint(ctx context.Context, client *bridge.Client, ep browserEndpoint) error {
	if client == nil {
		return errors.New("workspace runtime provider is not configured")
	}
	if p.cdpReachable(ctx, client, ep.Port) {
		return nil
	}
	if !ep.Default {
		return fmt.Errorf("browser %s is not reachable on its CDP port", ep.ID)
	}
	if err := p.startDesktopBrowser(ctx, client); err != nil {
		return err
	}
	deadline := time.Now().Add(browserStartupTimeout)
	for time.Now().Before(deadline) {
		if p.cdpReachable(ctx, client, ep.Port) {
			return nil
		}
		if err := sleepContext(ctx, 300*time.Millisecond); err != nil {
			return err
		}
	}
	return errors.New("workspace desktop browser CDP endpoint is not reachable")
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
  printf '%s\n' "$cmdline" | grep -Fq -- '--user-data-dir=/tmp/memoh-display-browser' || continue
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

type cdpTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	Title                string `json:"title"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func (t cdpTarget) publicMap() map[string]any {
	return map[string]any{
		"tab_id": t.ID,
		"id":     t.ID,
		"type":   t.Type,
		"url":    t.URL,
		"title":  t.Title,
	}
}

func publicTargets(targets []cdpTarget) []map[string]any {
	out := make([]map[string]any, 0, len(targets))
	index := 0
	for _, target := range targets {
		if target.Type == "page" {
			entry := target.publicMap()
			entry["tab_index"] = index
			index++
			out = append(out, entry)
		}
	}
	return out
}

func (p *BrowserProvider) listTargets(ctx context.Context, client *bridge.Client, ep browserEndpoint) ([]cdpTarget, error) {
	body, status, err := p.cdpHTTP(ctx, client, ep.Port, http.MethodGet, "/json/list", nil)
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

func (p *BrowserProvider) createTarget(ctx context.Context, client *bridge.Client, ep browserEndpoint, targetURL string) (cdpTarget, error) {
	if strings.TrimSpace(targetURL) == "" {
		targetURL = "about:blank"
	}
	body, status, err := p.cdpHTTP(ctx, client, ep.Port, http.MethodPut, "/json/new?"+escapeNewTargetURL(targetURL), nil)
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

// escapeNewTargetURL encodes a URL for Chrome's /json/new?<url>. Chrome
// percent-decodes the query it receives but leaves "+" alone, so the form
// encoding QueryEscape produces would turn every space of a data: URL (or a
// query string) into a literal plus sign.
func escapeNewTargetURL(targetURL string) string {
	return strings.ReplaceAll(url.QueryEscape(targetURL), "+", "%20")
}

// browserWebSocketURL returns the browser-level CDP websocket of ep from
// /json/version, which is what Target.* commands need.
func (p *BrowserProvider) browserWebSocketURL(ctx context.Context, client *bridge.Client, ep browserEndpoint) (string, error) {
	body, status, err := p.cdpHTTP(ctx, client, ep.Port, http.MethodGet, "/json/version", nil)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", fmt.Errorf("CDP /json/version failed (HTTP %d): %s", status, string(body))
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &version); err != nil {
		return "", err
	}
	if strings.TrimSpace(version.WebSocketDebuggerURL) == "" {
		return "", errors.New("the browser does not expose a browser-level CDP websocket")
	}
	return version.WebSocketDebuggerURL, nil
}

// createTargetInBackground opens a tab without giving it focus
// (Target.createTarget background=true over the browser-level connection).
// Browsers without that connection get an explicit error instead of a
// foreground tab.
func (p *BrowserProvider) createTargetInBackground(ctx context.Context, client *bridge.Client, ep browserEndpoint, targetURL string) (cdpTarget, error) {
	if strings.TrimSpace(targetURL) == "" {
		targetURL = "about:blank"
	}
	wsURL, err := p.browserWebSocketURL(ctx, client, ep)
	if err != nil {
		return cdpTarget{}, fmt.Errorf("visible=false is not supported by %s: %w", ep.ID, err)
	}
	conn, err := p.dialCDP(ctx, client, cdpTarget{WebSocketDebuggerURL: wsURL})
	if err != nil {
		return cdpTarget{}, fmt.Errorf("visible=false is not supported by %s: %w", ep.ID, err)
	}
	defer func() { _ = conn.Close() }()
	result, err := conn.Call(ctx, "Target.createTarget", map[string]any{"url": targetURL, "background": true})
	if err != nil {
		return cdpTarget{}, fmt.Errorf("open a background tab in %s: %w", ep.ID, err)
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(result, &created); err != nil || strings.TrimSpace(created.TargetID) == "" {
		return cdpTarget{}, errors.New("the browser did not return the id of the background tab")
	}
	targets, err := p.listTargets(ctx, client, ep)
	if err != nil {
		return cdpTarget{}, err
	}
	for _, target := range targets {
		if target.ID == created.TargetID {
			return target, nil
		}
	}
	return cdpTarget{ID: created.TargetID, Type: "page", URL: targetURL}, nil
}

func (p *BrowserProvider) activateTarget(ctx context.Context, client *bridge.Client, ep browserEndpoint, id string) error {
	body, status, err := p.cdpHTTP(ctx, client, ep.Port, http.MethodGet, "/json/activate/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("activate CDP target failed (HTTP %d): %s", status, string(body))
	}
	return nil
}

func (p *BrowserProvider) closeTarget(ctx context.Context, client *bridge.Client, ep browserEndpoint, id string) (map[string]any, error) {
	body, status, err := p.cdpHTTP(ctx, client, ep.Port, http.MethodGet, "/json/close/"+url.PathEscape(id), nil)
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

// cdpPage is one dialled tab. snapshotID is the snapshot refs on this page
// are expected to come from (empty when the session never observed it).
type cdpPage struct {
	conn       *cdpConn
	snapshotID string
}

// connectTab freezes the target of a page action, brings it to front, and
// dials its websocket.
func (p *BrowserProvider) connectTab(ctx context.Context, client *bridge.Client, state *guiSessionState, args map[string]any) (resolvedTab, *cdpPage, error) {
	tab, err := p.resolveTab(ctx, client, state, args)
	if err != nil {
		return resolvedTab{}, nil, err
	}
	if err := p.activateTarget(ctx, client, tab.Browser, tab.Target.ID); err != nil {
		return resolvedTab{}, nil, err
	}
	conn, err := p.dialCDP(ctx, client, tab.Target)
	if err != nil {
		return resolvedTab{}, nil, err
	}
	page := &cdpPage{conn: conn}
	if _, err := page.conn.Call(ctx, "Page.enable", nil); err != nil {
		_ = conn.Close()
		return resolvedTab{}, nil, err
	}
	if _, err := page.conn.Call(ctx, "Runtime.enable", nil); err != nil {
		_ = conn.Close()
		return resolvedTab{}, nil, err
	}
	_, _ = page.conn.Call(ctx, "DOM.enable", nil)
	// Refs are validated against the snapshot the model names, else against
	// the snapshot this session last took on this tab.
	page.snapshotID = strings.TrimSpace(StringArg(args, "snapshot_id"))
	if page.snapshotID == "" && state != nil {
		if rec, ok := state.browserSnapshot(tab.Target.ID); ok {
			page.snapshotID = rec.ID
		}
	}
	return tab, page, nil
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
		return nil, errors.New(pageExceptionMessage(out.ExceptionDetails.Exception.Description, out.ExceptionDetails.Text))
	}
	return remoteObjectValue(out.Result), nil
}

// pageExceptionMessage turns a Runtime.evaluate exception into one line for
// the model: the description's first line (Chrome appends the JS stack
// trace of the injected helper, which is noise) or the exception text.
func pageExceptionMessage(description, text string) string {
	msg := strings.TrimSpace(description)
	if idx := strings.Index(msg, "\n    at "); idx >= 0 {
		msg = strings.TrimSpace(msg[:idx])
	}
	if msg == "" {
		msg = strings.TrimSpace(text)
	}
	if msg == "" {
		msg = "page script threw an exception"
	}
	return strings.TrimPrefix(msg, "Error: ")
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

// evaluateObject evaluates an expression and decodes its JSON value into out.
func (p *cdpPage) evaluateObject(ctx context.Context, expression string, out any) error {
	value, err := p.evaluate(ctx, expression)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
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

// expr renders the page-side lookup for the target, bound to the page's
// expected snapshot so a ref from another observation is refused.
func (p *cdpPage) expr(target browserTarget) string {
	return fmt.Sprintf("mustTarget(%s, %s, %s)", jsQuote(target.Selector), jsQuote(target.Ref), jsQuote(p.snapshotID))
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

type elementPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

func (p *cdpPage) elementPoint(ctx context.Context, target browserTarget) (elementPoint, error) {
	var point elementPoint
	err := p.evaluateObject(ctx, fmt.Sprintf(`(() => {
const el = %s;
el.scrollIntoView({ block: "center", inline: "center" });
const rect = el.getBoundingClientRect();
if (rect.width === 0 || rect.height === 0) throw new Error("element has no visible box: " + %s);
return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
})()`, p.expr(target), jsQuote(target.label())), &point)
	return point, err
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
	var point elementPoint
	if err := p.evaluateObject(ctx, `({ x: window.innerWidth / 2, y: window.innerHeight / 2 })`, &point); err != nil {
		return elementPoint{}, err
	}
	if point.X <= 0 || point.Y <= 0 {
		point = elementPoint{X: defaultComputerWidth / 2, Y: defaultComputerHeight / 2}
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

// pressKey presses a chord. heldModifiers are the modifiers the session
// holds via keydown; they apply on top of the chord's own modifiers.
func (p *cdpPage) pressKey(ctx context.Context, key string, heldModifiers int) (err error) {
	parts := splitKeyChord(key)
	if len(parts) == 0 {
		return errors.New("key is required")
	}
	modifiers := heldModifiers
	pressed := make([]string, 0, len(parts))
	// Release everything this call pressed, even on failure, so the page is
	// never left with a modifier stuck down by a partial chord.
	defer func() {
		for i := len(pressed) - 1; i >= 0; i-- {
			modifiers &^= cdpModifier(pressed[i])
			if releaseErr := p.dispatchKeyWithModifiers(ctx, pressed[i], false, modifiers); releaseErr != nil && err == nil {
				err = releaseErr
			}
		}
	}()
	for _, part := range parts[:len(parts)-1] {
		modifiers |= cdpModifier(part)
		if err := p.dispatchKeyWithModifiers(ctx, part, true, modifiers); err != nil {
			return err
		}
		pressed = append(pressed, part)
	}
	mainKey := parts[len(parts)-1]
	if err := p.dispatchKeyWithModifiers(ctx, mainKey, true, modifiers); err != nil {
		return err
	}
	return p.dispatchKeyWithModifiers(ctx, mainKey, false, modifiers)
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

// annotatePinned draws each pinned ref's label over its element so a
// screenshot shows which ref addresses what. removeAnnotations must follow.
func (p *cdpPage) annotatePinned(ctx context.Context) (any, error) {
	return p.evaluate(ctx, `(() => {
const store = window.__memohSnapshot;
if (!store) return [];
const result = [];
for (const ref of Object.keys(store.byRef)) {
  const el = store.byRef[ref];
  if (!el || !el.isConnected) continue;
  const rect = el.getBoundingClientRect();
  if (rect.width === 0 || rect.height === 0) continue;
  result.push({ ref, tag: el.tagName.toLowerCase(), role: memohRole(el), name: memohElementName(el) });
  const label = document.createElement('div');
  label.className = '__memoh_annotation__';
  label.textContent = ref;
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

func (p *cdpPage) setInputFiles(ctx context.Context, target browserTarget, files []string) error {
	if target.Ref != "" {
		value, err := p.evaluate(ctx, fmt.Sprintf(`(() => memohCssPath(%s))()`, p.expr(target)))
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
  return Boolean(%s);
} catch (_) {
  return false;
}
})()`, p.expr(target)))
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
