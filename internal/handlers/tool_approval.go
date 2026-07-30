package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/accounts"
	toolapproval "github.com/memohai/memoh/internal/agent/decision/approval"
	"github.com/memohai/memoh/internal/agent/turn"
	"github.com/memohai/memoh/internal/auth"
	"github.com/memohai/memoh/internal/bots"
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
	ControlID string `json:"control_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
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
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
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
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
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
		return echo.NewHTTPError(http.StatusBadRequest, "bot_id and approval_id are required")
	}
	var req ToolApprovalDecisionRequest
	_ = c.Bind(&req)
	if err := h.turnService.RespondToolApproval(context.WithoutCancel(c.Request().Context()), turn.ToolApprovalResponse{
		ControlID:              strings.TrimSpace(req.ControlID),
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

func toolApprovalHTTPError(err error) *echo.HTTPError {
	switch {
	case errors.Is(err, toolapproval.ErrForbidden):
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	case errors.Is(err, toolapproval.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, toolapproval.ErrAlreadyDecided), errors.Is(err, toolapproval.ErrAmbiguous):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
}
