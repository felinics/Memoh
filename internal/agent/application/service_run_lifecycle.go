package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/runtimefence"
)

const (
	contextLifecycleStatusCompleted               = "completed"
	contextLifecycleStatusFailedProvider          = "failed_provider"
	contextLifecycleStatusAborted                 = "aborted"
	contextLifecycleWriteTimeout                  = 10 * time.Second
	contextLifecycleReconciliationBatchSize int32 = 100
)

type contextLifecycleStore interface {
	CreateContextLifecycle(context.Context, sqlc.CreateContextLifecycleParams) (sqlc.ContextLifecycle, error)
	GetContextLifecycleByRunID(context.Context, pgtype.UUID) (sqlc.ContextLifecycle, error)
	GetLatestAssistantContextLifecycleMetadataByRunID(context.Context, pgtype.UUID) ([]byte, error)
	UpdateAbortedContextLifecycleSnapshot(context.Context, sqlc.UpdateAbortedContextLifecycleSnapshotParams) (sqlc.ContextLifecycle, error)
	UpsertAbortedContextLifecycle(context.Context, sqlc.UpsertAbortedContextLifecycleParams) (sqlc.ContextLifecycle, error)
	UpsertTerminalContextLifecycle(context.Context, sqlc.UpsertTerminalContextLifecycleParams) (sqlc.ContextLifecycle, error)
	ListTerminalSessionRunsNeedingContextLifecycle(context.Context, int32) ([]sqlc.ListTerminalSessionRunsNeedingContextLifecycleRow, error)
}

type contextLifecycleCandidateKey struct {
	runID        string
	fencingToken int64
}

type contextLifecycleCandidateQuality uint8

const (
	contextLifecycleCandidateMinimal contextLifecycleCandidateQuality = iota
	contextLifecycleCandidateMetadata
	contextLifecycleCandidateAuthoritative
)

type contextLifecycleCandidate struct {
	botID     string
	sessionID string
	snapshot  []byte
	errorCode string
	quality   contextLifecycleCandidateQuality
}

func (s *Service) contextLifecycleTerminal(ctx context.Context, cfg native.RunConfig) func(error) {
	var once sync.Once
	return func(cause error) {
		once.Do(func() {
			s.persistRunContextLifecycle(ctx, cfg, cause)
		})
	}
}

func minimalContextLifecycleSnapshot() contextfrag.LifecycleSnapshot {
	return contextfrag.BuildLifecycleSnapshot(contextfrag.BuildManifest(nil))
}

// stageContextLifecycleCandidate keeps an admitted owner's content-light
// snapshot out of the diagnostic table until session_runs has accepted the
// same fencing token's terminal transition. Unfenced callers are direct runs
// with no durable ledger row and keep the immediate-write path below.
func (s *Service) stageContextLifecycleCandidate(
	ctx context.Context,
	runID, botID, sessionID string,
	snapshot *contextfrag.LifecycleSnapshot,
	cause error,
	quality contextLifecycleCandidateQuality,
) bool {
	if s == nil || s.contextLifecycles == nil {
		return false
	}
	fence, fenced := runtimefence.FromContext(nonNilContext(ctx))
	if !fenced {
		return false
	}
	status, _ := classifyContextLifecycleTerminal(ctx, cause)
	if snapshot == nil {
		s.recordContextLifecyclePersistenceError(
			errors.New("context lifecycle candidate snapshot is missing"),
			runID,
			botID,
			sessionID,
			status,
		)
		return true
	}
	if err := runtimefence.ValidateScope(ctx, botID, sessionID); err != nil {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
		return true
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		s.recordContextLifecyclePersistenceError(
			errors.New("context lifecycle candidate run id is missing"),
			runID,
			botID,
			sessionID,
			status,
		)
		return true
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
		return true
	}
	candidate := contextLifecycleCandidate{
		botID:     strings.TrimSpace(botID),
		sessionID: strings.TrimSpace(sessionID),
		snapshot:  append([]byte(nil), raw...),
		errorCode: string(apperror.CodeOf(cause)),
		quality:   quality,
	}
	key := contextLifecycleCandidateKey{runID: runID, fencingToken: fence.Token}
	s.contextLifecycleCandidatesMu.Lock()
	if s.contextLifecycleCandidates == nil {
		s.contextLifecycleCandidates = make(map[contextLifecycleCandidateKey]contextLifecycleCandidate)
	}
	existing, exists := s.contextLifecycleCandidates[key]
	if !exists || candidate.quality >= existing.quality {
		if candidate.errorCode == "" && exists {
			candidate.errorCode = existing.errorCode
		}
		s.contextLifecycleCandidates[key] = candidate
	} else if existing.errorCode == "" && candidate.errorCode != "" {
		existing.errorCode = candidate.errorCode
		s.contextLifecycleCandidates[key] = existing
	}
	s.contextLifecycleCandidatesMu.Unlock()
	return true
}

