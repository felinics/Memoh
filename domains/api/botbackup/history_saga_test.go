package botbackup

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	chatbackup "github.com/memohai/memoh/domains/agent/chat/backup"
	channelbackup "github.com/memohai/memoh/domains/channel/backup"
)

func TestCollectHistoryMapsOwnerSnapshots(t *testing.T) {
	routeID := "route"
	chat := &chatBackupFake{
		exportResult: chatbackup.Snapshot{
			Sessions: []chatbackup.Session{{ID: "session", RouteID: &routeID}},
			Messages: []chatbackup.Message{{ID: "message"}},
			Assets:   []chatbackup.Asset{{RelID: "asset"}},
		},
	}
	channel := &channelBackupFake{
		exportResult: channelbackup.Snapshot{
			DiscussCursors: []channelbackup.DiscussCursor{{SessionID: "session"}},
			SessionEvents:  []channelbackup.SessionEvent{{ID: "event"}},
			RouteActiveSessions: []channelbackup.RouteActiveSession{{
				RouteID: routeID, SessionID: "session",
			}},
		},
	}
	service := &Service{chatBackup: chat, channelBackup: channel}

	history, warnings := service.collectHistory(t.Context(), "bot", true)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(history.Sessions) != 1 || len(history.Messages) != 1 || len(history.Assets) != 1 {
		t.Fatalf("chat history = %+v", history)
	}
	if len(history.DiscussCursors) != 1 || len(history.SessionEvents) != 1 {
		t.Fatalf("channel history = %+v", history)
	}
	if len(history.RouteActiveSessions) != 1 ||
		history.RouteActiveSessions[0] != (backupRouteActiveSession{RouteID: "route", SessionID: "session"}) {
		t.Fatalf("route sessions = %+v", history.RouteActiveSessions)
	}
}

