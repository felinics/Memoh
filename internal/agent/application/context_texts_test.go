package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

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

func TestContextTextStorePersistsEachHashOnce(t *testing.T) {
	t.Parallel()

	queries := &recordingFragmentTextQueries{}
	store := newContextTextStore(queries, slog.New(slog.DiscardHandler))
	store.PersistFragmentTexts(context.Background(), []contextfrag.FragmentText{
		{TextHash: "h1", Kind: contextfrag.KindSystemPrompt, Text: "You are Memoh."},
		{TextHash: "h2", Kind: contextfrag.KindWorkspaceInstruction, Text: "Follow AGENTS.md"},
	})
	store.PersistFragmentTexts(context.Background(), []contextfrag.FragmentText{{TextHash: "h1", Kind: contextfrag.KindSystemPrompt, Text: "You are Memoh."}})
	store.wait()

	if len(queries.params) != 1 {
		t.Fatalf("upserts = %d, want one batch", len(queries.params))
	}
	batch := queries.params[0]
	if len(batch.ContentHashes) != 2 || batch.ContentHashes[0] != "h1" || batch.Kinds[1] != string(contextfrag.KindWorkspaceInstruction) || batch.Texts[1] != "Follow AGENTS.md" || batch.TextBytes[1] != int32(len("Follow AGENTS.md")) || batch.Truncated[0] {
		t.Fatalf("batch = %#v", batch)
	}
}

func TestContextTextStoreTruncatesOversizedTexts(t *testing.T) {
	t.Parallel()

	queries := &recordingFragmentTextQueries{}
	store := newContextTextStore(queries, slog.New(slog.DiscardHandler))
	store.PersistFragmentTexts(context.Background(), []contextfrag.FragmentText{{TextHash: "big", Kind: contextfrag.KindSkillsCatalog, Text: strings.Repeat("x", maxFragmentTextBytes+10)}})
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
	store.PersistFragmentTexts(context.Background(), text)
	store.wait()
	queries.mu.Lock()
	queries.err = nil
	queries.mu.Unlock()
	store.PersistFragmentTexts(context.Background(), text)
	store.wait()

	if len(queries.params) != 2 {
		t.Fatalf("upserts = %d, want the failed hash written again", len(queries.params))
	}
	store.PersistFragmentTexts(context.Background(), text)
	store.wait()
	if len(queries.params) != 2 {
		t.Fatalf("a persisted hash was written again")
	}
}
