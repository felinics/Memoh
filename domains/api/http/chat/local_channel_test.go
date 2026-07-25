package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/agent/application"
	sessionpkg "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/agent/command"
	"github.com/memohai/memoh/domains/agent/command/slash"
	skillset "github.com/memohai/memoh/domains/agent/extension/skills"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/http/httpfixture"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/domains/media/asset"
	attachmentpkg "github.com/memohai/memoh/domains/media/attachment"
	"github.com/memohai/memoh/domains/media/storage"
	"github.com/memohai/memoh/internal/apperror"
)

type fakeSessionTurnActiveChecker map[string]bool

func (f fakeSessionTurnActiveChecker) SessionTurnActive(botID, sessionID string) bool {
	return f[strings.TrimSpace(botID)+":"+strings.TrimSpace(sessionID)]
}

func TestNewWSAppErrorEventUsesPublicCatalogOnly(t *testing.T) {
	t.Parallel()

	event, ok := newWSAppErrorEvent(
		"stream-1",
		"session-1",
		apperror.Wrap(apperror.CodeACPConfigUpdateFailed, errors.New("SECRET transport path"), nil),
	)
	if !ok {
		t.Fatal("newWSAppErrorEvent() did not recognize application error")
	}
	feedback, ok := event.Feedback.(apperror.Public)
	if !ok || feedback.Code != apperror.CodeACPConfigUpdateFailed {
		t.Fatalf("event feedback = %#v", event.Feedback)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "i18n_key") {
		t.Fatalf("public WebSocket error leaked private or legacy data: %s", data)
	}
}

