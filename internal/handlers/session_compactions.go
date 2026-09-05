package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const (
	sessionCompactionsDefaultLimit = 50
	sessionCompactionsMaxLimit     = 200
)

// SessionCompactionsResponse lists the compactions recorded for one chat
// session, newest first, one keyset page at a time.
type SessionCompactionsResponse struct {
	Items []compaction.Log `json:"items"`
	// HasMore reports whether older compactions exist beyond this page.
	HasMore bool `json:"has_more"`
	// NextCursor is the opaque `before` value that continues past this
	// page's oldest compaction; absent when the page is complete.
	NextCursor string `json:"next_cursor,omitempty"`
}

// GetSessionCompactions godoc
// @Summary List a session's compactions
// @Description Return the compaction runs recorded for a chat session, newest first: status, the summary that replaced the covered messages, how many messages it covered and the conversation time it spans, the summarizer's usage and model, and when it ran. Pages by an opaque keyset cursor. Session access suffices: the summary is conversation the reader already sees
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param limit query int false "Maximum number of compactions to return (default 50, max 200)"
// @Param before query string false "Opaque next_cursor from a previous page; returns compactions older than it"
// @Success 200 {object} SessionCompactionsResponse
// @Failure 400 {object} apperror.Problem
// @Failure 401 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/compactions [get].
func (h *SessionInfoHandler) GetSessionCompactions(c echo.Context) error {
	access, err := h.authorizeContextLifecycleSession(c)
	if err != nil {
		return err
	}
	limit := sessionCompactionsLimit(c)
	probe := int32(limit + 1) //nolint:gosec // G115: limit is bounded to sessionCompactionsMaxLimit
	ctx := c.Request().Context()
	var rows []sqlc.BotHistoryMessageCompact
	if raw := strings.TrimSpace(c.QueryParam("before")); raw != "" {
		cursor, ok := decodeContextLifecycleCursor(raw)
		if !ok {
			return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
		}
		rows, err = h.queries.ListCompactionLogsBySessionBefore(ctx, sqlc.ListCompactionLogsBySessionBeforeParams{
			BotID:           access.botID,
			SessionID:       access.sessionID,
			Limit:           probe,
			BeforeStartedAt: pgtype.Timestamptz{Time: cursor.createdAt, Valid: true},
			BeforeID:        cursor.runID,
		})
	} else {
		rows, err = h.queries.ListCompactionLogsBySession(ctx, sqlc.ListCompactionLogsBySessionParams{
			BotID:     access.botID,
			SessionID: access.sessionID,
			Limit:     probe,
		})
	}
	if err != nil {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	response := SessionCompactionsResponse{Items: make([]compaction.Log, 0, min(len(rows), limit))}
	for _, row := range rows {
		if len(response.Items) >= limit {
			response.HasMore = true
			break
		}
		response.Items = append(response.Items, compaction.LogFromRow(row))
	}
	if response.HasMore && len(response.Items) > 0 {
		last := rows[len(response.Items)-1]
		response.NextCursor = encodeContextLifecycleCursor(last.StartedAt.Time, last.ID)
	}
	return c.JSON(http.StatusOK, response)
}

func sessionCompactionsLimit(c echo.Context) int {
	limit := sessionCompactionsDefaultLimit
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	return min(limit, sessionCompactionsMaxLimit)
}
