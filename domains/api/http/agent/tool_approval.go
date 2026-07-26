package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	agentdomain "github.com/memohai/memoh/domains/agent"
	toolapproval "github.com/memohai/memoh/domains/agent/decision/approval"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/identity/auth"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/apperror"
)

type ToolApprovalHandler struct {
	logger         *slog.Logger
	botService     *bot.Service
	accountService *account.Service
	turnService    toolApprovalResponder
}

type toolApprovalResponder interface {
	RespondToolApproval(ctx context.Context, input agentdomain.ToolApprovalResponse, eventCh chan<- json.RawMessage) error
}

type ToolApprovalDecisionRequest struct {
	Reason string `json:"reason,omitempty"`
}

func NewToolApprovalHandler(log *slog.Logger, botService *bot.Service, accountService *account.Service, turnService agentdomain.Service) *ToolApprovalHandler {
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
	if botID == "" {
		return apperror.Required("bot_id")
	}
	if approvalID == "" {
		return apperror.Required("approval_id")
	}
	var req ToolApprovalDecisionRequest
	_ = c.Bind(&req)
	if err := h.turnService.RespondToolApproval(context.WithoutCancel(c.Request().Context()), agentdomain.ToolApprovalResponse{
		BotID:                  botID,
		ActorChannelIdentityID: actorUserID,
		ActorUserID:            actorUserID,
		ApprovalID:             approvalID,
		Decision:               decision,
		Reason:                 strings.TrimSpace(req.Reason),
	}, nil); err != nil {
		return toolApprovalHTTPError(err)
	}
	return c.JSON(http.StatusOK, map[string]string{"status": decision})
}

func toolApprovalHTTPError(err error) error {
	switch {
	case errors.Is(err, toolapproval.ErrForbidden):
		return apperror.Forbidden("respond tool approval", err)
	case errors.Is(err, toolapproval.ErrNotFound):
		return apperror.NotFound("respond tool approval", err)
	case errors.Is(err, toolapproval.ErrAlreadyDecided), errors.Is(err, toolapproval.ErrAmbiguous):
		return apperror.Conflict("respond tool approval", err)
	default:
		return apperror.Internal("respond tool approval", err)
	}
}
