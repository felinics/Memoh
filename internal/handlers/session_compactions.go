package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const sessionCompactionsLimit = 50

// SessionCompactionsResponse lists the compactions recorded for one chat
// session, newest first.
type SessionCompactionsResponse struct {
	Items []compaction.Log `json:"items"`
}

// GetSessionCompactions godoc
// @Summary List a session's compactions
// @Description Return the compaction runs recorded for a chat session, newest first: status, the summary that replaced the covered messages, how many messages it covered and the conversation time it spans, the summarizer's usage and model, and when it ran. Session access suffices: the summary is conversation the reader already sees
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
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
	rows, err := h.queries.ListCompactionLogsBySession(c.Request().Context(), sqlc.ListCompactionLogsBySessionParams{
		BotID:     access.botID,
		SessionID: access.sessionID,
		Limit:     sessionCompactionsLimit,
	})
	if err != nil {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	items := make([]compaction.Log, 0, len(rows))
	for _, row := range rows {
		items = append(items, compaction.LogFromRow(row))
	}
	return c.JSON(http.StatusOK, SessionCompactionsResponse{Items: items})
}
