package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/logger"
)

var testWSHeartbeat = wsHeartbeat{
	pingInterval: 20 * time.Millisecond,
	readTimeout:  150 * time.Millisecond,
	writeTimeout: 100 * time.Millisecond,
}

func heartbeatTestHandler(logs *lockedBuffer) (*LocalChannelHandler, string, string) {
	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		currentUser = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	queries := localChannelSessionAuthQueries{
		bot: testBotRow(botID, map[string]any{}),
		grants: []sqlc.ListBotUserGrantsForUserRow{{
			ID:          testUUID("dddddddd-dddd-dddd-dddd-dddddddddddd"),
			BotID:       testUUID(botID),
			SubjectType: bots.GrantSubjectUser,
			UserID:      testUUID(currentUser),
			Permissions: []byte(`["workspace_exec"]`),
		}},
	}
	return &LocalChannelHandler{
		channelType:    channel.ChannelTypeLocal,
		botService:     bots.NewService(nil, queries),
		accountService: accounts.NewService(nil, testAdminAccountStore{role: "user"}),
		agentService:   &application.Service{},
		logger:         logger.New(logs, "info", "json"),
		wsHeartbeat:    testWSHeartbeat,
	}, botID, currentUser
}

// The ping is what keeps an idle chat tab's connection open through a proxy
// with an idle timeout, so it has to arrive on a connection that is doing
// nothing else.
func TestChatWSPingsAnIdleClient(t *testing.T) {
	t.Parallel()
	var logs lockedBuffer
	handler, botID, user := heartbeatTestHandler(&logs)
	client := openLocalChannelTestWS(t, handler, botID, user)

	pinged := make(chan struct{}, 1)
	defaultPing := client.PingHandler()
	client.SetPingHandler(func(data string) error {
		select {
		case pinged <- struct{}{}:
		default:
		}
		return defaultPing(data)
	})
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	select {
	case <-pinged:
	case <-time.After(10 * testWSHeartbeat.pingInterval):
		t.Fatal("no ping within ten intervals")
	}
}

// A peer that vanished without closing, such as a laptop that went to sleep,
// sends nothing and answers nothing. Without a read deadline the server holds
// the connection and its subscriptions until the TCP stack gives up, which
// can be hours.
func TestChatWSDisconnectsASilentClient(t *testing.T) {
	t.Parallel()
	var logs lockedBuffer
	handler, botID, user := heartbeatTestHandler(&logs)
	client := openLocalChannelTestWS(t, handler, botID, user)
	client.SetPingHandler(func(string) error { return nil })

	start := time.Now()
	// The client's own deadline is the failure case: the server did not end
	// the connection in twenty read timeouts.
	_ = client.SetReadDeadline(start.Add(20 * testWSHeartbeat.readTimeout))
	for {
		_, _, err := client.ReadMessage()
		if errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal("the server kept a silent client connected")
		}
		if err != nil {
			break
		}
	}
	// The server armed its deadline before start was taken, so only a
	// disconnect well short of it is wrong; the logged reason says why it
	// happened.
	if elapsed := time.Since(start); elapsed < testWSHeartbeat.readTimeout/2 {
		t.Errorf("disconnected after %v, long before the %v read timeout", elapsed, testWSHeartbeat.readTimeout)
	}
	assertDisconnectLog(t, &logs, map[string]any{"reason": "heartbeat_timeout"})
}

