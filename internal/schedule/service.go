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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/robfig/cron/v3"

	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/boot"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimekind"
	"github.com/felinics/memoh/internal/workdir"
)

// SessionSpec describes the session one schedule fire runs in. The creator
// (wired in the composition root) resolves the workdir path and assembles
// ACP runtime metadata; the schedule domain only states intent.
type SessionSpec struct {
	BotID string
	// BotAgentID is empty for Native and set for a persisted Agent selection.
	BotAgentID string
	// Title labels the session in user-facing lists (the schedule name).
	Title string
	// RuntimeType selects the Native or External Agent runtime.
	RuntimeType string
	// ACPAgentID names the agent when RuntimeType is RuntimeACPAgent.
	ACPAgentID string
	// WorkdirID optionally binds the session to a bot workdir.
	WorkdirID string
	// OwnerUserID becomes the session creator and, for External Agent sessions, the
	// runtime owner account.
	OwnerUserID string
}

// SessionCreator creates sessions for schedule runs.
type SessionCreator interface {
	CreateSession(ctx context.Context, botID, sessionType string) (string, error)
	// CreateScheduleSession creates the user-visible session one fire of a
	// schedule runs in, honoring the schedule's runtime and workdir.
	CreateScheduleSession(ctx context.Context, spec SessionSpec) (string, error)
}

// WorkdirValidator is the slice of the workdir domain the schedule service
// needs to validate a workdir binding at create/update time.
type WorkdirValidator interface {
	RequireActive(ctx context.Context, botID, workdirID string) (workdir.Workdir, error)
}

type Service struct {
	queries         dbstore.Queries
	cron            *cron.Cron
	parser          cron.Parser
	triggerer       Triggerer
	sessionCreator  SessionCreator
	workdirs        WorkdirValidator
	botAgents       *botagents.Service
	jwtSecret       string
	logger          *slog.Logger
	defaultLocation *time.Location
	mu              sync.Mutex
	jobs            map[string]cron.EntryID
}

func (s *Service) SetBotAgents(service *botagents.Service) {
	s.botAgents = service
}

func NewService(log *slog.Logger, queries dbstore.Queries, triggerer Triggerer, sessionCreator SessionCreator, workdirService *workdir.Service, runtimeConfig *boot.RuntimeConfig) *Service {
	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	location := time.UTC
	if runtimeConfig != nil && runtimeConfig.TimezoneLocation != nil {
		location = runtimeConfig.TimezoneLocation
	}
	c := cron.New(cron.WithParser(parser), cron.WithLocation(location))
	var workdirs WorkdirValidator
	if workdirService != nil {
		workdirs = workdirService
	}
	service := &Service{
		queries:         queries,
		cron:            c,
		parser:          parser,
		triggerer:       triggerer,
		sessionCreator:  sessionCreator,
		workdirs:        workdirs,
		jwtSecret:       runtimeConfig.JwtSecret,
		logger:          log.With(slog.String("service", "schedule")),
		defaultLocation: location,
		jobs:            map[string]cron.EntryID{},
	}
	c.Start()
	return service
}

