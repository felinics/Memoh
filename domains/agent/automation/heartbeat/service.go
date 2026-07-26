package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/memohai/memoh/domains/agent/automation/heartbeat/persistence"
	"github.com/memohai/memoh/domains/api/identity/auth"
)

const heartbeatTokenTTL = 10 * time.Minute

// heartbeatRunTimeout caps how long a single heartbeat execution may take.
// This prevents unbounded Generate() calls from hanging forever.
const heartbeatRunTimeout = 5 * time.Minute

const defaultHeartbeatIntervalMinutes = 1440

// SessionCreator creates sessions for heartbeat runs.
type SessionCreator interface {
	CreateSession(ctx context.Context, botID, sessionType string) (string, error)
}

type Service struct {
	store          persistence.Store
	cron           *cron.Cron
	triggerer      Triggerer
	sessionCreator SessionCreator
	jwtSecret      string
	logger         *slog.Logger
	mu             sync.Mutex
	jobs           map[string]cron.EntryID
	runCtx         context.Context
	cancel         context.CancelFunc
	started        bool
	stopped        bool
}

func NewService(log *slog.Logger, store persistence.Store, triggerer Triggerer, sessionCreator SessionCreator, jwtSecret string) *Service {
	c := cron.New()
	service := &Service{
		store:          store,
		cron:           c,
		triggerer:      triggerer,
		sessionCreator: sessionCreator,
		jwtSecret:      jwtSecret,
		logger:         log.With(slog.String("service", "heartbeat")),
		jobs:           map[string]cron.EntryID{},
	}
	return service
}

// Start begins dispatching heartbeat jobs. It is safe to call Start more than
// once. Call Bootstrap before Start so persisted jobs are registered before
// the scheduler begins dispatching them.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return errors.New("heartbeat service is stopped")
	}
	if s.started {
		return nil
	}
	s.runCtx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))
	s.started = true
	s.cron.Start()
	return nil
}

