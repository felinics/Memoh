package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/turn"
	messageevent "github.com/felinics/memoh/internal/chat/event"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/hooks"
)

var (
	// errEmptySummary marks a completed LLM call that produced no usable summary
	// text. The compacted rows must stay reclaimable, so this must never reach
	// MarkMessagesCompacted.
	errEmptySummary = errors.New("compaction: model returned an empty summary")
	// errIncompleteSummary marks a summarizer call that stopped for any reason
	// other than a natural stop (length cap, content filter): the text is
	// unusable because it may cut mid-thought.
	errIncompleteSummary = errors.New("compaction: model returned an incomplete summary")
	// errIneffectiveSummary marks a summary that would replay at least as many
	// tokens as the raw entries it replaces.
	errIneffectiveSummary = errors.New("compaction: summary does not reduce replay tokens")
	// ErrSummaryWindowTooSmall marks a summarizer whose declared window cannot
	// hold the fixed prompt plus the output reserve; running it would overflow
	// on every attempt, so it fails closed before claiming any source rows.
	ErrSummaryWindowTooSmall = errors.New("compaction: summarizer window too small for the compaction prompt")
	// errCompactionInputOverflow marks content the summarizer cannot read in one
	// call: an unsplittable tool exchange, or the earlier summary beside the
	// oldest one. Its text is never cut to fit, so it fails before claiming any
	// source rows.
	errCompactionInputOverflow = errors.New("compaction: content exceeds the summarizer input")
	// errNoSummaryRoom marks a request whose kept history leaves no room for a
	// summary, or a summary longer than that room.
	errNoSummaryRoom = errors.New("compaction: no room for the summary in the request")
	// errCompactionUnsized marks a call without the request budget or the
	// summarizer window it is sized from.
	errCompactionUnsized = errors.New("compaction: request budget or summarizer window unknown")
	// errSummaryPastCutoff marks an edit or retry whose view ends inside an
	// active summary: the view shows those rows raw, so no fold matches it.
	errSummaryPastCutoff = errors.New("compaction: an active summary extends past the request cutoff")
	// errStaleSource marks a pass whose claimed rows changed, or were claimed
	// by another writer, before it committed.
	errStaleSource = errors.New("compaction: source rows changed since they were claimed")
)

// inflightRun marks one session's running compaction for activity reporting.
type inflightRun struct {
	botID string
}

// Service manages context compaction for bot conversations.
type Service struct {
	queries     dbstore.Queries
	hookService *hooks.Service
	logger      *slog.Logger
	events      messageevent.Publisher

	inflightMu sync.Mutex
	inflight   map[string]*inflightRun
}

// NewService creates a new compaction Service.
func NewService(log *slog.Logger, queries dbstore.Queries) *Service {
	return &Service{
		queries:  queries,
		logger:   log,
		inflight: make(map[string]*inflightRun),
	}
}

// beginSessionCompaction marks a session as having a compaction in flight.
// Overlapping runs would select overlapping candidate sets and race
// MarkMessagesCompacted, so only one compaction may run per session at a time.
func (s *Service) beginSessionCompaction(sessionID string) (*inflightRun, bool) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if _, busy := s.inflight[sessionID]; busy {
		return nil, false
	}
	run := &inflightRun{}
	s.inflight[sessionID] = run
	return run, true
}

func (s *Service) endSessionCompaction(sessionID string) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	delete(s.inflight, sessionID)
}

func (s *Service) SetHookService(h *hooks.Service) {
	s.hookService = h
}

// SetEventPublisher wires lightweight activity notifications at startup.
func (s *Service) SetEventPublisher(p messageevent.Publisher) {
	s.events = p
}

