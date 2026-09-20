package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	a11yCLIPath        = "/opt/memoh/toolkit/display/bin/a11y-cli"
	a11yExecTimeoutSec = 15
	// a11yProtocolVersion must match PROTOCOL_VERSION in
	// crates/a11y-cli/src/main.rs. Output from any other version is refused
	// instead of being decoded into a plausible-looking but wrong result.
	a11yProtocolVersion      = 4
	a11ySnapshotDefaultLimit = 300
	a11ySnapshotMaxLimit     = 2000
)

// errA11yHelperOutdated is returned when the workspace ships an a11y-cli that
// speaks a different protocol than this server. The fix is a workspace image
// rebuild, so the message says so instead of surfacing a decode error.
var errA11yHelperOutdated = errors.New("workspace a11y helper does not match this server; rebuild the workspace image")

type a11yPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// a11yMaxPointerCoord mirrors MAX_POINTER_COORD in the helper: RFB pointer
// positions are 16-bit, and AT-SPI reports extents near math.MinInt32 for
// nodes that were never laid out.
const a11yMaxPointerCoord = 32767

func a11yPointerCoordValid(p a11yPoint) bool {
	return p.X >= 0 && p.X <= a11yMaxPointerCoord && p.Y >= 0 && p.Y <= a11yMaxPointerCoord
}

type a11ySnapshotItem struct {
	Ref     string   `json:"ref"`
	Role    string   `json:"role"`
	Name    string   `json:"name"`
	X       int      `json:"x"`
	Y       int      `json:"y"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`
	Depth   int      `json:"depth"`
	Value   *string  `json:"value,omitempty"`
	States  []string `json:"states,omitempty"`
	Actions []string `json:"actions,omitempty"`
	AppPID  int      `json:"app_pid,omitempty"`
}

// fingerprint is what a diff compares: everything the model sees on the line
// except the ref itself.
func (it a11ySnapshotItem) fingerprint() string {
	value := ""
	if it.Value != nil {
		value = *it.Value
	}
	fields := []string{
		it.Role, it.Name, value, strings.Join(it.States, ","), strings.Join(it.Actions, ","),
		strconv.Itoa(it.X), strconv.Itoa(it.Y), strconv.Itoa(it.Width), strconv.Itoa(it.Height), strconv.Itoa(it.Depth),
	}
	return strings.Join(fields, "\x1f")
}

// center returns the on-screen centre of the element, or false when the
// helper reported no usable box (such nodes can still be driven through
// AT-SPI actions but never through pointer coordinates).
func (it a11ySnapshotItem) center() (a11yPoint, bool) {
	if it.Width <= 0 || it.Height <= 0 {
		return a11yPoint{}, false
	}
	point := a11yPoint{X: it.X + it.Width/2, Y: it.Y + it.Height/2}
	if !a11yPointerCoordValid(point) {
		return a11yPoint{}, false
	}
	return point, true
}

type a11yDiagnostics struct {
	Apps            int    `json:"apps"`
	Visited         int    `json:"visited"`
	Accepted        int    `json:"accepted"`
	SkippedState    int    `json:"skipped_state"`
	SkippedRole     int    `json:"skipped_role"`
	SkippedGeometry int    `json:"skipped_geometry"`
	Errors          int    `json:"errors"`
	BusAddress      string `json:"bus_address,omitempty"`
	Display         string `json:"display,omitempty"`
}

// public returns the counters that explain an empty or short list to the
// model. Bus and display addresses stay in server logs.
func (d a11yDiagnostics) public() map[string]any {
	return map[string]any{
		"apps":             d.Apps,
		"visited":          d.Visited,
		"accepted":         d.Accepted,
		"skipped_state":    d.SkippedState,
		"skipped_role":     d.SkippedRole,
		"skipped_geometry": d.SkippedGeometry,
		"errors":           d.Errors,
	}
}

// a11ySnapshotApp is the application a snapshot was scoped to.
type a11ySnapshotApp struct {
	AppID string `json:"app_id"`
	PID   int    `json:"pid"`
	Name  string `json:"name"`
}