func TestFormatLocalStreamEvent_UsesChannelEventShape(t *testing.T) {
	t.Parallel()

	data, err := formatLocalStreamEvent(gateway.StreamEvent{
		Type:  gateway.StreamEventDelta,
		Delta: "hello",
		Phase: gateway.StreamPhaseText,
	})
	if err != nil {
		t.Fatalf("format local stream event failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}
	if got := payload["type"]; got != "delta" {
		t.Fatalf("expected type delta, got %#v", got)
	}
	if got := payload["delta"]; got != "hello" {
		t.Fatalf("expected delta hello, got %#v", got)
	}
	if got := payload["phase"]; got != "text" {
		t.Fatalf("expected phase text, got %#v", got)
	}
	if _, ok := payload["target"]; ok {
		t.Fatalf("unexpected wrapper field target in payload")
	}
	if _, ok := payload["event"]; ok {
		t.Fatalf("unexpected wrapper field event in payload")
	}
}

func TestFormatLocalStreamEvent_EncodesToolCallAsToolCallObject(t *testing.T) {
	t.Parallel()

	data, err := formatLocalStreamEvent(gateway.StreamEvent{
		Type: gateway.StreamEventToolCallStart,
		ToolCall: &gateway.StreamToolCall{
			Name:   "exec",
			CallID: "call-1",
			Input: map[string]any{
				"command": "pwd",
			},
		},
	})
	if err != nil {
		t.Fatalf("format local stream event failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}
	toolCall, ok := payload["tool_call"].(map[string]any)
	if !ok {
		t.Fatalf("expected tool_call object, got %#v", payload["tool_call"])
	}
	if got := toolCall["name"]; got != "exec" {
		t.Fatalf("expected tool_call.name exec, got %#v", got)
	}
	if got := toolCall["call_id"]; got != "call-1" {
		t.Fatalf("expected tool_call.call_id call-1, got %#v", got)
	}
	if _, ok := payload["toolName"]; ok {
		t.Fatalf("unexpected camelCase toolName in payload")
	}
}

func TestWSWriterIgnoresLateSendsAfterClose(t *testing.T) {
	t.Parallel()

	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("panic: %v", recovered)
			}
		}()

		writer := newWSWriter(conn)
		writer.Close()
		writer.Send([]byte(`{"type":"late"}`))
		writer.SendJSON(map[string]string{"type": "late"})
		writer.Close()
		done <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	_ = client.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket writer")
	}
}

func TestWSStreamRegistry_AbortsOnlyTargetStream(t *testing.T) {
	t.Parallel()

	registry := newWSStreamRegistry()
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	abortA := make(chan struct{}, 1)
	abortB := make(chan struct{}, 1)

	if err := registry.register(&activeWSStream{streamID: "stream-a", cancel: cancelA, abortCh: abortA}); err != nil {
		t.Fatalf("register stream-a: %v", err)
	}
	if err := registry.register(&activeWSStream{streamID: "stream-b", cancel: cancelB, abortCh: abortB}); err != nil {
		t.Fatalf("register stream-b: %v", err)
	}

	if !registry.abort("stream-a") {
		t.Fatal("expected stream-a abort to succeed")
	}

	select {
	case <-abortA:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream-a abort signal")
	}
	select {
	case <-ctxA.Done():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream-a context cancellation")
	}
	select {
	case <-abortB:
		t.Fatal("stream-b received abort signal")
	default:
	}
	select {
	case <-ctxB.Done():
		t.Fatal("stream-b context was cancelled")
	default:
	}
}

func TestWSStreamRegistry_RejectsDuplicateStreamID(t *testing.T) {
	t.Parallel()

	registry := newWSStreamRegistry()
	_, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	_, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	if err := registry.register(&activeWSStream{streamID: "stream-a", cancel: cancelA, abortCh: make(chan struct{}, 1)}); err != nil {
		t.Fatalf("register stream-a: %v", err)
	}
	if err := registry.register(&activeWSStream{streamID: "stream-a", cancel: cancelB, abortCh: make(chan struct{}, 1)}); err == nil {
		t.Fatal("expected duplicate stream id registration to fail")
	}
}

func TestLocalChannelHandlerIssueRuntimeOwnerBearerToken(t *testing.T) {
	t.Parallel()

	const secret = "local-ws-runtime-secret" //nolint:gosec // G101 false positive: test fixture, not a real credential.
	handler := &LocalChannelHandler{logger: slog.Default()}
	handler.SetAuthTokenConfig(secret, time.Hour)

	got := handler.issueRuntimeOwnerBearerToken("runtime-owner", "Bearer caller-token")
	raw := strings.TrimSpace(strings.TrimPrefix(got, "Bearer "))
	parsed, err := jwt.Parse(raw, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			t.Fatalf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("parse runtime token: token valid=%v err=%v", parsed != nil && parsed.Valid, err)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims type = %T, want jwt.MapClaims", parsed.Claims)
	}
	if gotSub := claims["sub"]; gotSub != "runtime-owner" {
		t.Fatalf("runtime token subject = %v, want runtime-owner", gotSub)
	}

	handler.SetAuthTokenConfig("", time.Hour)
	if fallback := handler.issueRuntimeOwnerBearerToken("runtime-owner", "Bearer caller-token"); fallback != "Bearer caller-token" {
		t.Fatalf("fallback token = %q, want original bearer", fallback)
	}
}

func TestWSStreamRegistry_HasSessionTracksActiveStreams(t *testing.T) {
	t.Parallel()

	registry := newWSStreamRegistry()
	_, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	_, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	if registry.hasSession("session-1") {
		t.Fatal("empty registry reported active session")
	}
	if err := registry.register(&activeWSStream{streamID: "stream-a", sessionID: " session-1 ", cancel: cancelA, abortCh: make(chan struct{}, 1)}); err != nil {
		t.Fatalf("register stream-a: %v", err)
	}
	if err := registry.register(&activeWSStream{streamID: "stream-b", sessionID: "session-2", cancel: cancelB, abortCh: make(chan struct{}, 1)}); err != nil {
		t.Fatalf("register stream-b: %v", err)
	}

	if !registry.hasSession("session-1") {
		t.Fatal("expected session-1 to be active")
	}
	if !registry.hasSession(" session-2 ") {
		t.Fatal("expected trimmed session-2 to be active")
	}
	if registry.hasSession("session-3") {
		t.Fatal("unexpected session-3 activity")
	}

	registry.finish("stream-a")
	if registry.hasSession("session-1") {
		t.Fatal("session-1 should be inactive after finish")
	}
	if !registry.hasSession("session-2") {
		t.Fatal("session-2 should remain active")
	}
}

func TestShouldRejectWSRequestedSkillsForActiveStream(t *testing.T) {
	t.Parallel()

	registry := newWSStreamRegistry()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := registry.register(&activeWSStream{streamID: "stream-a", sessionID: "session-1", cancel: cancel, abortCh: make(chan struct{}, 1)}); err != nil {
		t.Fatalf("register stream-a: %v", err)
	}

	if !shouldRejectWSSkillActivationForActiveStream(registry, nil, "bot-1", "session-1", true) {
		t.Fatal("expected requested skills to reject against an active session")
	}
	if shouldRejectWSSkillActivationForActiveStream(registry, nil, "bot-1", "session-1", false) {
		t.Fatal("ordinary messages should not reject against an active session")
	}
	if shouldRejectWSSkillActivationForActiveStream(registry, nil, "bot-1", "", true) {
		t.Fatal("empty session should not reject as active")
	}

	activeTurns := fakeSessionTurnActiveChecker{"bot-1:session-global": true}
	if !shouldRejectWSSkillActivationForActiveStream(newWSStreamRegistry(), activeTurns, "bot-1", "session-global", true) {
		t.Fatal("expected requested skills to reject against a globally active session")
	}
}

func TestWSRequestedSkillTurnRegistryReserve(t *testing.T) {
	t.Parallel()

	registry := newWSRequestedSkillTurnRegistry()
	release, ok := registry.reserve("bot-1", "session-1", "stream-a")
	if !ok {
		t.Fatal("first reservation rejected")
	}
	if _, ok := registry.reserve("bot-1", "session-1", "stream-b"); ok {
		t.Fatal("second reservation for same bot/session should reject")
	}
	if _, ok := registry.reserve("bot-1", "session-2", "stream-c"); !ok {
		t.Fatal("different session reservation should be allowed")
	}

	release()
	if releaseAgain, ok := registry.reserve("bot-1", "session-1", "stream-d"); !ok {
		t.Fatal("reservation should be allowed after release")
	} else {
		releaseAgain()
	}

	releaseActive := registry.enter("bot-1", "session-1", "stream-regular")
	if _, ok := registry.reserve("bot-1", "session-1", "stream-skill"); ok {
		t.Fatal("requested skill reservation should reject while a regular stream is active")
	}
	releaseActive()
	if releaseAfterActive, ok := registry.reserve("bot-1", "session-1", "stream-skill-2"); !ok {
		t.Fatal("requested skill reservation should be allowed after regular stream release")
	} else {
		releaseAfterActive()
	}
}

func TestCanOpenLocalWebSocketAllowsWorkspaceOrManage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		perms []string
		want  bool
	}{
		{name: "workspace exec", perms: []string{bot.PermissionWorkspaceExec}, want: true},
		{name: "manage", perms: []string{bot.PermissionManage}, want: true},
		{name: "chat only", perms: []string{bot.PermissionChat}, want: false},
		{name: "none", perms: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := canOpenLocalWebSocket(tt.perms); got != tt.want {
				t.Fatalf("canOpenLocalWebSocket(%v) = %v, want %v", tt.perms, got, tt.want)
			}
		})
	}
}

