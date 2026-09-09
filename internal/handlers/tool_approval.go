package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/bots"
)

type ToolApprovalHandler struct {
	logger         *slog.Logger
	botService     *bots.Service
	accountService *accounts.Service
	turnService    toolApprovalResponder
}

type toolApprovalResponder interface {
	RespondToolApproval(ctx context.Context, input turn.ToolApprovalResponse, eventCh chan<- json.RawMessage) error
}

type ToolApprovalDecisionRequest struct {
	// ControlID is the stable identity of one client mutation. New clients send
	// it for exact retries; it remains optional for older Web/Desktop clients.
	ControlID string `json:"control_id,omitempty"`
	// OptionID selects one of the agent-provided permission options carried on
	// the approval request; empty keeps the plain binary decision.
	OptionID string `json:"option_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func NewToolApprovalHandler(log *slog.Logger, botService *bots.Service, accountService *accounts.Service, turnService turn.Service) *ToolApprovalHandler {
	return &ToolApprovalHandler{
		logger:         log.With(slog.String("handler", "tool_approval")),
		botService:     botService,
		accountService: accountService,
		turnService:    turnService,
	}
}

func (h *ToolApprovalHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/tool-approvals")
	group.POST("/:approval_id/approve", h.Approve)
	group.POST("/:approval_id/reject", h.Reject)
}

// Approve godoc
// @Summary Approve a pending tool call
// @Tags tool-approvals
// @Param bot_id path string true "Bot ID"
// @Param approval_id path string true "Approval ID"
// @Param payload body ToolApprovalDecisionRequest false "Approval payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/tool-approvals/{approval_id}/approve [post].
func (h *ToolApprovalHandler) Approve(c echo.Context) error {
	return h.respond(c, "approve")
}

// Reject godoc
// @Summary Reject a pending tool call
// @Tags tool-approvals
// @Param bot_id path string true "Bot ID"
// @Param approval_id path string true "Approval ID"
// @Param payload body ToolApprovalDecisionRequest false "Rejection payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/tool-approvals/{approval_id}/reject [post].
func (h *ToolApprovalHandler) Reject(c echo.Context) error {
	return h.respond(c, "reject")
}

func (h *ToolApprovalHandler) respond(c echo.Context, decision string) error {
	actorUserID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	approvalID := strings.TrimSpace(c.Param("approval_id"))
	if botID == "" || approvalID == "" {
		return apperror.New(apperror.CodeToolApprovalRequestInvalid, nil)
	}
	var req ToolApprovalDecisionRequest
	if err := c.Bind(&req); err != nil && !errors.Is(err, io.EOF) {
		return apperror.Wrap(apperror.CodeToolApprovalRequestInvalid, err, nil)
	}
	controlID := strings.TrimSpace(req.ControlID)
	if err := h.turnService.RespondToolApproval(context.WithoutCancel(c.Request().Context()), turn.ToolApprovalResponse{
		ControlID:              controlID,
		BotID:                  botID,
		ActorChannelIdentityID: actorUserID,
		ActorUserID:            actorUserID,
		ApprovalID:             approvalID,
		Decision:               decision,
		OptionID:               req.OptionID,
		Reason:                 strings.TrimSpace(req.Reason),
	}, nil); err != nil {
		return toolApprovalHTTPError(err)
	}
	return c.JSON(http.StatusOK, map[string]string{"status": decision})
}

func toolApprovalHTTPError(err error) error {
	if err == nil || apperror.CodeOf(err) != "" {
		return err
	}
	switch {
	case errors.Is(err, toolapproval.ErrForbidden):
		return apperror.New(apperror.CodeToolApprovalForbidden, nil)
	case errors.Is(err, toolapproval.ErrNotFound):
		return apperror.New(apperror.CodeToolApprovalNotFound, nil)
	case errors.Is(err, toolapproval.ErrAlreadyDecided):
		return apperror.New(apperror.CodeToolApprovalExpired, nil)
	case errors.Is(err, toolapproval.ErrAmbiguous):
		return apperror.New(apperror.CodeToolApprovalAmbiguous, nil)
	case errors.Is(err, toolapproval.ErrOptionUnavailable):
		return apperror.Wrap(apperror.CodeToolApprovalRequestInvalid, err, nil)
	case errors.Is(err, sessionruntime.ErrCommandPayloadConflict):
		// Reusing one control identity for a different mutation is a malformed
		// idempotency request, not an infrastructure failure.
		return apperror.Wrap(apperror.CodeToolApprovalRequestInvalid, err, nil)
	default:
		return apperror.Wrap(apperror.CodeToolApprovalOperationFailed, err, nil)
	}
}
