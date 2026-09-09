package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/apperror"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const (
	contextLifecycleDefaultLimit = 50
	contextLifecycleMaxLimit     = 200
)

type ContextLifecycleResponse struct {
	Turns      []ContextLifecycleTurn     `json:"turns"`
	Aggregates ContextLifecycleAggregates `json:"aggregates"`
	// Limit is the page bound the turns and aggregates were computed over.
	Limit int `json:"limit"`
	// HasMore reports whether older lifecycle turns exist beyond this page.
	HasMore bool `json:"has_more"`
	// LegacySource reports that turns were recovered from pre-run-table
	// assistant metadata instead of the run-keyed lifecycle table.
	LegacySource bool `json:"legacy_source,omitempty"`
	// LegacyHistoryMayExist reports that pre-run-table assistant metadata also
	// exists for this session while the run-keyed table served the page, so
	// this response does not cover the session's full history era.
	LegacyHistoryMayExist bool `json:"legacy_history_may_exist,omitempty"`
	// AggregateScope is always "returned_page": aggregates cover the returned
	// turns, never the whole session.
	AggregateScope string `json:"aggregate_scope"`
}

const contextLifecycleAggregateScope = "returned_page"

// ContextLifecycleTurn is one persisted lifecycle snapshot, newest first.
type ContextLifecycleTurn struct {
	RunID              string                        `json:"run_id"`
	Status             string                        `json:"status,omitempty"`
	ErrorCode          string                        `json:"error_code,omitempty"`
	AssistantMessageID string                        `json:"assistant_message_id,omitempty"`
	CreatedAt          time.Time                     `json:"created_at"`
	Snapshot           contextfrag.LifecycleSnapshot `json:"snapshot"`
}

// ContextLifecycleAggregates sums facts observed on the returned page at
// Memoh's own boundary: native runs report SDK/provider usage, while ACP runs
// expose only protocol-level input, so an ACP zero means "not observable
// here", not "measured zero". Derived cache-comparison ratios and tool-roster
// churn are intentionally absent until a durable comparator exists.
type ContextLifecycleAggregates struct {
	Turns                 int            `json:"turns"`
	TotalCacheReadTokens  int            `json:"total_cache_read_tokens"`
	TotalCacheWriteTokens int            `json:"total_cache_write_tokens"`
	DropReasons           map[string]int `json:"drop_reasons,omitempty"`
	MutationKinds         map[string]int `json:"mutation_kinds,omitempty"`
}

// GetSessionContextLifecycle godoc
// @Summary Get session context lifecycle
// @Description List run-keyed context lifecycle snapshots for a chat session, newest first, with page-scoped aggregate totals (cache read/write tokens, drop reasons, mutation kinds). Aggregates cover only the returned page; has_more reports older turns. Sessions predating run lifecycle persistence fall back to legacy assistant metadata (legacy_source). Per-fragment selection_decisions are never returned; each turn's selection trace carries their rolled-up counts and token costs
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param limit query int false "Maximum number of turns to return (default 50, max 200)"
// @Success 200 {object} ContextLifecycleResponse
// @Failure 400 {object} apperror.Problem
// @Failure 401 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/context-lifecycle [get].
func (h *SessionInfoHandler) GetSessionContextLifecycle(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return mapContextLifecycleError(err)
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
	}
	pgSessionID, err := db.ParseUUID(sessionID)
	if err != nil {
		return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
	}

	ctx := c.Request().Context()
	sessionRow, err := h.queries.GetSessionByID(ctx, pgSessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperror.New(apperror.CodeContextLifecycleNotFound, nil)
		}
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	sessionMode, runtimeType := normalizedSessionDescriptor(session.Thread{
		Type:        sessionRow.Type,
		SessionMode: sessionRow.SessionMode,
		RuntimeType: sessionRow.RuntimeType,
	})
	bot, err := AuthorizeBotAccessWithPermission(
		ctx,
		h.botService,
		h.accountService,
		userID,
		botID,
		requiredReadPermissionForSessionRuntime(sessionMode, runtimeType),
	)
	if err != nil {
		return mapContextLifecycleError(err)
	}
	if sessionRow.BotID.String() != bot.ID {
		return apperror.New(apperror.CodeContextLifecycleNotFound, nil)
	}
	perms, err := h.resolveCurrentUserPermissions(c, userID, bot.ID)
	if err != nil {
		return mapContextLifecycleError(err)
	}
	sess := session.Thread{
		ID:          sessionRow.ID.String(),
		BotID:       sessionRow.BotID.String(),
		Type:        sessionRow.Type,
		SessionMode: sessionMode,
		RuntimeType: runtimeType,
	}
	if sessionRow.CreatedByUserID.Valid {
		sess.CreatedByUserID = sessionRow.CreatedByUserID.String()
	}
	if !canAccessSession(sess, userID, perms) {
		return apperror.New(apperror.CodeContextLifecycleNotFound, nil)
	}

	limit := contextLifecycleLimit(c)
	load, err := loadContextLifecycleTurns(ctx, h.queries, pgSessionID, limit)
	if err != nil {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	return c.JSON(http.StatusOK, ContextLifecycleResponse{
		Turns:                 load.Turns,
		Aggregates:            aggregateContextLifecycle(load.Turns),
		Limit:                 limit,
		HasMore:               load.HasMore,
		LegacySource:          load.LegacySource,
		LegacyHistoryMayExist: load.LegacyHistoryMayExist,
		AggregateScope:        contextLifecycleAggregateScope,
	})
}

