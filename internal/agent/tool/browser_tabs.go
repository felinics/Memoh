package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/chat/tabmark"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// isCDPTabAction says whether an action manages tabs (and marks on them)
// rather than acting inside one page.
func isCDPTabAction(action string) bool {
	switch action {
	case "tab_new", "tab_get", "tab_select", "tab_close", "tab_list", "tab_mark_deliverable", "tab_mark_handoff":
		return true
	default:
		return false
	}
}

// runCDPTabAction dispatches the tab-management actions. They resolve their
// own targets; only tab_new and tab_select change the session's tab.
func (p *BrowserProvider) runCDPTabAction(ctx context.Context, session SessionContext, client *bridge.Client, state *guiSessionState, action string, args map[string]any) (map[string]any, error) {
	switch action {
	case "tab_new":
		return p.tabNew(ctx, client, state, args)
	case "tab_get":
		return p.tabGet(ctx, session, client, state, args)
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
		out := map[string]any{"browser_id": tab.Browser.ID, "tab_id": tab.Target.ID, "tab_index": targetIndex(targets, tab.Target.ID), "target": tab.Target.publicMap(), "url": tab.Target.URL, "title": tab.Target.Title, "activated": true}
		if name := state.tabName(tab.Target.ID); name != "" {
			out["session_name"] = name
		}
		return out, nil
	case "tab_close":
		return p.tabClose(ctx, session, client, state, args)
	case "tab_list":
		return p.tabList(ctx, session, client, state, args)
	case "tab_mark_deliverable":
		return p.tabMark(ctx, session, client, state, args, tabmark.KindDeliverable)
	case "tab_mark_handoff":
		return p.tabMark(ctx, session, client, state, args, tabmark.KindHandoff)
	default:
		return nil, fmt.Errorf("unknown browser tab action: %s", action)
	}
}

// tabNew opens a tab, in the foreground by default or in the background with
// visible=false, records its session_name, and makes it the session's tab.
func (p *BrowserProvider) tabNew(ctx context.Context, client *bridge.Client, state *guiSessionState, args map[string]any) (map[string]any, error) {
	browser, err := p.resolveBrowser(ctx, client, state, StringArg(args, "browser_id"))
	if err != nil {
		return nil, err
	}
	visible := true
	if v, ok, err := BoolArg(args, "visible"); err != nil {
		return nil, err
	} else if ok {
		visible = v
	}
	name := strings.TrimSpace(StringArg(args, "session_name"))
	targetURL := StringArg(args, "url")
	var newTarget cdpTarget
	if visible {
		newTarget, err = p.createTarget(ctx, client, browser, targetURL)
	} else {
		newTarget, err = p.createTargetInBackground(ctx, client, browser, targetURL)
	}
	if err != nil {
		return nil, err
	}
	state.selectTab(browser.ID, newTarget.ID)
	state.setTabName(newTarget.ID, name)
	targets, _ := p.listTargets(ctx, client, browser)
	for _, target := range targets {
		// /json/new only echoes the id and url; the list carries the rest.
		if target.ID == newTarget.ID {
			newTarget = target
		}
	}
	out := map[string]any{
		"browser_id": browser.ID,
		"tab_id":     newTarget.ID,
		"tab_index":  targetIndex(targets, newTarget.ID),
		"target":     newTarget.publicMap(),
		"url":        newTarget.URL,
		"selected":   true,
		"visible":    visible,
		"state":      p.tabState(ctx, client, newTarget),
	}
	if name != "" {
		out["session_name"] = name
	}
	return out, nil
}

