package telegram

import (
	"strings"
	"testing"

	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/channel"
)

func TestTelegramInlineKeyboard_CallbackButton(t *testing.T) {
	t.Parallel()
	mk := telegramInlineKeyboard([]channel.Action{{Label: "Approve", Value: "approve:42"}})
	btn := firstButton(t, mk)
	if btn.Text != "Approve" || btn.Data != "approve:42" {
		t.Fatalf("expected callback button, got %+v", btn)
	}
	if btn.URL != "" {
		t.Fatalf("expected no URL on callback button, got %q", btn.URL)
	}
}

func TestTelegramInlineKeyboard_URLButton(t *testing.T) {
	t.Parallel()
	mk := telegramInlineKeyboard([]channel.Action{{Label: "Read more", URL: "https://example.com/article"}})
	btn := firstButton(t, mk)
	if btn.Text != "Read more" || btn.URL != "https://example.com/article" {
		t.Fatalf("expected URL button pointing at example.com, got %+v", btn)
	}
	if btn.Data != "" {
		t.Fatalf("expected no callback data on URL button, got %q", btn.Data)
	}
}

func TestTelegramInlineKeyboard_ValuePrecedesURL(t *testing.T) {
	t.Parallel()

	mk := telegramInlineKeyboard([]channel.Action{{Label: "Open", URL: "https://example.com/x", Value: "fallback"}})
	btn := firstButton(t, mk)
	if btn.Data != "fallback" {
		t.Fatalf("expected callback value to win, got %+v", btn)
	}
	if btn.URL != "" {
		t.Fatalf("expected URL to be dropped when callback value is present, got %q", btn.URL)
	}
}

func TestTelegramInlineKeyboard_DropsOversizedCallbackData(t *testing.T) {
	t.Parallel()

	mk := telegramInlineKeyboard([]channel.Action{{Label: "Too large", Value: strings.Repeat("x", telegramMaxCallbackDataBytes+1)}})
	if len(mk.InlineKeyboard) != 0 {
		t.Fatalf("expected oversized callback data to be dropped, got %+v", mk.InlineKeyboard)
	}
}

func TestTelegramInlineKeyboard_SortsRowsNumerically(t *testing.T) {
	t.Parallel()

	mk := telegramInlineKeyboard([]channel.Action{
		{Label: "Row 2", Value: "r2", Row: 2},
		{Label: "Row 1", Value: "r1", Row: 1},
	})
	if len(mk.InlineKeyboard) != 2 {
		t.Fatalf("expected two rows, got %+v", mk.InlineKeyboard)
	}
	if mk.InlineKeyboard[0][0].Text != "Row 1" || mk.InlineKeyboard[1][0].Text != "Row 2" {
		t.Fatalf("rows not sorted numerically: %+v", mk.InlineKeyboard)
	}
}

func TestTelegramInlineKeyboard_RejectsUnsafeURL(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"javascript:alert(1)", "data:text/html,xx", "mailto:a@example.com", "tel:+123", "#anchor", "tg://resolve?domain=x"} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			mk := telegramInlineKeyboard([]channel.Action{{Label: "x", URL: raw}})
			if len(mk.InlineKeyboard) != 0 {
				t.Fatalf("expected empty keyboard for %q, got %+v", raw, mk.InlineKeyboard)
			}
		})
	}
}

func TestTelegramInlineKeyboard_DropsActionWithoutTarget(t *testing.T) {
	t.Parallel()
	mk := telegramInlineKeyboard([]channel.Action{{Label: "Empty"}})
	if len(mk.InlineKeyboard) != 0 {
		t.Fatalf("expected empty keyboard when no URL or Value, got %+v", mk.InlineKeyboard)
	}
}

func firstButton(t *testing.T, mk *tele.ReplyMarkup) tele.InlineButton {
	t.Helper()
	if mk == nil || len(mk.InlineKeyboard) == 0 || len(mk.InlineKeyboard[0]) == 0 {
		t.Fatalf("expected at least one row+button, got %+v", mk)
	}
	return mk.InlineKeyboard[0][0]
}