// Shutdown stops dispatching new jobs and waits for active jobs to finish.
// The caller may bound the wait with ctx; the scheduler remains stopped when
// ctx expires.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = true
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	stopped := s.cron.Stop()
	select {
	case <-stopped.Done():
		return nil
	default:
	}
	select {
	case <-stopped.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Bootstrap(ctx context.Context) error {
	if s.store == nil {
		return errors.New("heartbeat queries not configured")
	}
	rows, err := s.store.ListEnabledBots(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		botID := row.ID
		ownerUserID := row.OwnerUserID
		cfg := Config{
			BotID:       botID,
			OwnerUserID: ownerUserID,
			Interval:    row.HeartbeatInterval,
		}
		if err := s.scheduleJob(ctx, cfg); err != nil {
			s.logger.ErrorContext(ctx, "failed to schedule heartbeat", slog.String("bot_id", botID), slog.Any("error", err))
		}
	}
	s.logger.InfoContext(ctx, "heartbeat bootstrap complete", slog.Int("count", len(rows)))
	return nil
}

func (s *Service) Reschedule(ctx context.Context, botID string) error {
	s.removeJob(botID)

	bot, err := s.store.GetBot(ctx, botID)
	if err != nil {
		return fmt.Errorf("get bot: %w", err)
	}
	if !bot.HeartbeatEnabled || bot.Status != "ready" {
		return nil
	}
	cfg := Config{
		BotID:       botID,
		OwnerUserID: bot.OwnerUserID,
		Interval:    bot.HeartbeatInterval,
	}
	return s.scheduleJob(ctx, cfg)
}

func (s *Service) Stop(botID string) {
	s.removeJob(botID)
}

func (s *Service) runHeartbeat(ctx context.Context, cfg Config) {
	if s.triggerer == nil {
		s.logger.ErrorContext(ctx, "heartbeat triggerer not configured")
		return
	}
	if _, err := uuid.Parse(strings.TrimSpace(cfg.BotID)); err != nil {
		s.logger.ErrorContext(ctx, "invalid bot id", slog.String("bot_id", cfg.BotID), slog.Any("error", err))
		return
	}

	var sessionID string
	if s.sessionCreator != nil {
		sid, err := s.sessionCreator.CreateSession(ctx, cfg.BotID, "heartbeat")
		if err != nil {
			s.logger.ErrorContext(ctx, "create heartbeat session failed", slog.String("bot_id", cfg.BotID), slog.Any("error", err))
		} else {
			sessionID = sid
		}
	}

	var lastHeartbeatAt string
	if prevLogs, listErr := s.store.ListLogsByBot(ctx, persistence.LogPage{
		BotID: cfg.BotID,
		Limit: 1,
	}); listErr == nil && len(prevLogs) > 0 {
		lastHeartbeatAt = prevLogs[0].StartedAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	logID, err := s.store.CreateLog(ctx, persistence.CreateLogCommand{
		BotID:     cfg.BotID,
		SessionID: sessionID,
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "create heartbeat log failed", slog.String("bot_id", cfg.BotID), slog.Any("error", err))
		return
	}

	token, err := s.generateTriggerToken(cfg.OwnerUserID)
	if err != nil {
		s.completeLog(ctx, logID, "error", "", err.Error(), nil, "")
		s.logger.ErrorContext(ctx, "generate trigger token failed", slog.String("bot_id", cfg.BotID), slog.Any("error", err))
		return
	}

	result, err := s.triggerer.TriggerHeartbeat(ctx, cfg.BotID, TriggerPayload{
		BotID:           cfg.BotID,
		Interval:        cfg.Interval,
		OwnerUserID:     cfg.OwnerUserID,
		SessionID:       sessionID,
		LastHeartbeatAt: lastHeartbeatAt,
	}, token)
	if err != nil {
		s.completeLog(ctx, logID, "error", "", err.Error(), nil, "")
		s.logger.ErrorContext(ctx, "heartbeat trigger failed", slog.String("bot_id", cfg.BotID), slog.Any("error", err))
		return
	}

	s.completeLog(ctx, logID, result.Status, result.Text, "", result.UsageBytes, result.ModelID)
	s.logger.InfoContext(ctx, "heartbeat completed", slog.String("bot_id", cfg.BotID), slog.String("status", result.Status))
}

func (s *Service) completeLog(ctx context.Context, logID, status, resultText, errorMessage string, usageBytes []byte, modelID string) {
	err := s.store.CompleteLog(ctx, persistence.CompleteLogCommand{
		ID:           logID,
		Status:       status,
		ResultText:   resultText,
		ErrorMessage: errorMessage,
		Usage:        usageBytes,
		ModelID:      modelID,
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "complete heartbeat log failed", slog.Any("error", err))
	}
}

func (s *Service) ListLogs(ctx context.Context, botID string, limit, offset int) ([]Log, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	total, err := s.store.CountLogsByBot(ctx, botID)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.store.ListLogsByBot(ctx, persistence.LogPage{
		BotID:  botID,
		Limit:  int32(limit),  //nolint:gosec // capped to 100 above
		Offset: int32(offset), //nolint:gosec // validated above
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]Log, 0, len(rows))
	for _, row := range rows {
		items = append(items, toLog(row))
	}
	return items, total, nil
}

func (s *Service) DeleteLogs(ctx context.Context, botID string) error {
	return s.store.DeleteLogsByBot(ctx, botID)
}

func (s *Service) generateTriggerToken(userID string) (string, error) {
	if strings.TrimSpace(s.jwtSecret) == "" {
		return "", errors.New("jwt secret not configured")
	}
	signed, _, err := auth.GenerateToken(userID, s.jwtSecret, heartbeatTokenTTL)
	if err != nil {
		return "", err
	}
	return "Bearer " + signed, nil
}

func (s *Service) scheduleJob(ctx context.Context, cfg Config) error {
	cfg.Interval = normalizeHeartbeatInterval(cfg.Interval)
	spec := fmt.Sprintf("@every %dm", cfg.Interval)
	job := func() {
		runCtx, runCancel, ok := s.jobContext(heartbeatRunTimeout)
		if !ok {
			return
		}
		defer runCancel()
		s.runHeartbeat(runCtx, cfg)
	}
	entryID, err := s.cron.AddFunc(spec, job)
	if err != nil {
		return fmt.Errorf("add heartbeat cron job: %w", err)
	}
	s.mu.Lock()
	s.jobs[cfg.BotID] = entryID
	s.mu.Unlock()
	s.logger.InfoContext(ctx, "heartbeat scheduled", slog.String("bot_id", cfg.BotID), slog.Int("interval_minutes", cfg.Interval))
	return nil
}

func (s *Service) jobContext(timeout time.Duration) (context.Context, context.CancelFunc, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.stopped || s.runCtx == nil {
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(s.runCtx, timeout)
	return ctx, cancel, true
}

func normalizeHeartbeatInterval(interval int) int {
	if interval <= 0 {
		return defaultHeartbeatIntervalMinutes
	}
	return interval
}

func (s *Service) removeJob(botID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entryID, ok := s.jobs[botID]
	if ok {
		s.cron.Remove(entryID)
		delete(s.jobs, botID)
	}
}

func toLog(row persistence.LogRecord) Log {
	l := Log{
		ID:           row.ID,
		BotID:        row.BotID,
		SessionID:    row.SessionID,
		Status:       row.Status,
		ResultText:   row.ResultText,
		ErrorMessage: row.ErrorMessage,
	}
	l.StartedAt = row.StartedAt
	if !row.CompletedAt.IsZero() {
		t := row.CompletedAt
		l.CompletedAt = &t
	}
	if row.Usage != nil {
		var usage any
		if err := json.Unmarshal(row.Usage, &usage); err == nil {
			l.Usage = usage
		}
	}
	return l
}
