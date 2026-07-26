package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	schedulepersistence "github.com/memohai/memoh/domains/agent/automation/schedule/persistence"

	"github.com/robfig/cron/v3"

	"github.com/memohai/memoh/domains/api/identity/auth"
)

// SessionCreator creates sessions for schedule runs.
type SessionCreator interface {
	CreateSession(ctx context.Context, botID, sessionType string) (string, error)
}

type Service struct {
	store           schedulepersistence.Store
	cron            *cron.Cron
	parser          cron.Parser
	triggerer       Triggerer
	sessionCreator  SessionCreator
	jwtSecret       string
	logger          *slog.Logger
	defaultLocation *time.Location
	mu              sync.Mutex
	jobs            map[string]cron.EntryID
	runCtx          context.Context
	cancel          context.CancelFunc
	started         bool
	stopped         bool
}

func NewService(log *slog.Logger, store schedulepersistence.Store, triggerer Triggerer, sessionCreator SessionCreator, jwtSecret string, location *time.Location) *Service {
	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	if location == nil {
		location = time.UTC
	}
	c := cron.New(cron.WithParser(parser), cron.WithLocation(location))
	service := &Service{
		store:           store,
		cron:            c,
		parser:          parser,
		triggerer:       triggerer,
		sessionCreator:  sessionCreator,
		jwtSecret:       jwtSecret,
		logger:          log.With(slog.String("service", "schedule")),
		defaultLocation: location,
		jobs:            map[string]cron.EntryID{},
	}
	return service
}

