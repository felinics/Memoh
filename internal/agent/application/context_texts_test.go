package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type recordingFragmentTextQueries struct {
	mu     sync.Mutex
	params []sqlc.UpsertContextFragmentTextsParams
	err    error
}

func (q *recordingFragmentTextQueries) UpsertContextFragmentTexts(_ context.Context, arg sqlc.UpsertContextFragmentTextsParams) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.params = append(q.params, arg)
	return q.err
}

var (
	textStoreBotA = pgtype.UUID{Bytes: [16]byte{0xa}, Valid: true}
	textStoreBotB = pgtype.UUID{Bytes: [16]byte{0xb}, Valid: true}
)

func TestContextTextStorePersistsEachHashOncePerBot(t *testing.T) {
	t.Parallel()

	queries := &recordingFragmentTextQueries{}
	store := newContextTextStore(queries, slog.New(slog.DiscardHandler))
	store.PersistFragmentTexts(context.Background(), textStoreBotA, []contextfrag.FragmentText{
		{TextHash: "h1", Kind: contextfrag.KindSystemPrompt, Text: "You are Memoh."},
		{TextHash: "h2", Kind: contextfrag.KindWorkspaceInstruction, Text: "Follow AGENTS.md"},
	})
	store.PersistFragmentTexts(context.Background(), textStoreBotA, []contextfrag.FragmentText{{TextHash: "h1", Kind: contextfrag.KindSystemPrompt, Text: "You are Memoh."}})
	store.wait()

	if len(queries.params) != 1 {
		t.Fatalf("upserts = %d, want one batch", len(queries.params))
	}
	batch := queries.params[0]
	if batch.BotID != textStoreBotA || len(batch.ContentHashes) != 2 || batch.ContentHashes[0] != "h1" || batch.Kinds[1] != string(contextfrag.KindWorkspaceInstruction) || batch.Texts[1] != "Follow AGENTS.md" || batch.TextBytes[1] != int32(len("Follow AGENTS.md")) || batch.Truncated[0] {
		t.Fatalf("batch = %#v", batch)
	}

	store.PersistFragmentTexts(context.Background(), textStoreBotB, []contextfrag.FragmentText{{TextHash: "h1", Kind: contextfrag.KindSystemPrompt, Text: "You are Memoh."}})
	store.PersistFragmentTexts(context.Background(), pgtype.UUID{}, []contextfrag.FragmentText{{TextHash: "h3", Kind: contextfrag.KindSystemPrompt, Text: "no bot"}})
	store.wait()
	if len(queries.params) != 2 || queries.params[1].BotID != textStoreBotB || len(queries.params[1].ContentHashes) != 1 {
		t.Fatalf("a second bot keeps its own copy and a run without a bot writes nothing: %#v", queries.params)
	}
}

func TestContextTextStoreTruncatesOversizedTexts(t *testing.T) {
	t.Parallel()

	queries := &recordingFragmentTextQueries{}
	store := newContextTextStore(queries, slog.New(slog.DiscardHandler))
	store.PersistFragmentTexts(context.Background(), textStoreBotA, []contextfrag.FragmentText{{TextHash: "big", Kind: contextfrag.KindSkillsCatalog, Text: strings.Repeat("x", maxFragmentTextBytes+10)}})
	store.wait()

	if len(queries.params) != 1 || len(queries.params[0].Texts[0]) != maxFragmentTextBytes || !queries.params[0].Truncated[0] || queries.params[0].TextBytes[0] != int32(maxFragmentTextBytes+10) {
		t.Fatalf("batch = %#v", queries.params)
	}
}

func TestContextTextStoreRetriesAHashAfterAFailedWrite(t *testing.T) {
	t.Parallel()

	queries := &recordingFragmentTextQueries{err: errors.New("db down")}
	store := newContextTextStore(queries, slog.New(slog.DiscardHandler))
	text := []contextfrag.FragmentText{{TextHash: "h9", Kind: contextfrag.KindSystemPrompt, Text: "retry me"}}
	store.PersistFragmentTexts(context.Background(), textStoreBotA, text)
	store.wait()
	queries.mu.Lock()
	queries.err = nil
	queries.mu.Unlock()
	store.PersistFragmentTexts(context.Background(), textStoreBotA, text)
	store.wait()

	if len(queries.params) != 2 {
		t.Fatalf("upserts = %d, want the failed hash written again", len(queries.params))
	}
	store.PersistFragmentTexts(context.Background(), textStoreBotA, text)
	store.wait()
	if len(queries.params) != 2 {
		t.Fatalf("a persisted hash was written again")
	}
}