func (s *Service) Bootstrap(ctx context.Context) error {
	if s.queries == nil {
		return errors.New("schedule queries not configured")
	}
	items, err := s.queries.ListEnabledSchedules(ctx)
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
	if s.queries == nil {
		return Schedule{}, errors.New("schedule queries not configured")
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Description) == "" || strings.TrimSpace(req.Pattern) == "" || strings.TrimSpace(req.Command) == "" {
		return Schedule{}, errors.New("name, description, pattern, command are required")
	}
	if _, err := s.parser.Parse(req.Pattern); err != nil {
		return Schedule{}, fmt.Errorf("invalid cron pattern: %w", err)
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return Schedule{}, err
	}
	maxCalls := pgtype.Int4{Valid: false}
	if req.MaxCalls.Set && req.MaxCalls.Value != nil {
		if *req.MaxCalls.Value < math.MinInt32 || *req.MaxCalls.Value > math.MaxInt32 {
			return Schedule{}, fmt.Errorf("max_calls out of range: %d", *req.MaxCalls.Value)
		}
		maxCalls = pgtype.Int4{Int32: int32(*req.MaxCalls.Value), Valid: true} //nolint:gosec // bounds checked above
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	exec, err := s.normalizeExecution(ctx, botID, req.ExecutionConfig)
	if err != nil {
		return Schedule{}, err
	}
	row, err := s.queries.CreateSchedule(ctx, sqlc.CreateScheduleParams{
		Name:            req.Name,
		Description:     req.Description,
		Pattern:         req.Pattern,
		MaxCalls:        maxCalls,
		Enabled:         enabled,
		Command:         req.Command,
		BotID:           pgBotID,
		RunTarget:       exec.RunTarget,
		TargetSessionID: db.ParseUUIDOrEmpty(exec.TargetSessionID),
		RuntimeType:     optionalText(exec.RuntimeType),
		BotAgentID:      db.ParseUUIDOrEmpty(exec.BotAgentID),
		AcpAgentID:      optionalText(exec.ACPAgentID),
		ModelID:         db.ParseUUIDOrEmpty(exec.ModelID),
		AcpModelID:      optionalText(exec.ACPModelID),
		ReasoningEffort: optionalText(exec.ReasoningEffort),
		WorkdirID:       db.ParseUUIDOrEmpty(exec.WorkdirID),
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
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return Schedule{}, err
	}
	row, err := s.queries.GetScheduleByID(ctx, pgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Schedule{}, errors.New("schedule not found")
		}
		return Schedule{}, err
	}
	return toSchedule(row), nil
}

func (s *Service) List(ctx context.Context, botID string) ([]Schedule, error) {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSchedulesByBot(ctx, pgBotID)
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
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return Schedule{}, err
	}
	existing, err := s.queries.GetScheduleByID(ctx, pgID)
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
			maxCalls = pgtype.Int4{Valid: false}
		} else {
			if *req.MaxCalls.Value < math.MinInt32 || *req.MaxCalls.Value > math.MaxInt32 {
				return Schedule{}, fmt.Errorf("max_calls out of range: %d", *req.MaxCalls.Value)
			}
			maxCalls = pgtype.Int4{Int32: int32(*req.MaxCalls.Value), Valid: true} //nolint:gosec // bounds checked above
		}
	}
	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	// Validate only when the caller sends a new execution block. The stored
	// block is deliberately not re-validated on unrelated updates: its
	// references may have gone stale (deleted target session, archived
	// workdir), and blocking a rename or disable on that would prevent the
	// exact fixes a user reaches for; the trigger path reports stale
	// references at fire time instead.
	exec := executionFromRow(existing)
	if req.Execution != nil {
		normalized, execErr := s.normalizeExecution(ctx, existing.BotID.String(), *req.Execution)
		if execErr != nil {
			return Schedule{}, execErr
		}
		exec = normalized
	}
	updated, err := s.queries.UpdateSchedule(ctx, sqlc.UpdateScheduleParams{
		ID:              pgID,
		Name:            name,
		Description:     description,
		Pattern:         pattern,
		MaxCalls:        maxCalls,
		Enabled:         enabled,
		Command:         command,
		RunTarget:       exec.RunTarget,
		TargetSessionID: db.ParseUUIDOrEmpty(exec.TargetSessionID),
		RuntimeType:     optionalText(exec.RuntimeType),
		BotAgentID:      db.ParseUUIDOrEmpty(exec.BotAgentID),
		AcpAgentID:      optionalText(exec.ACPAgentID),
		ModelID:         db.ParseUUIDOrEmpty(exec.ModelID),
		AcpModelID:      optionalText(exec.ACPModelID),
		ReasoningEffort: optionalText(exec.ReasoningEffort),
		WorkdirID:       db.ParseUUIDOrEmpty(exec.WorkdirID),
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
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	if err := s.queries.DeleteSchedule(ctx, pgID); err != nil {
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

// scheduleAgentRunTimeout is the cap for External Agent runs and existing
// sessions whose runtime is resolved only when the fire starts.
const scheduleAgentRunTimeout = 30 * time.Minute

// runTimeoutFor picks the execution cap for one fire. Existing-session
// schedules get the generous cap because the pinned session may run an
// External Agent.
func runTimeoutFor(sched Schedule) time.Duration {
	if runtimekind.IsExternal(sched.RuntimeType) || sched.RunTarget == RunTargetExistingSession {
		return scheduleAgentRunTimeout
	}
	return scheduleRunTimeout
}

func (s *Service) runSchedule(ctx context.Context, sched Schedule) error {
	if s.triggerer == nil {
		return errors.New("schedule triggerer not configured")
	}
	updated, err := s.queries.IncrementScheduleCalls(ctx, toUUID(sched.ID))
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

	sessionID, sessionErr := s.resolveRunSession(ctx, sched, ownerUserID)

	pgScheduleID := toUUID(sched.ID)
	pgBotID := toUUID(sched.BotID)

	logRow, err := s.queries.CreateScheduleLog(ctx, sqlc.CreateScheduleLogParams{
		ScheduleID: pgScheduleID,
		BotID:      pgBotID,
		SessionID:  db.ParseUUIDOrEmpty(sessionID),
	})
	if err != nil {
		s.logger.Error("create schedule log failed", slog.String("schedule_id", sched.ID), slog.Any("error", err))
	}

	if errors.Is(sessionErr, ErrTargetSessionGone) {
		// The session this schedule was pinned to no longer exists. Every
		// future fire would fail identically, so disable the schedule after
		// recording one error log instead of failing every tick.
		s.completeLog(ctx, logRow.ID, "error", "", sessionErr.Error(), nil, pgtype.UUID{})
		s.disableGoneSchedule(ctx, sched.ID)
		return sessionErr
	}
	if sessionErr != nil {
		s.completeLog(ctx, logRow.ID, "error", "", sessionErr.Error(), nil, pgtype.UUID{})
		return sessionErr
	}

	token, err := s.generateTriggerToken(ownerUserID)
	if err != nil {
		s.completeLog(ctx, logRow.ID, "error", "", err.Error(), nil, pgtype.UUID{})
		return fmt.Errorf("generate trigger token: %w", err)
	}

	result, triggerErr := s.triggerer.TriggerSchedule(ctx, sched.BotID, TriggerPayload{
		ID:              sched.ID,
		Name:            sched.Name,
		Description:     sched.Description,
		Pattern:         sched.Pattern,
		MaxCalls:        sched.MaxCalls,
		Command:         sched.Command,
		OwnerUserID:     ownerUserID,
		SessionID:       sessionID,
		ModelID:         sched.ModelID,
		ACPModelID:      sched.ACPModelID,
		ReasoningEffort: sched.ReasoningEffort,
	}, token)
	if triggerErr != nil {
		s.completeLog(ctx, logRow.ID, "error", "", triggerErr.Error(), nil, pgtype.UUID{})
		return triggerErr
	}

	modelID := db.ParseUUIDOrEmpty(result.ModelID)
	s.completeLog(ctx, logRow.ID, result.Status, result.Text, "", result.UsageBytes, modelID)
	s.logger.Info("schedule completed", slog.String("schedule_id", sched.ID), slog.String("status", result.Status))
	return nil
}

// resolveRunSession decides which session this fire runs in. new_session
// mode creates a fresh user-visible session honoring the schedule's runtime
// and workdir; existing_session mode re-checks the pinned session and maps
// a vanished target to ErrTargetSessionGone so the caller can disable the
// schedule.
func (s *Service) resolveRunSession(ctx context.Context, sched Schedule, ownerUserID string) (string, error) {
	if sched.RunTarget == RunTargetExistingSession {
		target := strings.TrimSpace(sched.TargetSessionID)
		if target == "" {
			// The FK degraded the reference to NULL when the session was
			// hard-deleted (or it was never written, which validation
			// prevents).
			return "", ErrTargetSessionGone
		}
		pgSessionID, err := db.ParseUUID(target)
		if err != nil {
			return "", fmt.Errorf("invalid target session id: %w", err)
		}
		sess, err := s.queries.GetSessionByID(ctx, pgSessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Soft-deleted sessions keep their row, so the FK cannot
				// catch this path.
				return "", ErrTargetSessionGone
			}
			return "", err
		}
		if sess.BotID.String() != sched.BotID {
			return "", ErrTargetSessionGone
		}
		return target, nil
	}
	if s.sessionCreator == nil {
		return "", errors.New("schedule session creator not configured")
	}
	if strings.TrimSpace(sched.BotAgentID) != "" {
		resolved, err := s.resolveBotAgentExecution(ctx, sched.BotID, sched.ExecutionConfig)
		if err != nil {
			return "", err
		}
		sched.ExecutionConfig = resolved
	}
	sessionID, err := s.sessionCreator.CreateScheduleSession(ctx, SessionSpec{
		BotID:       sched.BotID,
		BotAgentID:  sched.BotAgentID,
		Title:       sched.Name,
		RuntimeType: sched.RuntimeType,
		ACPAgentID:  sched.ACPAgentID,
		WorkdirID:   sched.WorkdirID,
		OwnerUserID: ownerUserID,
	})
	if err != nil {
		return "", fmt.Errorf("create schedule session: %w", err)
	}
	return sessionID, nil
}

// disableGoneSchedule turns off a schedule whose target session vanished and
// unhooks its cron job.
func (s *Service) disableGoneSchedule(ctx context.Context, scheduleID string) {
	if _, err := s.queries.DisableSchedule(ctx, toUUID(scheduleID)); err != nil {
		s.logger.Error("disable schedule with deleted target session failed",
			slog.String("schedule_id", scheduleID), slog.Any("error", err))
		return
	}
	s.removeJob(scheduleID)
	s.logger.Warn("schedule disabled: target session was deleted", slog.String("schedule_id", scheduleID))
}

func (s *Service) completeLog(ctx context.Context, logID pgtype.UUID, status, resultText, errorMessage string, usageBytes []byte, modelID pgtype.UUID) {
	if !logID.Valid {
		return
	}
	_, err := s.queries.CompleteScheduleLog(ctx, sqlc.CompleteScheduleLogParams{
		ID:           logID,
		Status:       status,
		ResultText:   resultText,
		ErrorMessage: errorMessage,
		Usage:        usageBytes,
		ModelID:      modelID,
	})
	if err != nil {
		s.logger.Error("complete schedule log failed", slog.Any("error", err))
	}
}

func (s *Service) ListLogs(ctx context.Context, botID string, limit, offset int) ([]Log, int64, error) {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	total, err := s.queries.CountScheduleLogsByBot(ctx, pgBotID)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.queries.ListScheduleLogsByBot(ctx, sqlc.ListScheduleLogsByBotParams{
		BotID:  pgBotID,
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
	pgID, err := db.ParseUUID(scheduleID)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	total, err := s.queries.CountScheduleLogsBySchedule(ctx, pgID)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.queries.ListScheduleLogsBySchedule(ctx, sqlc.ListScheduleLogsByScheduleParams{
		ScheduleID: pgID,
		Limit:      int32(limit),  //nolint:gosec // capped to 100 above
		Offset:     int32(offset), //nolint:gosec // validated above
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
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.DeleteScheduleLogsByBot(ctx, pgBotID)
}

func toScheduleLog(row sqlc.ListScheduleLogsByBotRow) Log {
	l := Log{
		ID:           row.ID.String(),
		ScheduleID:   row.ScheduleID.String(),
		BotID:        row.BotID.String(),
		SessionID:    row.SessionID.String(),
		Status:       row.Status,
		ResultText:   row.ResultText,
		ErrorMessage: row.ErrorMessage,
	}
	if row.StartedAt.Valid {
		l.StartedAt = row.StartedAt.Time
	}
	if row.CompletedAt.Valid {
		t := row.CompletedAt.Time
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

func toScheduleLogFromSchedule(row sqlc.ListScheduleLogsByScheduleRow) Log {
	l := Log{
		ID:           row.ID.String(),
		ScheduleID:   row.ScheduleID.String(),
		BotID:        row.BotID.String(),
		SessionID:    row.SessionID.String(),
		Status:       row.Status,
		ResultText:   row.ResultText,
		ErrorMessage: row.ErrorMessage,
	}
	if row.StartedAt.Valid {
		l.StartedAt = row.StartedAt.Time
	}
	if row.CompletedAt.Valid {
		t := row.CompletedAt.Time
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

// resolveBotOwner returns the owner user ID for the given bot.
func (s *Service) resolveBotOwner(ctx context.Context, botID string) (string, error) {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return "", err
	}
	bot, err := s.queries.GetBotByID(ctx, pgBotID)
	if err != nil {
		return "", fmt.Errorf("get bot: %w", err)
	}
	ownerID := bot.OwnerUserID.String()
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

func (s *Service) scheduleJob(ctx context.Context, schedule sqlc.Schedule) error {
	id := schedule.ID.String()
	if id == "" {
		return errors.New("schedule id missing")
	}
	job := func() {
		item := toSchedule(schedule)
		runCtx, runCancel := context.WithTimeout(context.WithoutCancel(ctx), runTimeoutFor(item))
		defer runCancel()
		if err := s.runSchedule(runCtx, item); err != nil {
			s.logger.Error("scheduled job failed", slog.String("schedule_id", schedule.ID.String()), slog.Any("error", err))
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

func (s *Service) rescheduleJob(ctx context.Context, schedule sqlc.Schedule) error {
	id := schedule.ID.String()
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

func toSchedule(row sqlc.Schedule) Schedule {
	item := Schedule{
		ID:              row.ID.String(),
		Name:            row.Name,
		Description:     row.Description,
		Pattern:         row.Pattern,
		CurrentCalls:    int(row.CurrentCalls),
		Enabled:         row.Enabled,
		Command:         row.Command,
		BotID:           row.BotID.String(),
		ExecutionConfig: executionFromRow(row),
	}
	if row.MaxCalls.Valid {
		maxCalls := int(row.MaxCalls.Int32)
		item.MaxCalls = &maxCalls
	}
	if row.CreatedAt.Valid {
		item.CreatedAt = row.CreatedAt.Time
	}
	if row.UpdatedAt.Valid {
		item.UpdatedAt = row.UpdatedAt.Time
	}
	return item
}

func executionFromRow(row sqlc.Schedule) ExecutionConfig {
	exec := ExecutionConfig{
		RunTarget:       row.RunTarget,
		RuntimeType:     row.RuntimeType.String,
		ACPAgentID:      row.AcpAgentID.String,
		ACPModelID:      row.AcpModelID.String,
		ReasoningEffort: row.ReasoningEffort.String,
	}
	if row.BotAgentID.Valid {
		exec.BotAgentID = row.BotAgentID.String()
	}
	if row.TargetSessionID.Valid {
		exec.TargetSessionID = row.TargetSessionID.String()
	}
	if row.ModelID.Valid {
		exec.ModelID = row.ModelID.String()
	}
	if row.WorkdirID.Valid {
		exec.WorkdirID = row.WorkdirID.String()
	}
	return exec
}

func optionalText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func toUUID(id string) pgtype.UUID {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgID
}

// resolveBotLocation returns the bot's configured timezone location, falling
// back to the system default when the bot has no timezone set or the value is
// invalid.
func (s *Service) resolveBotLocation(ctx context.Context, botID pgtype.UUID) *time.Location {
	if s.queries == nil || !botID.Valid {
		return s.defaultLocation
	}
	row, err := s.queries.GetBotByID(ctx, botID)
	if err != nil {
		return s.defaultLocation
	}
	if !row.Timezone.Valid {
		return s.defaultLocation
	}
	tz := strings.TrimSpace(row.Timezone.String)
	if tz == "" {
		return s.defaultLocation
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		s.logger.Warn("invalid bot timezone for schedule, using default",
			slog.String("bot_id", botID.String()),
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
