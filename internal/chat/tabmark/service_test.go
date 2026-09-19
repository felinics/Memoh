package tabmark

import (
	"context"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/chat/thread"
)

type fakeThreads struct {
	metadata map[string]any
	writes   int
}

func (f *fakeThreads) Get(_ context.Context, sessionID string) (thread.Thread, error) {
	return thread.Thread{ID: sessionID, Metadata: f.metadata}, nil
}

func (f *fakeThreads) UpdateMetadata(_ context.Context, sessionID string, metadata map[string]any) (thread.Thread, error) {
	f.metadata = metadata
	f.writes++
	return thread.Thread{ID: sessionID, Metadata: metadata}, nil
}

func TestPutIsIdempotentAndSwitchesKind(t *testing.T) {
	t.Parallel()

	threads := &fakeThreads{metadata: map[string]any{"title_locked": true}}
	svc := NewService(threads)
	base := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	now := base
	svc.now = func() time.Time { return now }
	ctx := context.Background()

	first, err := svc.Put(ctx, "s1", Mark{Kind: KindDeliverable, BrowserID: "chrome-9222", TabID: "T1", URL: "https://a", Title: "A", SessionName: "checkout"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Key != "chrome-9222/T1" || !first.MarkedAt.Equal(base) {
		t.Fatalf("first mark: %#v", first)
	}
	if threads.metadata["title_locked"] != true {
		t.Fatal("other metadata keys must be preserved")
	}

	now = base.Add(time.Minute)
	second, err := svc.Put(ctx, "s1", Mark{Kind: KindHandoff, BrowserID: "chrome-9222", TabID: "T1", URL: "https://a/login", Title: "Login", Note: "enter the code"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Kind != KindHandoff || !second.MarkedAt.Equal(base) || !second.UpdatedAt.Equal(now) || second.Note != "enter the code" {
		t.Fatalf("re-marking must replace the kind and keep marked_at: %#v", second)
	}
	marks, err := svc.List(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 1 || marks[0].Kind != KindHandoff || marks[0].URL != "https://a/login" {
		t.Fatalf("list after re-mark: %#v", marks)
	}

	// The persisted form is plain JSON so other readers of the metadata
	// (and the API) see ordinary objects.
	stored, ok := threads.metadata[MetadataKey].([]any)
	if !ok || len(stored) != 1 {
		t.Fatalf("stored marks: %#v", threads.metadata[MetadataKey])
	}

	now = base.Add(2 * time.Minute)
	closed, changed, err := svc.MarkClosed(ctx, "s1", "chrome-9222/T1")
	if err != nil || !changed || closed.ClosedAt == nil || !closed.ClosedAt.Equal(now) {
		t.Fatalf("mark closed: %#v changed=%v err=%v", closed, changed, err)
	}
	if _, changed, _ := svc.MarkClosed(ctx, "s1", "chrome-9222/T1"); changed {
		t.Fatal("closing twice must not rewrite")
	}
	if _, changed, _ := svc.MarkClosed(ctx, "s1", "chrome-9222/nope"); changed {
		t.Fatal("unknown keys are ignored")
	}

	// Marking the closed tab again revives the mark.
	revived, err := svc.Put(ctx, "s1", Mark{Kind: KindDeliverable, BrowserID: "chrome-9222", TabID: "T1"})
	if err != nil || revived.ClosedAt != nil {
		t.Fatalf("re-marking must clear closed_at: %#v err=%v", revived, err)
	}

	if ok, err := svc.Delete(ctx, "s1", "chrome-9222/T1"); err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	if _, present := threads.metadata[MetadataKey]; present {
		t.Fatal("deleting the last mark must drop the metadata key")
	}
	if ok, _ := svc.Delete(ctx, "s1", "chrome-9222/T1"); ok {
		t.Fatal("deleting again must report not found")
	}
}

func TestPutRejectsInvalidMarks(t *testing.T) {
	t.Parallel()

	svc := NewService(&fakeThreads{})
	if _, err := svc.Put(context.Background(), "s1", Mark{Kind: "other", BrowserID: "b", TabID: "t"}); err == nil {
		t.Fatal("unknown kinds must be rejected")
	}
	if _, err := svc.Put(context.Background(), "s1", Mark{Kind: KindDeliverable, TabID: "t"}); err == nil {
		t.Fatal("a mark without a browser must be rejected")
	}
	var nilSvc *Service
	if _, err := nilSvc.List(context.Background(), "s1"); err == nil {
		t.Fatal("an unconfigured service must fail explicitly")
	}
}

func TestDecodeToleratesMalformedMetadata(t *testing.T) {
	t.Parallel()

	if got := decode(map[string]any{MetadataKey: "not a list"}); got != nil {
		t.Fatalf("malformed marks must decode to nothing: %#v", got)
	}
	got := decode(map[string]any{MetadataKey: []any{
		map[string]any{"kind": "deliverable", "browser_id": "chrome-9222", "tab_id": "T2", "marked_at": "2026-09-19T10:01:00Z"},
		map[string]any{"kind": "handoff", "browser_id": "chrome-9222", "tab_id": "T1", "marked_at": "2026-09-19T10:00:00Z"},
		map[string]any{"kind": "handoff", "browser_id": "chrome-9222"},
	}})
	if len(got) != 2 || got[0].TabID != "T1" || got[0].Key != "chrome-9222/T1" || got[1].TabID != "T2" {
		t.Fatalf("decode must drop tab-less entries, fill keys, and order by marked_at: %#v", got)
	}
}
