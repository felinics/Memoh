package application

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// maxFragmentTextBytes bounds one stored fragment text; longer texts keep
// their head and are marked truncated.
const maxFragmentTextBytes = 256 << 10

type contextTextQueries interface {
	UpsertContextFragmentTexts(ctx context.Context, arg sqlc.UpsertContextFragmentTextsParams) error
}

// textStoreKey is the row identity of a stored text: the bot that rendered
// it and the text hash.
type textStoreKey struct {
	bot  [16]byte
	hash string
}

// contextTextStore persists rendered fragment texts content-addressed by
// their text hash, per bot, off the turn path. A hash is remembered only
// after its write succeeded, so a failed batch is retried by the next run
// that renders the same text.
type contextTextStore struct {
	queries  contextTextQueries
	logger   *slog.Logger
	mu       sync.Mutex
	seen     map[textStoreKey]struct{}
	inflight map[textStoreKey]struct{}
	pending  sync.WaitGroup
}

func newContextTextStore(queries contextTextQueries, logger *slog.Logger) *contextTextStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &contextTextStore{queries: queries, logger: logger, seen: make(map[textStoreKey]struct{}), inflight: make(map[textStoreKey]struct{})}
}

func (s *contextTextStore) PersistFragmentTexts(ctx context.Context, botID pgtype.UUID, texts []contextfrag.FragmentText) {
	if s == nil || s.queries == nil || !botID.Valid || len(texts) == 0 {
		return
	}
	params := sqlc.UpsertContextFragmentTextsParams{BotID: botID}
	keys := make([]textStoreKey, 0, len(texts))
	s.mu.Lock()
	for _, text := range texts {
		if text.TextHash == "" || text.Text == "" {
			continue
		}
		key := textStoreKey{bot: botID.Bytes, hash: text.TextHash}
		if _, seen := s.seen[key]; seen {
			continue
		}
		if _, writing := s.inflight[key]; writing {
			continue
		}
		s.inflight[key] = struct{}{}
		keys = append(keys, key)
		body := text.Text
		truncated := false
		if len(body) > maxFragmentTextBytes {
			body = body[:maxFragmentTextBytes]
			truncated = true
		}
		params.ContentHashes = append(params.ContentHashes, text.TextHash)
		params.Kinds = append(params.Kinds, string(text.Kind))
		params.Labels = append(params.Labels, text.Label)
		params.Texts = append(params.Texts, body)
		params.TextBytes = append(params.TextBytes, int32(min(len(text.Text), 1<<31-1))) //nolint:gosec // G115: bounded above
		params.Truncated = append(params.Truncated, truncated)
	}
	s.mu.Unlock()
	if len(params.ContentHashes) == 0 {
		return
	}
	s.pending.Add(1)
	go func(ctx context.Context) {
		defer s.pending.Done()
		err := s.upsert(ctx, params)
		s.mu.Lock()
		for _, key := range keys {
			delete(s.inflight, key)
			if err == nil {
				s.seen[key] = struct{}{}
			}
		}
		s.mu.Unlock()
		if err != nil {
			s.logger.Warn("context fragment texts not persisted", slog.Int("count", len(params.ContentHashes)), slog.Any("error", err))
		}
	}(context.WithoutCancel(ctx))
}

// upsert never lets the debug store take a turn down: a store that panics
// is reported like a failed write.
func (s *contextTextStore) upsert(ctx context.Context, params sqlc.UpsertContextFragmentTextsParams) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("context fragment text store panicked: %v", recovered)
		}
	}()
	return s.queries.UpsertContextFragmentTexts(ctx, params)
}

func (s *contextTextStore) wait() {
	s.pending.Wait()
}

// contextTextStore returns the service-wide fragment text store, created on
// first use so tests and services without a database never pay for it.
func (s *Service) contextTextStore() *contextTextStore {
	if s == nil || s.queries == nil {
		return nil
	}
	s.contextTextsOnce.Do(func() {
		s.contextTexts = newContextTextStore(s.queries, s.logger)
	})
	return s.contextTexts
}

// runTextSink binds the store to one run's bot and context, which carries
// the team the texts belong to; cancellation of the turn must not lose them.
type runTextSink struct {
	ctx   context.Context
	store *contextTextStore
	botID pgtype.UUID
}

func (s runTextSink) PersistFragmentTexts(texts []contextfrag.FragmentText) {
	s.store.PersistFragmentTexts(s.ctx, s.botID, texts)
}

// newContextLifecycleHolder creates a run's lifecycle holder wired to the
// fragment text store of the run's bot. A run without a bot id keeps its
// texts unrecorded.
func (s *Service) newContextLifecycleHolder(ctx context.Context, botID string) *contextfrag.LifecycleHolder {
	holder := contextfrag.NewLifecycleHolder()
	store := s.contextTextStore()
	if store == nil {
		return holder
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return holder
	}
	holder.SetTextSink(runTextSink{ctx: context.WithoutCancel(ctx), store: store, botID: pgBotID})
	return holder
}