// Start begins dispatching scheduled jobs. It is safe to call Start more than
// once. Call Bootstrap before Start so persisted jobs are registered before
// the scheduler begins dispatching them.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return errors.New("schedule service is stopped")
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
		return errors.New("schedule queries not configured")
	}
	items, err := s.store.ListEnabled(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := s.scheduleJob(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Create(ctx context.Context, botID string, req CreateRequest) (Schedule, error) {
	if s.store == nil {
		return Schedule{}, errors.New("schedule queries not configured")
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Description) == "" || strings.TrimSpace(req.Pattern) == "" || strings.TrimSpace(req.Command) == "" {
		return Schedule{}, errors.New("name, description, pattern, command are required")
	}
	if _, err := s.parser.Parse(req.Pattern); err != nil {
		return Schedule{}, fmt.Errorf("invalid cron pattern: %w", err)
	}
	var maxCalls *int
	if req.MaxCalls.Set && req.MaxCalls.Value != nil {
		if *req.MaxCalls.Value < math.MinInt32 || *req.MaxCalls.Value > math.MaxInt32 {
			return Schedule{}, fmt.Errorf("max_calls out of range: %d", *req.MaxCalls.Value)
		}
		maxCalls = req.MaxCalls.Value
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	row, err := s.store.Create(ctx, schedulepersistence.CreateCommand{
		Name:        req.Name,
		Description: req.Description,
		Pattern:     req.Pattern,
		MaxCalls:    maxCalls,
		Enabled:     enabled,
		Command:     req.Command,
		BotID:       botID,
	})
	if err != nil {
		return Schedule{}, err
	}
	if row.Enabled {
		if err := s.scheduleJob(ctx, row); err != nil {
			return Schedule{}, err
		}
	}
	return toSchedule(row), nil
}

func (s *Service) Get(ctx context.Context, id string) (Schedule, error) {
	row, err := s.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, schedulepersistence.ErrNotFound) {
			return Schedule{}, errors.New("schedule not found")
		}
		return Schedule{}, err
	}
	return toSchedule(row), nil
}

func (s *Service) List(ctx context.Context, botID string) ([]Schedule, error) {
	rows, err := s.store.ListByBot(ctx, botID)
	if err != nil {
		return nil, err
	}
	items := make([]Schedule, 0, len(rows))
	for _, row := range rows {
		items = append(items, toSchedule(row))
	}
	return items, nil
}

func (s *Service) Update(ctx context.Context, id string, req UpdateRequest) (Schedule, error) {
	existing, err := s.store.Get(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	name := existing.Name
	if req.Name != nil {
		name = *req.Name
	}
	description := existing.Description
	if req.Description != nil {
		description = *req.Description
	}
	pattern := existing.Pattern
	if req.Pattern != nil {
		if _, err := s.parser.Parse(*req.Pattern); err != nil {
			return Schedule{}, fmt.Errorf("invalid cron pattern: %w", err)
		}
		pattern = *req.Pattern
	}
	command := existing.Command
	if req.Command != nil {
		command = *req.Command
	}
	maxCalls := existing.MaxCalls
	if req.MaxCalls.Set {
		if req.MaxCalls.Value == nil {
			maxCalls = nil
		} else {
			if *req.MaxCalls.Value < math.MinInt32 || *req.MaxCalls.Value > math.MaxInt32 {
				return Schedule{}, fmt.Errorf("max_calls out of range: %d", *req.MaxCalls.Value)
			}
			maxCalls = req.MaxCalls.Value
		}
	}
	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	updated, err := s.store.Update(ctx, schedulepersistence.UpdateCommand{
		ID:          id,
		Name:        name,
		Description: description,
		Pattern:     pattern,
		MaxCalls:    maxCalls,
		Enabled:     enabled,
		Command:     command,
	})
	if err != nil {
		return Schedule{}, err
	}
	if err := s.rescheduleJob(ctx, updated); err != nil {
		return Schedule{}, fmt.Errorf("reschedule job: %w", err)
	}
	return toSchedule(updated), nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}
	s.removeJob(id)
	return nil
}

func (s *Service) Trigger(ctx context.Context, scheduleID string) error {
	if s.triggerer == nil {
		return errors.New("schedule triggerer not configured")
	}
	sched, err := s.Get(ctx, scheduleID)
	if err != nil {
		return err
	}
	if !sched.Enabled {
		return errors.New("schedule is disabled")
	}
	return s.runSchedule(ctx, sched)
}

const scheduleTokenTTL = 10 * time.Minute

// scheduleRunTimeout caps how long a single schedule execution may take.
// This prevents unbounded Generate() calls from hanging forever.
const scheduleRunTimeout = 5 * time.Minute

func (s *Service) runSchedule(ctx context.Context, sched Schedule) error {
	if s.triggerer == nil {
		return errors.New("schedule triggerer not configured")
	}
	updated, err := s.store.IncrementCalls(ctx, sched.ID)
	if err != nil {
		return err
	}
	if !updated.Enabled {
		s.removeJob(sched.ID)
	}

	ownerUserID, err := s.resolveBotOwner(ctx, sched.BotID)
	if err != nil {
		return fmt.Errorf("resolve bot owner: %w", err)
	}

	var sessionID string
	if s.sessionCreator != nil {
		sid, err := s.sessionCreator.CreateSession(ctx, sched.BotID, "schedule")
		if err != nil {
			s.logger.ErrorContext(ctx, "create schedule session failed", slog.String("bot_id", sched.BotID), slog.Any("error", err))
		} else {
			sessionID = sid
		}
	}

	logID, err := s.store.CreateLog(ctx, schedulepersistence.CreateLogCommand{
		ScheduleID: sched.ID,
		BotID:      sched.BotID,
		SessionID:  sessionID,
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "create schedule log failed", slog.String("schedule_id", sched.ID), slog.Any("error", err))
	}

	token, err := s.generateTriggerToken(ownerUserID)
	if err != nil {
		s.completeLog(ctx, logID, "error", "", err.Error(), nil, "")
		return fmt.Errorf("generate trigger token: %w", err)
	}

	result, triggerErr := s.triggerer.TriggerSchedule(ctx, sched.BotID, TriggerPayload{
		ID:          sched.ID,
		Name:        sched.Name,
		Description: sched.Description,
		Pattern:     sched.Pattern,
		MaxCalls:    sched.MaxCalls,
		Command:     sched.Command,
		OwnerUserID: ownerUserID,
		SessionID:   sessionID,
	}, token)
	if triggerErr != nil {
		s.completeLog(ctx, logID, "error", "", triggerErr.Error(), nil, "")
		return triggerErr
	}

	s.completeLog(ctx, logID, result.Status, result.Text, "", result.UsageBytes, result.ModelID)
	s.logger.InfoContext(ctx, "schedule completed", slog.String("schedule_id", sched.ID), slog.String("status", result.Status))
	return nil
}

func (s *Service) completeLog(ctx context.Context, logID, status, resultText, errorMessage string, usageBytes []byte, modelID string) {
	if logID == "" {
		return
	}
	err := s.store.CompleteLog(ctx, schedulepersistence.CompleteLogCommand{
		ID:           logID,
		Status:       status,
		ResultText:   resultText,
		ErrorMessage: errorMessage,
		Usage:        usageBytes,
		ModelID:      modelID,
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "complete schedule log failed", slog.Any("error", err))
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

	rows, err := s.store.ListLogsByBot(ctx, schedulepersistence.LogPage{
		ID:     botID,
		Limit:  int32(limit),  //nolint:gosec // capped to 100 above
		Offset: int32(offset), //nolint:gosec // validated above
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]Log, 0, len(rows))
	for _, row := range rows {
		items = append(items, toScheduleLog(row))
	}
	return items, total, nil
}

func (s *Service) ListLogsBySchedule(ctx context.Context, scheduleID string, limit, offset int) ([]Log, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	total, err := s.store.CountLogsBySchedule(ctx, scheduleID)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.store.ListLogsBySchedule(ctx, schedulepersistence.LogPage{
		ID:     scheduleID,
		Limit:  int32(limit),  //nolint:gosec // capped to 100 above
		Offset: int32(offset), //nolint:gosec // validated above
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]Log, 0, len(rows))
	for _, row := range rows {
		items = append(items, toScheduleLogFromSchedule(row))
	}
	return items, total, nil
}

func (s *Service) DeleteLogs(ctx context.Context, botID string) error {
	return s.store.DeleteLogsByBot(ctx, botID)
}

func toScheduleLog(row schedulepersistence.LogRecord) Log {
	l := Log{
		ID:           row.ID,
		ScheduleID:   row.ScheduleID,
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

func toScheduleLogFromSchedule(row schedulepersistence.LogRecord) Log {
	return toScheduleLog(row)
}

// resolveBotOwner returns the owner user ID for the given bot.
func (s *Service) resolveBotOwner(ctx context.Context, botID string) (string, error) {
	bot, err := s.store.GetBot(ctx, botID)
	if err != nil {
		return "", fmt.Errorf("get bot: %w", err)
	}
	ownerID := bot.OwnerUserID
	if ownerID == "" {
		return "", errors.New("bot owner not found")
	}
	return ownerID, nil
}

// generateTriggerToken creates a short-lived JWT for schedule trigger callbacks.
func (s *Service) generateTriggerToken(userID string) (string, error) {
	if strings.TrimSpace(s.jwtSecret) == "" {
		return "", errors.New("jwt secret not configured")
	}
	signed, _, err := auth.GenerateToken(userID, s.jwtSecret, scheduleTokenTTL)
	if err != nil {
		return "", err
	}
	return "Bearer " + signed, nil
}

func (s *Service) scheduleJob(ctx context.Context, schedule schedulepersistence.Record) error {
	id := schedule.ID
	if id == "" {
		return errors.New("schedule id missing")
	}
	job := func() {
		runCtx, runCancel, ok := s.jobContext(scheduleRunTimeout)
		if !ok {
			return
		}
		defer runCancel()
		if err := s.runSchedule(runCtx, toSchedule(schedule)); err != nil {
			s.logger.ErrorContext(runCtx, "scheduled job failed", slog.String("schedule_id", schedule.ID), slog.Any("error", err))
		}
	}

	// Resolve bot timezone so cron expressions are interpreted in the bot's
	// configured timezone rather than the system default.
	loc := s.resolveBotLocation(ctx, schedule.BotID)
	sched, err := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor).Parse(schedule.Pattern)
	if err != nil {
		return err
	}
	entryID := s.cron.Schedule(newLocationSchedule(sched, loc), cron.FuncJob(job))
	s.mu.Lock()
	s.jobs[id] = entryID
	s.mu.Unlock()
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

func (s *Service) rescheduleJob(ctx context.Context, schedule schedulepersistence.Record) error {
	id := schedule.ID
	if id == "" {
		return nil
	}
	s.removeJob(id)
	if schedule.Enabled {
		return s.scheduleJob(ctx, schedule)
	}
	return nil
}

func (s *Service) removeJob(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entryID, ok := s.jobs[id]
	if ok {
		s.cron.Remove(entryID)
		delete(s.jobs, id)
	}
}

func toSchedule(row schedulepersistence.Record) Schedule {
	return Schedule(row)
}

// resolveBotLocation returns the bot's configured timezone location, falling
// back to the system default when the bot has no timezone set or the value is
// invalid.
func (s *Service) resolveBotLocation(ctx context.Context, botID string) *time.Location {
	if s.store == nil || strings.TrimSpace(botID) == "" {
		return s.defaultLocation
	}
	row, err := s.store.GetBot(ctx, botID)
	if err != nil {
		return s.defaultLocation
	}
	tz := strings.TrimSpace(row.Timezone)
	if tz == "" {
		return s.defaultLocation
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		s.logger.WarnContext(ctx, "invalid bot timezone for schedule, using default",
			slog.String("bot_id", botID),
			slog.String("timezone", tz),
			slog.Any("error", err),
		)
		return s.defaultLocation
	}
	return loc
}

// locationSchedule wraps a cron.Schedule to evaluate Next() in a specific
// timezone, regardless of the global cron location.
type locationSchedule struct {
	inner cron.Schedule
	loc   *time.Location
}

func newLocationSchedule(inner cron.Schedule, loc *time.Location) cron.Schedule {
	if loc == nil {
		return inner
	}
	return &locationSchedule{inner: inner, loc: loc}
}

func (s *locationSchedule) Next(t time.Time) time.Time {
	return s.inner.Next(t.In(s.loc))
}
