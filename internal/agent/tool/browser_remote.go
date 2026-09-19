package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/chat/tabmark"
	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/cdpsession"
)

// remoteSessionProtocol is the version of the browser_remote_session
// contract. Protocol 1 used the tab id as the session id and close closed
// the tab; protocol 2 issues revocable session ids bound to one tab, close
// revokes the session and its proxied connections, and the tab is only
// closed with close_tab=true.
const remoteSessionProtocol = 2

// execRemoteSession implements browser_remote_session.
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
		return p.remoteSessionCreate(ctx, session, botID, client, state, browser, args)
	case "status":
		return p.remoteSessionStatus(ctx, session, botID, client, browser, args)
	case "close":
		return p.remoteSessionClose(ctx, session, botID, client, state, args)
	default:
		return nil, fmt.Errorf("unknown session action: %s", spec.Name)
	}
}

// directEndpoint describes the unscoped, in-workspace CDP endpoint of a
// browser. It is what workspace scripts can reach; it is browser-wide and
// cannot be revoked short of restarting the browser, and the result says so.
func directEndpoint(browser browserEndpoint, target cdpTarget) map[string]any {
	return map[string]any{
		"cdp_url":          browser.baseURL(),
		"connect_over_cdp": browser.baseURL(),
		"ws_endpoint":      target.WebSocketDebuggerURL,
		"reachable_from":   "inside the workspace only (scripts run with exec)",
		"scope":            "browser-wide: every tab of " + browser.ID + ", not just this one",
		"revocable":        false,
		"note":             "this endpoint cannot be revoked independently of the browser; prefer the proxied session when the client is outside the workspace",
	}
}

func cdpProxyPath(botID, sessionID string) string {
	return "/bots/" + botID + "/container/cdp/" + sessionID
}

func proxiedEndpoint(sess cdpsession.Session) map[string]any {
	base := cdpProxyPath(sess.BotID, sess.ID)
	return map[string]any{
		"path":           base,
		"json_version":   base + "/json/version",
		"json_list":      base + "/json/list",
		"ws_endpoint":    base + "/devtools/page/" + sess.TabID,
		"reachable_from": "clients that can reach the Memoh server: prefix the paths with the server origin (the same origin the API is served from, including any /api prefix); no bearer token is needed, the session id in the path is the credential",
		"scope":          "this tab only: /json/list shows just it and the websocket attaches to it; the browser-level websocket is not exposed",
		"revocable":      true,
		"expires_at":     sess.ExpiresAt.UTC().Format(time.RFC3339),
		"idle_ttl_note":  "the session expires after being unused for a while; any request or open connection keeps it alive",
	}
}

func (p *BrowserProvider) remoteSessionCreate(ctx context.Context, session SessionContext, botID string, client *bridge.Client, state *guiSessionState, browser browserEndpoint, args map[string]any) (any, error) {
	targetURL := strings.TrimSpace(StringArg(args, "url"))
	explicitTab := strings.TrimSpace(StringArg(args, "tab_id"))
	var target cdpTarget
	created := false
	var source string
	switch {
	case targetURL != "":
		var err error
		target, err = p.createTarget(ctx, client, browser, targetURL)
		if err != nil {
			return nil, err
		}
		created = true
		source = "created"
	case explicitTab != "":
		tab, err := p.resolveExplicitTab(ctx, client, browser.ID, explicitTab)
		if err != nil {
			return nil, err
		}
		target = tab.Target
		source = "explicit"
	default:
		tab, err := p.resolveTab(ctx, client, state, map[string]any{"browser_id": browser.ID})
		if err != nil {
			return nil, err
		}
		target = tab.Target
		source = tab.Source
		created = tab.Source == "created"
	}
	out := map[string]any{
		"session_protocol": remoteSessionProtocol,
		"browser_id":       browser.ID,
		"tab_id":           target.ID,
		"tab_source":       source,
		"created_tab":      created,
		"status":           "active",
		"target":           target.publicMap(),
		"direct":           directEndpoint(browser, target),
	}
	if p.cdpSessions == nil {
		out["session_id"] = ""
		out["proxy_unavailable"] = "the revocable proxied session is not configured in this deployment; only the direct endpoint above is available and it cannot be revoked"
		return out, nil
	}
	sess, err := p.cdpSessions.Create(cdpsession.Session{
		BotID:      botID,
		ThreadID:   session.SessionID,
		BrowserID:  browser.ID,
		Port:       browser.Port,
		TabID:      target.ID,
		CreatedTab: created,
	})
	if err != nil {
		return nil, fmt.Errorf("issue CDP session: %w", err)
	}
	out["session_id"] = sess.ID
	out["proxy"] = proxiedEndpoint(sess)
	out["close_note"] = "browser_remote_session close revokes the proxied session (open connections are dropped); pass close_tab=true to also close the tab"
	return out, nil
}

