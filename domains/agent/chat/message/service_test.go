package message

import (
	"context"
	"errors"
	"strings"
	"testing"

	messageevent "github.com/memohai/memoh/domains/agent/chat/event"
)

type fakePersistence struct {
	Persistence
	created        Record
	deleted        []string
	deletedBot     string
	deletedSession string
	assetErr       error
	historyErr     error
	linkAttempts   int
	conflictOnce   bool
}

func (f *fakePersistence) InTransaction(_ context.Context, fn func(Persistence) error) error {
	created, deleted := f.created, append([]string(nil), f.deleted...)
	if err := fn(f); err != nil {
		f.created, f.deleted = created, deleted
		return err
	}
	return nil
}

func (f *fakePersistence) InRuntimeFenceTransaction(_ context.Context, _, _ string, fn func(Persistence) error) error {
	return fn(f)
}

func (*fakePersistence) SupportsAtomicDirectWrites() bool { return false }

func (*fakePersistence) GetSessionSnapshot(_ context.Context, id string) (SessionSnapshot, error) {
	return SessionSnapshot{
		ID: id, Type: "acp_agent", SessionMode: "chat", RuntimeType: "acp_agent",
	}, nil
}

func (f *fakePersistence) CreateMessage(_ context.Context, record Record) (Message, error) {
	f.created = record
	return Message{
		ID: "33333333-3333-3333-3333-333333333333", BotID: record.BotID,
		SessionID: record.SessionID, Role: record.Role, Content: record.Content,
		Metadata: record.Metadata, Usage: record.Usage, SessionMode: record.SessionMode,
		RuntimeType: record.RuntimeType, DisplayContent: record.DisplayText,
	}, nil
}

func (f *fakePersistence) CreateMessageWithHistoryTurn(ctx context.Context, record Record, turnID string) (Message, error) {
	message, err := f.CreateMessage(ctx, record)
	if err != nil {
		return Message{}, err
	}
	if f.historyErr != nil {
		return Message{}, f.historyErr
	}
	if err := f.LinkMessageToHistoryTurn(ctx, message.ID, turnID, 1); err != nil {
		return Message{}, err
	}
	return message, nil
}

func (*fakePersistence) CreateMessageInHistoryTurnByRequest(context.Context, Record, string) (Message, error) {
	return Message{}, ErrNotFound
}

func (f *fakePersistence) CreateHistoryTurn(_ context.Context, record HistoryTurnCreate) (HistoryTurn, error) {
	if f.historyErr != nil {
		return HistoryTurn{}, f.historyErr
	}
	return HistoryTurn{
		ID:    "66666666-6666-6666-6666-666666666666",
		BotID: record.BotID, SessionID: record.SessionID,
	}, nil
}

func (f *fakePersistence) LinkMessageToHistoryTurn(_ context.Context, _, _ string, _ int64) error {
	f.linkAttempts++
	if f.conflictOnce && f.linkAttempts == 1 {
		return ErrTurnSequenceConflict
	}
	return nil
}

func (f *fakePersistence) CreateAssetLink(context.Context, AssetLink) error {
	return f.assetErr
}

func (f *fakePersistence) DeleteMessages(_ context.Context, ids []string) error {
	f.deleted = append(f.deleted, ids...)
	return nil
}

func (f *fakePersistence) DeleteMessagesByBot(_ context.Context, id string) error {
	f.deletedBot = id
	return nil
}

func (f *fakePersistence) DeleteMessagesBySession(_ context.Context, id string) error {
	f.deletedSession = id
	return nil
}

func TestPersistResolvesRuntimeSnapshotFromSession(t *testing.T) {
	store := &fakePersistence{}
	svc := NewService(nil, store)
	msg, err := svc.Persist(t.Context(), PersistInput{
		BotID: "11111111-1111-1111-1111-111111111111", SessionID: "22222222-2222-2222-2222-222222222222",
		Role: "user", Content: []byte(`{"type":"text","text":"hello"}`),
	})
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	if store.created.SessionMode != "chat" || store.created.RuntimeType != "acp_agent" {
		t.Fatalf("record runtime snapshot = %q/%q", store.created.SessionMode, store.created.RuntimeType)
	}
	if msg.SessionMode != "chat" || msg.RuntimeType != "acp_agent" {
		t.Fatalf("message runtime snapshot = %q/%q", msg.SessionMode, msg.RuntimeType)
	}
}