func mapContextLifecycleError(err error) error {
	if err == nil {
		return nil
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	switch httpErr.Code {
	case http.StatusBadRequest:
		return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
	case http.StatusUnauthorized:
		return apperror.New(apperror.CodeContextLifecycleAuthenticationRequired, nil)
	case http.StatusForbidden:
		return apperror.New(apperror.CodeContextLifecycleAccessDenied, nil)
	case http.StatusNotFound:
		return apperror.New(apperror.CodeContextLifecycleNotFound, nil)
	default:
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
}

func contextLifecycleLimit(c echo.Context) int {
	limit := contextLifecycleDefaultLimit
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > contextLifecycleMaxLimit {
		limit = contextLifecycleMaxLimit
	}
	return limit
}

type contextLifecycleQueries interface {
	ListRecentContextLifecyclesBySession(
		context.Context,
		sqlc.ListRecentContextLifecyclesBySessionParams,
	) ([]sqlc.ListRecentContextLifecyclesBySessionRow, error)
	ListRecentAssistantMessagesBySession(
		context.Context,
		sqlc.ListRecentAssistantMessagesBySessionParams,
	) ([]sqlc.ListRecentAssistantMessagesBySessionRow, error)
	HasUnmaterializedContextLifecycleMetadataBySession(ctx context.Context, sessionID pgtype.UUID) (bool, error)
	GetLatestContextLifecycleBySession(ctx context.Context, sessionID pgtype.UUID) ([]byte, error)
}

type contextLifecycleLoad struct {
	Turns []ContextLifecycleTurn
	// LegacySource reports that every returned turn came from pre-run-table
	// assistant metadata.
	LegacySource bool
	// HasMore reports that the chosen source holds older rows beyond the page.
	HasMore bool
	// LegacyHistoryMayExist reports that run-keyed rows were returned while
	// older pre-run-table metadata also exists for the session, so the page
	// does not cover the session's full history era.
	LegacyHistoryMayExist bool
}

func loadContextLifecycleTurns(
	ctx context.Context,
	queries contextLifecycleQueries,
	sessionID pgtype.UUID,
	limit int,
) (contextLifecycleLoad, error) {
	probe := limit + 1
	rows, err := queries.ListRecentContextLifecyclesBySession(ctx, sqlc.ListRecentContextLifecyclesBySessionParams{
		SessionID: sessionID,
		MaxCount:  int32(probe), //nolint:gosec // G115: limit is bounded to contextLifecycleMaxLimit
	})
	if err != nil {
		return contextLifecycleLoad{}, fmt.Errorf("list run lifecycles: %w", err)
	}
	if len(rows) > 0 {
		turns, err := lifecycleTurnsFromRunRows(rows, limit)
		if err != nil {
			return contextLifecycleLoad{}, err
		}
		unmaterialized, err := queries.HasUnmaterializedContextLifecycleMetadataBySession(ctx, sessionID)
		if err != nil {
			return contextLifecycleLoad{}, fmt.Errorf("probe unmaterialized legacy lifecycles: %w", err)
		}
		return contextLifecycleLoad{
			Turns:                 turns,
			HasMore:               len(rows) > limit,
			LegacyHistoryMayExist: unmaterialized,
		}, nil
	}

	legacyRows, err := queries.ListRecentAssistantMessagesBySession(ctx, sqlc.ListRecentAssistantMessagesBySessionParams{
		SessionID: sessionID,
		MaxCount:  int32(probe), //nolint:gosec // G115: limit is bounded to contextLifecycleMaxLimit
	})
	if err != nil {
		return contextLifecycleLoad{}, fmt.Errorf("list legacy assistant lifecycles: %w", err)
	}
	turns := legacyLifecycleTurnsFromRows(legacyRows, limit)
	return contextLifecycleLoad{
		Turns:        turns,
		LegacySource: len(turns) > 0,
		HasMore:      len(legacyRows) > limit,
	}, nil
}

func lifecycleTurnsFromRunRows(
	rows []sqlc.ListRecentContextLifecyclesBySessionRow,
	limit int,
) ([]ContextLifecycleTurn, error) {
	turns := make([]ContextLifecycleTurn, 0, min(len(rows), limit))
	for _, row := range rows {
		if len(turns) >= limit {
			break
		}
		snapshot, err := contextfrag.DecodeLifecycleSnapshot(row.Snapshot)
		if err != nil {
			return nil, fmt.Errorf("decode lifecycle snapshot for run %s: %w", row.RunID.String(), err)
		}
		errorCode := ""
		if row.ErrorCode.Valid {
			errorCode = row.ErrorCode.String
		}
		turns = append(turns, ContextLifecycleTurn{
			RunID:              row.RunID.String(),
			Status:             row.Status,
			ErrorCode:          errorCode,
			AssistantMessageID: snapshot.AssistantMessageID,
			CreatedAt:          row.CreatedAt.Time,
			Snapshot:           snapshot,
		})
	}
	return turns, nil
}

// latestContextLifecycleSnapshot reads the newest bounded summary only: one
// row, no page probe, and never the per-fragment audit.
func latestContextLifecycleSnapshot(
	ctx context.Context,
	queries contextLifecycleQueries,
	sessionID pgtype.UUID,
) (contextfrag.LifecycleSnapshot, bool, error) {
	raw, err := queries.GetLatestContextLifecycleBySession(ctx, sessionID)
	if err == nil {
		snapshot, err := contextfrag.DecodeLifecycleSnapshot(raw)
		if err != nil {
			return contextfrag.LifecycleSnapshot{}, false, fmt.Errorf("decode latest lifecycle snapshot: %w", err)
		}
		return snapshot, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contextfrag.LifecycleSnapshot{}, false, fmt.Errorf("get latest run lifecycle: %w", err)
	}
	legacyRows, err := queries.ListRecentAssistantMessagesBySession(ctx, sqlc.ListRecentAssistantMessagesBySessionParams{
		SessionID: sessionID,
		MaxCount:  1,
	})
	if err != nil {
		return contextfrag.LifecycleSnapshot{}, false, fmt.Errorf("list legacy assistant lifecycles: %w", err)
	}
	turns := legacyLifecycleTurnsFromRows(legacyRows, 1)
	if len(turns) == 0 {
		return contextfrag.LifecycleSnapshot{}, false, nil
	}
	return turns[0].Snapshot, true, nil
}

// legacyLifecycleTurnsFromRows extracts pre-run-table lifecycle snapshots from
// assistant message metadata, newest first, bounded by limit.
func legacyLifecycleTurnsFromRows(rows []sqlc.ListRecentAssistantMessagesBySessionRow, limit int) []ContextLifecycleTurn {
	turns := make([]ContextLifecycleTurn, 0, min(len(rows), limit))
	for _, row := range rows {
		if len(turns) >= limit {
			break
		}
		snapshot, ok := contextfrag.LifecycleSnapshotFromMetadata(row.Metadata)
		if !ok {
			continue
		}
		turns = append(turns, ContextLifecycleTurn{
			RunID:              row.RunID.String(),
			AssistantMessageID: row.ID.String(),
			CreatedAt:          row.CreatedAt.Time,
			Snapshot:           snapshot,
		})
	}
	return turns
}

func contextComposition(snapshot contextfrag.LifecycleSnapshot) ([]contextfrag.KindBreakdown, []ToolDefBucket, *contextfrag.ContextBudgetPlan) {
	var buckets []ToolDefBucket
	if len(snapshot.ToolDefs) > 0 {
		byProvider := make(map[string]*ToolDefBucket, 4)
		for _, def := range snapshot.ToolDefs {
			bucket := byProvider[def.Provider]
			if bucket == nil {
				bucket = &ToolDefBucket{Provider: def.Provider}
				byProvider[def.Provider] = bucket
			}
			bucket.Tools++
			bucket.TokenEstimate += def.TokenEstimate
		}
		buckets = make([]ToolDefBucket, 0, len(byProvider))
		for _, bucket := range byProvider {
			buckets = append(buckets, *bucket)
		}
		sort.Slice(buckets, func(i, j int) bool {
			return buckets[i].Provider < buckets[j].Provider
		})
	}
	return snapshot.Breakdown, buckets, snapshot.BudgetPlan
}

func aggregateContextLifecycle(turns []ContextLifecycleTurn) ContextLifecycleAggregates {
	agg := ContextLifecycleAggregates{Turns: len(turns)}
	for _, turn := range turns {
		agg.TotalCacheReadTokens += turn.Snapshot.CacheReadTokens
		agg.TotalCacheWriteTokens += turn.Snapshot.CacheWriteTokens
		for reason, count := range turn.Snapshot.Selection.DropReasons {
			if agg.DropReasons == nil {
				agg.DropReasons = make(map[string]int, 4)
			}
			agg.DropReasons[reason] += count
		}
		for _, record := range turn.Snapshot.Mutations {
			if agg.MutationKinds == nil {
				agg.MutationKinds = make(map[string]int, 4)
			}
			agg.MutationKinds[string(record.Kind)]++
		}
	}
	return agg
}
