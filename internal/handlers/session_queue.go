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
	"github.com/felinics/memoh/internal/agent/runtime/session/queue"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type SessionQueueHandler struct {
	queries        dbstore.Queries
	botService     *bots.Service
	accountService *accounts.Service
}

type queueTransactionRunner interface {
	InTx(context.Context, func(dbstore.Queries) error) error
}

func NewSessionQueueHandler(queries dbstore.Queries, botService *bots.Service, accountService *accounts.Service) *SessionQueueHandler {
	return &SessionQueueHandler{queries: queries, botService: botService, accountService: accountService}
}

func (h *SessionQueueHandler) Register(e *echo.Echo) {
	e.POST("/bots/:bot_id/sessions/:session_id/steer-queue", h.EnqueueSteer)
	e.GET("/bots/:bot_id/sessions/:session_id/steer-queue", h.ListSteer)
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
	sess, err := h.queries.GetSessionByID(c.Request().Context(), sid)
	if err != nil || sess.BotID.String() != botID {
		return "", "", echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	// Queue admission is session-scoped. A chat grant is sufficient only for
	// sessions owned by that actor; manage access retains the existing ability
	// to operate on every session of the bot.
	if _, manageErr := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, identityID, botID, bots.PermissionManage); manageErr != nil {
		if _, err = AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, identityID, botID, bots.PermissionChat); err != nil {
			return "", "", err
		}
		if !sess.CreatedByUserID.Valid || sess.CreatedByUserID.String() != identityID {
			return "", "", echo.NewHTTPError(http.StatusNotFound, "session not found")
		}
	}
	return botID, sessionID, nil
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
	if _, err := uuid.Parse(string(req.Item.ItemID)); err != nil {
		return req, apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	if req.Before.ItemID != "" {
		if _, err := uuid.Parse(string(req.Before.ItemID)); err != nil {
			return req, apperror.New(apperror.CodeQueueRequestInvalid, nil)
		}
	}
	return req, nil
}

