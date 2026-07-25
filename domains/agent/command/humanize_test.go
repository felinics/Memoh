package command

import (
	"strings"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/i18n"
)

func TestCommandText(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		target   string
		expected string
	}{
		{name: "plain", command: "help", expected: "/help"},
		{name: "addressed", command: "/help", target: "@snowluocat_bot", expected: "/help@snowluocat_bot"},
		{name: "arguments follow target", command: "help model", target: "snowluocat_bot", expected: "/help@snowluocat_bot model"},
		{name: "preserve existing target", command: "/help@other_bot", target: "snowluocat_bot", expected: "/help@other_bot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CommandText(tt.command, tt.target); got != tt.expected {
				t.Fatalf("CommandText(%q, %q) = %q, want %q", tt.command, tt.target, got, tt.expected)
			}
		})
	}
}

func TestTelegramGroupCommandTip(t *testing.T) {
	got := TelegramGroupCommandTip(i18n.New("en"), "@snowluocat_bot")
	if !strings.Contains(got, "`/help@snowluocat_bot`") {
		t.Fatalf("TelegramGroupCommandTip() = %q", got)
	}
	if got := TelegramGroupCommandTip(i18n.New("en"), ""); got != "" {
		t.Fatalf("TelegramGroupCommandTip(empty) = %q, want empty", got)
	}
}

func TestRelativeSince(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"just now", now.Add(-2 * time.Second), "just now"},
		{"seconds", now.Add(-42 * time.Second), "42s ago"},
		{"minutes", now.Add(-5 * time.Minute), "5m ago"},
		{"hours", now.Add(-3 * time.Hour), "3h ago"},
		{"days", now.Add(-2 * 24 * time.Hour), "2d ago"},
		{"old absolute", now.Add(-30 * 24 * time.Hour), now.Add(-30 * 24 * time.Hour).Format("2006-01-02 15:04")},
		{"future absolute", now.Add(time.Hour), now.Add(time.Hour).Format("2006-01-02 15:04")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relativeSince(tt.t, now); got != tt.want {
				t.Errorf("relativeSince = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHumanizeTimeZero(t *testing.T) {
	if got := humanizeTime(time.Time{}); got != "" {
		t.Errorf("humanizeTime(zero) = %q, want empty", got)
	}
}

func TestHumanizeDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{820 * time.Millisecond, "820ms"},
		{3200 * time.Millisecond, "3.2s"},
		{2*time.Minute + 5*time.Second, "2m 5s"},
		{time.Hour + 4*time.Minute, "1h 4m"},
		{-3 * time.Second, "3.0s"},
	}
	for _, tt := range tests {
		if got := humanizeDuration(tt.d); got != tt.want {
			t.Errorf("humanizeDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestHumanizeBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, tt := range tests {
		if got := humanizeBytes(tt.n); got != tt.want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1500, "1.5K"},
		{3_400_000, "3.4M"},
	}
	for _, tt := range tests {
		if got := formatTokens(tt.n); got != tt.want {
			t.Errorf("formatTokens(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestHumanizeCron(t *testing.T) {
	tests := []struct {
		pattern string
		want    string
	}{
		{"0 9 * * *", "daily at 09:00"},
		{"30 14 * * *", "daily at 14:30"},
		{"*/15 * * * *", "every 15 minutes"},
		{"* * * * *", "every minute"},
		{"0 * * * *", "hourly"},
		{"0 9 * * 1", "Mondays at 09:00"},
		{"0 9 * * 0", "Sundays at 09:00"},
		{"0 9 * * 7", "Sundays at 09:00"}, // cron allows 7 for Sunday
		// Unmodeled / invalid patterns fall back to the raw string.
		{"0 9 1 * *", "0 9 1 * *"},     // day-of-month set
		{"0 9 * 6 *", "0 9 * 6 *"},     // month set
		{"weird", "weird"},             // wrong field count
		{"99 99 * * *", "99 99 * * *"}, // out-of-range clock
	}
	for _, tt := range tests {
		if got := humanizeCron(tt.pattern); got != tt.want {
			t.Errorf("humanizeCron(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}
}

func TestHumanizeStatus(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"connected", "Connected"},
		{"error", "Error"},
		{"failed", "Failed"},
		{"error", "Error"},
		{"unknown", "Not checked"},
		{"ok", "Success"},
		{"allow", "Allowed"},
		{"deny", "Denied"},
		{"sent", "Sent"},
		{"pending", "Pending"},
		{"inactive", "Inactive"}, // unknown enum -> Title-cased as-is
		{"", ""},
	}
	for _, tt := range tests {
		if got := humanizeStatus(tt.in); got != tt.want {
			t.Errorf("humanizeStatus(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
