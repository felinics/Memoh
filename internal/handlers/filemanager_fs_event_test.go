package handlers

import (
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/fsevent"
)

func collectFSEvents(t *testing.T, env *skillsTestEnv) <-chan []string {
	t.Helper()
	hub := fsevent.NewHub(10 * time.Millisecond)
	env.handler.SetFSEventHub(hub)
	ch := make(chan []string, 8)
	cancel := hub.Subscribe(env.botID, func(paths []string) { ch <- paths })
	t.Cleanup(cancel)
	return ch
}

func expectFSEvent(t *testing.T, ch <-chan []string, want []string) {
	t.Helper()
	select {
	case got := <-ch:
		sort.Strings(got)
		sorted := append([]string(nil), want...)
		sort.Strings(sorted)
		if len(got) != len(sorted) {
			t.Fatalf("fs event paths = %v, want %v", got, sorted)
		}
		for i := range sorted {
			if got[i] != sorted[i] {
				t.Fatalf("fs event paths = %v, want %v", got, sorted)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for fs event %v", want)
	}
}

func TestFSWritePublishesFSEvent(t *testing.T) {
	env := newSkillsTestEnv(t)
	events := collectFSEvents(t, env)

	rec, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/write", map[string]any{
		"path":    "/data/note.txt",
		"content": "hello",
	}, env.handler.FSWrite)
	if err != nil {
		t.Fatalf("FSWrite returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	expectFSEvent(t, events, []string{"/data/note.txt"})
}

func TestFSMkdirDeleteRenamePublishFSEvents(t *testing.T) {
	env := newSkillsTestEnv(t)
	events := collectFSEvents(t, env)

	if _, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/mkdir", map[string]any{
		"path": "/data/dir-a",
	}, env.handler.FSMkdir); err != nil {
		t.Fatalf("FSMkdir returned error: %v", err)
	}
	expectFSEvent(t, events, []string{"/data/dir-a"})

	if _, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/rename", map[string]any{
		"oldPath": "/data/dir-a",
		"newPath": "/data/dir-b",
	}, env.handler.FSRename); err != nil {
		t.Fatalf("FSRename returned error: %v", err)
	}
	expectFSEvent(t, events, []string{"/data/dir-a", "/data/dir-b"})

	if _, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/delete", map[string]any{
		"path":      "/data/dir-b",
		"recursive": true,
	}, env.handler.FSDelete); err != nil {
		t.Fatalf("FSDelete returned error: %v", err)
	}
	expectFSEvent(t, events, []string{"/data/dir-b"})
}

func TestFSWriteFailurePublishesNothing(t *testing.T) {
	env := newSkillsTestEnv(t)
	events := collectFSEvents(t, env)

	_, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/write", map[string]any{
		"path":             "/data/absent.txt",
		"content":          "x",
		"expectedRevision": "sha256:stale",
	}, env.handler.FSWrite)
	if err == nil {
		t.Fatal("expected conflict error")
	}
	select {
	case got := <-events:
		t.Fatalf("unexpected fs event %v after failed write", got)
	case <-time.After(100 * time.Millisecond):
	}
}