type a11ySnapshotOutput struct {
	OK              bool               `json:"ok"`
	ProtocolVersion int                `json:"protocol_version"`
	HelperVersion   string             `json:"helper_version"`
	SnapshotID      string             `json:"snapshot_id"`
	App             *a11ySnapshotApp   `json:"app,omitempty"`
	Scope           string             `json:"scope,omitempty"`
	Limit           int                `json:"limit"`
	Truncated       bool               `json:"truncated"`
	ReusedRefs      int                `json:"reused_refs"`
	CarriedRefs     int                `json:"carried_refs"`
	Items           []a11ySnapshotItem `json:"items"`
	Lines           []string           `json:"lines"`
	RefsPath        string             `json:"refs_path"`
	Diagnostics     a11yDiagnostics    `json:"diagnostics"`
}

func (o *a11ySnapshotOutput) text() string {
	if o == nil || len(o.Lines) == 0 {
		return "(no on-screen elements)"
	}
	return strings.Join(o.Lines, "\n")
}

type a11ySelection struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Mode  string `json:"mode"`
}

type a11yActionOutput struct {
	OK              bool           `json:"ok"`
	ProtocolVersion int            `json:"protocol_version"`
	Ref             string         `json:"ref"`
	Action          string         `json:"action"`
	Detail          string         `json:"detail,omitempty"`
	Fallback        *a11yPoint     `json:"fallback,omitempty"`
	Error           string         `json:"error,omitempty"`
	Unsupported     bool           `json:"unsupported,omitempty"`
	Selection       *a11ySelection `json:"selection,omitempty"`
	// Carried is set when the ref was not part of the latest (subtree)
	// snapshot but carried over from the previous index; the helper then
	// offers no pointer fallback because its box was not re-read.
	Carried bool `json:"carried,omitempty"`
}

// fallbackPoint returns the helper's pointer fallback only when it is a real
// screen position.
func (o *a11yActionOutput) fallbackPoint() (a11yPoint, bool) {
	if o == nil || o.Fallback == nil || !a11yPointerCoordValid(*o.Fallback) {
		return a11yPoint{}, false
	}
	return *o.Fallback, true
}

// failure renders a failed helper action as an error for the model.
func (o *a11yActionOutput) failure(what, ref string) error {
	hint := ""
	if o.Carried {
		hint = "; the ref was carried over from an earlier observation (the latest snapshot was scoped to a subtree), so observe the whole target again before retrying"
	}
	if o.Error != "" {
		return fmt.Errorf("a11y %s %s failed: %s%s", what, ref, o.Error, hint)
	}
	return fmt.Errorf("a11y %s %s failed without diagnostic%s", what, ref, hint)
}

type a11yLocateOutput struct {
	OK              bool       `json:"ok"`
	ProtocolVersion int        `json:"protocol_version"`
	Ref             string     `json:"ref"`
	Role            string     `json:"role"`
	Name            string     `json:"name"`
	X               int        `json:"x"`
	Y               int        `json:"y"`
	Width           int        `json:"width"`
	Height          int        `json:"height"`
	Center          *a11yPoint `json:"center,omitempty"`
	States          []string   `json:"states,omitempty"`
	Actions         []string   `json:"actions,omitempty"`
	AppPID          int        `json:"app_pid,omitempty"`
	Carried         bool       `json:"carried,omitempty"`
}

// noCenterError explains why a located ref cannot be turned into pointer
// coordinates.
func (o *a11yLocateOutput) noCenterError(ref, what string) error {
	if o.Carried {
		return fmt.Errorf("ref %s was not part of the latest snapshot (it was scoped to a subtree), so its position was not re-read; observe the whole target again before a %s, or pass x/y", ref, what)
	}
	return fmt.Errorf("ref %s has no on-screen box, and a %s cannot be expressed as an accessibility action; observe again or pass x/y", ref, what)
}

type a11yAppWindow struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Active bool   `json:"active"`
}

