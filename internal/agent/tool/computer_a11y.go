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
	a11yProtocolVersion      = 2
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

type a11ySnapshotItem struct {
	Ref    string   `json:"ref"`
	Role   string   `json:"role"`
	Name   string   `json:"name"`
	X      int      `json:"x"`
	Y      int      `json:"y"`
	Width  int      `json:"width"`
	Height int      `json:"height"`
	States []string `json:"states,omitempty"`
}

// a11yMaxPointerCoord mirrors MAX_POINTER_COORD in the helper: RFB pointer
// positions are 16-bit, and AT-SPI reports extents near math.MinInt32 for
// nodes that were never laid out.
const a11yMaxPointerCoord = 32767

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

func a11yPointerCoordValid(p a11yPoint) bool {
	return p.X >= 0 && p.X <= a11yMaxPointerCoord && p.Y >= 0 && p.Y <= a11yMaxPointerCoord
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

type a11ySnapshotOutput struct {
	OK              bool               `json:"ok"`
	ProtocolVersion int                `json:"protocol_version"`
	HelperVersion   string             `json:"helper_version"`
	Limit           int                `json:"limit"`
	Truncated       bool               `json:"truncated"`
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

type a11yActionOutput struct {
	OK              bool       `json:"ok"`
	ProtocolVersion int        `json:"protocol_version"`
	Ref             string     `json:"ref"`
	Action          string     `json:"action"`
	Detail          string     `json:"detail,omitempty"`
	Fallback        *a11yPoint `json:"fallback,omitempty"`
	Error           string     `json:"error,omitempty"`
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

func computerA11ySnapshot(ctx context.Context, client *bridge.Client, limit int) (*a11ySnapshotOutput, error) {
	if limit <= 0 {
		limit = a11ySnapshotDefaultLimit
	}
	raw, err := execA11y(ctx, client, "snapshot", "--limit", strconv.Itoa(limit))
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

func computerA11yLocate(ctx context.Context, client *bridge.Client, ref string) (*a11yLocateOutput, error) {
	raw, err := execA11y(ctx, client, "locate", "--ref", ref)
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

func computerA11yClick(ctx context.Context, client *bridge.Client, ref string) (*a11yActionOutput, error) {
	return runA11yAction(ctx, client, "click", ref, nil)
}

func computerA11yEdit(ctx context.Context, client *bridge.Client, ref, text string, replace bool) (*a11yActionOutput, error) {
	action := "type"
	if replace {
		action = "fill"
	}
	return runA11yAction(ctx, client, action, ref, &text)
}

// runA11yAction invokes a ref-based helper action. text is passed through
// verbatim when non-nil, including the empty string, so fill can clear.
func runA11yAction(ctx context.Context, client *bridge.Client, action, ref string, text *string) (*a11yActionOutput, error) {
	args := []string{action, "--ref", ref}
	if text != nil {
		args = append(args, "--text", *text)
	}
	raw, err := execA11y(ctx, client, args...)
	if err != nil {
		return nil, err
	}
	var out a11yActionOutput
	if err := decodeA11y(raw, action, &out); err != nil {
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