type localChannelSessionAuthQueries struct {
	stubThreadStore
	bot            bot.Record
	session        sessionpkg.Thread
	permissions    []byte
	createSession  func(context.Context, sessionpkg.CreateRecord) (sessionpkg.Thread, error)
	getSessionByID func(context.Context, string) (sessionpkg.Thread, error)
}

func newLocalChannelBotService(queries localChannelSessionAuthQueries) *bot.Service {
	return httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions))
}

func (q localChannelSessionAuthQueries) InTransaction(_ context.Context, fn func(sessionpkg.Store) error) error {
	return fn(q)
}

func (q localChannelSessionAuthQueries) GetBotMetadata(_ context.Context, _ string) (map[string]any, error) {
	return botMetadataMap(q.bot), nil
}

func (q localChannelSessionAuthQueries) GetThread(ctx context.Context, id string) (sessionpkg.Thread, error) {
	if q.getSessionByID != nil {
		return q.getSessionByID(ctx, id)
	}
	return q.session, nil
}

func (q localChannelSessionAuthQueries) CreateThread(ctx context.Context, record sessionpkg.CreateRecord) (sessionpkg.Thread, error) {
	if q.createSession == nil {
		return sessionpkg.Thread{}, errors.New("CreateSession not configured")
	}
	return q.createSession(ctx, record)
}

func TestLocalChannelCreateWSChatSessionDoesNotBindRoute(t *testing.T) {
	t.Parallel()

	const (
		botID     = "11111111-1111-1111-1111-111111111111"
		userID    = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		sessionID = "22222222-2222-2222-2222-222222222222"
	)
	var gotParams sessionpkg.CreateRecord
	called := false
	now := time.Now()
	queries := localChannelSessionAuthQueries{
		createSession: func(_ context.Context, params sessionpkg.CreateRecord) (sessionpkg.Thread, error) {
			called = true
			gotParams = params
			return sessionpkg.Thread{
				ID:              sessionID,
				BotID:           params.BotID,
				RouteID:         params.RouteID,
				ChannelType:     params.ChannelType,
				Type:            params.Type,
				SessionMode:     params.SessionMode,
				RuntimeType:     params.RuntimeType,
				RuntimeMetadata: params.RuntimeMetadata,
				Metadata:        params.Metadata,
				CreatedByUserID: params.CreatedByUserID,
				CreatedAt:       now,
				UpdatedAt:       now,
			}, nil
		},
	}
	handler := &LocalChannelHandler{
		channelType:    gateway.ChannelTypeLocal,
		sessionService: newThreadServiceForTest(queries),
	}

	sess, err := handler.createWSChatSession(context.Background(), botID, userID)
	if err != nil {
		t.Fatalf("createWSChatSession: %v", err)
	}
	if !called {
		t.Fatal("CreateSession was not called")
	}
	if gotParams.RouteID != "" {
		t.Fatalf("route id = %q, want empty", gotParams.RouteID)
	}
	if sess.RouteID != "" {
		t.Fatalf("session route id = %q, want empty", sess.RouteID)
	}
	if got := gotParams.BotID; got != botID {
		t.Fatalf("bot id = %q, want %q", got, botID)
	}
	if got := gotParams.CreatedByUserID; got != userID {
		t.Fatalf("created by user id = %q, want %q", got, userID)
	}
	if got := gotParams.ChannelType; got != gateway.ChannelTypeLocal.String() {
		t.Fatalf("channel type = %q, want local", got)
	}
}

type testRuntimeSkillResolver struct {
	entries []skillset.Entry
	catalog []skillset.SafeCatalogItem
}

func (r testRuntimeSkillResolver) ListSafeSkillCatalog(context.Context, string) ([]skillset.SafeCatalogItem, error) {
	if r.catalog != nil {
		return r.catalog, nil
	}
	return skillset.BuildSafeCatalog(r.entries)
}

func (r testRuntimeSkillResolver) ResolveTextRequestedSkills(_ context.Context, _ string, names []string) ([]skillset.ResolvedSkill, error) {
	return skillset.ResolveTextRequestedSkills(r.entries, names, skillset.ResolveLimits{
		MaxCount:       5,
		MaxSingleBytes: 1024 * 1024,
		MaxTotalBytes:  2 * 1024 * 1024,
	})
}

func openLocalChannelTestWS(t *testing.T, handler *LocalChannelHandler, botID, userID string) *websocket.Conn {
	t.Helper()

	e := echo.New()
	e.GET("/bots/:bot_id/local/ws", func(c echo.Context) error {
		c.Set("user", &jwt.Token{
			Valid: true,
			Claims: jwt.MapClaims{
				"sub":     userID,
				"user_id": userID,
			},
		})
		return handler.HandleWebSocket(c)
	})
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/bots/" + botID + "/local/ws"
	client, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil && resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestLocalChannelAuthorizeWSSessionScopesChatToCreator(t *testing.T) {
	t.Parallel()

	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		sessionID   = "22222222-2222-2222-2222-222222222222"
		currentUser = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		otherUser   = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	)
	queries := localChannelSessionAuthQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: sessionpkg.Thread{
			ID:              sessionID,
			BotID:           botID,
			Type:            sessionpkg.TypeChat,
			CreatedByUserID: otherUser,
			Metadata:        map[string]any{},
		},
		permissions: []byte(`["chat"]`),
	}
	handler := &LocalChannelHandler{
		botService:     newLocalChannelBotService(queries),
		accountService: account.NewService(nil, httpfixture.AdminAccountStore{Role: "user"}),
		sessionService: newThreadServiceForTest(queries),
	}

	err := handler.authorizeWSSession(testEchoContext(currentUser), currentUser, botID, sessionID)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusNotFound {
		t.Fatalf("authorizeWSSession() error = %v, want HTTP 404", err)
	}
}

