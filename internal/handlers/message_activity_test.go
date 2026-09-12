package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/bots"
	messageevent "github.com/felinics/memoh/internal/chat/event"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const (
	activityTestBotID     = "11111111-1111-1111-1111-111111111111"
	activityTestSessionID = "22222222-2222-2222-2222-222222222222"
)

func activityTestHandler() (*MessageHandler, *messageevent.Hub) {
	hub := messageevent.NewHub()
	queries := &sessionDeleteQueries{
		bot: testBotRow(activityTestBotID, nil),
		session: sqlc.BotSession{
			ID: testUUID(activityTestSessionID), BotID: testUUID(activityTestBotID), Type: session.TypeChat,
		},
	}
	return NewMessageHandler(slog.Default(), nil, session.NewService(nil, queries, hub),
		bots.NewService(nil, queries), newTestAdminAccountService("admin"), hub), hub
}

// Queue an admission racing connection setup, after Subscribe but before the
// handler sends its ready frame. It must still reach the client afterward.
type admissionOnSubscribe struct{ hub *messageevent.Hub }

func (s admissionOnSubscribe) Subscribe(botID string, buffer int) (*messageevent.Subscription, func()) {
	sub, cancel := s.hub.Subscribe(botID, buffer)
	messageevent.InvalidateSession(s.hub, botID, activityTestSessionID)
	return sub, cancel
}

type activityResponseRecorder struct {
	*httptest.ResponseRecorder
	flushes int
	cancel  context.CancelFunc
}

func (r *activityResponseRecorder) Flush() {
	r.ResponseRecorder.Flush()
	r.flushes++
	if r.flushes == 2 {
		r.cancel()
	}
}

func TestSessionActivitySendsReadyThenQueuedInvalidation(t *testing.T) {
	for _, supported := range []bool{false, true} {
		h, hub := activityTestHandler()
		h.SetSessionActivityInvalidationSupported(supported)
		h.messageEvents = admissionOnSubscribe{hub}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		rec := &activityResponseRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
		req := httptest.NewRequest(http.MethodGet, "/bots/"+activityTestBotID+"/sessions/events", nil).WithContext(ctx)
		c := testAuthContext(echo.New(), req, rec, "user-1")
		c.SetParamNames("bot_id")
		c.SetParamValues(activityTestBotID)
		if err := h.StreamSessionsActivityEvents(c); err != nil {
			t.Fatal(err)
		}
		frames := strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n")
		if len(frames) != 2 {
			t.Fatalf("expected ready and invalidation, got %q", rec.Body.String())
		}
		for i, frame := range frames {
			var payload map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "data: ")), &payload); err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				if payload["type"] != "activity_ready" || payload["cache_invalidation"] != supported || payload["session_id"] != "" {
					t.Fatalf("unexpected ready frame: %+v", payload)
				}
			} else if payload["type"] != "session_invalidated" || payload["session_id"] != activityTestSessionID {
				t.Fatalf("queued admission invalidation missing: %+v", payload)
			}
		}
	}
}

type activityHistoryStore struct {
	messagepkg.Service
	deleteErr error
}

func (s activityHistoryStore) DeleteBySession(context.Context, string) error { return s.deleteErr }
func (s activityHistoryStore) DeleteByBot(context.Context, string) error     { return s.deleteErr }

type activityHistoryReset struct{ recordingACPSessionCloser }

func (r *activityHistoryReset) BeginBotHistoryReset(ctx context.Context, botID string) (context.Context, func(), error) {
	return r.BeginSessionHistoryReset(ctx, botID, "")
}

func TestDeleteMessagesInvalidatesOnlyCommittedHistory(t *testing.T) {
	for _, sessionID := range []string{activityTestSessionID, ""} {
		for _, failure := range []bool{false, true} {
			h, hub := activityTestHandler()
			sub, cancel := hub.Subscribe(activityTestBotID, 4)
			defer cancel()
			store := activityHistoryStore{}
			if failure {
				store.deleteErr = errors.New("delete failed")
			}
			h.messageService = store
			h.SetRuntimeResetService(&activityHistoryReset{})
			req := httptest.NewRequest(http.MethodDelete, "/bots/"+activityTestBotID+"/messages?session_id="+sessionID, nil)
			rec := httptest.NewRecorder()
			c := testAuthContext(echo.New(), req, rec, "user-1")
			c.SetParamNames("bot_id")
			c.SetParamValues(activityTestBotID)
			err := h.DeleteMessages(c)
			if (err != nil) != failure {
				t.Fatalf("unexpected delete outcome: %v", err)
			}
			if !failure && rec.Code != http.StatusNoContent {
				t.Fatalf("delete status = %d", rec.Code)
			}
			select {
			case ev := <-sub.Events:
				var payload messageevent.SessionInvalidation
				if err := json.Unmarshal(ev.Data, &payload); err != nil {
					t.Fatal(err)
				}
				if failure || ev.Type != messageevent.EventTypeSessionInvalidated || payload.SessionID != sessionID {
					t.Fatalf("unexpected invalidation after delete: %+v", ev)
				}
			default:
				if !failure {
					t.Fatal("cleared history emitted no invalidation")
				}
			}
		}
	}
}
