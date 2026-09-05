package application

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// maxFragmentTextBytes bounds one stored fragment text; longer texts keep
// their head and are marked truncated.
const maxFragmentTextBytes = 256 << 10

const (
	// maxFragmentTextWriters bounds the batches in flight at once; a batch
	// that finds no free writer is dropped and retried by the next run that
	// renders the same text, so a slow database never piles up goroutines.
	maxFragmentTextWriters = 4
	// fragmentTextWriteTimeout bounds one batch write.
	fragmentTextWriteTimeout = 30 * time.Second
	// maxRememberedFragmentTexts bounds the in-memory record of written
	// hashes; past it the record starts over and the store relies on the
	// database's conflict handling.
	maxRememberedFragmentTexts = 16384
)

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
	timeout  time.Duration
	mu       sync.Mutex
	seen     map[textStoreKey]struct{}
	inflight map[textStoreKey]struct{}
	writers  chan struct{}
	pending  sync.WaitGroup
}

func newContextTextStore(queries contextTextQueries, logger *slog.Logger) *contextTextStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &contextTextStore{
		queries:  queries,
		logger:   logger,
		timeout:  fragmentTextWriteTimeout,
		seen:     make(map[textStoreKey]struct{}),
		inflight: make(map[textStoreKey]struct{}),
		writers:  make(chan struct{}, maxFragmentTextWriters),
	}
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
		body, truncated := storableText(text.Text)
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
	select {
	case s.writers <- struct{}{}:
	default:
		s.release(keys, false)
		s.logger.Warn("context fragment texts dropped: writers busy", slog.Int("count", len(params.ContentHashes)))
		return
	}
	s.pending.Add(1)
	go func(ctx context.Context) {
		defer s.pending.Done()
		defer func() { <-s.writers }()
		ctx, cancel := context.WithTimeout(ctx, s.timeout)
		defer cancel()
		err := s.upsert(ctx, params)
		s.release(keys, err == nil)
		if err != nil {
			s.logger.Warn("context fragment texts not persisted", slog.Int("count", len(params.ContentHashes)), slog.Any("error", err))
		}
	}(context.WithoutCancel(ctx))
}

// storableText makes a rendered text safe for a UTF-8 text column: invalid
// bytes become U+FFFD, and an oversized text keeps its head cut at a rune
// boundary, because a byte cut inside a multi-byte character would make
// Postgres reject the whole batch.
func storableText(text string) (body string, truncated bool) {
	body = strings.ToValidUTF8(text, "\uFFFD")
	if len(body) <= maxFragmentTextBytes {
		return body, false
	}
	cut := maxFragmentTextBytes
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return body[:cut], true
}

// release clears the in-flight marks of a batch and, when it was written,
// remembers its keys so they are not written again by this process.
func (s *contextTextStore) release(keys []textStoreKey, written bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if written && len(s.seen)+len(keys) > maxRememberedFragmentTexts {
		s.seen = make(map[textStoreKey]struct{}, len(keys))
	}
	for _, key := range keys {
		delete(s.inflight, key)
		if written {
			s.seen[key] = struct{}{}
		}
	}
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

// SubagentLifecycleHolder builds the lifecycle holder of a spawned run so
// its injected fragment texts reach the store like the parent run's.
func (s *Service) SubagentLifecycleHolder(ctx context.Context, botID string) *contextfrag.LifecycleHolder {
	return s.newContextLifecycleHolder(ctx, botID)
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