// tabGet describes a tab named explicitly by tab_id / tab_index without
// touching the session's tab.
func (p *BrowserProvider) tabGet(ctx context.Context, session SessionContext, client *bridge.Client, state *guiSessionState, args map[string]any) (map[string]any, error) {
	tab, err := p.resolveTab(ctx, client, state, args)
	if err != nil {
		return nil, err
	}
	out := tab.identity()
	for k, v := range tab.Target.publicMap() {
		out[k] = v
	}
	_, selectedTab, _ := state.defaults()
	out["selected"] = selectedTab == tab.Target.ID
	if name := state.tabName(tab.Target.ID); name != "" {
		out["session_name"] = name
	}
	if rec, ok := state.browserSnapshot(tab.Target.ID); ok {
		out["snapshot_id"] = rec.ID
		out["snapshot_taken_at"] = rec.Taken.UTC().Format(time.RFC3339Nano)
	}
	if mark, ok := p.markFor(ctx, session, tab.Browser.ID, tab.Target.ID); ok {
		out["mark"] = markPublic(mark)
	}
	targets, _ := p.listTargets(ctx, client, tab.Browser)
	out["tab_index"] = targetIndex(targets, tab.Target.ID)
	out["state"] = p.tabState(ctx, client, tab.Target)
	return out, nil
}

// tabClose closes a tab, forgets the session's bookkeeping for it, revokes
// remote sessions bound to it, and records its mark (if any) as closed.
func (p *BrowserProvider) tabClose(ctx context.Context, session SessionContext, client *bridge.Client, state *guiSessionState, args map[string]any) (map[string]any, error) {
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
	out := map[string]any{"browser_id": tab.Browser.ID, "tab_id": tab.Target.ID, "closed": targetIndex(targets, tab.Target.ID), "result": result}
	if n := p.cdpSessions.RevokeTab(session.BotID, tab.Target.ID); n > 0 {
		out["revoked_sessions"] = n
	}
	if p.marks != nil && strings.TrimSpace(session.SessionID) != "" {
		if _, changed, err := p.marks.MarkClosed(ctx, session.SessionID, tabmark.KeyFor(tab.Browser.ID, tab.Target.ID)); err == nil && changed {
			out["mark_closed"] = true
		}
	}
	return out, nil
}

// tabList lists the browser's page tabs with the session's names and marks.
func (p *BrowserProvider) tabList(ctx context.Context, session SessionContext, client *bridge.Client, state *guiSessionState, args map[string]any) (map[string]any, error) {
	browser, err := p.resolveBrowser(ctx, client, state, StringArg(args, "browser_id"))
	if err != nil {
		return nil, err
	}
	targets, err := p.listTargets(ctx, client, browser)
	if err != nil {
		return nil, err
	}
	_, selectedTab, _ := state.defaults()
	marks := p.marksByKey(ctx, session)
	tabs := publicTargets(targets)
	for _, tab := range tabs {
		id, _ := tab["tab_id"].(string)
		tab["selected"] = id == selectedTab
		if name := state.tabName(id); name != "" {
			tab["session_name"] = name
		}
		if mark, ok := marks[tabmark.KeyFor(browser.ID, id)]; ok {
			tab["mark"] = string(mark.Kind)
		}
	}
	return map[string]any{"browser_id": browser.ID, "tabs": tabs, "selected_tab_id": selectedTab}, nil
}