func decodeFollowUpReorderRequest(c echo.Context) (followUpQueueReorderRequest, error) {
	var req followUpQueueReorderRequest
	if err := c.Bind(&req); err != nil {
		return req, apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	if _, err := uuid.Parse(string(req.Item.ItemID)); err != nil {
		return req, apperror.New(apperror.CodeQueueRequestInvalid, nil)
	}
	if req.Before.ItemID != "" {
		if _, err := uuid.Parse(string(req.Before.ItemID)); err != nil {
			return req, apperror.New(apperror.CodeQueueRequestInvalid, nil)
		}
	}
	return req, nil
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
	var item queue.SteerItem
	err = h.inTransaction(c.Request().Context(), func(q dbstore.Queries) error {
		payload, marshalErr := json.Marshal(map[string]string{"text": strings.TrimSpace(req.Text)})
		if marshalErr != nil {
			return marshalErr
		}
		item, err = queue.NewPostgresStore(q).EnqueueSteer(c.Request().Context(), botID, sid, uuid.NewString(), req.InvocationID, payload)
		return err
	})
	if errors.Is(err, queue.ErrNoActiveRun) || errors.Is(err, pgx.ErrNoRows) {
		return apperror.New(apperror.CodeQueueNoActiveRun, nil)
	}
	if errors.Is(err, queue.ErrInvocationConflict) {
		return apperror.New(apperror.CodeSessionInvocationConflict, nil)
	}
	if err != nil {
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
	var item queue.FollowUpItem
	err = h.inTransaction(c.Request().Context(), func(q dbstore.Queries) error {
		payload, marshalErr := json.Marshal(map[string]string{"text": strings.TrimSpace(req.Text)})
		if marshalErr != nil {
			return marshalErr
		}
		item, err = queue.NewPostgresStore(q).EnqueueFollowUp(c.Request().Context(), botID, sid, uuid.NewString(), req.InvocationID, payload)
		return err
	})
	if errors.Is(err, queue.ErrNoActiveRun) || errors.Is(err, pgx.ErrNoRows) {
		return apperror.New(apperror.CodeQueueNoActiveRun, nil)
	}
	if errors.Is(err, queue.ErrInvocationConflict) {
		return apperror.New(apperror.CodeSessionInvocationConflict, nil)
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusAccepted, followUpQueueItemResponseFrom(item))
}

func (h *SessionQueueHandler) inTransaction(ctx context.Context, fn func(dbstore.Queries) error) error {
	if runner, ok := h.queries.(queueTransactionRunner); ok {
		return runner.InTx(ctx, fn)
	}
	return errors.New("session queue handler requires transactional queries")
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
	botID, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	_ = botID
	items, err := queue.NewPostgresStore(h.queries).PendingSteer(c.Request().Context(), sid)
	if err != nil {
		return err
	}
	out := make([]steerQueueItemResponse, 0, len(items))
	for _, item := range items {
		out = append(out, steerQueueItemResponseFrom(item))
	}
	return c.JSON(http.StatusOK, steerQueueResponse{Items: out})
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
	botID, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	_ = botID
	items, err := queue.NewPostgresStore(h.queries).PendingFollowUp(c.Request().Context(), sid)
	if err != nil {
		return err
	}
	out := make([]followUpQueueItemResponse, 0, len(items))
	for _, item := range items {
		out = append(out, followUpQueueItemResponseFrom(item))
	}
	return c.JSON(http.StatusOK, followUpQueueResponse{Items: out})
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
	var items []queue.SteerItem
	err = h.inTransaction(c.Request().Context(), func(q dbstore.Queries) error {
		var reorderErr error
		items, reorderErr = queue.NewPostgresStore(q).ReorderSteer(c.Request().Context(), sid, req.Item, req.Before)
		return reorderErr
	})
	if errors.Is(err, queue.ErrNotPending) {
		return apperror.New(apperror.CodeQueueItemNotPending, nil)
	}
	if err != nil {
		return err
	}
	out := make([]steerQueueItemResponse, 0, len(items))
	for _, item := range items {
		out = append(out, steerQueueItemResponseFrom(item))
	}
	return c.JSON(http.StatusOK, steerQueueResponse{Items: out})
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
	var items []queue.FollowUpItem
	err = h.inTransaction(c.Request().Context(), func(q dbstore.Queries) error {
		var reorderErr error
		items, reorderErr = queue.NewPostgresStore(q).ReorderFollowUp(c.Request().Context(), sid, req.Item, req.Before)
		return reorderErr
	})
	if errors.Is(err, queue.ErrNotPending) {
		return apperror.New(apperror.CodeQueueItemNotPending, nil)
	}
	if err != nil {
		return err
	}
	out := make([]followUpQueueItemResponse, 0, len(items))
	for _, item := range items {
		out = append(out, followUpQueueItemResponseFrom(item))
	}
	return c.JSON(http.StatusOK, followUpQueueResponse{Items: out})
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
	botID, sid, err := h.authorize(c)
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
	var item queue.SteerItem
	err = h.inTransaction(c.Request().Context(), func(q dbstore.Queries) error {
		if err := lockQueueSession(c.Request().Context(), q, botID, sid); err != nil {
			return err
		}
		payload, marshalErr := json.Marshal(map[string]string{"text": strings.TrimSpace(req.Text)})
		if marshalErr != nil {
			return marshalErr
		}
		item, err = queue.NewPostgresStore(q).UpdateSteer(c.Request().Context(), sid, itemID, payload)
		return err
	})
	if errors.Is(err, queue.ErrNotPending) || errors.Is(err, pgx.ErrNoRows) {
		return apperror.New(apperror.CodeQueueItemNotPending, nil)
	}
	if err != nil {
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
	botID, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	itemID, err := queueItemIDParam(c)
	if err != nil {
		return err
	}
	err = h.inTransaction(c.Request().Context(), func(q dbstore.Queries) error {
		if err := lockQueueSession(c.Request().Context(), q, botID, sid); err != nil {
			return err
		}
		return queue.NewPostgresStore(q).CancelSteer(c.Request().Context(), sid, itemID)
	})
	if errors.Is(err, queue.ErrNotPending) || errors.Is(err, pgx.ErrNoRows) {
		return apperror.New(apperror.CodeQueueItemNotPending, nil)
	}
	if err != nil {
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
	botID, sid, err := h.authorize(c)
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
	var item queue.FollowUpItem
	err = h.inTransaction(c.Request().Context(), func(q dbstore.Queries) error {
		if err := lockQueueSession(c.Request().Context(), q, botID, sid); err != nil {
			return err
		}
		payload, marshalErr := json.Marshal(map[string]string{"text": strings.TrimSpace(req.Text)})
		if marshalErr != nil {
			return marshalErr
		}
		item, err = queue.NewPostgresStore(q).UpdateFollowUp(c.Request().Context(), sid, itemID, payload)
		return err
	})
	if errors.Is(err, queue.ErrNotPending) || errors.Is(err, pgx.ErrNoRows) {
		return apperror.New(apperror.CodeQueueItemNotPending, nil)
	}
	if err != nil {
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
	botID, sid, err := h.authorize(c)
	if err != nil {
		return err
	}
	itemID, err := queueItemIDParam(c)
	if err != nil {
		return err
	}
	err = h.inTransaction(c.Request().Context(), func(q dbstore.Queries) error {
		if err := lockQueueSession(c.Request().Context(), q, botID, sid); err != nil {
			return err
		}
		return queue.NewPostgresStore(q).CancelFollowUp(c.Request().Context(), sid, itemID)
	})
	if errors.Is(err, queue.ErrNotPending) || errors.Is(err, pgx.ErrNoRows) {
		return apperror.New(apperror.CodeQueueItemNotPending, nil)
	}
	if err != nil {
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
	var result queue.PromoteFollowUpResult
	err = h.inTransaction(c.Request().Context(), func(q dbstore.Queries) error {
		if err := lockQueueSession(c.Request().Context(), q, botID, sid); err != nil {
			return err
		}
		var promoteErr error
		result, promoteErr = queue.NewPromotionCoordinator(q).PromoteFollowUpToSteer(
			c.Request().Context(),
			queue.PromoteFollowUpRequest{
				BotID: botID, SessionID: sid,
				FollowUp: queue.FollowUpPendingRef{ItemID: queue.FollowUpItemID(itemID)},
			},
		)
		return promoteErr
	})
	if errors.Is(err, queue.ErrNoActiveRun) || errors.Is(err, pgx.ErrNoRows) {
		return apperror.New(apperror.CodeQueueNoActiveRun, nil)
	}
	if errors.Is(err, queue.ErrNotPending) || errors.Is(err, queue.ErrInvalidReference) {
		return apperror.New(apperror.CodeQueueItemNotPending, nil)
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusAccepted, steerQueueItemResponseFrom(result.Steer))
}

func lockQueueSession(ctx context.Context, q dbstore.Queries, botID, sessionID string) error {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	sessionUUID, err := db.ParseUUID(sessionID)
	if err != nil {
		return err
	}
	_, err = q.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: botUUID, SessionID: sessionUUID})
	return err
}

func steerQueueItemResponseFrom(item queue.SteerItem) steerQueueItemResponse {
	return steerQueueItemResponse{ItemID: item.ID, Status: item.Status, Position: item.Position, Text: queuePayloadText(item.Payload), TargetRunID: item.TargetRunID}
}

func followUpQueueItemResponseFrom(item queue.FollowUpItem) followUpQueueItemResponse {
	return followUpQueueItemResponse{ItemID: item.ID, Status: item.Status, Position: item.Position, Text: queuePayloadText(item.Payload), EnqueuedDuringRunID: item.EnqueuedDuringRunID}
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