// EnsureTerminalContextLifecycle records a content-light fallback for runs
// that fail before native context assembly creates a snapshot. A terminal
// writer with an authoritative holder always wins this read-before-create race.
func (s *Service) EnsureTerminalContextLifecycle(
	ctx context.Context,
	runID, botID, sessionID string,
	cause error,
) {
	if s == nil || s.contextLifecycles == nil || contextLifecycleOwnershipLost(ctx, cause) {
		return
	}
	ctx = nonNilContext(ctx)
	snapshot := minimalContextLifecycleSnapshot()
	if s.stageContextLifecycleCandidate(
		ctx,
		runID,
		botID,
		sessionID,
		&snapshot,
		cause,
		contextLifecycleCandidateMinimal,
	) {
		return
	}
	status, _ := classifyContextLifecycleTerminal(ctx, cause)
	runUUID, botUUID, sessionUUID, err := parseContextLifecycleIDs(runID, botID, sessionID)
	if err != nil {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
		return
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), contextLifecycleWriteTimeout)
	defer cancel()
	existing, err := s.contextLifecycles.GetContextLifecycleByRunID(readCtx, runUUID)
	if err == nil {
		if existing.BotID != botUUID || existing.SessionID != sessionUUID {
			s.recordContextLifecyclePersistenceError(
				errors.New("existing context lifecycle identity does not match terminal fallback"),
				runID,
				botID,
				sessionID,
				status,
			)
		}
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
		return
	}
	s.persistContextLifecycleSnapshot(ctx, runID, botID, sessionID, &snapshot, cause, false)
}

func (s *Service) persistRunContextLifecycle(ctx context.Context, cfg native.RunConfig, cause error) {
	if cfg.ContextLifecycle == nil {
		return
	}
	snapshot, ok := cfg.ContextLifecycle.Snapshot()
	if !ok {
		return
	}
	s.persistContextLifecycleSnapshot(
		ctx,
		cfg.RunID,
		cfg.Identity.BotID,
		cfg.Identity.SessionID,
		&snapshot,
		cause,
		true,
	)
}

func (s *Service) recoverContextLifecycleFromAssistantMetadata(
	ctx context.Context,
	runID, botID, sessionID string,
	cause error,
) {
	if s == nil || s.contextLifecycles == nil || contextLifecycleOwnershipLost(ctx, cause) {
		return
	}
	ctx = nonNilContext(ctx)
	status, _ := classifyContextLifecycleTerminal(ctx, cause)
	runUUID, _, _, err := parseContextLifecycleIDs(runID, botID, sessionID)
	if err != nil {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
		return
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), contextLifecycleWriteTimeout)
	defer cancel()
	if _, err = s.contextLifecycles.GetContextLifecycleByRunID(readCtx, runUUID); err == nil {
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
		return
	}
	raw, ready, err := s.assistantContextLifecycleSnapshot(readCtx, runUUID)
	if err != nil {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
		return
	}
	if !ready {
		return
	}
	var snapshot contextfrag.LifecycleSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
		return
	}
	s.persistContextLifecycleSnapshot(ctx, runID, botID, sessionID, &snapshot, cause, false)
}

