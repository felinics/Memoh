package telegram

import (
	"encoding/json"
	"net/http"
	"testing"

	tele "gopkg.in/telebot.v4"
)

type telegramRoundTripFunc func(*http.Request) (*http.Response, error)

func (f telegramRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestTelegramBot(transport http.RoundTripper) *tele.Bot {
	bot, err := tele.NewBot(tele.Settings{
		Token:   "fake",
		URL:     "https://api.telegram.test",
		Client:  &http.Client{Transport: transport},
		Offline: true,
	})
	if err != nil {
		panic(err)
	}
	return bot
}

// decodeTelegramBody parses a JSON request body sent by telebot's Raw() API
// into a map for field-level assertions.
func decodeTelegramBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	payload := map[string]any{}
	if len(body) == 0 {
		return payload
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("parse body: %v (raw=%q)", err, string(body))
	}
	return payload
}

// telegramRichHTMLFromBody decodes a JSON request body sent by telebot's Raw()
// API and returns the rich_message.html field for assertions.
func telegramRichHTMLFromBody(t *testing.T, body map[string]any) string {
	t.Helper()
	rich, ok := body["rich_message"].(map[string]any)
	if !ok {
		t.Fatalf("expected rich_message object in body: %#v", body)
	}
	html, _ := rich["html"].(string)
	if html == "" {
		t.Fatalf("expected rich_message.html, got %#v", rich)
	}
	return html
}

// telegramRichMarkdownFromBody decodes a JSON request body sent by telebot's
// Raw() API and returns the rich_message.markdown field for assertions.
func telegramRichMarkdownFromBody(t *testing.T, body map[string]any) string {
	t.Helper()
	rich, ok := body["rich_message"].(map[string]any)
	if !ok {
		t.Fatalf("expected rich_message object in body: %#v", body)
	}
	markdown, _ := rich["markdown"].(string)
	if markdown == "" {
		t.Fatalf("expected rich_message.markdown, got %#v", rich)
	}
	return markdown
}
