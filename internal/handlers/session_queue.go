package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/runtime/session/queue"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// SessionQueueHandler exposes the durable steer and follow-up queues. It owns
// authorization and request/response mapping only; transactions and queue
// semantics live in the application service.
type SessionQueueHandler struct {
	queries        dbstore.Queries
	agentService   *application.Service
	botService     *bots.Service
	accountService *accounts.Service
}

func NewSessionQueueHandler(queries dbstore.Queries, agentService *application.Service, botService *bots.Service, accountService *accounts.Service) *SessionQueueHandler {
	return &SessionQueueHandler{queries: queries, agentService: agentService, botService: botService, accountService: accountService}
}

func (h *SessionQueueHandler) Register(e *echo.Echo) {
	e.POST("/bots/:bot_id/sessions/:session_id/steer-queue", h.EnqueueSteer)
	e.GET("/bots/:bot_id/sessions/:session_id/steer-queue", h.ListSteer)
	e.GET("/bots/:bot_id/sessions/:session_id/queue", h.ListSessionQueue)
	e.PUT("/bots/:bot_id/sessions/:session_id/steer-queue/reorder", h.ReorderSteer)
	e.PATCH("/bots/:bot_id/sessions/:session_id/steer-queue/:item_id", h.UpdateSteer)
	e.DELETE("/bots/:bot_id/sessions/:session_id/steer-queue/:item_id", h.CancelSteer)
	e.POST("/bots/:bot_id/sessions/:session_id/follow-up-queue", h.EnqueueFollowUp)
	e.GET("/bots/:bot_id/sessions/:session_id/follow-up-queue", h.ListFollowUp)
	e.PUT("/bots/:bot_id/sessions/:session_id/follow-up-queue/reorder", h.ReorderFollowUp)
	e.PATCH("/bots/:bot_id/sessions/:session_id/follow-up-queue/:item_id", h.UpdateFollowUp)
	e.DELETE("/bots/:bot_id/sessions/:session_id/follow-up-queue/:item_id", h.CancelFollowUp)
	e.POST("/bots/:bot_id/sessions/:session_id/follow-up-queue/:item_id/steer", h.PromoteFollowUpToSteer)
}

type enqueueQueueRequest struct {
	InvocationID string `json:"invocation_id" validate:"required"`
	Text         string `json:"text" validate:"required"`
}
type updateQueueRequest struct {
	Text string `json:"text" validate:"required"`
}
type steerQueueItemResponse struct {
	ItemID      queue.SteerItemID `json:"item_id"`
	Status      queue.Status      `json:"status"`
	Position    int64             `json:"position"`
	Text        string            `json:"text"`
	TargetRunID string            `json:"target_run_id"`
}
type followUpQueueItemResponse struct {
	ItemID              queue.FollowUpItemID `json:"item_id"`
	Status              queue.Status         `json:"status"`
	Position            int64                `json:"position"`
	Text                string               `json:"text"`
	EnqueuedDuringRunID string               `json:"enqueued_during_run_id"`
}
type steerQueueResponse struct {
	Items []steerQueueItemResponse `json:"items"`
}
type followUpQueueResponse struct {
	Items []followUpQueueItemResponse `json:"items"`
}
type sessionQueueResponse struct {
	Steer    []steerQueueItemResponse    `json:"steer"`
	FollowUp []followUpQueueItemResponse `json:"follow_up"`
}
type steerQueueReorderRequest struct {
	Item   queue.SteerPendingRef `json:"item"`
	Before queue.SteerPendingRef `json:"before"`
}
type followUpQueueReorderRequest struct {
	Item   queue.FollowUpPendingRef `json:"item"`
	Before queue.FollowUpPendingRef `json:"before"`
}

