package telegram

import (
	"strconv"
	"testing"

	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/channel"
)

func Test_directoryLimit(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want int
	}{
		{"zero", 0, defaultDirectoryLimit},
		{"negative", -1, defaultDirectoryLimit},
		{"one", 1, 1},
		{"default", defaultDirectoryLimit, defaultDirectoryLimit},
		{"over max", maxDirectoryLimit + 100, maxDirectoryLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := directoryLimit(tt.n); got != tt.want {
				t.Errorf("directoryLimit() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_parseTelegramChatInput(t *testing.T) {
	tests := []struct {
		input        string
		wantID       int64
		wantUsername string
	}{
		{"123456789", 123456789, ""},
		{"  -100123  ", -100123, ""},
		{"@channel", 0, "@channel"},
		{"  @supergroup  ", 0, "@supergroup"},
		{"", 0, ""},
		{"abc", 0, ""},
	}
	for _, tt := range tests {
		chatID, username := parseTelegramChatInput(tt.input)
		if chatID != tt.wantID || username != tt.wantUsername {
			t.Errorf("parseTelegramChatInput(%q) = %d, %q; want %d, %q", tt.input, chatID, username, tt.wantID, tt.wantUsername)
		}
	}
}

func Test_parseTelegramUserInput(t *testing.T) {
	tests := []struct {
		input    string
		wantChat int64
		wantUser int64
	}{
		{"12345", 12345, 0},
		{"  -100  ", -100, 0},
		{"12345:67890", 12345, 67890},
		{"  -100 : 200  ", -100, 200},
		{"", 0, 0},
		{"abc", 0, 0},
		{"1:2:3", 0, 0},
	}
	for _, tt := range tests {
		chatID, userID := parseTelegramUserInput(tt.input)
		if chatID != tt.wantChat || userID != tt.wantUser {
			t.Errorf("parseTelegramUserInput(%q) = %d, %d; want %d, %d", tt.input, chatID, userID, tt.wantChat, tt.wantUser)
		}
	}
}

func Test_telegramUserToEntry(t *testing.T) {
	u := &tele.User{ID: 123, Username: "alice", FirstName: "Alice", LastName: "Smith"}
	e := telegramUserToEntry(u)
	if e.Kind != channel.DirectoryEntryUser {
		t.Errorf("Kind = %q", e.Kind)
	}
	if e.ID != strconv.FormatInt(123, 10) {
		t.Errorf("ID = %q", e.ID)
	}
	if e.Name != "Alice Smith" {
		t.Errorf("Name = %q", e.Name)
	}
	if e.Handle != "@alice" {
		t.Errorf("Handle = %q", e.Handle)
	}
	if e.Metadata["user_id"] != "123" || e.Metadata["username"] != "alice" {
		t.Errorf("Metadata = %+v", e.Metadata)
	}
	// nil user
	e2 := telegramUserToEntry(nil)
	if e2.Kind != channel.DirectoryEntryUser || e2.ID != "" {
		t.Errorf("telegramUserToEntry(nil) = %+v", e2)
	}
}