type a11yAppInfo struct {
	AppID   string          `json:"app_id"`
	PID     int             `json:"pid"`
	BusName string          `json:"bus_name"`
	Name    string          `json:"name"`
	Toolkit string          `json:"toolkit"`
	Version string          `json:"version"`
	Windows []a11yAppWindow `json:"windows"`
}

// publicMap is what list_apps returns to the model: identity, status, and
// the windows the model can recognise the application by. The bus name is a
// transport detail and stays out.
func (a a11yAppInfo) publicMap() map[string]any {
	windows := make([]map[string]any, 0, len(a.Windows))
	for _, w := range a.Windows {
		win := map[string]any{"name": w.Name, "role": w.Role, "active": w.Active}
		if w.Width > 0 && w.Height > 0 {
			win["bounds"] = map[string]int{"x": w.X, "y": w.Y, "width": w.Width, "height": w.Height}
		}
		windows = append(windows, win)
	}
	out := map[string]any{
		"app_id":  a.AppID,
		"name":    a.Name,
		"status":  "running",
		"windows": windows,
	}
	if a.PID > 0 {
		out["pid"] = a.PID
	}
	if a.Toolkit != "" {
		out["toolkit"] = a.Toolkit
	}
	if a.Version != "" {
		out["toolkit_version"] = a.Version
	}
	return out
}

type a11yAppsOutput struct {
	OK              bool          `json:"ok"`
	ProtocolVersion int           `json:"protocol_version"`
	HelperVersion   string        `json:"helper_version"`
	Apps            []a11yAppInfo `json:"apps"`
	BusAddress      string        `json:"bus_address,omitempty"`
	Display         string        `json:"display,omitempty"`
}

func execA11y(ctx context.Context, client *bridge.Client, args ...string) ([]byte, error) {
	if client == nil {
		return nil, errors.New("workspace bridge client is not configured")
	}
	cmd := fmt.Sprintf("DISPLAY=:99 %s %s", shellQuote(a11yCLIPath), shellQuoteArgs(args))
	result, err := client.Exec(ctx, cmd, "/", a11yExecTimeoutSec)
	if err != nil {
		return nil, err
	}
	stdout := strings.TrimSpace(result.Stdout)
	if result.ExitCode != 0 {
		stderr := strings.TrimSpace(result.Stderr)
		if a11yUsageError(stderr) {
			return nil, errA11yHelperOutdated
		}
		if stderr == "" {
			stderr = stdout
		}
		if stderr == "" {
			stderr = fmt.Sprintf("a11y-cli exited with code %d", result.ExitCode)
		}
		return nil, errors.New(stderr)
	}
	if stdout == "" {
		return nil, errors.New("a11y-cli returned empty output")
	}
	return []byte(stdout), nil
}

// a11yUsageError recognises clap's rejection of a subcommand or flag this
// server sent, which means the helper predates it.
func a11yUsageError(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "unrecognized subcommand") || strings.Contains(lower, "unexpected argument")
}