// Answering pings is enough to stay connected, and so is sending messages:
// a client whose pongs are delayed behind a busy socket must not be cut off
// while it is plainly talking.
func TestChatWSKeepsAClientThatShowsSignsOfLife(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		pongs bool
		sends bool
	}{
		{name: "answers pings", pongs: true},
		{name: "sends messages", sends: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs lockedBuffer
			handler, botID, user := heartbeatTestHandler(&logs)
			client := openLocalChannelTestWS(t, handler, botID, user)
			if !tc.pongs {
				client.SetPingHandler(func(string) error { return nil })
			}
			replies := make(chan string, 64)
			readErr := make(chan error, 1)
			go func() {
				for {
					_, raw, err := client.ReadMessage()
					if err != nil {
						readErr <- err
						return
					}
					replies <- string(raw)
				}
			}()

			until := time.Now().Add(4 * testWSHeartbeat.readTimeout)
			for time.Now().Before(until) {
				if tc.sends {
					if err := client.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
						t.Fatalf("write: %v", err)
					}
				}
				select {
				case err := <-readErr:
					t.Fatalf("connection ended: %v", err)
				case <-time.After(testWSHeartbeat.readTimeout / 3):
				}
			}

			// Still served, not merely still open.
			if err := client.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
				t.Fatalf("write: %v", err)
			}
			deadline := time.After(testWSHeartbeat.readTimeout)
			for {
				select {
				case reply := <-replies:
					if strings.Contains(reply, "invalid message format") {
						return
					}
				case err := <-readErr:
					t.Fatalf("connection ended: %v", err)
				case <-deadline:
					t.Fatal("no reply to the final message")
				}
			}
		})
	}
}

// A client that closes the connection is the ordinary end of a chat session,
// and the log says so with the close code rather than the peer's text.
func TestChatWSLogsAClientCloseWithItsCode(t *testing.T) {
	t.Parallel()
	var logs lockedBuffer
	handler, botID, user := heartbeatTestHandler(&logs)
	client := openLocalChannelTestWS(t, handler, botID, user)

	msg := websocket.FormatCloseMessage(websocket.CloseGoingAway, "synthetic-peer-text")
	if err := client.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	line := assertDisconnectLog(t, &logs, map[string]any{"reason": "closed", "close_code": float64(websocket.CloseGoingAway)})
	if strings.Contains(line, "synthetic-peer-text") {
		t.Errorf("the disconnect log carries the peer's close text: %s", line)
	}
}

func assertDisconnectLog(t *testing.T, logs *lockedBuffer, want map[string]any) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		line := lineContaining(logs.String(), `"msg":"ws disconnected; active stream can finish in background"`)
		if line != "" {
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["level"] != "INFO" {
				t.Errorf("level = %v, want INFO", entry["level"])
			}
			if entry["bot_id"] == nil {
				t.Error("the disconnect log lost bot_id")
			}
			if _, ok := entry["error"]; ok {
				t.Errorf("the disconnect log carries the raw error: %s", line)
			}
			for key, value := range want {
				if entry[key] != value {
					t.Errorf("%s = %v, want %v", key, entry[key], value)
				}
			}
			return line
		}
		if time.Now().After(deadline) {
			t.Fatalf("no disconnect log; got:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// wsPair is a connected server and client, without the chat handler.
func wsPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- conn
	}))
	t.Cleanup(srv.Close)
	client, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	t.Cleanup(func() { _ = client.Close() })
	server = <-accepted
	t.Cleanup(func() { _ = server.Close() })
	return server, client
}

// The handler closes the connection after Stop returns. A ping goroutine
// still running then would write to a closed connection, and one that never
// exits would outlive every chat session.
func TestHeartbeatStopWaitsForThePingGoroutine(t *testing.T) {
	t.Parallel()
	server, _ := wsPair(t)
	keepalive := testWSHeartbeat.start(server)
	keepalive.Stop()
	select {
	case <-keepalive.done:
	default:
		t.Fatal("Stop returned while the ping goroutine was still running")
	}
	keepalive.Stop() // a second Stop must not panic or block
}

// A broken connection ends the ping goroutine by itself; the read loop's
// deadline ends the handler.
func TestHeartbeatStopsPingingABrokenConnection(t *testing.T) {
	t.Parallel()
	server, _ := wsPair(t)
	keepalive := testWSHeartbeat.start(server)
	defer keepalive.Stop()
	_ = server.UnderlyingConn().Close()
	select {
	case <-keepalive.done:
	case <-time.After(10 * testWSHeartbeat.pingInterval):
		t.Fatal("the ping goroutine kept running on a closed connection")
	}
}

func lineContaining(logs, substr string) string {
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}
