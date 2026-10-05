package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/apperror"
)

// sessionInvocationLookup is the read-only slice of the session run ledger
// the invocation endpoint needs; satisfied by ledger.Store.
type sessionInvocationLookup interface {
	GetByInvocation(ctx context.Context, sessionID, invocationID string) (ledger.Run, error)
}

// SetInvocationLookup installs the session run ledger used to answer what
// happened to a client invocation after its acknowledgement was lost.
func (h *SessionHandler) SetInvocationLookup(lookup sessionInvocationLookup) {
	h.invocations = lookup
}

// sessionInvocationResponse reports whether an invocation was admitted into a
// session and, when it was, the run it became. Only the identity a client
// already holds and the run's durable state are exposed: never the input,
// its fingerprint, or ownership/fencing internals.
type sessionInvocationResponse struct {
	// Found is false when the session has no run for this invocation, which
	// is an ordinary answer (never admitted, or not yet) rather than an error.
	Found        bool   `json:"found"`
	InvocationID string `json:"invocation_id"`
	SessionID    string `json:"session_id"`
	RunID        string `json:"run_id,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	TurnPosition int64  `json:"turn_position,omitempty"`
	// State is the durable run state as the ledger records it, e.g.
	// accepted, running, waiting_decision, finishing, completed, aborted,
	// failed or lost.
	State string `json:"state,omitempty"`
}

// GetSessionInvocation godoc
// @Summary Look up the run admitted for a client invocation
// @Description Read-only. Lets a client that lost the acknowledgement of a send learn whether the invocation was admitted and what state its run is in. An unknown invocation returns 200 with found=false.
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param invocation_id path string true "Client invocation ID"
// @Success 200 {object} sessionInvocationResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/invocations/{invocation_id} [get].
func (h *SessionHandler) GetSessionInvocation(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "session id is required")
	}
	invocationID := strings.TrimSpace(c.Param("invocation_id"))
	if invocationID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "invocation id is required")
	}
	// Same gate as reading the session itself, so a caller who cannot read
	// the session learns nothing more here than from GetSession.
	_, _, sess, err := h.authorizeSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}
	if h.invocations == nil {
		return apperror.New(apperror.CodeInternal, nil)
	}
	resp := sessionInvocationResponse{InvocationID: invocationID, SessionID: sess.ID}
	// The lookup is always scoped to the authorized session, so an invocation
	// id admitted into another session can never resolve here.
	run, err := h.invocations.GetByInvocation(c.Request().Context(), sess.ID, invocationID)
	switch {
	case errors.Is(err, ledger.ErrRunNotFound):
		return c.JSON(http.StatusOK, resp)
	case err != nil:
		return apperror.Wrap(apperror.CodeInternal, err, nil)
	case run.SessionID != sess.ID:
		// Defensive: a store that ignored the session scope must not leak a
		// foreign run through this endpoint.
		return c.JSON(http.StatusOK, resp)
	}
	resp.Found = true
	resp.RunID = run.RunID
	resp.TurnID = run.TurnID
	resp.TurnPosition = run.TurnPosition
	resp.State = string(run.State)
	return c.JSON(http.StatusOK, resp)
}