// ActiveSessions is a process-local activity snapshot, not an admission gate.
func (s *Service) ActiveSessions(botID string) []string {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	ids := make([]string, 0)
	for id, run := range s.inflight {
		if run.botID == botID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (s *Service) publishActivity(botID string) {
	if s.events != nil {
		s.events.Publish(messageevent.Event{
			Type: messageevent.EventTypeCompactionChanged, BotID: botID,
			Data: json.RawMessage(`{}`),
		})
	}
}

// Compact runs one compaction event for the caller's request: it folds the
// active summary and every candidate the request does not keep raw into one
// summary. A pass folds as much as one summarizer call can read and commits
// it; the event passes again only while candidates are left that the call
// could not read. It returns the passes it committed: none when there was
// nothing to fold or the session's compaction was already in flight. A
// failed pass ends the event with an *EventError and leaves the passes before
// it committed.
func (s *Service) Compact(ctx context.Context, cfg TriggerConfig) ([]Pass, error) {
	run, ok := s.beginSessionCompaction(cfg.SessionID)
	if !ok {
		s.logger.InfoContext(ctx, "compaction: already in flight for session",
			slog.String("bot_id", cfg.BotID),
			slog.String("session_id", cfg.SessionID),
		)
		return nil, nil
	}
	defer func() {
		s.endSessionCompaction(cfg.SessionID)
		s.publishActivity(cfg.BotID)
	}()

	s.inflightMu.Lock()
	run.botID = cfg.BotID
	s.inflightMu.Unlock()
	s.publishActivity(cfg.BotID)

	var compactErr error
	preHookRan := false
	defer func() {
		if r := recover(); r != nil {
			panic(r)
		}
		if !preHookRan {
			return
		}
		s.runPostCompactHook(context.WithoutCancel(ctx), cfg, compactErr)
	}()

	if err := s.runCompactionHook(ctx, hooks.EventPreCompact, cfg, nil); err != nil {
		return nil, err
	}
	preHookRan = true
	botUUID, err := db.ParseUUID(cfg.BotID)
	if err != nil {
		compactErr = err
		return nil, err
	}
	sessionUUID, err := db.ParseUUID(cfg.SessionID)
	if err != nil {
		compactErr = err
		return nil, err
	}

	var passes []Pass
	for {
		pass, folded, err := s.doCompaction(ctx, botUUID, sessionUUID, cfg)
		if err != nil {
			compactErr = err
			return passes, err
		}
		if !folded {
			return passes, nil
		}
		passes = append(passes, pass)
		if cfg.View != nil {
			cfg.View = slices.DeleteFunc(slices.Clone(cfg.View), func(source turn.ContextMessageSource) bool {
				return source.Kind == "summary" && slices.Contains(pass.Absorbed, source.ID)
			})
			cfg.View = append(cfg.View, turn.ContextMessageSource{Kind: "summary", ID: pass.ArtifactID})
		}
		if !pass.More || len(pass.Covered) == 0 {
			return passes, nil
		}
	}
}

// runPostCompactHook dispatches PostCompact after the compaction outcome is
// already decided. Post-hook failures are advisory, so an error only logs a
// warning and a panic is contained here.
func (s *Service) runPostCompactHook(ctx context.Context, cfg TriggerConfig, compactErr error) {
	defer func() {
		if r := recover(); r != nil && s.logger != nil {
			s.logger.WarnContext(ctx, "post compaction hook panicked", slog.String("bot_id", cfg.BotID), slog.Any("panic", r))
		}
	}()
	extra := map[string]any{}
	if compactErr != nil {
		extra["error"] = compactErr.Error()
	}
	if err := s.runCompactionHook(ctx, hooks.EventPostCompact, cfg, extra); err != nil && s.logger != nil {
		s.logger.WarnContext(ctx, "post compaction hook failed", slog.String("bot_id", cfg.BotID), slog.Any("error", err))
	}
}

func (s *Service) runCompactionHook(ctx context.Context, eventName string, cfg TriggerConfig, extra map[string]any) error {
	if s == nil || s.hookService == nil {
		return nil
	}
	payload := map[string]any{
		"input_tokens":          cfg.TotalInputTokens,
		"history_budget_tokens": cfg.HistoryBudgetTokens,
		"model_id":              cfg.ModelID,
	}
	for key, value := range extra {
		payload[key] = value
	}
	req := hooks.Request{
		Version:   1,
		Event:     eventName,
		BotID:     cfg.BotID,
		SessionID: cfg.SessionID,
		Workspace: hooks.WorkspaceInfo{
			CWD: hooks.DefaultWorkDir,
		},
		Turn: payload,
	}
	res, err := s.hookService.Run(ctx, req, nil)
	if err != nil {
		return err
	}
	if res.Decision == hooks.DecisionDeny {
		return hooks.ErrDenied
	}
	return nil
}

// ListLogs returns paginated compaction logs for a bot.
func (s *Service) ListLogs(ctx context.Context, botID string, limit, offset int) ([]Log, int64, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, 0, err
	}

	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	total, err := s.queries.CountCompactionLogsByBot(ctx, botUUID)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.queries.ListCompactionLogsByBot(ctx, sqlc.ListCompactionLogsByBotParams{
		BotID:  botUUID,
		Limit:  int32(limit),  //nolint:gosec // clamped above
		Offset: int32(offset), //nolint:gosec // validated above
	})
	if err != nil {
		return nil, 0, err
	}

	logs := make([]Log, len(rows))
	for i, r := range rows {
		logs[i] = toLog(r)
	}
	return logs, total, nil
}

func toLog(r sqlc.BotHistoryMessageCompact) Log {
	l := Log{
		ID:           formatUUID(r.ID),
		BotID:        formatUUID(r.BotID),
		SessionID:    formatUUID(r.SessionID),
		Status:       r.Status,
		Summary:      r.Summary,
		MessageCount: int(r.MessageCount),
		ErrorMessage: r.ErrorMessage,
		ModelID:      formatUUID(r.ModelID),
		StartedAt:    r.StartedAt.Time,
	}
	if r.CompletedAt.Valid {
		t := r.CompletedAt.Time
		l.CompletedAt = &t
	}
	if len(r.Usage) > 0 {
		var u any
		if json.Unmarshal(r.Usage, &u) == nil {
			l.Usage = u
		}
	}
	return l
}

func formatUUID(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}