func (s *Service) persistContextLifecycleSnapshot(
	ctx context.Context,
	runID, botID, sessionID string,
	snapshot *contextfrag.LifecycleSnapshot,
	cause error,
	authoritative bool,
) {
	if s == nil || s.contextLifecycles == nil || snapshot == nil || contextLifecycleOwnershipLost(ctx, cause) {
		return
	}
	ctx = nonNilContext(ctx)
	quality := contextLifecycleCandidateMetadata
	if authoritative {
		quality = contextLifecycleCandidateAuthoritative
	}
	if s.stageContextLifecycleCandidate(ctx, runID, botID, sessionID, snapshot, cause, quality) {
		return
	}
	status, errorCode := classifyContextLifecycleTerminal(ctx, cause)
	runUUID, botUUID, sessionUUID, err := parseContextLifecycleIDs(runID, botID, sessionID)
	if err != nil {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
		return
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
		return
	}
	var code pgtype.Text
	if errorCode != "" {
		code = pgtype.Text{String: errorCode, Valid: true}
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), contextLifecycleWriteTimeout)
	defer cancel()
	_, err = s.contextLifecycles.CreateContextLifecycle(writeCtx, sqlc.CreateContextLifecycleParams{
		RunID:     runUUID,
		BotID:     botUUID,
		SessionID: sessionUUID,
		Status:    status,
		ErrorCode: code,
		Snapshot:  raw,
	})
	if err == nil {
		return
	}
	if !db.IsUniqueViolation(err) {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
		return
	}
	if !authoritative {
		return
	}
	_, err = s.contextLifecycles.UpdateAbortedContextLifecycleSnapshot(
		writeCtx,
		sqlc.UpdateAbortedContextLifecycleSnapshotParams{
			Snapshot:  raw,
			RunID:     runUUID,
			BotID:     botUUID,
			SessionID: sessionUUID,
		},
	)
	if err == nil {
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := s.contextLifecycles.GetContextLifecycleByRunID(writeCtx, runUUID)
		if getErr == nil && existing.BotID == botUUID && existing.SessionID == sessionUUID {
			return
		}
		if getErr != nil {
			err = getErr
		} else {
			err = errors.New("existing context lifecycle identity does not match terminal write")
		}
	}
	s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, status)
}

