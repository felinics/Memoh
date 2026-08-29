package handlers

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type fakeFSWatchSubscriptions struct {
	mu      sync.Mutex
	sets    []fakeFSWatchSet
	dropped []string
	notify  chan struct{}
}

type fakeFSWatchSet struct {
	subID string
	botID string
	dirs  []string
}

func newFakeFSWatchSubscriptions() *fakeFSWatchSubscriptions {
	return &fakeFSWatchSubscriptions{notify: make(chan struct{}, 16)}
}

func (f *fakeFSWatchSubscriptions) SetSubscription(subID, botID string, dirs []string) {
	f.mu.Lock()
	f.sets = append(f.sets, fakeFSWatchSet{subID: subID, botID: botID, dirs: append([]string(nil), dirs...)})
	f.mu.Unlock()
	f.notify <- struct{}{}
}

func (f *fakeFSWatchSubscriptions) DropSubscription(subID string) {
	f.mu.Lock()
	f.dropped = append(f.dropped, subID)
	f.mu.Unlock()
	f.notify <- struct{}{}
}

func (f *fakeFSWatchSubscriptions) wait(t *testing.T) {
	t.Helper()
	select {
	case <-f.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch subscription call")
	}
}

func TestLocalChannelWSFSWatchRoutesSanitizedDirs(t *testing.T) {
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
	watches := newFakeFSWatchSubscriptions()
	handler := &LocalChannelHandler{
		channelType:    channel.ChannelTypeLocal,
		botService:     bots.NewService(nil, queries),
		accountService: accounts.NewService(nil, testAdminAccountStore{role: "user"}),
		agentService:   &application.Service{},
		logger:         slog.Default(),
	}
	handler.SetFSWatchSubscriptions(watches)

	client := openLocalChannelTestWS(t, handler, botID, currentUser)

	if err := client.WriteJSON(map[string]any{
		"type": "fs_watch",
		"dirs": []string{"/data", "/data/sub", "../../etc", "  "},
	}); err != nil {
		t.Fatalf("write fs_watch: %v", err)
	}
	watches.wait(t)

	watches.mu.Lock()
	if len(watches.sets) != 1 {
		watches.mu.Unlock()
		t.Fatalf("sets = %d, want 1", len(watches.sets))
	}
	set := watches.sets[0]
	watches.mu.Unlock()
	if set.botID != botID {
		t.Fatalf("bot = %q", set.botID)
	}
	if set.subID == "" {
		t.Fatal("sub id is empty")
	}
	if len(set.dirs) != 2 || set.dirs[0] != "/data" || set.dirs[1] != "/data/sub" {
		t.Fatalf("dirs = %v, want [/data /data/sub]", set.dirs)
	}

	_ = client.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	_ = client.Close()
	watches.wait(t)

	watches.mu.Lock()
	defer watches.mu.Unlock()
	if len(watches.dropped) != 1 || watches.dropped[0] != set.subID {
		t.Fatalf("dropped = %v, want [%s]", watches.dropped, set.subID)
	}
}