func TestLocalChannelAuthorizeWSSessionAllowsManageAccess(t *testing.T) {
	t.Parallel()

	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		sessionID   = "22222222-2222-2222-2222-222222222222"
		currentUser = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		otherUser   = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	)
	queries := localChannelSessionAuthQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: sessionpkg.Thread{
			ID:              sessionID,
			BotID:           botID,
			Type:            sessionpkg.TypeChat,
			CreatedByUserID: otherUser,
			Metadata:        map[string]any{},
		},
		permissions: []byte(`["manage"]`),
	}
	handler := &LocalChannelHandler{
		botService:     newLocalChannelBotService(queries),
		accountService: account.NewService(nil, httpfixture.AdminAccountStore{Role: "user"}),
		sessionService: newThreadServiceForTest(queries),
	}

	if err := handler.authorizeWSSession(testEchoContext(currentUser), currentUser, botID, sessionID); err != nil {
		t.Fatalf("authorizeWSSession() error = %v, want nil", err)
	}
}

func TestLocalChannelWSMessageAuthorizesSessionBeforeSlashCommand(t *testing.T) {
	t.Parallel()

	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		sessionID   = "22222222-2222-2222-2222-222222222222"
		currentUser = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		otherUser   = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	)
	queries := localChannelSessionAuthQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: sessionpkg.Thread{
			ID:              sessionID,
			BotID:           botID,
			Type:            sessionpkg.TypeChat,
			CreatedByUserID: otherUser,
			Metadata:        map[string]any{},
		},
		permissions: []byte(`["workspace_exec"]`),
	}
	handler := &LocalChannelHandler{
		channelType:    gateway.ChannelTypeLocal,
		botService:     newLocalChannelBotService(queries),
		accountService: account.NewService(nil, httpfixture.AdminAccountStore{Role: "user"}),
		sessionService: newThreadServiceForTest(queries),
		agentService:   &application.Service{},
		commandHandler: command.NewHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil),
		logger:         slog.Default(),
	}

	e := echo.New()
	e.GET("/bots/:bot_id/local/ws", func(c echo.Context) error {
		c.Set("user", &jwt.Token{
			Valid: true,
			Claims: jwt.MapClaims{
				"sub":     currentUser,
				"user_id": currentUser,
			},
		})
		return handler.HandleWebSocket(c)
	})
	server := httptest.NewServer(e)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/bots/" + botID + "/local/ws"
	client, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.WriteJSON(map[string]any{
		"type":       "message",
		"stream_id":  "stream-1",
		"session_id": sessionID,
		"text":       "/help",
	}); err != nil {
		t.Fatalf("write ws message: %v", err)
	}

	var event map[string]any
	if err := client.ReadJSON(&event); err != nil {
		t.Fatalf("read ws event: %v", err)
	}
	if got := event["type"]; got != "error" {
		t.Fatalf("event type = %#v, want error; event=%#v", got, event)
	}
	if got := event["message"]; got != "session not found" {
		t.Fatalf("event message = %#v, want session not found; event=%#v", got, event)
	}
	if _, ok := event["result"]; ok {
		t.Fatalf("unexpected command result before session authorization: %#v", event)
	}
}

func TestLocalChannelWSQuickActionRequiresChatAccessWithoutSession(t *testing.T) {
	t.Parallel()

	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		currentUser = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	queries := localChannelSessionAuthQueries{
		bot:         httpfixture.BotRow(botID, map[string]any{}),
		permissions: []byte(`["workspace_exec"]`),
	}
	handler := &LocalChannelHandler{
		channelType:    gateway.ChannelTypeLocal,
		botService:     newLocalChannelBotService(queries),
		accountService: account.NewService(nil, httpfixture.AdminAccountStore{Role: "user"}),
		agentService:   &application.Service{},
		logger:         slog.Default(),
	}

	e := echo.New()
	e.GET("/bots/:bot_id/local/ws", func(c echo.Context) error {
		c.Set("user", &jwt.Token{
			Valid: true,
			Claims: jwt.MapClaims{
				"sub":     currentUser,
				"user_id": currentUser,
			},
		})
		return handler.HandleWebSocket(c)
	})
	server := httptest.NewServer(e)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/bots/" + botID + "/local/ws"
	client, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.WriteJSON(map[string]any{
		"type":      "message",
		"stream_id": "stream-1",
		"text":      "/skill list",
	}); err != nil {
		t.Fatalf("write ws message: %v", err)
	}

	var event map[string]any
	if err := client.ReadJSON(&event); err != nil {
		t.Fatalf("read ws event: %v", err)
	}
	if got := event["type"]; got != "error" {
		t.Fatalf("event type = %#v, want error; event=%#v", got, event)
	}
	if _, ok := event["result"]; ok {
		t.Fatalf("workspace_exec-only user received command result: %#v", event)
	}
}