// decodeA11y parses helper JSON after confirming the protocol version, so a
// stale helper produces a clear "rebuild" error rather than an empty tree.
func decodeA11y(raw []byte, what string, out any) error {
	var header struct {
		ProtocolVersion *int `json:"protocol_version"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return fmt.Errorf("parse a11y-cli %s output: %w", what, err)
	}
	switch {
	case header.ProtocolVersion == nil:
		return fmt.Errorf("%w (helper output has no protocol_version)", errA11yHelperOutdated)
	case *header.ProtocolVersion != a11yProtocolVersion:
		return fmt.Errorf("%w (helper protocol %d, server expects %d)", errA11yHelperOutdated, *header.ProtocolVersion, a11yProtocolVersion)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("parse a11y-cli %s output: %w", what, err)
	}
	return nil
}

func computerA11yApps(ctx context.Context, client *bridge.Client) (*a11yAppsOutput, error) {
	raw, err := execA11y(ctx, client, "apps")
	if err != nil {
		return nil, err
	}
	var out a11yAppsOutput
	if err := decodeA11y(raw, "apps", &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, errors.New("a11y-cli apps reported failure")
	}
	return &out, nil
}

// computerA11ySnapshot walks the desktop, only the application selected by
// app (an app_id such as app:1234) when it is non-empty, or only the subtree
// below scope (a ref from the current index). reuse keeps the ref ids of
// elements that were in the previous index.
func computerA11ySnapshot(ctx context.Context, client *bridge.Client, limit int, app, scope string, reuse bool) (*a11ySnapshotOutput, error) {
	if limit <= 0 {
		limit = a11ySnapshotDefaultLimit
	}
	args := []string{"snapshot", "--limit", strconv.Itoa(limit)}
	if app = strings.TrimSpace(app); app != "" {
		args = append(args, "--app", app)
	}
	if scope = strings.TrimSpace(scope); scope != "" {
		args = append(args, "--scope", scope)
	}
	if reuse {
		args = append(args, "--reuse")
	}
	raw, err := execA11y(ctx, client, args...)
	if err != nil {
		return nil, err
	}
	var out a11ySnapshotOutput
	if err := decodeA11y(raw, "snapshot", &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, errors.New("a11y-cli snapshot reported failure")
	}
	return &out, nil
}

func a11ySnapshotArgs(snapshotID string) []string {
	if snapshotID = strings.TrimSpace(snapshotID); snapshotID == "" {
		return nil
	}
	return []string{"--snapshot", snapshotID}
}

func computerA11yLocate(ctx context.Context, client *bridge.Client, ref, snapshotID string) (*a11yLocateOutput, error) {
	args := append([]string{"locate", "--ref", ref}, a11ySnapshotArgs(snapshotID)...)
	raw, err := execA11y(ctx, client, args...)
	if err != nil {
		return nil, err
	}
	var out a11yLocateOutput
	if err := decodeA11y(raw, "locate", &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("a11y-cli could not resolve ref %s", ref)
	}
	// Defence in depth against a helper that still reports unrealised
	// extents: never hand a coordinate the pointer cannot address to RFB.
	if out.Center != nil && !a11yPointerCoordValid(*out.Center) {
		out.Center = nil
	}
	return &out, nil
}

func computerA11yClick(ctx context.Context, client *bridge.Client, ref, snapshotID string) (*a11yActionOutput, error) {
	return runA11yAction(ctx, client, "click", ref, snapshotID)
}

func computerA11yEdit(ctx context.Context, client *bridge.Client, ref, text string, replace bool, snapshotID string) (*a11yActionOutput, error) {
	action := "type"
	if replace {
		action = "fill"
	}
	return runA11yAction(ctx, client, action, ref, snapshotID, "--text", text)
}

func computerA11ySetValue(ctx context.Context, client *bridge.Client, ref, value, snapshotID string) (*a11yActionOutput, error) {
	return runA11yAction(ctx, client, "set-value", ref, snapshotID, "--value", value)
}

func computerA11ySelectText(ctx context.Context, client *bridge.Client, ref, text, prefix, suffix, mode, snapshotID string) (*a11yActionOutput, error) {
	return runA11yAction(ctx, client, "select-text", ref, snapshotID, "--text", text, "--prefix", prefix, "--suffix", suffix, "--mode", mode)
}

func computerA11yNamedAction(ctx context.Context, client *bridge.Client, ref, name, snapshotID string) (*a11yActionOutput, error) {
	return runA11yAction(ctx, client, "action", ref, snapshotID, "--name", name)
}

// runA11yAction invokes a ref-based helper subcommand. extra flags are passed
// verbatim, including empty values, so fill can clear a field.
func runA11yAction(ctx context.Context, client *bridge.Client, subcommand, ref, snapshotID string, extra ...string) (*a11yActionOutput, error) {
	args := []string{subcommand, "--ref", ref}
	args = append(args, a11ySnapshotArgs(snapshotID)...)
	args = append(args, extra...)
	raw, err := execA11y(ctx, client, args...)
	if err != nil {
		return nil, err
	}
	var out a11yActionOutput
	if err := decodeA11y(raw, subcommand, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func shellQuote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func shellQuoteArgs(args []string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}
