package handlers

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// A skill activation sent while another turn of the same session is still
// running is refused because the session is busy. The session supports skills,
// so the answer must not claim otherwise. The refusal ends with one result
// record.
func TestWSSkillActivationOnBusySessionAnswersSessionBusy(t *testing.T) {
	t.Parallel()

	const (
		botID       = "11111111-1111-1111-1111-111111111111"
		sessionID   = "22222222-2222-2222-2222-222222222222"
		currentUser = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	queries := localChannelSessionAuthQueries{
		bot: testBotRow(botID, map[string]any{}),
		session: sqlc.BotSession{
			ID:              testUUID(sessionID),
			BotID:           testUUID(botID),
			Type:            sessionpkg.TypeChat,
			SessionMode:     sessionpkg.TypeChat,
			CreatedByUserID: testUUID(currentUser),
			Metadata:        []byte(`{}`),
		},
		grants: []sqlc.ListBotUserGrantsForUserRow{{
			ID:          testUUID("dddddddd-dddd-dddd-dddd-dddddddddddd"),
			BotID:       testUUID(botID),
			SubjectType: bots.GrantSubjectUser,
			UserID:      testUUID(currentUser),
			Permissions: []byte(`["chat","workspace_exec"]`),
		}},
	}
	var logs lockedBuffer
	handler := &LocalChannelHandler{
		channelType:    channel.ChannelTypeLocal,
		botService:     bots.NewService(nil, queries),
		accountService: accounts.NewService(nil, testAdminAccountStore{role: "user"}),
		sessionService: sessionpkg.NewService(nil, queries, nil),
		agentService:   &application.Service{},
		logger:         slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	// Another skill turn of the same session holds the reservation.
	release, reserved := handler.reserveWSRequestedSkillTurn(botID, sessionID, "invocation-running")
	if !reserved {
		t.Fatal("first reservation was refused")
	}
	defer release()

	client := openLocalChannelTestWS(t, handler, botID, currentUser)
	if err := client.WriteJSON(map[string]any{
		"type":             "message",
		"invocation_id":    "invocation-second",
		"session_id":       sessionID,
		"text":             "use the skill",
		"requested_skills": []map[string]any{{"name": "alpha"}},
	}); err != nil {
		t.Fatalf("write ws message: %v", err)
	}
	var frame map[string]any
	if err := client.ReadJSON(&frame); err != nil {
		t.Fatalf("read ws frame: %v", err)
	}
	if frame["type"] != "command_error" || frame["code"] != string(apperror.CodeSessionBusy) || frame["fault"] != "client" {
		t.Fatalf("frame = %#v, want command_error %s", frame, apperror.CodeSessionBusy)
	}

	var records []map[string]any
	waitFor(t, "ws request record", func() bool {
		records = records[:0]
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var record map[string]any
			if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "ws request" {
				records = append(records, record)
			}
		}
		return len(records) > 0
	})
	// A second record would have been written by now.
	time.Sleep(50 * time.Millisecond)
	records = records[:0]
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "ws request" {
			records = append(records, record)
		}
	}
	if len(records) != 1 {
		t.Fatalf("ws request records = %v, want exactly one", records)
	}
	if records[0]["reason"] != string(apperror.CodeSessionBusy) || records[0]["fault"] != "client" || records[0]["invocation_id"] != "invocation-second" {
		t.Fatalf("record = %v, want a client record for %s", records[0], apperror.CodeSessionBusy)
	}
}
