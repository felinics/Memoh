package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

func (p *BrowserProvider) execComputerObserve(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	spec, err := computerObserveContract.normalize(args)
	if err != nil {
		return nil, err
	}
	botID, err := p.requireComputerDisplay(session)
	if err != nil {
		return nil, err
	}
	client, err := p.ensureComputerDisplay(ctx, botID)
	if err != nil {
		return nil, err
	}
	state := p.sessions.get(session)
	switch spec.Name {
	case "screenshot":
		return p.computerScreenshot(ctx, session, botID, args, nil)
	case "snapshot":
		opts, err := guiSnapshotOptionsFrom(args)
		if err != nil {
			return nil, err
		}
		return p.computerSnapshot(ctx, botID, client, state, args, opts)
	case "state_and_screenshot":
		return p.computerStateAndScreenshot(ctx, session, botID, client, state, args)
	case "probe":
		return p.computerProbe(ctx, botID, client, args), nil
	default:
		return nil, fmt.Errorf("unknown computer observe: %s", spec.Name)
	}
}

// computerScreenshot captures the desktop. The result states the coordinate
// space the model should use for computer_action: desktop pixels with the
// origin at the top-left corner, one image pixel per desktop pixel.
func (p *BrowserProvider) computerScreenshot(ctx context.Context, session SessionContext, botID string, args map[string]any, extra map[string]any) (any, error) {
	img, mime, err := p.display.Screenshot(ctx, botID)
	if err != nil {
		return nil, err
	}
	data := map[string]any{
		"coordinate_space": "desktop pixels; x/y for computer_action are image pixels",
		"origin":           "top-left corner of the desktop",
		"scale":            1,
	}
	if width, height, ok := imageDimensions(img); ok {
		data["desktop_width"] = width
		data["desktop_height"] = height
	}
	for k, v := range extra {
		data[k] = v
	}
	return p.screenshotResult(ctx, session, botID, img, mime, data, args), nil
}

// computerSnapshot observes the desktop or one application through the
// helper, keeps refs stable across observations, and presents the result as
// a full or incremental page.
func (p *BrowserProvider) computerSnapshot(ctx context.Context, botID string, client *bridge.Client, state *guiSessionState, args map[string]any, opts guiSnapshotOptions) (map[string]any, error) {
	appID := strings.TrimSpace(StringArg(args, "app_id"))
	if appID == "" {
		_, _, appID = state.defaults()
	}
	key := "computer:" + appID
	if opts.Cursor != "" {
		page, baseline, err := continueSnapshot(state, key, StringArg(args, "snapshot_id"), opts.Cursor, opts.Limit)
		if err != nil {
			return nil, err
		}
		out := page.result()
		out["snapshot_id"] = baseline.SnapshotID
		if appID != "" {
			out["app_id"] = appID
		}
		return out, nil
	}
	if opts.ScopeRef != "" {
		if _, ok := state.lastComputerSnapshot(); !ok {
			return nil, fmt.Errorf("scope_ref %s needs a desktop snapshot in this conversation first", opts.ScopeRef)
		}
	}
	snapshot, err := computerA11ySnapshot(ctx, client, guiSnapshotWalkLimit, appID, opts.ScopeRef, true)
	if err != nil {
		return nil, err
	}
	nodes := make([]guiNode, 0, len(snapshot.Items))
	for i, item := range snapshot.Items {
		line := ""
		if i < len(snapshot.Lines) {
			line = snapshot.Lines[i]
		}
		nodes = append(nodes, guiNode{Key: item.Ref, Ref: item.Ref, Line: line, Fingerprint: item.fingerprint()})
	}
	state.recordComputerSnapshot(guiSnapshotRecord{ID: snapshot.SnapshotID, AppID: appID, Taken: time.Now()})
	page := presentSnapshot(state, key, opts.ScopeRef, snapshot.SnapshotID, nodes, nil, 0, snapshot.Truncated, opts)
	p.logger.Debug("computer snapshot",
		slog.String("bot_id", botID),
		slog.String("helper_version", snapshot.HelperVersion),
		slog.String("snapshot_id", snapshot.SnapshotID),
		slog.String("app_id", appID),
		slog.String("scope", opts.ScopeRef),
		slog.String("bus_address", snapshot.Diagnostics.BusAddress),
		slog.String("display", snapshot.Diagnostics.Display),
		slog.Int("accepted", snapshot.Diagnostics.Accepted),
		slog.Int("reused_refs", snapshot.ReusedRefs),
		slog.Int("carried_refs", snapshot.CarriedRefs),
		slog.String("mode", page.Mode),
	)
	out := page.result()
	out["snapshot_id"] = snapshot.SnapshotID
	out["ref_count"] = len(snapshot.Items)
	out["reused_refs"] = snapshot.ReusedRefs
	if snapshot.CarriedRefs > 0 {
		out["carried_refs"] = snapshot.CarriedRefs
		out["carried_note"] = "refs outside the observed subtree keep their ids and still resolve for accessibility actions, but their position is not re-read; observe the whole target again before pointer actions on them"
	}
	out["limit"] = opts.Limit
	out["helper_version"] = snapshot.HelperVersion
	out["diagnostics"] = snapshot.Diagnostics.public()
	out["taken_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	if snapshot.App != nil {
		out["app_id"] = snapshot.App.AppID
		out["app_name"] = snapshot.App.Name
	} else {
		out["scope"] = "desktop"
	}
	if snapshot.Scope != "" {
		out["scope_ref"] = snapshot.Scope
	}
	return out, nil
}

