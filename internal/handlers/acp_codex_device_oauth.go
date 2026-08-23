package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

type acpCodexDeviceAuthStatus string

const (
	acpCodexDeviceAuthStatusPending   acpCodexDeviceAuthStatus = "pending"
	acpCodexDeviceAuthStatusWriting   acpCodexDeviceAuthStatus = "writing"
	acpCodexDeviceAuthStatusSuccess   acpCodexDeviceAuthStatus = "success"
	acpCodexDeviceAuthStatusError     acpCodexDeviceAuthStatus = "error"
	acpCodexDeviceAuthStatusCancelled acpCodexDeviceAuthStatus = "cancelled"
	acpCodexDeviceAuthStatusExpired   acpCodexDeviceAuthStatus = "expired"

	acpCodexDeviceAuthGenericError = "device authorization failed"
)

type ACPCodexOAuthDeviceAuthorizeResponse struct {
	SessionID       string    `json:"session_id"`
	VerificationURL string    `json:"verification_url"`
	UserCode        string    `json:"user_code"`
	ExpiresAt       time.Time `json:"expires_at"`
	IntervalSeconds int64     `json:"interval_seconds"`
}

type ACPCodexOAuthDeviceSessionRequest struct {
	SessionID string `json:"session_id" validate:"required"`
}

