package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	tele "gopkg.in/telebot.v4"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/i18n"
)

type guardedInteractionStore struct {
	askUserInteractionService
	calls   int
	input   userinput.AdvanceInteractionInput
	request userinput.Request
}

func (s *guardedInteractionStore) AdvanceInteraction(_ context.Context, in userinput.AdvanceInteractionInput) (userinput.AdvanceInteractionResult, error) {
	s.calls++
	s.input = in
	return userinput.AdvanceInteractionResult{}, errors.New("stop after authorization")
}

func (s *guardedInteractionStore) Get(context.Context, string) (userinput.Request, error) {
	return s.request, nil
}

func TestAskUserAuthorizesBeforeDraftMutation(t *testing.T) {
	for _, path := range []string{"button", "text"} {
		for _, authorization := range []string{"allow", "deny", "error", "missing"} {
			t.Run(path+"/"+authorization, func(t *testing.T) {
				store := &guardedInteractionStore{}
				adapter := &TelegramAdapter{userInput: store}
				if authorization != "missing" {
					adapter.SetUserInputAuthorizer(func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) (bool, error) {
						if msg.Sender.SubjectID != "42" {
							t.Fatalf("wrong actor: %#v", msg.Sender)
						}
						if authorization == "error" {
							return false, errors.New("ACL unavailable")
						}
						return authorization == "allow", nil
					})
				}
				bot := newTestTelegramBot(telegramRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
				}))
				raw := &tele.Message{ID: 5, Text: "0点", Sender: &tele.User{ID: 42}, Chat: &tele.Chat{ID: 123, Type: tele.ChatPrivate}, ReplyTo: &tele.Message{ID: 4}}
				update := &tele.Update{ID: 1, Message: raw}
				if path == "button" {
					update.Callback = &tele.Callback{ID: "cb", Sender: &tele.User{ID: 42}, Message: raw, Data: encodeAskUserCallback("s", testAskUserRequestID, "en", 0, 0)}
					adapter.handleAskUserWizardCallback(context.Background(), channel.ChannelConfig{BotID: "bot"}, nil, bot, update)
				} else {
					adapter.askUserPromptStore().put(123, 4, askUserTextPrompt{RequestID: testAskUserRequestID, QuestionID: "q1", Locale: "en"})
					adapter.tryHandleAskUserTextReply(context.Background(), channel.ChannelConfig{BotID: "bot"}, nil, bot, update)
					if authorization != "allow" {
						if _, ok := adapter.askUserPromptStore().take(123, 4); !ok {
							t.Fatal("denied sender consumed the legitimate reply prompt")
						}
					}
				}
				if (store.calls == 1) != (authorization == "allow") {
					t.Fatalf("mutations=%d", store.calls)
				}
				if raw.Text != "0点" {
					t.Fatal("authorization overwrote the user's text")
				}
				if path == "text" && authorization == "allow" && store.input.Op.Text != "0点" {
					t.Fatal("original answer was lost")
				}
			})
		}
	}
}

func TestTypedAnswerUpdatesOriginalTelegramCard(t *testing.T) {
	for _, state := range []string{"pending", "submitted", "draft", "canceled"} {
		t.Run(state, func(t *testing.T) {
			req := userinput.Request{
				ID: testAskUserRequestID, BotID: "bot", SourcePlatform: "telegram", ReplyTarget: "123", PromptExternalMessageID: "5", Status: state,
				UIPayload:   userinput.UIPayload{Questions: []userinput.UIQuestion{{ID: "q1", Text: "First?", Kind: userinput.QuestionKindText}, {ID: "q2", Text: "Second?", Kind: userinput.QuestionKindText}}},
				Interaction: userinput.TextInteractionState{QuestionIndex: 1, Answers: []userinput.QuestionAnswer{{QuestionID: "q1", Text: "0点"}}},
			}
			if state == "draft" {
				req.Status = userinput.StatusPending
				req.Interaction.Completed = true
			}
			var body string
			bot := newTestTelegramBot(telegramRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				data, _ := io.ReadAll(r.Body)
				body = string(data)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":5,"chat":{"id":123,"type":"private"}}}`))}, nil
			}))
			cfg := channel.ChannelConfig{ID: "config", BotID: "bot", Credentials: map[string]any{"bot_token": "test"}}
			adapter := &TelegramAdapter{userInput: &guardedInteractionStore{request: req}, bots: map[string]*tele.Bot{telegramBotCacheKey(Config{BotToken: "test"}, cfg.ID): bot}}
			updated, err := adapter.UpdateUserInputCard(context.Background(), cfg, req.ID, i18n.New("en"))
			if err != nil {
				t.Fatal(err)
			}
			if updated != (state == "pending" || state == "submitted") {
				t.Fatalf("updated=%v, state=%s", updated, state)
			}
			if updated && (!strings.Contains(body, `"message_id":"5"`) || !strings.Contains(body, `"chat_id":"123"`)) {
				t.Fatalf("wrong card: %s", body)
			}
			if state == "pending" && (!strings.Contains(body, "Second?") || !strings.Contains(body, "aui~")) {
				t.Fatalf("stale keyboard: %s", body)
			}
			if state == "submitted" && (strings.Contains(body, "aui~") || !strings.Contains(body, "0点")) {
				t.Fatalf("submitted card: %s", body)
			}
		})
	}
}
