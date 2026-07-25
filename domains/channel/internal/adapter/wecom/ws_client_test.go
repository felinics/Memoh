package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSClientRun_ReconnectsAfterDisconnect(t *testing.T) {
	t.Parallel()

	var connCount atomic.Int32
	callbackSent := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		n := connCount.Add(1)

		var subscribeFrame WSFrame
		if err := conn.ReadJSON(&subscribeFrame); err != nil {
			return
		}
		_ = conn.WriteJSON(WSFrame{
			Headers: WSHeaders{ReqID: subscribeFrame.Headers.ReqID},
			ErrCode: 0,
		})

		if n == 1 {
			return
		}
		body, _ := json.Marshal(MessageCallbackBody{
			MsgID:      "m1",
			ChatID:     "chat_1",
			ChatType:   "group",
			CreateTime: time.Now().UnixMilli(),
			From:       CallbackFrom{UserID: "u1"},
			MsgType:    "text",
			Text:       &MessageText{Content: "hello"},
		})
		_ = conn.WriteJSON(WSFrame{
			Cmd:     WSCmdMsgCallback,
			Headers: WSHeaders{ReqID: "cb_req"},
			Body:    body,
		})
		select {
		case callbackSent <- struct{}{}:
		default:
		}
		<-time.After(100 * time.Millisecond)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client := NewWSClient(WSClientOptions{
		URL:                  wsURL,
		AckTimeout:           200 * time.Millisecond,
		HeartbeatInterval:    10 * time.Second,
		ReconnectBaseDelay:   10 * time.Millisecond,
		ReconnectMaxDelay:    20 * time.Millisecond,
		MaxReconnectAttempts: 5,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- client.Run(ctx, AuthCredentials{BotID: "bot", Credential: "sec"}, func(context.Context, WSFrame) error {
			cancel()
			return nil
		})
	}()

	select {
	case <-callbackSent:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting callback on reconnected session")
	}
	select {
	case err := <-runErrCh:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting run return")
	}
	if connCount.Load() < 2 {
		t.Fatalf("expected reconnect attempts >= 2, got %d", connCount.Load())
	}
}

func TestWSClientRun_HeartbeatDoesNotRequireAck(t *testing.T) {
	t.Parallel()

	var connCount atomic.Int32
	heartbeatSeen := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		n := connCount.Add(1)

		var subscribeFrame WSFrame
		if err := conn.ReadJSON(&subscribeFrame); err != nil {
			return
		}
		_ = conn.WriteJSON(WSFrame{
			Headers: WSHeaders{ReqID: subscribeFrame.Headers.ReqID},
			ErrCode: 0,
		})
		if n > 1 {
			return
		}
		var heartbeatFrame WSFrame
		if err := conn.ReadJSON(&heartbeatFrame); err != nil {
			return
		}
		if heartbeatFrame.Cmd == WSCmdHeartbeat {
			select {
			case heartbeatSeen <- struct{}{}:
			default:
			}
		}
		<-time.After(200 * time.Millisecond)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client := NewWSClient(WSClientOptions{
		URL:                  wsURL,
		HeartbeatInterval:    20 * time.Millisecond,
		ReconnectBaseDelay:   10 * time.Millisecond,
		ReconnectMaxDelay:    20 * time.Millisecond,
		MaxReconnectAttempts: 5,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- client.Run(ctx, AuthCredentials{BotID: "bot", Credential: "sec"}, nil)
	}()

	select {
	case <-heartbeatSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting heartbeat frame")
	}
	<-time.After(250 * time.Millisecond)
	cancel()
	select {
	case err := <-runErrCh:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting run return")
	}
	if connCount.Load() < 1 {
		t.Fatalf("expected at least one session, got %d", connCount.Load())
	}
}
