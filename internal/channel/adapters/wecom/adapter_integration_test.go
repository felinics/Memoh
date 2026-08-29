package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/felinics/memoh/internal/channel"
)

func TestWeComAdapter_ReplyUsesRespondCmd(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{}
	receivedRespond := make(chan WSFrame, 1)
	serverErr := make(chan error, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			select {
			case serverErr <- err:
			default:
			}
			return
		}
		defer func() { _ = conn.Close() }()

		var subscribeFrame WSFrame
		if err := conn.ReadJSON(&subscribeFrame); err != nil {
			select {
			case serverErr <- err:
			default:
			}
			return
		}
		if err := conn.WriteJSON(WSFrame{
			Headers: WSHeaders{ReqID: subscribeFrame.Headers.ReqID},
			ErrCode: 0,
		}); err != nil {
			select {
			case serverErr <- err:
			default:
			}
			return
		}

		body, _ := json.Marshal(MessageCallbackBody{
			MsgID:       "msg_1",
			ChatID:      "chat_1",
			ChatType:    "group",
			CreateTime:  time.Now().UnixMilli(),
			From:        CallbackFrom{UserID: "u1"},
			MsgType:     "text",
			ResponseURL: "https://example.com/resp",
			Text:        &MessageText{Content: "hello"},
		})
		if err := conn.WriteJSON(WSFrame{
			Cmd:     WSCmdMsgCallback,
			Headers: WSHeaders{ReqID: "callback_req_id"},
			Body:    body,
		}); err != nil {
			select {
			case serverErr <- err:
			default:
			}
			return
		}

		var respondFrame WSFrame
		if err := conn.ReadJSON(&respondFrame); err != nil {
			select {
			case serverErr <- err:
			default:
			}
			return
		}
		select {
		case receivedRespond <- respondFrame:
		default:
		}
		_ = conn.WriteJSON(WSFrame{
			Headers: WSHeaders{ReqID: respondFrame.Headers.ReqID},
			ErrCode: 0,
		})
	}))
	defer server.Close()

	adapter := NewWeComAdapter(nil)
	cfg := channel.ChannelConfig{
		ID: "config-a",
		Credentials: map[string]any{
			"botId":  "bot",
			"secret": "sec",
			"wsUrl":  "ws" + strings.TrimPrefix(server.URL, "http"),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inboundCh := make(chan channel.InboundMessage, 1)
	conn, err := adapter.Connect(ctx, cfg, func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) error {
		select {
		case inboundCh <- msg:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("connect error: %v", err)
	}
	defer func() { _ = conn.Stop(context.Background()) }()

	select {
	case inbound := <-inboundCh:
		if inbound.Message.ID != "msg_1" {
			t.Fatalf("unexpected inbound message id: %s", inbound.Message.ID)
		}
	case err := <-serverErr:
		t.Fatalf("server error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting inbound callback")
	}

	err = adapter.Send(context.Background(), cfg, channel.PreparedOutboundMessage{
		Target: "chat_id:chat_1",
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Format: channel.MessageFormatPlain,
				Text:   "reply content",
				Reply: &channel.ReplyRef{
					MessageID: "msg_1",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("send error: %v", err)
	}

	select {
	case frame := <-receivedRespond:
		if frame.Cmd != WSCmdRespond {
			t.Fatalf("expected cmd=%s got=%s", WSCmdRespond, frame.Cmd)
		}
		if frame.Headers.ReqID != "callback_req_id" {
			t.Fatalf("expected req_id callback_req_id got=%s", frame.Headers.ReqID)
		}
	case err := <-serverErr:
		t.Fatalf("server error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting respond frame")
	}
}