// computerStateAndScreenshot takes a snapshot and a screenshot back to back
// and reports whether the set of applications on the bus stayed the same in
// between, which is the closest the desktop offers to "same generation".
func (p *BrowserProvider) computerStateAndScreenshot(ctx context.Context, session SessionContext, botID string, client *bridge.Client, state *guiSessionState, args map[string]any) (any, error) {
	opts, err := guiSnapshotOptionsFrom(args)
	if err != nil {
		return nil, err
	}
	if opts.Cursor != "" {
		return nil, errors.New("state_and_screenshot always takes a new snapshot; use snapshot with cursor to continue reading one")
	}
	before := p.appGeneration(ctx, client)
	snapshot, err := p.computerSnapshot(ctx, botID, client, state, args, opts)
	if err != nil {
		return nil, err
	}
	snapshotAt := time.Now().UTC().Format(time.RFC3339Nano)
	extra := map[string]any{"snapshot_taken_at": snapshotAt}
	for k, v := range snapshot {
		if k == "taken_at" {
			continue
		}
		extra[k] = v
	}
	after := p.appGeneration(ctx, client)
	extra["consistent"] = before != "" && before == after
	if before != after {
		extra["consistency_note"] = "the set of applications on the desktop changed between the snapshot and the screenshot; observe again before relying on refs"
	}
	return p.computerScreenshot(ctx, session, botID, args, extra)
}

// appGeneration fingerprints the applications currently on the bus.
func (*BrowserProvider) appGeneration(ctx context.Context, client *bridge.Client) string {
	apps, err := computerA11yApps(ctx, client)
	if err != nil {
		return ""
	}
	ids := make([]string, 0, len(apps.Apps))
	for _, app := range apps.Apps {
		ids = append(ids, app.AppID)
		for _, w := range app.Windows {
			if w.Active {
				ids = append(ids, "active:"+w.Name)
			}
		}
	}
	return strings.Join(ids, ",")
}

type a11yProbeOutput struct {
	OK    bool   `json:"ok"`
	Apps  *int   `json:"apps"`
	Error string `json:"error"`
}

// computerProbe reports the health of every desktop channel without private
// diagnostics: whether the accessibility bus answers and how many
// applications it has, whether the display and its input port are up,
// whether a screenshot can be taken, and whether the clipboard backend
// exists.
func (p *BrowserProvider) computerProbe(ctx context.Context, botID string, client *bridge.Client, args map[string]any) map[string]any {
	out := map[string]any{"protocol_version": a11yProtocolVersion}
	a11y := map[string]any{"available": false}
	if raw, err := execA11y(ctx, client, "probe"); err != nil {
		a11y["error"] = "accessibility helper failed to run"
		p.logger.Debug("a11y probe failed", slog.String("bot_id", botID), slog.Any("error", err))
	} else {
		var probe a11yProbeOutput
		if json.Unmarshal(raw, &probe) == nil && probe.OK {
			a11y["available"] = true
			if probe.Apps != nil {
				a11y["applications"] = *probe.Apps
			}
		} else {
			a11y["error"] = "accessibility bus is not reachable"
			p.logger.Debug("a11y probe not ok", slog.String("bot_id", botID), slog.String("error", probe.Error))
		}
	}
	if apps, err := computerA11yApps(ctx, client); err == nil {
		out["helper_version"] = apps.HelperVersion
		if appID := strings.TrimSpace(StringArg(args, "app_id")); appID != "" {
			found := false
			for _, app := range apps.Apps {
				if app.AppID == appID {
					a11y["application"] = app.publicMap()
					found = true
				}
			}
			if !found {
				a11y["application_error"] = appID + " is not on the accessibility bus"
			}
		}
	}
	out["accessibility"] = a11y
	ready := computerDisplayReady(ctx, client)
	out["display"] = map[string]any{"available": ready, "detail": "X display :99 and RFB input port"}
	out["input"] = map[string]any{"available": ready, "backend": "rfb pointer and key events"}
	shot := map[string]any{"available": false}
	if img, mime, err := p.display.Screenshot(ctx, botID); err == nil {
		shot["available"] = true
		shot["mime"] = mime
		if w, h, ok := imageDimensions(img); ok {
			shot["desktop_width"], shot["desktop_height"] = w, h
		}
	} else {
		shot["error"] = "screenshot capture failed"
		p.logger.Debug("probe screenshot failed", slog.String("bot_id", botID), slog.Any("error", err))
	}
	out["screenshot"] = shot
	out["clipboard"] = p.clipboardCapability(ctx, client)
	return out
}
