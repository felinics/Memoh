package weixin

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPollQRStatusNormalizesLegacyScannedStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/ilink/bot/get_qrcode_status" {
			t.Fatalf("path = %q, want %q", got, "/ilink/bot/get_qrcode_status")
		}
		if got := r.URL.Query().Get("qrcode"); got != "legacy-code" {
			t.Fatalf("qrcode = %q, want %q", got, "legacy-code")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"scaned","bot_token":"bot-token"}`))
	}))
	defer server.Close()

	client := NewClient(slog.Default())
	status, err := client.PollQRStatus(context.Background(), server.URL, "legacy-code")
	if err != nil {
		t.Fatalf("PollQRStatus() error = %v", err)
	}
	if status.Status != "scanned" {
		t.Fatalf("status = %q, want %q", status.Status, "scanned")
	}
	if status.BotToken != "bot-token" {
		t.Fatalf("botToken = %q, want %q", status.BotToken, "bot-token")
	}
}