type blockingFragmentTextQueries struct {
	recordingFragmentTextQueries
	started chan struct{}
	release chan struct{}
}

func (q *blockingFragmentTextQueries) UpsertContextFragmentTexts(ctx context.Context, arg sqlc.UpsertContextFragmentTextsParams) error {
	q.started <- struct{}{}
	select {
	case <-q.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return q.recordingFragmentTextQueries.UpsertContextFragmentTexts(ctx, arg)
}

func TestContextTextStoreBoundsWritersAndRetriesDroppedBatches(t *testing.T) {
	t.Parallel()

	queries := &blockingFragmentTextQueries{started: make(chan struct{}, maxFragmentTextWriters+1), release: make(chan struct{})}
	store := newContextTextStore(queries, slog.New(slog.DiscardHandler))
	for i := range maxFragmentTextWriters + 1 {
		store.PersistFragmentTexts(context.Background(), textStoreBotA, []contextfrag.FragmentText{{TextHash: fmt.Sprintf("h%d", i), Kind: contextfrag.KindSystemPrompt, Text: "text"}})
	}
	for range maxFragmentTextWriters {
		<-queries.started
	}
	select {
	case <-queries.started:
		t.Fatalf("a fifth batch started while every writer was busy")
	case <-time.After(50 * time.Millisecond):
	}
	close(queries.release)
	store.wait()
	if len(queries.params) != maxFragmentTextWriters {
		t.Fatalf("batches written = %d, want the writer bound", len(queries.params))
	}

	store.PersistFragmentTexts(context.Background(), textStoreBotA, []contextfrag.FragmentText{{TextHash: fmt.Sprintf("h%d", maxFragmentTextWriters), Kind: contextfrag.KindSystemPrompt, Text: "text"}})
	<-queries.started
	store.wait()
	if len(queries.params) != maxFragmentTextWriters+1 {
		t.Fatalf("the dropped batch must be written by a later run: %d batches", len(queries.params))
	}
}

func TestContextTextStoreTimesOutAStuckWrite(t *testing.T) {
	t.Parallel()

	queries := &blockingFragmentTextQueries{started: make(chan struct{}, 1), release: make(chan struct{})}
	store := newContextTextStore(queries, slog.New(slog.DiscardHandler))
	store.timeout = 20 * time.Millisecond
	store.PersistFragmentTexts(context.Background(), textStoreBotA, []contextfrag.FragmentText{{TextHash: "slow", Kind: contextfrag.KindSystemPrompt, Text: "text"}})
	<-queries.started
	store.wait()
	if len(queries.params) != 0 {
		t.Fatalf("a timed-out write must not count as written")
	}
	store.PersistFragmentTexts(context.Background(), textStoreBotA, []contextfrag.FragmentText{{TextHash: "slow", Kind: contextfrag.KindSystemPrompt, Text: "text"}})
	select {
	case <-queries.started:
	case <-time.After(time.Second):
		t.Fatalf("a timed-out hash must be retried")
	}
	close(queries.release)
	store.wait()
}

func TestContextTextStoreTruncatesAtARuneBoundaryAndRepairsInvalidBytes(t *testing.T) {
	t.Parallel()

	queries := &recordingFragmentTextQueries{}
	store := newContextTextStore(queries, slog.New(slog.DiscardHandler))
	store.PersistFragmentTexts(context.Background(), textStoreBotA, []contextfrag.FragmentText{
		{TextHash: "cjk", Kind: contextfrag.KindSkillsCatalog, Text: strings.Repeat("x", maxFragmentTextBytes-1) + "中文"},
		{TextHash: "broken", Kind: contextfrag.KindWorkspaceInstruction, Text: "ok\xffbroken"},
	})
	store.wait()
	if len(queries.params) != 1 || len(queries.params[0].Texts) != 2 {
		t.Fatalf("batches = %#v, want one batch with both texts", queries.params)
	}
	batch := queries.params[0]
	if !utf8.ValidString(batch.Texts[0]) || len(batch.Texts[0]) > maxFragmentTextBytes || !batch.Truncated[0] || !strings.HasSuffix(batch.Texts[0], "x") {
		t.Fatalf("oversized text must be cut before the character it cannot hold: len=%d truncated=%v", len(batch.Texts[0]), batch.Truncated[0])
	}
	if batch.Texts[1] != "ok\uFFFDbroken" || batch.Truncated[1] {
		t.Fatalf("invalid bytes must be repaired, not rejected: %q", batch.Texts[1])
	}
}