type ACPCodexOAuthDeviceStatusResponse struct {
	Status          string     `json:"status"`
	HasToken        bool       `json:"has_token"`
	AccountID       string     `json:"account_id,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	NextPollAfter   *time.Time `json:"next_poll_after,omitempty"`
	IntervalSeconds int64      `json:"interval_seconds,omitempty"`
	Error           string     `json:"error,omitempty"`
}

type acpCodexDeviceAuthSession struct {
	SessionID         string
	BotID             string
	ChannelIdentityID string
	DeviceAuthID      string
	UserCode          string
	VerificationURL   string
	CreatedAt         time.Time
	ExpiresAt         time.Time
	TerminalExpiresAt time.Time
	Interval          time.Duration
	NextPollAfter     time.Time
	Status            acpCodexDeviceAuthStatus
	Polling           bool
	Generation        int64
	AccountID         string
	LastError         string
	WriteCancel       context.CancelFunc
}

// AuthorizeDevice godoc
// @Summary Start Codex ACP device code authorization
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} ACPCodexOAuthDeviceAuthorizeResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acp/codex/oauth/device/authorize [post].
func (h *ACPCodexOAuthHandler) AuthorizeDevice(c echo.Context) error {
	botID, channelIdentityID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.provider == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "openai codex oauth provider is not configured")
	}
	if h.acpWorkspace == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "workspace manager is not configured")
	}
	if err := h.ensureManagedWorkspace(c.Request().Context(), botID); err != nil {
		return err
	}

	device, err := h.provider.StartOpenAICodexACPDeviceAuthorization(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	deviceAuthID := strings.TrimSpace(device.DeviceAuthID)
	userCode := strings.TrimSpace(device.UserCode)
	verificationURL := strings.TrimSpace(device.VerificationURL)
	if deviceAuthID == "" || userCode == "" || verificationURL == "" {
		return echo.NewHTTPError(http.StatusInternalServerError, "codex device authorization response is incomplete")
	}
	sessionID, err := generateACPCodexDeviceAuthSessionID()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	now := time.Now().UTC()
	interval := time.Duration(device.IntervalSeconds) * time.Second
	if interval < acpCodexDeviceAuthMinInterval {
		interval = acpCodexDeviceAuthMinInterval
	}
	session := &acpCodexDeviceAuthSession{
		SessionID:         sessionID,
		BotID:             botID,
		ChannelIdentityID: channelIdentityID,
		DeviceAuthID:      deviceAuthID,
		UserCode:          userCode,
		VerificationURL:   verificationURL,
		CreatedAt:         now,
		ExpiresAt:         now.Add(acpCodexDeviceAuthTTL),
		Interval:          interval,
		NextPollAfter:     now,
		Status:            acpCodexDeviceAuthStatusPending,
		Generation:        1,
	}

	h.mu.Lock()
	if h.deviceSessions == nil {
		h.deviceSessions = map[string]*acpCodexDeviceAuthSession{}
	}
	h.pruneExpiredLocked(now)
	h.deviceSessions[sessionID] = session
	h.mu.Unlock()

	return c.JSON(http.StatusOK, ACPCodexOAuthDeviceAuthorizeResponse{
		SessionID:       sessionID,
		VerificationURL: verificationURL,
		UserCode:        userCode,
		ExpiresAt:       session.ExpiresAt,
		IntervalSeconds: int64(interval / time.Second),
	})
}

// PollDevice godoc
// @Summary Poll Codex ACP device code authorization
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param request body ACPCodexOAuthDeviceSessionRequest true "Device authorization session"
// @Success 200 {object} ACPCodexOAuthDeviceStatusResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acp/codex/oauth/device/poll [post].
func (h *ACPCodexOAuthHandler) PollDevice(c echo.Context) error {
	botID, channelIdentityID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.provider == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "openai codex oauth provider is not configured")
	}
	var req ACPCodexOAuthDeviceSessionRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "session_id is required")
	}

	session, generation, shouldPoll, err := h.prepareDevicePoll(sessionID, botID, channelIdentityID, time.Now().UTC())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if !shouldPoll {
		return c.JSON(http.StatusOK, deviceStatusResponse(session))
	}

	pollResult, pollErr := h.provider.PollOpenAICodexACPDeviceAuthorization(c.Request().Context(), session.DeviceAuthID, session.UserCode)
	if pollErr != nil {
		if isTransientCodexDevicePollError(pollErr) {
			updated := h.finishDevicePollPending(sessionID, generation, time.Now().UTC())
			return c.JSON(http.StatusOK, deviceStatusResponse(updated))
		}
		updated := h.finishDevicePollError(sessionID, generation, pollErr, time.Now().UTC())
		return c.JSON(http.StatusOK, deviceStatusResponse(updated))
	}
	if pollResult.Pending {
		updated := h.finishDevicePollPending(sessionID, generation, time.Now().UTC())
		return c.JSON(http.StatusOK, deviceStatusResponse(updated))
	}

	creds, exchangeErr := h.provider.ExchangeOpenAICodexACPDeviceCode(c.Request().Context(), pollResult.AuthorizationCode, pollResult.CodeVerifier)
	if exchangeErr != nil {
		updated := h.finishDevicePollError(sessionID, generation, exchangeErr, time.Now().UTC())
		return c.JSON(http.StatusOK, deviceStatusResponse(updated))
	}
	writeCtx, writeCancel, err := h.beginDeviceAuthWrite(c.Request().Context(), sessionID, generation, time.Now().UTC())
	if err != nil {
		updated := h.deviceSessionSnapshot(sessionID)
		if updated == nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return c.JSON(http.StatusOK, deviceStatusResponse(updated))
	}
	defer writeCancel()
	writeErr := h.writeCodexOAuthAuth(writeCtx, botID, creds)
	updated := h.finishDeviceAuthWrite(sessionID, generation, creds.AccountID, writeErr, time.Now().UTC())
	return c.JSON(http.StatusOK, deviceStatusResponse(updated))
}

// CancelDevice godoc
// @Summary Cancel Codex ACP device code authorization
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param request body ACPCodexOAuthDeviceSessionRequest true "Device authorization session"
// @Success 200 {object} ACPCodexOAuthDeviceStatusResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acp/codex/oauth/device/cancel [post].
func (h *ACPCodexOAuthHandler) CancelDevice(c echo.Context) error {
	botID, channelIdentityID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	var req ACPCodexOAuthDeviceSessionRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "session_id is required")
	}

	var writeCancel context.CancelFunc
	h.mu.Lock()
	now := time.Now().UTC()
	h.pruneExpiredLocked(now)
	session, err := h.requireDeviceSessionLocked(sessionID, botID, channelIdentityID)
	if err != nil {
		h.mu.Unlock()
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if session.Status == acpCodexDeviceAuthStatusPending || session.Status == acpCodexDeviceAuthStatusWriting {
		writeCancel = session.WriteCancel
		session.WriteCancel = nil
		session.Status = acpCodexDeviceAuthStatusCancelled
		session.Polling = false
		session.Generation++
		session.TerminalExpiresAt = now.Add(acpCodexDeviceAuthTerminalTTL)
	}
	response := deviceStatusResponse(session)
	h.mu.Unlock()
	if writeCancel != nil {
		writeCancel()
	}
	return c.JSON(http.StatusOK, response)
}

func (h *ACPCodexOAuthHandler) prepareDevicePoll(sessionID, botID, channelIdentityID string, now time.Time) (*acpCodexDeviceAuthSession, int64, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneExpiredLocked(now)
	session, err := h.requireDeviceSessionLocked(sessionID, botID, channelIdentityID)
	if err != nil {
		return nil, 0, false, err
	}
	if session.isTerminal() || session.Status == acpCodexDeviceAuthStatusWriting {
		return session.clone(), session.Generation, false, nil
	}
	if !session.ExpiresAt.IsZero() && now.After(session.ExpiresAt) {
		expireDeviceSessionLocked(session, now)
		return session.clone(), session.Generation, false, nil
	}
	if session.Polling || (!session.NextPollAfter.IsZero() && now.Before(session.NextPollAfter)) {
		return session.clone(), session.Generation, false, nil
	}
	session.Polling = true
	session.NextPollAfter = now.Add(session.Interval)
	return session.clone(), session.Generation, true, nil
}

func (h *ACPCodexOAuthHandler) finishDevicePollPending(sessionID string, generation int64, now time.Time) *acpCodexDeviceAuthSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.deviceSessions[sessionID]
	if !deviceSessionMatchesGeneration(session, generation) {
		return cloneDeviceSession(session)
	}
	if !session.ExpiresAt.IsZero() && now.After(session.ExpiresAt) {
		expireDeviceSessionLocked(session, now)
		return session.clone()
	}
	session.Polling = false
	session.NextPollAfter = now.Add(session.Interval)
	return session.clone()
}

func (h *ACPCodexOAuthHandler) finishDevicePollError(sessionID string, generation int64, err error, now time.Time) *acpCodexDeviceAuthSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.deviceSessions[sessionID]
	if !deviceSessionMatchesGeneration(session, generation) {
		return cloneDeviceSession(session)
	}
	h.logCodexDeviceAuthError("codex device authorization poll failed", session, err)
	session.Status = acpCodexDeviceAuthStatusError
	session.Polling = false
	session.LastError = acpCodexDeviceAuthGenericError
	session.TerminalExpiresAt = now.Add(acpCodexDeviceAuthTerminalTTL)
	return session.clone()
}

func (h *ACPCodexOAuthHandler) beginDeviceAuthWrite(
	parentCtx context.Context,
	sessionID string,
	generation int64,
	now time.Time,
) (context.Context, context.CancelFunc, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.deviceSessions[sessionID]
	if !deviceSessionMatchesGeneration(session, generation) {
		return nil, nil, errors.New("device authorization session changed before auth write")
	}
	if !session.ExpiresAt.IsZero() && now.After(session.ExpiresAt) {
		expireDeviceSessionLocked(session, now)
		return nil, nil, errors.New("device authorization session expired before auth write")
	}
	var writeCtx context.Context
	var writeCancel context.CancelFunc
	if !session.ExpiresAt.IsZero() {
		writeCtx, writeCancel = context.WithDeadline(parentCtx, session.ExpiresAt)
	} else {
		writeCtx, writeCancel = context.WithCancel(parentCtx)
	}
	session.Status = acpCodexDeviceAuthStatusWriting
	session.WriteCancel = writeCancel
	return writeCtx, writeCancel, nil
}

func (h *ACPCodexOAuthHandler) finishDeviceAuthWrite(sessionID string, generation int64, accountID string, writeErr error, now time.Time) *acpCodexDeviceAuthSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.deviceSessions[sessionID]
	if !deviceSessionMatchesWriting(session, generation) {
		return cloneDeviceSession(session)
	}
	writeCancel := session.WriteCancel
	session.WriteCancel = nil
	session.Polling = false
	session.TerminalExpiresAt = now.Add(acpCodexDeviceAuthTerminalTTL)
	if !session.ExpiresAt.IsZero() && now.After(session.ExpiresAt) {
		expireDeviceSessionLocked(session, now)
		if writeCancel != nil {
			writeCancel()
		}
		return session.clone()
	}
	if writeErr != nil {
		h.logCodexDeviceAuthError("codex device authorization write failed", session, writeErr)
		session.Status = acpCodexDeviceAuthStatusError
		session.LastError = acpCodexDeviceAuthGenericError
		if writeCancel != nil {
			writeCancel()
		}
		return session.clone()
	}
	session.Status = acpCodexDeviceAuthStatusSuccess
	session.AccountID = strings.TrimSpace(accountID)
	session.LastError = ""
	if writeCancel != nil {
		writeCancel()
	}
	return session.clone()
}

func (h *ACPCodexOAuthHandler) deviceSessionSnapshot(sessionID string) *acpCodexDeviceAuthSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return cloneDeviceSession(h.deviceSessions[sessionID])
}

func (h *ACPCodexOAuthHandler) requireDeviceSessionLocked(sessionID, botID, channelIdentityID string) (*acpCodexDeviceAuthSession, error) {
	session := h.deviceSessions[sessionID]
	if session == nil {
		return nil, errors.New("device authorization session is invalid or expired")
	}
	if session.BotID != botID {
		return nil, errors.New("device authorization session does not match bot")
	}
	if session.ChannelIdentityID != channelIdentityID {
		return nil, errors.New("device authorization session does not match channel identity")
	}
	return session, nil
}

func expireDeviceSessionLocked(session *acpCodexDeviceAuthSession, now time.Time) {
	if session == nil || session.isTerminal() {
		return
	}
	writeCancel := session.WriteCancel
	session.WriteCancel = nil
	session.Status = acpCodexDeviceAuthStatusExpired
	session.Polling = false
	session.Generation++
	session.TerminalExpiresAt = now.Add(acpCodexDeviceAuthTerminalTTL)
	if writeCancel != nil {
		writeCancel()
	}
}

func (s *acpCodexDeviceAuthSession) isTerminal() bool {
	if s == nil {
		return true
	}
	switch s.Status {
	case acpCodexDeviceAuthStatusSuccess, acpCodexDeviceAuthStatusError, acpCodexDeviceAuthStatusCancelled, acpCodexDeviceAuthStatusExpired:
		return true
	default:
		return false
	}
}

func (s *acpCodexDeviceAuthSession) clone() *acpCodexDeviceAuthSession {
	return cloneDeviceSession(s)
}

func cloneDeviceSession(session *acpCodexDeviceAuthSession) *acpCodexDeviceAuthSession {
	if session == nil {
		return nil
	}
	next := *session
	next.WriteCancel = nil
	return &next
}

func deviceSessionMatchesGeneration(session *acpCodexDeviceAuthSession, generation int64) bool {
	return session != nil && session.Generation == generation && session.Status == acpCodexDeviceAuthStatusPending && session.Polling
}

func deviceSessionMatchesWriting(session *acpCodexDeviceAuthSession, generation int64) bool {
	return session != nil && session.Generation == generation && session.Status == acpCodexDeviceAuthStatusWriting && session.Polling
}

func deviceStatusResponse(session *acpCodexDeviceAuthSession) ACPCodexOAuthDeviceStatusResponse {
	if session == nil {
		return ACPCodexOAuthDeviceStatusResponse{Status: string(acpCodexDeviceAuthStatusError)}
	}
	status := session.Status
	if status == acpCodexDeviceAuthStatusWriting {
		status = acpCodexDeviceAuthStatusPending
	}
	expiresAt := session.ExpiresAt
	nextPollAfter := session.NextPollAfter
	return ACPCodexOAuthDeviceStatusResponse{
		Status:          string(status),
		HasToken:        session.Status == acpCodexDeviceAuthStatusSuccess,
		AccountID:       session.AccountID,
		ExpiresAt:       &expiresAt,
		NextPollAfter:   &nextPollAfter,
		IntervalSeconds: int64(session.Interval / time.Second),
		Error:           session.LastError,
	}
}

func isTransientCodexDevicePollError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "execute codex device poll request") ||
		strings.Contains(message, "read codex device poll response")
}

func (h *ACPCodexOAuthHandler) logCodexDeviceAuthError(event string, session *acpCodexDeviceAuthSession, err error) {
	if err == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("event", event),
		slog.String("error_type", fmt.Sprintf("%T", err)),
		slog.Bool("transient", isTransientCodexDevicePollError(err)),
	}
	if session != nil {
		attrs = append(attrs,
			slog.String("bot_id", session.BotID),
			slog.String("session_hash", codexDeviceSessionLogID(session.SessionID)),
			slog.String("status", string(session.Status)),
		)
	}
	logger := h.logger
	if logger == nil {
		logger = slog.Default().With(slog.String("handler", "acp_codex_oauth"))
	}
	logger.LogAttrs(context.Background(), slog.LevelWarn, "codex device authorization error", attrs...)
}

func codexDeviceSessionLogID(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:8])
}

func generateACPCodexDeviceAuthSessionID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return acpCodexDeviceAuthSessionPrefix + hex.EncodeToString(raw[:]), nil
}
