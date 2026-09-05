package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	tele "gopkg.in/telebot.v4"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/i18n"
)

type submissionStore struct {
	askUserInteractionService
	status string
}

func (s *submissionStore) Get(context.Context, string) (userinput.Request, error) {
	return userinput.Request{Status: s.status}, nil
}

func TestAskUserSummaryRequiresDurableAcceptance(t *testing.T) {
	for _, state := range []string{"handler-error", "accepted-output-error", userinput.StatusPending, userinput.StatusSubmitted} {
		t.Run(state, func(t *testing.T) {
			var edits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				edits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":123,"type":"private"}}}`))
			}))
			defer server.Close()
			bot, err := tele.NewBot(tele.Settings{Token: "test", URL: server.URL, Offline: true})
			if err != nil {
				t.Fatal(err)
			}
			storedStatus := state
			if state == "accepted-output-error" {
				storedStatus = userinput.StatusSubmitted
			}
			adapter := &TelegramAdapter{userInput: &submissionStore{status: storedStatus}}
			req := userinput.Request{ID: "question-1", UIPayload: userinput.UIPayload{Questions: []userinput.UIQuestion{{ID: "q1", Text: "End time?"}}}, Interaction: userinput.TextInteractionState{Completed: true, Answers: []userinput.QuestionAnswer{{QuestionID: "q1", CustomText: "0点"}}}}
			handler := func(context.Context, channel.ChannelConfig, channel.InboundMessage) error {
				if edits.Load() != 0 {
					t.Fatal("card edited before submission")
				}
				if state == "handler-error" || state == "accepted-output-error" {
					return errors.New("rejected")
				}
				return nil
			}
			err = adapter.finishAskUserSubmission(context.Background(), channel.ChannelConfig{}, handler, bot, i18n.New("en"), req, channel.InboundMessage{}, 123, 1)
			if (err != nil) != (state == "handler-error" || state == "accepted-output-error") {
				t.Fatalf("result: %v", err)
			}
			if (edits.Load() == 1) != (storedStatus == userinput.StatusSubmitted) {
				t.Fatalf("edits = %d", edits.Load())
			}
		})
	}
}