// tabMark persists (or with clear=true removes) a deliverable / handoff mark
// on an explicitly named tab.
func (p *BrowserProvider) tabMark(ctx context.Context, session SessionContext, client *bridge.Client, state *guiSessionState, args map[string]any, kind tabmark.Kind) (map[string]any, error) {
	if p.marks == nil {
		return nil, errors.New("tab marks are not available: the conversation store is not wired in this deployment")
	}
	if strings.TrimSpace(session.SessionID) == "" {
		return nil, errors.New("tab marks need a conversation session to attach to")
	}
	tab, err := p.resolveTab(ctx, client, state, args)
	if err != nil {
		return nil, err
	}
	key := tabmark.KeyFor(tab.Browser.ID, tab.Target.ID)
	out := map[string]any{"browser_id": tab.Browser.ID, "tab_id": tab.Target.ID, "mark_key": key}
	if clearMark, _, err := BoolArg(args, "clear"); err != nil {
		return nil, err
	} else if clearMark {
		removed, err := p.marks.Delete(ctx, session.SessionID, key)
		if err != nil {
			return nil, fmt.Errorf("clear tab mark: %w", err)
		}
		out["cleared"] = removed
		if !removed {
			out["note"] = "the tab had no mark"
		}
		return out, nil
	}
	mark, err := p.marks.Put(ctx, session.SessionID, tabmark.Mark{
		Kind:            kind,
		BrowserID:       tab.Browser.ID,
		TabID:           tab.Target.ID,
		URL:             tab.Target.URL,
		Title:           tab.Target.Title,
		SessionName:     state.tabName(tab.Target.ID),
		Note:            strings.TrimSpace(StringArg(args, "note")),
		BrowserInstance: browserInstance(tab.Browser),
	})
	if err != nil {
		return nil, fmt.Errorf("persist tab mark: %w", err)
	}
	out["mark"] = markPublic(mark)
	switch kind {
	case tabmark.KindDeliverable:
		out["shown_to_user"] = "the conversation now lists this tab as the deliverable and can open it; the mark persists with the session and reads as closed once the tab is gone"
	case tabmark.KindHandoff:
		out["shown_to_user"] = "the conversation now asks the user to take over this tab; this does not mark the task as finished"
	}
	return out, nil
}

func (p *BrowserProvider) markFor(ctx context.Context, session SessionContext, browserID, tabID string) (tabmark.Mark, bool) {
	mark, ok := p.marksByKey(ctx, session)[tabmark.KeyFor(browserID, tabID)]
	return mark, ok
}

// marksByKey loads the session's marks; an unavailable store yields none.
func (p *BrowserProvider) marksByKey(ctx context.Context, session SessionContext) map[string]tabmark.Mark {
	out := map[string]tabmark.Mark{}
	if p.marks == nil || strings.TrimSpace(session.SessionID) == "" {
		return out
	}
	marks, err := p.marks.List(ctx, session.SessionID)
	if err != nil {
		p.logger.Debug("list tab marks", slog.String("session_id", session.SessionID), slog.Any("error", err))
		return out
	}
	for _, m := range marks {
		out[m.Key] = m
	}
	return out
}

func markPublic(m tabmark.Mark) map[string]any {
	out := map[string]any{
		"key":        m.Key,
		"kind":       string(m.Kind),
		"browser_id": m.BrowserID,
		"tab_id":     m.TabID,
		"marked_at":  m.MarkedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": m.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"status":     "active",
	}
	if m.URL != "" {
		out["url"] = m.URL
	}
	if m.Title != "" {
		out["title"] = m.Title
	}
	if m.SessionName != "" {
		out["session_name"] = m.SessionName
	}
	if m.Note != "" {
		out["note"] = m.Note
	}
	if m.ClosedAt != nil {
		out["status"] = "closed"
		out["closed_at"] = m.ClosedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// browserInstance identifies the browser process behind an endpoint so a
// mark can tell a restarted browser from the one it was made in.
func browserInstance(ep browserEndpoint) string {
	if ep.PID > 0 {
		return fmt.Sprintf("%s@pid%d", ep.ID, ep.PID)
	}
	return ep.ID
}

// tabState reads a tab's document readiness over its own CDP connection
// without activating it.
func (p *BrowserProvider) tabState(ctx context.Context, client *bridge.Client, target cdpTarget) map[string]any {
	stateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := p.dialCDP(stateCtx, client, target)
	if err != nil {
		return map[string]any{"attached": false, "error": "could not attach to the tab: " + err.Error()}
	}
	defer func() { _ = conn.Close() }()
	page := &cdpPage{conn: conn}
	ready, err := page.evaluateString(stateCtx, "document.readyState")
	if err != nil {
		return map[string]any{"attached": true, "error": "could not read the document state: " + err.Error()}
	}
	href, _ := page.evaluateString(stateCtx, "location.href")
	title, _ := page.evaluateString(stateCtx, "document.title")
	return map[string]any{"attached": true, "ready_state": ready, "ready": ready == "complete", "url": href, "title": title}
}