func (p *BrowserProvider) sessionPublic(ctx context.Context, session SessionContext, client *bridge.Client, sess cdpsession.Session) map[string]any {
	out := map[string]any{
		"session_id":        sess.ID,
		"session_protocol":  remoteSessionProtocol,
		"status":            "active",
		"browser_id":        sess.BrowserID,
		"tab_id":            sess.TabID,
		"created_tab":       sess.CreatedTab,
		"this_conversation": sess.ThreadID != "" && sess.ThreadID == session.SessionID,
		"connections":       p.cdpSessions.Connections(sess.ID),
		"created_at":        sess.CreatedAt.UTC().Format(time.RFC3339),
		"last_used_at":      sess.LastUsed.UTC().Format(time.RFC3339),
		"expires_at":        sess.ExpiresAt.UTC().Format(time.RFC3339),
		"proxy":             proxiedEndpoint(sess),
	}
	tab, err := p.resolveExplicitTab(ctx, client, sess.BrowserID, sess.TabID)
	if err != nil {
		out["target"] = map[string]any{"tab_id": sess.TabID, "status": "closed"}
		out["target_note"] = "the tab behind this session no longer exists; connections to it fail, close the session"
	} else {
		out["target"] = tab.Target.publicMap()
		out["direct"] = directEndpoint(tab.Browser, tab.Target)
	}
	return out
}

func (p *BrowserProvider) remoteSessionStatus(ctx context.Context, session SessionContext, botID string, client *bridge.Client, browser browserEndpoint, args map[string]any) (any, error) {
	if id := strings.TrimSpace(StringArg(args, "session_id")); id != "" {
		if p.cdpSessions == nil {
			return nil, errors.New("proxied CDP sessions are not configured in this deployment")
		}
		sess, ok := p.cdpSessions.Get(id, botID)
		if !ok {
			return nil, p.explainUnknownSession(ctx, client, browser, id)
		}
		return p.sessionPublic(ctx, session, client, sess), nil
	}
	targets, err := p.listTargets(ctx, client, browser)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"session_protocol": remoteSessionProtocol,
		"browser_id":       browser.ID,
		"targets":          publicTargets(targets),
		"direct":           directEndpoint(browser, cdpTarget{}),
	}
	delete(out["direct"].(map[string]any), "ws_endpoint")
	sessions := make([]map[string]any, 0)
	if p.cdpSessions != nil {
		for _, sess := range p.cdpSessions.ListForBot(botID) {
			sessions = append(sessions, p.sessionPublic(ctx, session, client, sess))
		}
	} else {
		out["proxy_unavailable"] = "the revocable proxied session is not configured in this deployment"
	}
	out["sessions"] = sessions
	out["scope_note"] = "sessions are listed for this bot (the authorised scope); this_conversation says which belong to the current conversation"
	return out, nil
}

func (p *BrowserProvider) remoteSessionClose(ctx context.Context, session SessionContext, botID string, client *bridge.Client, state *guiSessionState, args map[string]any) (any, error) {
	id := strings.TrimSpace(StringArg(args, "session_id"))
	if p.cdpSessions == nil {
		return nil, errors.New("proxied CDP sessions are not configured in this deployment; the direct endpoint cannot be revoked")
	}
	browserID := StringArg(args, "browser_id")
	sess, ok := p.cdpSessions.Get(id, botID)
	if !ok {
		browser, err := p.resolveBrowser(ctx, client, state, browserID)
		if err != nil {
			return nil, err
		}
		return nil, p.explainUnknownSession(ctx, client, browser, id)
	}
	revoked, dropped, _ := p.cdpSessions.Revoke(sess.ID, botID)
	out := map[string]any{
		"session_id":          revoked.ID,
		"session_protocol":    remoteSessionProtocol,
		"status":              "closed",
		"revoked":             true,
		"dropped_connections": dropped,
		"browser_id":          revoked.BrowserID,
		"tab_id":              revoked.TabID,
		"tab_closed":          false,
	}
	closeTab, _, err := BoolArg(args, "close_tab")
	if err != nil {
		return nil, err
	}
	if !closeTab {
		out["tab_note"] = "the tab stays open; pass close_tab=true to close it as well"
		return out, nil
	}
	tab, err := p.resolveExplicitTab(ctx, client, revoked.BrowserID, revoked.TabID)
	if err != nil {
		out["tab_note"] = "the tab was already gone: " + err.Error()
		return out, nil
	}
	if _, err := p.closeTarget(ctx, client, tab.Browser, tab.Target.ID); err != nil {
		return nil, fmt.Errorf("the session was revoked but closing its tab failed: %w", err)
	}
	state.forgetTab(tab.Target.ID)
	if p.marks != nil && strings.TrimSpace(session.SessionID) != "" {
		_, _, _ = p.marks.MarkClosed(ctx, session.SessionID, tabmark.KeyFor(tab.Browser.ID, tab.Target.ID))
	}
	out["tab_closed"] = true
	return out, nil
}

// explainUnknownSession distinguishes a stale session id from a caller still
// using the protocol 1 convention of passing a tab id.
func (p *BrowserProvider) explainUnknownSession(ctx context.Context, client *bridge.Client, browser browserEndpoint, id string) error {
	if targets, err := p.listTargets(ctx, client, browser); err == nil {
		for _, target := range targets {
			if target.ID == id {
				return fmt.Errorf("%s is a tab id, not a session id: since session protocol %d browser_remote_session create returns a separate session_id, and close revokes that session instead of closing the tab; use browser_action tab_close to close the tab itself", id, remoteSessionProtocol)
			}
		}
	}
	return fmt.Errorf("session %s is unknown, expired, or already closed", id)
}