func (s *Service) reconcileTerminalContextLifecycle(ctx context.Context, run sessionruntime.TerminalRun) {
	if s == nil || s.contextLifecycles == nil {
		return
	}
	status, ok := contextLifecycleStatusForTerminalRun(run.State)
	if !ok {
		return
	}
	runUUID, botUUID, sessionUUID, err := parseContextLifecycleIDs(run.RunID, run.BotID, run.SessionID)
	if err != nil {
		s.clearContextLifecycleCandidates(run.RunID)
		s.recordContextLifecyclePersistenceError(err, run.RunID, run.BotID, run.SessionID, status)
		return
	}

	writeCtx, cancel := contextLifecycleBoundedContext(ctx)
	defer cancel()
	existing, getErr := s.contextLifecycles.GetContextLifecycleByRunID(writeCtx, runUUID)
	existingReady := getErr == nil
	if getErr != nil && !errors.Is(getErr, pgx.ErrNoRows) {
		s.recordContextLifecyclePersistenceError(getErr, run.RunID, run.BotID, run.SessionID, status)
		return
	}
	if existingReady && (existing.BotID != botUUID || existing.SessionID != sessionUUID) {
		s.clearContextLifecycleCandidates(run.RunID)
		s.recordContextLifecyclePersistenceError(
			errors.New("existing context lifecycle identity does not match terminal run"),
			run.RunID,
			run.BotID,
			run.SessionID,
			status,
		)
		return
	}

	candidate, candidateReady := s.contextLifecycleCandidateFor(run)
	if candidateReady && (candidate.botID != strings.TrimSpace(run.BotID) || candidate.sessionID != strings.TrimSpace(run.SessionID)) {
		s.recordContextLifecyclePersistenceError(
			errors.New("context lifecycle candidate identity does not match terminal run"),
			run.RunID,
			run.BotID,
			run.SessionID,
			status,
		)
		candidateReady = false
	}

	var (
		snapshot        []byte
		replaceSnapshot bool
	)
	switch {
	case candidateReady && candidate.quality > contextLifecycleCandidateMinimal:
		snapshot = append([]byte(nil), candidate.snapshot...)
		replaceSnapshot = true
	case existingReady:
		snapshot = append([]byte(nil), existing.Snapshot...)
	case !existingReady:
		recovered, ready, recoverErr := s.assistantContextLifecycleSnapshot(writeCtx, runUUID)
		if recoverErr != nil {
			s.recordContextLifecyclePersistenceError(recoverErr, run.RunID, run.BotID, run.SessionID, status)
			return
		}
		switch {
		case ready:
			snapshot = recovered
			replaceSnapshot = true
		case candidateReady:
			snapshot = append([]byte(nil), candidate.snapshot...)
		default:
			snapshot, err = json.Marshal(minimalContextLifecycleSnapshot())
			if err != nil {
				s.recordContextLifecyclePersistenceError(err, run.RunID, run.BotID, run.SessionID, status)
				return
			}
		}
	}

	errorCode := terminalContextLifecycleErrorCode(run, status, candidate, candidateReady, existing, existingReady)
	var code pgtype.Text
	if errorCode != "" {
		code = pgtype.Text{String: errorCode, Valid: true}
	}
	_, err = s.contextLifecycles.UpsertTerminalContextLifecycle(writeCtx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:            runUUID,
		BotID:            botUUID,
		SessionID:        sessionUUID,
		Status:           status,
		ErrorCode:        code,
		Snapshot:         snapshot,
		ReplaceSnapshot:  replaceSnapshot,
		ReplaceErrorCode: candidateReady && candidate.errorCode != "",
	})
	if err == nil {
		s.clearContextLifecycleCandidates(run.RunID)
		return
	}
	// A connection can fail after PostgreSQL committed the upsert. Verify the
	// authoritative identity and status before retaining the candidate for the
	// elected repair pass.
	confirmed, confirmErr := s.contextLifecycles.GetContextLifecycleByRunID(writeCtx, runUUID)
	if confirmErr == nil && confirmed.BotID == botUUID && confirmed.SessionID == sessionUUID && confirmed.Status == status {
		s.clearContextLifecycleCandidates(run.RunID)
		return
	}
	s.recordContextLifecyclePersistenceError(err, run.RunID, run.BotID, run.SessionID, status)
}

func (s *Service) reconcileTerminalContextLifecycles(ctx context.Context) error {
	if s == nil || s.contextLifecycles == nil {
		return nil
	}
	repairCtx, cancel := contextLifecycleBoundedContext(ctx)
	defer cancel()
	rows, err := s.contextLifecycles.ListTerminalSessionRunsNeedingContextLifecycle(
		repairCtx,
		contextLifecycleReconciliationBatchSize,
	)
	if err != nil {
		return fmt.Errorf("list terminal runs needing context lifecycle: %w", err)
	}
	for _, row := range rows {
		if err := repairCtx.Err(); err != nil {
			return fmt.Errorf("reconcile terminal context lifecycles: %w", err)
		}
		errorCode := ""
		if row.ErrorCode.Valid {
			errorCode = row.ErrorCode.String
		}
		s.reconcileTerminalContextLifecycle(repairCtx, sessionruntime.TerminalRun{
			RunID:        pgUUIDString(row.RunID),
			BotID:        pgUUIDString(row.BotID),
			SessionID:    pgUUIDString(row.SessionID),
			FencingToken: row.FencingToken,
			State:        row.State,
			ErrorCode:    errorCode,
		})
	}
	if err := repairCtx.Err(); err != nil {
		return fmt.Errorf("reconcile terminal context lifecycles: %w", err)
	}
	return nil
}

func contextLifecycleBoundedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx = nonNilContext(ctx)
	timeout := contextLifecycleWriteTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (s *Service) contextLifecycleCandidateFor(run sessionruntime.TerminalRun) (contextLifecycleCandidate, bool) {
	key := contextLifecycleCandidateKey{
		runID:        strings.TrimSpace(run.RunID),
		fencingToken: run.FencingToken,
	}
	s.contextLifecycleCandidatesMu.Lock()
	candidate, ok := s.contextLifecycleCandidates[key]
	s.contextLifecycleCandidatesMu.Unlock()
	if ok {
		candidate.snapshot = append([]byte(nil), candidate.snapshot...)
	}
	return candidate, ok
}

func (s *Service) clearContextLifecycleCandidates(runID string) {
	runID = strings.TrimSpace(runID)
	s.contextLifecycleCandidatesMu.Lock()
	for key := range s.contextLifecycleCandidates {
		if key.runID == runID {
			delete(s.contextLifecycleCandidates, key)
		}
	}
	s.contextLifecycleCandidatesMu.Unlock()
}

func contextLifecycleStatusForTerminalRun(state string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "completed":
		return contextLifecycleStatusCompleted, true
	case "aborted":
		return contextLifecycleStatusAborted, true
	case "failed", "lost":
		return contextLifecycleStatusFailedProvider, true
	default:
		return "", false
	}
}

func terminalContextLifecycleErrorCode(
	run sessionruntime.TerminalRun,
	status string,
	candidate contextLifecycleCandidate,
	candidateReady bool,
	existing sqlc.ContextLifecycle,
	existingReady bool,
) string {
	if status != contextLifecycleStatusFailedProvider {
		return ""
	}
	if candidateReady && candidate.errorCode != "" {
		return candidate.errorCode
	}
	if existingReady && existing.Status == status && existing.ErrorCode.Valid {
		return existing.ErrorCode.String
	}
	return strings.TrimSpace(run.ErrorCode)
}

func classifyContextLifecycleTerminal(ctx context.Context, cause error) (string, string) {
	if cause == nil {
		return contextLifecycleStatusCompleted, ""
	}
	privateCause := apperror.CauseOf(cause)
	explicitlyCanceled := errors.Is(context.Cause(nonNilContext(ctx)), context.Canceled) &&
		(errors.Is(cause, context.Canceled) || errors.Is(privateCause, context.Canceled))
	if explicitlyCanceled {
		return contextLifecycleStatusAborted, ""
	}
	return contextLifecycleStatusFailedProvider, string(apperror.CodeOf(cause))
}

func contextLifecycleOwnershipLost(ctx context.Context, cause error) bool {
	return errors.Is(context.Cause(nonNilContext(ctx)), sessionruntime.ErrRunOwnershipLost) ||
		errors.Is(cause, sessionruntime.ErrRunOwnershipLost) ||
		errors.Is(apperror.CauseOf(cause), sessionruntime.ErrRunOwnershipLost) ||
		errors.Is(cause, sessionruntime.ErrCommandTargetNotActive) ||
		errors.Is(apperror.CauseOf(cause), sessionruntime.ErrCommandTargetNotActive)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func parseContextLifecycleIDs(runID, botID, sessionID string) (pgtype.UUID, pgtype.UUID, pgtype.UUID, error) {
	runUUID, err := db.ParseUUID(runID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, err
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, err
	}
	sessionUUID, err := db.ParseUUID(sessionID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, err
	}
	return runUUID, botUUID, sessionUUID, nil
}

func (s *Service) recordContextLifecyclePersistenceError(
	err error,
	runID, botID, sessionID, status string,
) {
	count := s.contextLifecyclePersistenceErrors.Add(1)
	if s.logger == nil {
		return
	}
	s.logger.Error("persist context lifecycle failed",
		slog.Any("error", err),
		slog.String("run_id", runID),
		slog.String("bot_id", botID),
		slog.String("session_id", sessionID),
		slog.String("status", status),
		slog.Uint64("failure_count", count),
	)
}
