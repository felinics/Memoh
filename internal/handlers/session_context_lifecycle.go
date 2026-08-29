package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	Turns []ContextLifecycleTurn `json:"turns"`
}

// ContextLifecycleTurn is one persisted lifecycle snapshot, newest first.
type ContextLifecycleTurn struct {
	RunID              string                        `json:"run_id"`
	Status             string                        `json:"status,omitempty"`
	ErrorCode          string                        `json:"error_code,omitempty"`
	AssistantMessageID string                        `json:"assistant_message_id,omitempty"`
	CreatedAt          time.Time                     `json:"created_at"`
	Snapshot           contextfrag.LifecycleSnapshot `json:"snapshot"`
}

// GetSessionContextLifecycle godoc
// @Summary Get session context lifecycle
// @Description List run-keyed context lifecycle snapshots for a chat session; sessions predating run lifecycle persistence fall back to legacy assistant metadata
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

	turns, err := loadContextLifecycleTurns(ctx, h.queries, pgSessionID, contextLifecycleLimit(c))
	if err != nil {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	return c.JSON(http.StatusOK, ContextLifecycleResponse{Turns: turns})
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
}

func loadContextLifecycleTurns(
	ctx context.Context,
	queries contextLifecycleQueries,
	sessionID pgtype.UUID,
	limit int,
) ([]ContextLifecycleTurn, error) {
	rows, err := queries.ListRecentContextLifecyclesBySession(ctx, sqlc.ListRecentContextLifecyclesBySessionParams{
		SessionID: sessionID,
		MaxCount:  int32(limit), //nolint:gosec // G115: limit is bounded to contextLifecycleMaxLimit
	})
	if err != nil {
		return nil, fmt.Errorf("list run lifecycles: %w", err)
	}
	if len(rows) > 0 {
		return lifecycleTurnsFromRunRows(rows, limit)
	}

	legacyRows, err := queries.ListRecentAssistantMessagesBySession(ctx, sqlc.ListRecentAssistantMessagesBySessionParams{
		SessionID: sessionID,
		MaxCount:  int32(limit), //nolint:gosec // G115: limit is bounded to contextLifecycleMaxLimit
	})
	if err != nil {
		return nil, fmt.Errorf("list legacy assistant lifecycles: %w", err)
	}
	return legacyLifecycleTurnsFromRows(legacyRows, limit), nil
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
		var snapshot contextfrag.LifecycleSnapshot
		if err := json.Unmarshal(row.Snapshot, &snapshot); err != nil {
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

// legacyLifecycleTurnsFromRows extracts pre-run-table lifecycle snapshots from
// assistant message metadata, newest first, bounded by limit.
func legacyLifecycleTurnsFromRows(rows []sqlc.ListRecentAssistantMessagesBySessionRow, limit int) []ContextLifecycleTurn {
	turns := make([]ContextLifecycleTurn, 0, min(len(rows), limit))
	for _, row := range rows {
		if len(turns) >= limit {
			break
		}
		snapshot, ok := lifecycleSnapshotFromMetadata(row.Metadata)
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

func lifecycleSnapshotFromMetadata(raw []byte) (contextfrag.LifecycleSnapshot, bool) {
	if len(raw) == 0 {
		return contextfrag.LifecycleSnapshot{}, false
	}
	var metadata struct {
		ContextLifecycle *contextfrag.LifecycleSnapshot `json:"context_lifecycle"`
	}
	if json.Unmarshal(raw, &metadata) != nil || metadata.ContextLifecycle == nil {
		return contextfrag.LifecycleSnapshot{}, false
	}
	return *metadata.ContextLifecycle, true
}