func TestRestoreHistoryCoordinatesOwners(t *testing.T) {
	calls := []string{}
	chat := &chatBackupFake{
		calls: &calls,
		importResult: chatbackup.ImportResult{
			SessionIDs: map[string]string{"old-session": "new-session"},
			MessageIDs: map[string]string{"old-message": "new-message"},
			EventReferences: []chatbackup.EventReference{{
				MessageID:  "new-message",
				OldEventID: "old-event",
			}},
			Receipt: chatbackup.ImportReceipt{BotID: "bot", SessionIDs: []string{"new-session"}},
		},
	}
	channel := &channelBackupFake{
		calls: &calls,
		importResult: channelbackup.ImportResult{
			EventIDs: map[string]string{"old-event": "new-event"},
			Receipt:  channelbackup.ImportReceipt{BotID: "bot", EventIDs: []string{"new-event"}},
		},
	}
	service := &Service{chatBackup: chat, channelBackup: channel}
	state := historySagaState(t)

	if err := service.restoreHistory(t.Context(), "actor", "bot", state, true, true); err != nil {
		t.Fatalf("restoreHistory() error = %v", err)
	}

	wantCalls := []string{"chat.import", "channel.import", "chat.bind"}
	if !slices.Equal(calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
	if !chat.importRequest.Replace || chat.importRequest.ActorUserID != "actor" {
		t.Fatalf("chat import request = %+v", chat.importRequest)
	}
	if got := channel.importRequest.SessionIDs["old-session"]; got != "new-session" {
		t.Fatalf("channel session mapping = %q, want new-session", got)
	}
	if len(channel.importRequest.RouteSessions) != 1 ||
		channel.importRequest.RouteSessions[0].RouteID != "old-route" {
		t.Fatalf("channel route sessions = %+v", channel.importRequest.RouteSessions)
	}
	if len(chat.bindRequest.Bindings) != 1 ||
		chat.bindRequest.Bindings[0] != (chatbackup.EventBinding{MessageID: "new-message", EventID: "new-event"}) {
		t.Fatalf("event bindings = %+v", chat.bindRequest.Bindings)
	}
	if state.counts[SectionHistory] != 1 || state.counts[SectionAssets] != 1 {
		t.Fatalf("counts = %v", state.counts)
	}
}

func TestRestoreHistoryCompensatesInReverseOrder(t *testing.T) {
	calls := []string{}
	chat := &chatBackupFake{
		calls:   &calls,
		bindErr: errors.New("bind failed"),
		importResult: chatbackup.ImportResult{
			SessionIDs: map[string]string{"old-session": "new-session"},
			Receipt:    chatbackup.ImportReceipt{BotID: "bot"},
		},
	}
	channel := &channelBackupFake{
		calls: &calls,
		importResult: channelbackup.ImportResult{
			Receipt: channelbackup.ImportReceipt{BotID: "bot"},
		},
	}
	service := &Service{chatBackup: chat, channelBackup: channel}

	err := service.restoreHistory(t.Context(), "actor", "bot", historySagaState(t), true, false)
	if err == nil || !errors.Is(err, chat.bindErr) {
		t.Fatalf("restoreHistory() error = %v, want bind error", err)
	}
	wantCalls := []string{
		"chat.import",
		"channel.import",
		"chat.bind",
		"channel.compensate",
		"chat.compensate",
	}
	if !slices.Equal(calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
}

func historySagaState(t *testing.T) *importState {
	t.Helper()
	entries := map[string]backupZipEntry{}
	put := func(path string, value any) {
		raw, err := marshalJSON(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", path, err)
		}
		entries[path] = backupZipEntry{data: raw}
	}
	routeID := "old-route"
	put("history/sessions.json", []chatbackup.Session{{
		ID:       "old-session",
		BotID:    "old-bot",
		RouteID:  &routeID,
		Metadata: json.RawMessage(`{}`),
	}})
	put("history/messages.json", []chatbackup.Message{{
		ID:        "old-message",
		BotID:     "old-bot",
		SessionID: testPointer("old-session"),
		Role:      "user",
		Content:   json.RawMessage(`{"text":"hello"}`),
		Metadata:  json.RawMessage(`{}`),
		EventID:   testPointer("old-event"),
	}})
	put("assets/message_assets.json", []chatbackup.Asset{{
		RelID:     "old-asset",
		MessageID: "old-message",
	}})
	put("history/discuss_cursors.json", []channelbackup.DiscussCursor{{
		SessionID: "old-session",
		ScopeKey:  "default",
		RouteID:   &routeID,
	}})
	put("history/session_events.json", []channelbackup.SessionEvent{{
		ID:        "old-event",
		BotID:     "old-bot",
		SessionID: "old-session",
		EventKind: "message",
		EventData: json.RawMessage(`{}`),
	}})
	put("history/route_active_sessions.json", []backupRouteActiveSession{{
		RouteID:   routeID,
		SessionID: "old-session",
	}})
	return &importState{entries: entries, counts: map[Section]int{}}
}

func testPointer[T any](value T) *T {
	return &value
}

type chatBackupFake struct {
	calls         *[]string
	exportResult  chatbackup.Snapshot
	importRequest chatbackup.ImportRequest
	importResult  chatbackup.ImportResult
	importErr     error
	bindRequest   chatbackup.BindEventReferencesRequest
	bindErr       error
	compensateErr error
}

func (f *chatBackupFake) Export(context.Context, chatbackup.ExportRequest) (chatbackup.Snapshot, error) {
	return f.exportResult, nil
}

func (f *chatBackupFake) Import(_ context.Context, request chatbackup.ImportRequest) (chatbackup.ImportResult, error) {
	*f.calls = append(*f.calls, "chat.import")
	f.importRequest = request
	return f.importResult, f.importErr
}

func (f *chatBackupFake) BindEventReferences(_ context.Context, request chatbackup.BindEventReferencesRequest) error {
	*f.calls = append(*f.calls, "chat.bind")
	f.bindRequest = request
	return f.bindErr
}

func (f *chatBackupFake) Compensate(context.Context, chatbackup.ImportReceipt) error {
	*f.calls = append(*f.calls, "chat.compensate")
	return f.compensateErr
}

func (*chatBackupFake) Summary(context.Context, string) (chatbackup.Summary, error) {
	return chatbackup.Summary{}, nil
}

type channelBackupFake struct {
	calls         *[]string
	exportResult  channelbackup.Snapshot
	importRequest channelbackup.ImportRequest
	importResult  channelbackup.ImportResult
	importErr     error
	compensateErr error
}

func (f *channelBackupFake) Export(context.Context, string) (channelbackup.Snapshot, error) {
	return f.exportResult, nil
}

func (f *channelBackupFake) Import(_ context.Context, request channelbackup.ImportRequest) (channelbackup.ImportResult, error) {
	*f.calls = append(*f.calls, "channel.import")
	f.importRequest = request
	return f.importResult, f.importErr
}

func (f *channelBackupFake) Compensate(context.Context, channelbackup.ImportReceipt) error {
	*f.calls = append(*f.calls, "channel.compensate")
	return f.compensateErr
}