func (h *SessionQueueHandler) authorize(c echo.Context) (string, string, error) {
	identityID, err := auth.UserIDFromContext(c)
	if err != nil {
		return "", "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if botID == "" || sessionID == "" {
		return "", "", apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return "", "", apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	if h.agentService == nil || h.queries == nil {
		return "", "", apperror.New(apperror.CodeQueueAdmissionUnavailable, nil)
	}
	sess, err := h.queries.GetSessionByID(c.Request().Context(), sid)
	if err != nil || sess.BotID.String() != botID {
		return "", "", echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	// Queue admission is session-scoped. A chat grant is sufficient only for
	// sessions owned by that actor; manage access retains the existing ability
	// to operate on every session of the bot. This is a hot path (every queue
	// panel refresh), so permissions are resolved once from a bot row fetched
	// without runtime check summaries instead of through two full authorizations.
	if err := h.authorizeQueueAccess(c.Request().Context(), identityID, botID, sess.CreatedByUserID.Valid && sess.CreatedByUserID.String() == identityID); err != nil {
		return "", "", err
	}
	return botID, sessionID, nil
}

func (h *SessionQueueHandler) authorizeQueueAccess(ctx context.Context, identityID, botID string, ownsSession bool) error {
	if h.botService == nil || h.accountService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "bot services not configured")
	}
	isAdmin, err := h.accountService.IsAdmin(ctx, identityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	bot, err := h.botService.GetForAccess(ctx, botID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, bots.ErrBotNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "bot not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	perms, err := h.botService.ResolveUserPermissionsForBot(ctx, bot, identityID, isAdmin)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	switch {
	case bots.HasPermission(perms, bots.PermissionManage):
		return nil
	case bots.HasPermission(perms, bots.PermissionChat):
		if !ownsSession {
			return echo.NewHTTPError(http.StatusNotFound, "session not found")
		}
		return nil
	default:
		return echo.NewHTTPError(http.StatusForbidden, "bot access denied")
	}
}

func decodeQueueRequest(c echo.Context) (enqueueQueueRequest, error) {
	var req enqueueQueueRequest
	if err := c.Bind(&req); err != nil {
		return req, apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	req.InvocationID = strings.TrimSpace(req.InvocationID)
	if req.InvocationID == "" || strings.TrimSpace(req.Text) == "" {
		return req, apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	return req, nil
}

func marshalQueuePayload(text string) ([]byte, error) {
	return json.Marshal(map[string]string{"text": strings.TrimSpace(text)})
}

func queueAdmissionError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, queue.ErrNoActiveRun), errors.Is(err, pgx.ErrNoRows):
		return apperror.New(apperror.CodeQueueNoActiveRun, nil)
	case errors.Is(err, queue.ErrInvocationConflict):
		return apperror.New(apperror.CodeSessionInvocationConflict, nil)
	case errors.Is(err, queue.ErrAdmissionOverloaded):
		return apperror.New(apperror.CodeQueueAdmissionOverloaded, nil)
	case errors.Is(err, queue.ErrInvalidReference):
		return apperror.New(apperror.CodeQueueRequestInvalid, nil)
	default:
		return err
	}
}

func queueMutationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, queue.ErrNoActiveRun):
		return apperror.New(apperror.CodeQueueNoActiveRun, nil)
	case errors.Is(err, queue.ErrNotPending), errors.Is(err, queue.ErrInvalidReference), errors.Is(err, pgx.ErrNoRows):
		return apperror.New(apperror.CodeQueueItemNotPending, nil)
	default:
		return err
	}
}

func decodeUpdateQueueRequest(c echo.Context) (updateQueueRequest, error) {
	var req updateQueueRequest
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		return req, apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	return req, nil
}

func queueItemIDParam(c echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("item_id"))
	if _, err := uuid.Parse(id); err != nil {
		return "", apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	return id, nil
}

