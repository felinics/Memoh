package cdpsession

import (
	"testing"
	"time"
)

type fakeConn struct{ closed int }

func (f *fakeConn) Close() error {
	f.closed++
	return nil
}

func TestRevokeClosesAttachedConnections(t *testing.T) {
	t.Parallel()

	store := New(time.Minute)
	session, err := store.Create(Session{BotID: "bot", ThreadID: "thread", BrowserID: "chrome-9222", Port: 9222, TabID: "tab-1", CreatedTab: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.ID) != 32 {
		t.Fatalf("session id must be a 16-byte hex secret: %q", session.ID)
	}
	if _, ok := store.Get(session.ID, "other-bot"); ok {
		t.Fatal("a session must not resolve under another bot")
	}
	if got, ok := store.Get(session.ID, "bot"); !ok || got.TabID != "tab-1" {
		t.Fatalf("session must resolve for its bot: %#v %v", got, ok)
	}

	conn := &fakeConn{}
	release, ok := store.Attach(session.ID, "bot", conn)
	if !ok {
		t.Fatal("attach must succeed for a live session")
	}
	if store.Connections(session.ID) != 1 {
		t.Fatalf("connections: %d", store.Connections(session.ID))
	}
	ended := &fakeConn{}
	releaseEnded, _ := store.Attach(session.ID, "bot", ended)
	releaseEnded()
	if store.Connections(session.ID) != 1 {
		t.Fatalf("released connection must be forgotten: %d", store.Connections(session.ID))
	}

	revoked, dropped, ok := store.Revoke(session.ID, "bot")
	if !ok || dropped != 1 || revoked.ID != session.ID {
		t.Fatalf("revoke: %#v dropped=%d ok=%v", revoked, dropped, ok)
	}
	if conn.closed != 1 {
		t.Fatalf("revoking must close the attached connection, closed=%d", conn.closed)
	}
	if ended.closed != 0 {
		t.Fatal("a connection that ended on its own must not be closed again")
	}
	if _, ok := store.Get(session.ID, "bot"); ok {
		t.Fatal("revoked session must be gone")
	}
	release()
	if _, _, ok := store.Revoke(session.ID, "bot"); ok {
		t.Fatal("revoking twice must report not found")
	}
}

func TestSessionsExpireAndRevokeTab(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	store := New(time.Minute)
	store.now = func() time.Time { return now }
	a, _ := store.Create(Session{BotID: "bot", Port: 9222, TabID: "tab-1"})
	b, _ := store.Create(Session{BotID: "bot", Port: 9222, TabID: "tab-2"})
	if got := store.ListForBot("bot"); len(got) != 2 {
		t.Fatalf("list: %#v", got)
	}
	if store.RevokeTab("bot", "tab-1") != 1 {
		t.Fatal("closing tab-1 must revoke its session")
	}
	if _, ok := store.Get(a.ID, "bot"); ok {
		t.Fatal("session bound to the closed tab must be gone")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := store.Get(b.ID, "bot"); ok {
		t.Fatal("idle session must expire")
	}
	if got := store.ListForBot("bot"); len(got) != 0 {
		t.Fatalf("expired sessions must not be listed: %#v", got)
	}
}