func TestLocalChannelWSSkillActivationRequiresChatAccessWithSession(t *testing.T) {
	t.Parallel()

	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		sessionID   = "22222222-2222-2222-2222-222222222222"
		currentUser = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	queries := localChannelSessionAuthQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: sessionpkg.Thread{
			ID:              sessionID,
			BotID:           botID,
			Type:            sessionpkg.TypeChat,
			SessionMode:     sessionpkg.TypeChat,
			RuntimeType:     sessionpkg.RuntimeACPAgent,
			CreatedByUserID: currentUser,
			Metadata:        map[string]any{},
		},
		permissions: []byte(`["workspace_exec"]`),
	}
	handler := &LocalChannelHandler{
		channelType:    gateway.ChannelTypeLocal,
		botService:     newLocalChannelBotService(queries),
		accountService: account.NewService(nil, httpfixture.AdminAccountStore{Role: "user"}),
		sessionService: newThreadServiceForTest(queries),
		agentService:   &application.Service{},
		logger:         slog.Default(),
	}

	e := echo.New()
	e.GET("/bots/:bot_id/local/ws", func(c echo.Context) error {
		c.Set("user", &jwt.Token{
			Valid: true,
			Claims: jwt.MapClaims{
				"sub":     currentUser,
				"user_id": currentUser,
			},
		})
		return handler.HandleWebSocket(c)
	})
	server := httptest.NewServer(e)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/bots/" + botID + "/local/ws"
	client, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.WriteJSON(map[string]any{
		"type":       "message",
		"stream_id":  "stream-1",
		"session_id": sessionID,
		"text":       "/alpha",
	}); err != nil {
		t.Fatalf("write ws message: %v", err)
	}

	var event map[string]any
	if err := client.ReadJSON(&event); err != nil {
		t.Fatalf("read ws event: %v", err)
	}
	if got := event["type"]; got != "error" {
		t.Fatalf("event type = %#v, want error; event=%#v", got, event)
	}
	if got := event["message"]; got != "bot access denied" {
		t.Fatalf("event message = %#v, want bot access denied; event=%#v", got, event)
	}
	if _, ok := event["error"]; ok {
		t.Fatalf("workspace_exec-only skill activation reached slash command handling: %#v", event)
	}
}

func TestLocalChannelWSQuickActionHelpOmitsSkillsForACPSession(t *testing.T) {
	t.Parallel()

	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		sessionID   = "22222222-2222-2222-2222-222222222222"
		currentUser = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	queries := localChannelSessionAuthQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: sessionpkg.Thread{
			ID:              sessionID,
			BotID:           botID,
			Type:            sessionpkg.TypeChat,
			SessionMode:     sessionpkg.TypeChat,
			RuntimeType:     sessionpkg.RuntimeACPAgent,
			CreatedByUserID: currentUser,
			Metadata:        map[string]any{},
		},
		permissions: []byte(`["chat","workspace_exec"]`),
	}
	handler := &LocalChannelHandler{
		channelType:    gateway.ChannelTypeLocal,
		botService:     newLocalChannelBotService(queries),
		accountService: account.NewService(nil, httpfixture.AdminAccountStore{Role: "user"}),
		sessionService: newThreadServiceForTest(queries),
		agentService:   &application.Service{},
		skillResolver: testRuntimeSkillResolver{catalog: []skillset.SafeCatalogItem{
			{Name: "alpha", DisplayName: "alpha", Description: "Alpha", State: skillset.StateEffective},
		}},
		logger: slog.Default(),
	}

	client := openLocalChannelTestWS(t, handler, botID, currentUser)
	if err := client.WriteJSON(map[string]any{
		"type":       "message",
		"stream_id":  "stream-1",
		"session_id": sessionID,
		"text":       "/help",
	}); err != nil {
		t.Fatalf("write ws message: %v", err)
	}

	var event CommandEventResponse
	if err := client.ReadJSON(&event); err != nil {
		t.Fatalf("read ws event: %v", err)
	}
	if event.Type != "command_result" {
		t.Fatalf("event type = %q, want command_result; event=%#v", event.Type, event)
	}
	if event.Result == nil {
		t.Fatalf("missing command result: %#v", event)
	}
	for _, item := range event.Result.Items {
		if item.ID == "skill.list" || item.Kind == "skill" {
			t.Fatalf("ACP help exposed skill action item: %#v", item)
		}
	}
	if strings.Contains(event.Result.Text, "/skill list") || strings.Contains(event.Result.Text, "/<skill-name>") {
		t.Fatalf("ACP help exposed skill activation guidance: %q", event.Result.Text)
	}
}

func TestLocalChannelWSQuickActionSkillListRejectsACPSession(t *testing.T) {
	t.Parallel()

	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		sessionID   = "22222222-2222-2222-2222-222222222222"
		currentUser = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	queries := localChannelSessionAuthQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: sessionpkg.Thread{
			ID:              sessionID,
			BotID:           botID,
			Type:            sessionpkg.TypeChat,
			SessionMode:     sessionpkg.TypeChat,
			RuntimeType:     sessionpkg.RuntimeACPAgent,
			CreatedByUserID: currentUser,
			Metadata:        map[string]any{},
		},
		permissions: []byte(`["chat","workspace_exec"]`),
	}
	handler := &LocalChannelHandler{
		channelType:    gateway.ChannelTypeLocal,
		botService:     newLocalChannelBotService(queries),
		accountService: account.NewService(nil, httpfixture.AdminAccountStore{Role: "user"}),
		sessionService: newThreadServiceForTest(queries),
		agentService:   &application.Service{},
		skillResolver: testRuntimeSkillResolver{catalog: []skillset.SafeCatalogItem{
			{Name: "alpha", DisplayName: "alpha", Description: "Alpha", State: skillset.StateEffective},
		}},
		logger: slog.Default(),
	}

	client := openLocalChannelTestWS(t, handler, botID, currentUser)
	if err := client.WriteJSON(map[string]any{
		"type":       "message",
		"stream_id":  "stream-1",
		"session_id": sessionID,
		"text":       "/skill list",
	}); err != nil {
		t.Fatalf("write ws message: %v", err)
	}

	var event CommandEventResponse
	if err := client.ReadJSON(&event); err != nil {
		t.Fatalf("read ws event: %v", err)
	}
	if event.Type != "command_error" {
		t.Fatalf("event type = %q, want command_error; event=%#v", event.Type, event)
	}
	if event.Error == nil || event.Error.Code != slash.CodeUnsupportedSkillSlashContext {
		t.Fatalf("error = %#v, want code %q", event.Error, slash.CodeUnsupportedSkillSlashContext)
	}
}