func decodeSteerReorderRequest(c echo.Context) (steerQueueReorderRequest, error) {
	var req steerQueueReorderRequest
	if err := c.Bind(&req); err != nil {
		return req, apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	if err := validateReorderRefs(string(req.Item.ItemID), string(req.Before.ItemID)); err != nil {
		return req, err
	}
	return req, nil
}

func decodeFollowUpReorderRequest(c echo.Context) (followUpQueueReorderRequest, error) {
	var req followUpQueueReorderRequest
	if err := c.Bind(&req); err != nil {
		return req, apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	if err := validateReorderRefs(string(req.Item.ItemID), string(req.Before.ItemID)); err != nil {
		return req, err
	}
	return req, nil
}

func validateReorderRefs(item, before string) error {
	if _, err := uuid.Parse(item); err != nil {
		return apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	if before != "" {
		if _, err := uuid.Parse(before); err != nil {
			return apperror.New(apperror.CodeQueueRequestInvalid, nil)
		}
	}
	return nil
}

// EnqueueSteer godoc
// @Summary Enqueue steer input for the active session run
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param body body enqueueQueueRequest true "Steer payload"
// @Success 202 {object} steerQueueItemResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/steer-queue [post].
func (h *SessionQueueHandler) EnqueueSteer(c echo.Context) error {
	botID, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	req, err := decodeQueueRequest(c)
	if err != nil {
		return err
	}
	payload, err := marshalQueuePayload(req.Text)
	if err != nil {
		return err
	}
	item, err := h.agentService.EnqueueSteer(c.Request().Context(), botID, sid, req.InvocationID, payload)
	if err = queueAdmissionError(err); err != nil {
		return err
	}
	return c.JSON(http.StatusAccepted, steerQueueItemResponseFrom(item))
}

// EnqueueFollowUp godoc
// @Summary Enqueue follow-up input for the active session run
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param body body enqueueQueueRequest true "Follow-up payload"
// @Success 202 {object} followUpQueueItemResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/follow-up-queue [post].
func (h *SessionQueueHandler) EnqueueFollowUp(c echo.Context) error {
	botID, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	req, err := decodeQueueRequest(c)
	if err != nil {
		return err
	}
	payload, err := marshalQueuePayload(req.Text)
	if err != nil {
		return err
	}
	item, err := h.agentService.EnqueueFollowUp(c.Request().Context(), botID, sid, req.InvocationID, payload)
	if err = queueAdmissionError(err); err != nil {
		return err
	}
	return c.JSON(http.StatusAccepted, followUpQueueItemResponseFrom(item))
}

// ListSteer godoc
// @Summary List pending steer inputs
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} steerQueueResponse
// @Failure 403 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/steer-queue [get].
func (h *SessionQueueHandler) ListSteer(c echo.Context) error {
	_, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	queues, err := h.agentService.ListSessionQueues(c.Request().Context(), sid)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, steerQueueResponse{Items: mapQueueItems(queues.Steer, steerQueueItemResponseFrom)})
}

// ListFollowUp godoc
// @Summary List pending follow-up inputs
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} followUpQueueResponse
// @Failure 403 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/follow-up-queue [get].
func (h *SessionQueueHandler) ListFollowUp(c echo.Context) error {
	_, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	queues, err := h.agentService.ListSessionQueues(c.Request().Context(), sid)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, followUpQueueResponse{Items: mapQueueItems(queues.FollowUp, followUpQueueItemResponseFrom)})
}

// ListSessionQueue godoc
// @Summary List pending steer and follow-up inputs in one response
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} sessionQueueResponse
// @Failure 403 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/queue [get].
func (h *SessionQueueHandler) ListSessionQueue(c echo.Context) error {
	_, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	queues, err := h.agentService.ListSessionQueues(c.Request().Context(), sid)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, sessionQueueResponse{
		Steer:    mapQueueItems(queues.Steer, steerQueueItemResponseFrom),
		FollowUp: mapQueueItems(queues.FollowUp, followUpQueueItemResponseFrom),
	})
}

// ReorderSteer godoc
// @Summary Reorder accepted steer inputs
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param body body steerQueueReorderRequest true "Typed steer queue references"
// @Success 200 {object} steerQueueResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/steer-queue/reorder [put].
func (h *SessionQueueHandler) ReorderSteer(c echo.Context) error {
	_, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	req, err := decodeSteerReorderRequest(c)
	if err != nil {
		return err
	}
	items, err := h.agentService.ReorderSteer(c.Request().Context(), sid, req.Item, req.Before)
	if err = queueMutationError(err); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, steerQueueResponse{Items: mapQueueItems(items, steerQueueItemResponseFrom)})
}

// ReorderFollowUp godoc
// @Summary Reorder accepted follow-up inputs
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param body body followUpQueueReorderRequest true "Typed follow-up queue references"
// @Success 200 {object} followUpQueueResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/follow-up-queue/reorder [put].
func (h *SessionQueueHandler) ReorderFollowUp(c echo.Context) error {
	_, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	req, err := decodeFollowUpReorderRequest(c)
	if err != nil {
		return err
	}
	items, err := h.agentService.ReorderFollowUp(c.Request().Context(), sid, req.Item, req.Before)
	if err = queueMutationError(err); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, followUpQueueResponse{Items: mapQueueItems(items, followUpQueueItemResponseFrom)})
}