func TestPersistCopiesSenderSnapshotToRecord(t *testing.T) {
	store := &fakePersistence{}
	_, err := NewService(nil, store).Persist(t.Context(), PersistInput{
		BotID:             "11111111-1111-1111-1111-111111111111",
		Role:              "user",
		Content:           []byte(`{}`),
		SenderDisplayName: "Alice",
		SenderAvatarURL:   "https://example.com/alice.png",
		SkipHistoryTurn:   true,
	})
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	if store.created.SenderDisplayName != "Alice" ||
		store.created.SenderAvatarURL != "https://example.com/alice.png" {
		t.Fatalf("sender snapshot = %q/%q", store.created.SenderDisplayName, store.created.SenderAvatarURL)
	}
}

func TestPersistPropagatesMessageAssetCreationFailure(t *testing.T) {
	store := &fakePersistence{assetErr: errors.New("asset link failed")}
	_, err := NewService(nil, store).Persist(t.Context(), PersistInput{
		BotID: "11111111-1111-1111-1111-111111111111", SessionID: "22222222-2222-2222-2222-222222222222",
		Role: "user", Content: []byte(`{}`), Assets: []AssetRef{{ContentHash: "sha256:asset"}},
	})
	if err == nil || !strings.Contains(err.Error(), "asset link failed") {
		t.Fatalf("Persist() error = %v", err)
	}
}

func TestDeleteByScopeClearsCanonicalHistory(t *testing.T) {
	store := &fakePersistence{}
	svc := NewService(nil, store)
	const botID = "11111111-1111-1111-1111-111111111111"
	const sessionID = "22222222-2222-2222-2222-222222222222"
	if err := svc.DeleteByBot(t.Context(), botID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteBySession(t.Context(), sessionID); err != nil {
		t.Fatal(err)
	}
	if store.deletedBot != botID || store.deletedSession != sessionID {
		t.Fatalf("deleted scopes = %q/%q", store.deletedBot, store.deletedSession)
	}
}

type recordingPublisher struct {
	events []messageevent.Event
}

func (p *recordingPublisher) Publish(event messageevent.Event) {
	p.events = append(p.events, event)
}

func TestPersistRollsBackMessageWhenHistoryTurnFails(t *testing.T) {
	store := &fakePersistence{historyErr: errors.New("boom")}
	publisher := &recordingPublisher{}
	_, err := NewService(nil, store, publisher).Persist(t.Context(), PersistInput{
		BotID: "11111111-1111-1111-1111-111111111111", SessionID: "22222222-2222-2222-2222-222222222222",
		Role: "user", Content: []byte(`{}`),
	})
	if err == nil || store.created.ID != "" || len(store.deleted) != 0 || len(publisher.events) != 0 {
		t.Fatalf("error=%v created=%+v deleted=%v events=%d", err, store.created, store.deleted, len(publisher.events))
	}
}

func TestPersistRetriesTurnSequenceConflict(t *testing.T) {
	store := &fakePersistence{conflictOnce: true}
	publisher := &recordingPublisher{}
	msg, err := NewService(nil, store, publisher).Persist(t.Context(), PersistInput{
		BotID: "11111111-1111-1111-1111-111111111111", SessionID: "22222222-2222-2222-2222-222222222222",
		Role: "user", Content: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	if msg.ID == "" || store.linkAttempts != 2 || len(store.deleted) != 0 || len(publisher.events) != 1 {
		t.Fatalf("message=%+v attempts=%d deleted=%v events=%d", msg, store.linkAttempts, store.deleted, len(publisher.events))
	}
}