func TestResolveWebRequestedSkillContextsRejectsBlankName(t *testing.T) {
	t.Parallel()

	handler := &LocalChannelHandler{
		skillResolver: testRuntimeSkillResolver{entries: []skillset.Entry{
			{
				Name:                    "alpha",
				Description:             "Alpha",
				Content:                 "alpha context",
				State:                   skillset.StateEffective,
				RuntimeUsable:           true,
				RuntimeUsabilityChecked: true,
			},
		}},
	}
	_, err := handler.resolveWebRequestedSkillContexts(context.Background(), "bot-1", []webRequestedSkill{{Name: " "}})
	var slashErr slash.Error
	if !errors.As(err, &slashErr) || slashErr.Code != slash.CodeInvalidSkillSlashSyntax {
		t.Fatalf("err = %#v, want %s", err, slash.CodeInvalidSkillSlashSyntax)
	}
}

func TestExecuteQuickActionAcceptsSessionIDAsCapabilityContext(t *testing.T) {
	t.Parallel()

	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		sessionID   = "22222222-2222-2222-2222-222222222222"
		currentUser = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	queries := localChannelSessionAuthQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: sessionpkg.Thread{
			ID:              sessionID,
			BotID:           botID,
			Type:            sessionpkg.TypeChat,
			SessionMode:     sessionpkg.TypeChat,
			RuntimeType:     sessionpkg.RuntimeModel,
			CreatedByUserID: currentUser,
			Metadata:        map[string]any{},
		},
		permissions: []byte(`["chat"]`),
	}
	handler := &LocalChannelHandler{
		botService:     newLocalChannelBotService(queries),
		accountService: account.NewService(nil, httpfixture.AdminAccountStore{Role: "user"}),
		sessionService: newThreadServiceForTest(queries),
		logger:         slog.Default(),
	}

	body := strings.NewReader(`{"action_id":"help","session_id":"` + sessionID + `","invocation_id":"inv-1","composer_scope":"scope-1"}`)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots/"+botID+"/quick-actions/execute", body)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e := echo.New()
	c := httpfixture.AuthContext(e, req, rec, currentUser)
	c.SetParamNames("bot_id")
	c.SetParamValues(botID)

	if err := handler.ExecuteQuickAction(c); err != nil {
		t.Fatalf("ExecuteQuickAction: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var event CommandEventResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &event); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if event.Type != "command_result" {
		t.Fatalf("event type = %q, want command_result; event=%#v", event.Type, event)
	}
	if event.SessionID != sessionID {
		t.Fatalf("session id = %q, want %q", event.SessionID, sessionID)
	}
	if event.ActionID != "help" {
		t.Fatalf("action id = %q, want help", event.ActionID)
	}
	if event.InvocationID != "inv-1" {
		t.Fatalf("invocation id = %q, want inv-1", event.InvocationID)
	}
	if event.ComposerScope != "scope-1" {
		t.Fatalf("composer scope = %q, want scope-1", event.ComposerScope)
	}
	if event.Result == nil {
		t.Fatalf("missing result: %#v", event)
	}
	if !strings.Contains(event.Result.Text, "/skill list") {
		t.Fatalf("help text = %q, want skill quick action guidance", event.Result.Text)
	}
}

func TestExecuteWebQuickActionHelpListsAllQuickActions(t *testing.T) {
	t.Parallel()

	handler := &LocalChannelHandler{}

	full, slashErr := handler.executeWebQuickAction(context.Background(), "bot-1", "help", true)
	if slashErr != nil {
		t.Fatalf("executeWebQuickAction: %v", slashErr)
	}
	gotIDs := make([]string, 0, len(full.Items))
	for _, item := range full.Items {
		gotIDs = append(gotIDs, item.ID)
	}
	wantIDs := []string{"help", "new", "compact", "skill.list", "model"}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("item ids = %v, want %v", gotIDs, wantIDs)
	}
	for _, label := range []string{"/help", "/new", "/compact", "/skill list", "/model"} {
		if !strings.Contains(full.Text, label) {
			t.Fatalf("help text = %q, missing %q", full.Text, label)
		}
	}

	restricted, slashErr := handler.executeWebQuickAction(context.Background(), "bot-1", "help", false)
	if slashErr != nil {
		t.Fatalf("executeWebQuickAction: %v", slashErr)
	}
	gotRestrictedIDs := make([]string, 0, len(restricted.Items))
	for _, item := range restricted.Items {
		gotRestrictedIDs = append(gotRestrictedIDs, item.ID)
	}
	wantRestrictedIDs := []string{"help", "new", "compact"}
	if !slices.Equal(gotRestrictedIDs, wantRestrictedIDs) {
		t.Fatalf("restricted item ids = %v, want %v", gotRestrictedIDs, wantRestrictedIDs)
	}
	if strings.Contains(restricted.Text, "/skill list") || strings.Contains(restricted.Text, "/model") {
		t.Fatalf("restricted help text should omit skill/model actions: %q", restricted.Text)
	}
}

func TestPostMessageRejectsSlashOnLegacyRESTEndpoint(t *testing.T) {
	t.Parallel()

	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		currentUser = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	)
	queries := localChannelSessionAuthQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
	}
	handler := &LocalChannelHandler{
		channelType:    gateway.ChannelTypeLocal,
		channelManager: &gateway.Manager{},
		channelStore:   &gateway.Store{},
		botService:     newLocalChannelBotService(queries),
		accountService: account.NewService(nil, httpfixture.AdminAccountStore{Role: "user"}),
		logger:         slog.Default(),
	}

	body := strings.NewReader(`{"message":{"text":"/help"}}`)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots/"+botID+"/local/messages", body)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e := echo.New()
	c := httpfixture.AuthContext(e, req, rec, currentUser)
	c.SetParamNames("bot_id")
	c.SetParamValues(botID)

	if err := handler.PostMessage(c); err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var event CommandEventResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &event); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if event.Type != "command_error" {
		t.Fatalf("event type = %q, want command_error; event=%#v", event.Type, event)
	}
	if event.Error == nil || event.Error.Code != slash.CodeUnsupportedLegacyEndpoint {
		t.Fatalf("error = %#v, want code %q", event.Error, slash.CodeUnsupportedLegacyEndpoint)
	}
}