// UpdateSteer godoc
// @Summary Edit an accepted steer input
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param item_id path string true "Queue item ID"
// @Param body body updateQueueRequest true "Updated steer payload"
// @Success 200 {object} steerQueueItemResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/steer-queue/{item_id} [patch].
func (h *SessionQueueHandler) UpdateSteer(c echo.Context) error {
	_, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	itemID, err := queueItemIDParam(c)
	if err != nil {
		return err
	}
	req, err := decodeUpdateQueueRequest(c)
	if err != nil {
		return err
	}
	payload, err := marshalQueuePayload(req.Text)
	if err != nil {
		return err
	}
	item, err := h.agentService.UpdateSteer(c.Request().Context(), sid, itemID, payload)
	if err = queueMutationError(err); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, steerQueueItemResponseFrom(item))
}

// CancelSteer godoc
// @Summary Cancel an accepted steer input
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param item_id path string true "Queue item ID"
// @Success 204
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/steer-queue/{item_id} [delete].
func (h *SessionQueueHandler) CancelSteer(c echo.Context) error {
	_, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	itemID, err := queueItemIDParam(c)
	if err != nil {
		return err
	}
	if err = queueMutationError(h.agentService.CancelSteer(c.Request().Context(), sid, itemID)); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// UpdateFollowUp godoc
// @Summary Edit an accepted follow-up input
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param item_id path string true "Queue item ID"
// @Param body body updateQueueRequest true "Updated follow-up payload"
// @Success 200 {object} followUpQueueItemResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/follow-up-queue/{item_id} [patch].
func (h *SessionQueueHandler) UpdateFollowUp(c echo.Context) error {
	_, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	itemID, err := queueItemIDParam(c)
	if err != nil {
		return err
	}
	req, err := decodeUpdateQueueRequest(c)
	if err != nil {
		return err
	}
	payload, err := marshalQueuePayload(req.Text)
	if err != nil {
		return err
	}
	item, err := h.agentService.UpdateFollowUp(c.Request().Context(), sid, itemID, payload)
	if err = queueMutationError(err); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, followUpQueueItemResponseFrom(item))
}

// CancelFollowUp godoc
// @Summary Cancel an accepted follow-up input
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param item_id path string true "Queue item ID"
// @Success 204
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/follow-up-queue/{item_id} [delete].
func (h *SessionQueueHandler) CancelFollowUp(c echo.Context) error {
	_, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	itemID, err := queueItemIDParam(c)
	if err != nil {
		return err
	}
	if err = queueMutationError(h.agentService.CancelFollowUp(c.Request().Context(), sid, itemID)); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// PromoteFollowUpToSteer godoc
// @Summary Promote an accepted follow-up input to steer the active run
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param item_id path string true "Follow-up queue item ID"
// @Success 202 {object} steerQueueItemResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/follow-up-queue/{item_id}/steer [post].
func (h *SessionQueueHandler) PromoteFollowUpToSteer(c echo.Context) error {
	botID, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	itemID, err := queueItemIDParam(c)
	if err != nil {
		return err
	}
	result, err := h.agentService.PromoteFollowUpToSteer(c.Request().Context(), botID, sid, queue.FollowUpPendingRef{ItemID: queue.FollowUpItemID(itemID)})
	if err = queueMutationError(err); err != nil {
		return err
	}
	return c.JSON(http.StatusAccepted, steerQueueItemResponseFrom(result.Steer))
}

func steerQueueItemResponseFrom(item queue.SteerItem) steerQueueItemResponse {
	return steerQueueItemResponse{ItemID: item.ID, Status: item.Status, Position: item.Position, Text: queuePayloadText(item.Payload), TargetRunID: item.TargetRunID}
}

func followUpQueueItemResponseFrom(item queue.FollowUpItem) followUpQueueItemResponse {
	return followUpQueueItemResponse{ItemID: item.ID, Status: item.Status, Position: item.Position, Text: queuePayloadText(item.Payload), EnqueuedDuringRunID: item.EnqueuedDuringRunID}
}

func mapQueueItems[T any, R any](items []T, mapItem func(T) R) []R {
	out := make([]R, 0, len(items))
	for _, item := range items {
		out = append(out, mapItem(item))
	}
	return out
}

func queuePayloadText(payload []byte) string {
	var body struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(payload, &body) == nil && strings.TrimSpace(body.Text) != "" {
		return body.Text
	}
	return strings.TrimSpace(string(payload))
}
