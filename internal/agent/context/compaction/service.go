package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/chat/checkpoint"
	messageevent "github.com/felinics/memoh/internal/chat/event"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/hooks"
)

var (
	// errEmptySummary marks a completed LLM call that produced no usable summary
	// text. The pass fails and its rows stay candidates.
	errEmptySummary = errors.New("compaction: model returned an empty summary")
	// errIncompleteSummary marks a summarizer call that stopped for any reason
	// other than a natural stop (length cap, content filter): the text is
	// unusable because it may cut mid-thought.
	errIncompleteSummary = errors.New("compaction: model returned an incomplete summary")
	// errIneffectiveSummary marks a summary that would replay at least as many
	// tokens as the raw entries it replaces.
	errIneffectiveSummary = errors.New("compaction: summary does not reduce replay tokens")
	// errCompactionInputOverflow marks content no pass can read: a request no
	// prefix of which leaves the summarizer room to answer, or rows left after
	// a read that folds nothing, a call group larger than any read admits.
	errCompactionInputOverflow = errors.New("compaction: content exceeds the summarizer input")
	// errNoSummaryRoom marks a request whose kept history leaves no room for a
	// summary, or a summary longer than that room.
	errNoSummaryRoom = errors.New("compaction: no room for the summary in the request")
	// errStaleSource marks a pass whose request rendered another checkpoint
	// or another version of a row than the pass reads.
	errStaleSource = errors.New("compaction: the request rendered history that changed since")
)

// Service manages context compaction for bot conversations.
type Service struct {
	queries     dbstore.Queries
	checkpoints *checkpoint.Store
	hookService *hooks.Service
	logger      *slog.Logger
	events      messageevent.Publisher

	// inflight names the bot of each session with a compaction running in
	// this process, for activity reporting.
	inflightMu sync.Mutex
	inflight   map[string]string
}

// NewService creates a new compaction Service.
func NewService(log *slog.Logger, queries dbstore.Queries) *Service {
	return &Service{
		queries:     queries,
		checkpoints: checkpoint.NewStore(log, queries),
		logger:      log,
		inflight:    make(map[string]string),
	}
}

// beginSessionCompaction marks a session as having a compaction in flight.
// A session runs one turn at a time, and its compactions run in that turn.
func (s *Service) beginSessionCompaction(sessionID, botID string) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	s.inflight[sessionID] = botID
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
	for id, bot := range s.inflight {
		if bot == botID {
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

// Compact runs one compaction event over a session's history read from its
// rows, for a caller whose request cannot render them: a load the byte budget
// truncated, a run that is not armed, input a Discuss composer left out. It
// folds the summaries of the session's active checkpoint and every row the
// caller does not keep into one summary, each pass forking a request built
// from the rows (see synthesize) through the compaction model. An edit or
// retry's event first rewinds the session to the checkpoint its replacement
// keeps, as the replacement's first step does. It returns the passes it
// committed; a failed pass ends the event with an *EventError and leaves the
// passes before it committed.
func (s *Service) Compact(ctx context.Context, cfg TriggerConfig) ([]Pass, error) {
	return s.event(ctx, cfg.hookScope(), cfg.ReplacedTurnID, func(ctx context.Context) ([]Pass, error) {
		return s.compactRows(ctx, cfg)
	})
}

// event runs one compaction event for a session, between the PreCompact and
// PostCompact hooks, after an edit or retry's session moved to the checkpoint
// its replacement keeps.
func (s *Service) event(ctx context.Context, scope hookScope, replacedTurnID string, run func(context.Context) ([]Pass, error)) ([]Pass, error) {
	s.beginSessionCompaction(scope.SessionID, scope.BotID)
	defer func() {
		s.endSessionCompaction(scope.SessionID)
		s.publishActivity(scope.BotID)
	}()
	s.publishActivity(scope.BotID)

	var eventErr error
	preHookRan := false
	defer func() {
		if r := recover(); r != nil {
			panic(r)
		}
		if preHookRan {
			s.runPostCompactHook(context.WithoutCancel(ctx), scope, eventErr)
		}
	}()
	if err := s.runCompactionHook(ctx, hooks.EventPreCompact, scope, nil); err != nil {
		return nil, err
	}
	preHookRan = true
	if replacedTurnID != "" {
		if err := s.rewindReplacedTurn(ctx, scope.BotID, scope.SessionID, replacedTurnID); err != nil {
			eventErr = err
			return nil, err
		}
	}
	passes, err := run(ctx)
	eventErr = err
	return passes, err
}

// runPostCompactHook dispatches PostCompact after the compaction outcome is
// already decided. Post-hook failures are advisory, so an error only logs a
// warning and a panic is contained here.
func (s *Service) runPostCompactHook(ctx context.Context, scope hookScope, compactErr error) {
	defer func() {
		if r := recover(); r != nil && s.logger != nil {
			s.logger.WarnContext(ctx, "post compaction hook panicked", slog.String("bot_id", scope.BotID), slog.Any("panic", r))
		}
	}()
	extra := map[string]any{}
	if compactErr != nil {
		extra["error"] = compactErr.Error()
	}
	if err := s.runCompactionHook(ctx, hooks.EventPostCompact, scope, extra); err != nil && s.logger != nil {
		s.logger.WarnContext(ctx, "post compaction hook failed", slog.String("bot_id", scope.BotID), slog.Any("error", err))
	}
}

func (s *Service) runCompactionHook(ctx context.Context, eventName string, scope hookScope, extra map[string]any) error {
	if s == nil || s.hookService == nil {
		return nil
	}
	payload := map[string]any{
		"input_tokens":          scope.InputTokens,
		"history_budget_tokens": scope.BudgetTokens,
		"model_id":              scope.ModelID,
	}
	for key, value := range extra {
		payload[key] = value
	}
	req := hooks.Request{
		Version:   1,
		Event:     eventName,
		BotID:     scope.BotID,
		SessionID: scope.SessionID,
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