func TestLocalChannelWSSessionSupportsRequestedSkills(t *testing.T) {
	t.Parallel()

	const (
		botID     = "11111111-1111-1111-1111-111111111111"
		sessionID = "22222222-2222-2222-2222-222222222222"
	)
	tests := []struct {
		name    string
		session sessionpkg.Thread
		want    bool
	}{
		{
			name: "model chat",
			session: sessionpkg.Thread{
				ID:          sessionID,
				BotID:       botID,
				Type:        sessionpkg.TypeChat,
				SessionMode: sessionpkg.TypeChat,
				RuntimeType: sessionpkg.RuntimeModel,
				Metadata:    map[string]any{},
			},
			want: true,
		},
		{
			name: "discuss",
			session: sessionpkg.Thread{
				ID:          sessionID,
				BotID:       botID,
				Type:        sessionpkg.TypeDiscuss,
				SessionMode: sessionpkg.TypeDiscuss,
				RuntimeType: sessionpkg.RuntimeModel,
				Metadata:    map[string]any{},
			},
			want: false,
		},
		{
			name: "acp chat",
			session: sessionpkg.Thread{
				ID:              sessionID,
				BotID:           botID,
				Type:            sessionpkg.TypeChat,
				SessionMode:     sessionpkg.TypeChat,
				RuntimeType:     sessionpkg.RuntimeACPAgent,
				Metadata:        map[string]any{},
				RuntimeMetadata: map[string]any{},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := &LocalChannelHandler{
				sessionService: newThreadServiceForTest(localChannelSessionAuthQueries{session: tc.session}),
			}
			got, err := handler.wsSessionSupportsRequestedSkills(context.Background(), sessionID)
			if err != nil {
				t.Fatalf("wsSessionSupportsRequestedSkills: %v", err)
			}
			if got != tc.want {
				t.Fatalf("supported = %v, want %v", got, tc.want)
			}
		})
	}
}

func testEchoContext(userID string) echo.Context {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ws", nil)
	rec := httptest.NewRecorder()
	return httpfixture.AuthContext(e, req, rec, userID)
}

