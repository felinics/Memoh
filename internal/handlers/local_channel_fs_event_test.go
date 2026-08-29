package handlers

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/fsevent"
)

func TestLocalChannelWSForwardsFSChangedEvents(t *testing.T) {
	t.Parallel()

	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		currentUser = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	queries := localChannelSessionAuthQueries{
		bot: testBotRow(botID, map[string]any{}),
		grants: []sqlc.ListBotUserGrantsForUserRow{
			{
				ID:          testUUID("dddddddd-dddd-dddd-dddd-dddddddddddd"),
				BotID:       testUUID(botID),
				SubjectType: bots.GrantSubjectUser,
				UserID:      testUUID(currentUser),
				Permissions: []byte(`["workspace_exec"]`),
			},
		},
	}
	hub := fsevent.NewHub(10 * time.Millisecond)
	handler := &LocalChannelHandler{
		channelType:    channel.ChannelTypeLocal,
		botService:     bots.NewService(nil, queries),
		accountService: accounts.NewService(nil, testAdminAccountStore{role: "user"}),
		agentService:   &application.Service{},
		logger:         slog.Default(),
	}
	handler.SetFSEventHub(hub)

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

	// The hub subscription is registered synchronously during the upgrade
	// handshake, so a publish right after a successful dial must reach the
	// client.
	hub.Publish(botID, []string{"/data/report.md"})

	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event struct {
		Type  string   `json:"type"`
		Paths []string `json:"paths"`
	}
	if err := client.ReadJSON(&event); err != nil {
		t.Fatalf("read ws event: %v", err)
	}
	if event.Type != "fs_changed" {
		t.Fatalf("event type = %q, want fs_changed", event.Type)
	}
	if len(event.Paths) != 1 || event.Paths[0] != "/data/report.md" {
		t.Fatalf("event paths = %v, want [/data/report.md]", event.Paths)
	}

	// A wildcard delivery serializes paths as null.
	hub.Publish(botID, nil)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var wildcard struct {
		Type  string    `json:"type"`
		Paths *[]string `json:"paths"`
	}
	if err := client.ReadJSON(&wildcard); err != nil {
		t.Fatalf("read wildcard event: %v", err)
	}
	if wildcard.Type != "fs_changed" {
		t.Fatalf("wildcard type = %q, want fs_changed", wildcard.Type)
	}
	if wildcard.Paths != nil && *wildcard.Paths != nil {
		t.Fatalf("wildcard paths = %v, want null", *wildcard.Paths)
	}
}