func TestWSIngestAttachments_RewritesContainerPathToAssetRef(t *testing.T) {
	t.Parallel()

	handler, provider := newLocalChannelHandlerWithMedia()
	provider.SeedContainerFile("bot-1", "/data/images/demo.png", []byte("image-bytes"))

	original, err := json.Marshal(map[string]any{
		"type": "attachment_delta",
		"attachments": []any{
			map[string]any{
				"type": "image",
				"path": "/data/images/demo.png",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal original event: %v", err)
	}

	processed := handler.wsIngestAttachments(context.Background(), "bot-1", original)
	if len(processed) != 1 {
		t.Fatalf("expected 1 processed event, got %d", len(processed))
	}

	var envelope struct {
		Type        string           `json:"type"`
		Attachments []map[string]any `json:"attachments"`
	}
	if err := json.Unmarshal(processed[0], &envelope); err != nil {
		t.Fatalf("unmarshal processed event: %v", err)
	}
	if envelope.Type != "attachment_delta" {
		t.Fatalf("unexpected event type: %q", envelope.Type)
	}
	if len(envelope.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(envelope.Attachments))
	}

	att := envelope.Attachments[0]
	if got, _ := att["content_hash"].(string); got == "" {
		t.Fatalf("expected content_hash after ingestion, got %#v", att["content_hash"])
	}
	if got, _ := att["name"].(string); got != "demo.png" {
		t.Fatalf("expected inferred name demo.png, got %#v", att["name"])
	}
	meta, ok := att["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata map, got %#v", att["metadata"])
	}
	if meta["source_path"] != "/data/images/demo.png" {
		t.Fatalf("expected source_path preserved, got %#v", meta["source_path"])
	}
	if meta["storage_key"] == nil || meta["storage_key"] == "" {
		t.Fatalf("expected storage_key metadata, got %#v", meta["storage_key"])
	}
	if got, _ := att["url"].(string); got != "" {
		t.Fatalf("expected empty url after asset rewrite, got %#v", att["url"])
	}
}

func TestBuildTtsAttachment_FallbackKeepsDataURLInBase64Field(t *testing.T) {
	t.Parallel()

	handler := &LocalChannelHandler{logger: slog.Default()}
	att := handler.buildTtsAttachment(context.Background(), "bot-1", "audio/mpeg", []byte("audio"))

	if got, _ := att["type"].(string); got != "voice" {
		t.Fatalf("expected voice attachment type, got %#v", att["type"])
	}
	if got, _ := att["base64"].(string); got == "" {
		t.Fatalf("expected fallback data URL in base64 field, got %#v", att["base64"])
	}
	if got, _ := att["url"].(string); got != "" {
		t.Fatalf("expected empty url field in fallback attachment, got %#v", att["url"])
	}
}

func TestExtractAssetRefsFromProcessedEvent_UsesBundleParsing(t *testing.T) {
	t.Parallel()

	event, err := json.Marshal(map[string]any{
		"type": "attachment_delta",
		"attachments": []any{
			map[string]any{
				"type":         "image",
				"content_hash": "asset-1",
				"mime":         "image/png",
				"size":         42,
				"metadata": map[string]any{
					"name":        "demo.png",
					"storage_key": "aa/asset-1.png",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	refs := extractAssetRefsFromProcessedEvent(event)
	if len(refs) != 1 {
		t.Fatalf("expected 1 asset ref, got %d", len(refs))
	}
	if refs[0].ContentHash != "asset-1" {
		t.Fatalf("unexpected content hash: %q", refs[0].ContentHash)
	}
	if refs[0].Name != "demo.png" {
		t.Fatalf("expected metadata name fallback, got %q", refs[0].Name)
	}
	if refs[0].StorageKey != "aa/asset-1.png" {
		t.Fatalf("unexpected storage key: %q", refs[0].StorageKey)
	}
	if refs[0].SizeBytes != 42 {
		t.Fatalf("unexpected size bytes: %d", refs[0].SizeBytes)
	}
}

func TestParseWSClientAttachments_NormalizesToolStyleInputs(t *testing.T) {
	t.Parallel()

	rawAttachments := []json.RawMessage{
		json.RawMessage(`"screenshot.png"`),
		json.RawMessage(`{"url":"data:image/png;base64,AAAA","type":"image"}`),
	}
	got, err := parseWSClientAttachments(rawAttachments)
	if err != nil {
		t.Fatalf("parseWSClientAttachments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(got))
	}
	if got[0].Path != "/data/screenshot.png" {
		t.Fatalf("expected bare path normalized to /data prefix, got %q", got[0].Path)
	}
	if got[1].Base64 != "data:image/png;base64,AAAA" {
		t.Fatalf("expected inline data preserved in base64 field, got %q", got[1].Base64)
	}
	if got[1].Mime != "image/png" {
		t.Fatalf("expected inferred image/png mime, got %q", got[1].Mime)
	}
}

func TestParseWSClientAttachmentsRejectsReservedMetadata(t *testing.T) {
	t.Parallel()

	_, err := parseWSClientAttachments([]json.RawMessage{
		json.RawMessage(`{"url":"data:image/png;base64,AAAA","type":"image","metadata":{"model_requested_skills":["alpha"]}}`),
	})
	var slashErr slash.Error
	if !errors.As(err, &slashErr) || slashErr.Code != slash.CodeReservedSkillMetadata {
		t.Fatalf("err = %#v, want %s", err, slash.CodeReservedSkillMetadata)
	}
}

func TestApplyBundleToItemMap_UsesMergedBundleFields(t *testing.T) {
	t.Parallel()

	item := map[string]any{
		"legacy": "keep",
		"url":    "stale",
	}
	got := applyBundleToItemMap(item, attachmentpkg.Bundle{
		Type:        "image",
		ContentHash: "asset-1",
		Mime:        "image/png",
		Metadata: map[string]any{
			attachmentpkg.MetadataKeyStorageKey: "aa/asset-1.png",
		},
	})
	if got["legacy"] != "keep" {
		t.Fatalf("expected unknown fields preserved, got %#v", got["legacy"])
	}
	if got["content_hash"] != "asset-1" {
		t.Fatalf("expected content_hash updated, got %#v", got["content_hash"])
	}
	if url, ok := got["url"]; ok && url != "" {
		t.Fatalf("expected url absent or empty after bundle merge, got %#v", url)
	}
}

type localChannelMemoryProvider struct {
	mu             sync.Mutex
	objects        map[string][]byte
	containerFiles map[string][]byte
}

func (p *localChannelMemoryProvider) Put(ctx context.Context, key string, reader io.Reader) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.objects[key] = append([]byte(nil), data...)
	_ = ctx
	return nil
}

func (p *localChannelMemoryProvider) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	data, ok := p.objects[key]
	if !ok {
		return nil, io.EOF
	}
	_ = ctx
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (*localChannelMemoryProvider) Delete(context.Context, string) error { return nil }

func (*localChannelMemoryProvider) AccessPath(_ context.Context, key string) string {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 {
		return "/data/media/" + key
	}
	return "/data/media/" + parts[1]
}

func (p *localChannelMemoryProvider) OpenContainerFile(ctx context.Context, botID, containerPath string) (io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	data, ok := p.containerFiles[botID+":"+containerPath]
	if !ok {
		return nil, io.EOF
	}
	_ = ctx
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (p *localChannelMemoryProvider) SeedContainerFile(botID, containerPath string, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.containerFiles[botID+":"+containerPath] = append([]byte(nil), data...)
}

var (
	_ storage.Provider            = (*localChannelMemoryProvider)(nil)
	_ storage.ContainerFileOpener = (*localChannelMemoryProvider)(nil)
)

func newLocalChannelHandlerWithMedia() (*LocalChannelHandler, *localChannelMemoryProvider) {
	provider := &localChannelMemoryProvider{
		objects:        make(map[string][]byte),
		containerFiles: make(map[string][]byte),
	}
	handler := &LocalChannelHandler{logger: slog.Default()}
	handler.SetMediaService(asset.NewService(slog.Default(), provider))
	return handler, provider
}

func TestExtractAssetRefsFromProcessedEvent_CarriesToolCallID(t *testing.T) {
	t.Parallel()

	event, err := json.Marshal(map[string]any{
		"type":       "attachment_delta",
		"toolCallId": "call-42",
		"attachments": []any{
			map[string]any{"type": "image", "content_hash": "asset-1", "mime": "image/png"},
		},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	refs := extractAssetRefsFromProcessedEvent(event)
	if len(refs) != 1 {
		t.Fatalf("expected 1 asset ref, got %d", len(refs))
	}
	if got, _ := refs[0].Metadata["tool_call_id"].(string); got != "call-42" {
		t.Fatalf("tool_call_id metadata = %q, want call-42", got)
	}
}
