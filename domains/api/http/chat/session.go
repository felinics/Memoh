package chat

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	session "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/api/bot"
	httpx "github.com/memohai/memoh/domains/api/http"
	agenthttp "github.com/memohai/memoh/domains/api/http/agent"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/apperror"
)

// SessionHandler handles bot session CRUD endpoints.
type SessionHandler struct {
	sessionService *session.Service
	threadEnricher threadEnricher
	acpPool        acpSessionCloser
	botService     *bot.Service
	accountService *account.Service
	logger         *slog.Logger
}

type acpSessionCloser interface {
	CloseSession(sessionID string) error
	BindRuntime(botID, runtimeID, sessionID, agentID, projectPath, runtimeOwnerAccountID string) error
}

type threadEnricher interface {
	EnrichThreads(ctx context.Context, botID string, threads []session.Thread) ([]session.Thread, error)
}

// NewSessionHandler creates a SessionHandler.
func NewSessionHandler(log *slog.Logger, sessionService *session.Service, acpPool acpSessionCloser, botService *bot.Service, accountService *account.Service) *SessionHandler {
	return &SessionHandler{
		sessionService: sessionService,
		acpPool:        acpPool,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "session")),
	}
}

// SetThreadEnricher installs the Channel-owned route projection used by list
// responses. Thread persistence stays independent from the route table.
func (h *SessionHandler) SetThreadEnricher(enricher threadEnricher) {
	h.threadEnricher = enricher
}

// Register registers session routes.
func (h *SessionHandler) Register(e *echo.Echo) {
	g := e.Group("/bots/:bot_id/sessions")
	g.POST("", h.CreateSession)
	g.GET("", h.ListSessions)
	g.GET("/:session_id", h.GetSession)
	g.POST("/:session_id/fork", h.ForkSession)
	g.PATCH("/:session_id", h.UpdateSession)
	g.DELETE("/:session_id", h.DeleteSession)
}

type createSessionRequest struct {
	Type            string         `json:"type,omitempty"`
	SessionMode     string         `json:"session_mode,omitempty"`
	RuntimeType     string         `json:"runtime_type,omitempty"`
	Title           string         `json:"title"`
	ChannelType     string         `json:"channel_type,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	RuntimeMetadata map[string]any `json:"runtime_metadata,omitempty"`
	// ACPRuntimeID optionally binds a warm pre-session runtime (created via
	// POST /bots/{bot_id}/acp-runtimes) to the new ACP session. It is a
	// transient in-memory handle reference, never persisted in metadata.
	ACPRuntimeID string `json:"acp_runtime_id,omitempty"`
}

type updateSessionRequest struct {
	Title           *string        `json:"title,omitempty"`
	Type            *string        `json:"type,omitempty"`
	SessionMode     *string        `json:"session_mode,omitempty"`
	RuntimeType     *string        `json:"runtime_type,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	RuntimeMetadata map[string]any `json:"runtime_metadata,omitempty"`
}

type forkSessionRequest struct {
	MessageID string `json:"message_id" validate:"required"`
	Title     string `json:"title,omitempty"`
}

// CreateSession godoc
// @Summary Create a new chat session
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param body body createSessionRequest true "Session data"
// @Success 201 {object} session.Thread
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions [post].
func (h *SessionHandler) CreateSession(c echo.Context) error {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	var req createSessionRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind session payload", err)
	}
	sessionType := strings.TrimSpace(req.Type)
	if sessionType == "" {
		sessionType = session.TypeChat
	}
	if !session.IsKnownType(sessionType) {
		return apperror.Field("type", apperror.FieldUnsupported)
	}
	targetType, targetMode, targetRuntimeType, err := session.ResolveDescriptor(sessionType, req.SessionMode, req.RuntimeType)
	if err != nil {
		return apperror.Invalid("resolve session descriptor", err)
	}
	bot, err := httpx.AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, requiredPermissionForSessionRuntime(targetMode, targetRuntimeType))
	if err != nil {
		return err
	}
	if targetRuntimeType == session.RuntimeACPAgent {
		req.Metadata = session.ApplyACPMetadataDefaults(mergeSessionMetadata(req.Metadata, req.RuntimeMetadata))
		req.RuntimeMetadata = session.ApplyACPMetadataDefaults(mergeSessionMetadata(req.RuntimeMetadata, req.Metadata))
		if err := validateACPCreate(bot, req.Metadata); err != nil {
			return err
		}
	}
	sess, err := h.sessionService.Create(c.Request().Context(), session.CreateInput{
		BotID:           bot.ID,
		ChannelType:     req.ChannelType,
		Type:            targetType,
		SessionMode:     targetMode,
		RuntimeType:     targetRuntimeType,
		Title:           req.Title,
		Metadata:        req.Metadata,
		RuntimeMetadata: req.RuntimeMetadata,
		CreatedByUserID: channelIdentityID,
	})
	if err != nil {
		return sessionServiceError(err)
	}
	// Best-effort bind of a warm pre-session runtime: the session lives in
	// the database and the runtime in memory, so this is sequenced (bind only
	// after a successful create), not transactional. A failed bind keeps the
	// session — the first prompt simply cold starts a runtime.
	if runtimeID := strings.TrimSpace(req.ACPRuntimeID); runtimeID != "" && session.IsACPRuntime(sess) && h.acpPool != nil {
		if bindErr := h.acpPool.BindRuntime(
			bot.ID,
			runtimeID,
			sess.ID,
			httpx.SessionMetadataString(sess.Metadata, "acp_agent_id"),
			httpx.SessionMetadataString(sess.Metadata, "project_path"),
			httpx.SessionMetadataString(sess.Metadata, "runtime_owner_account_id"),
		); bindErr != nil {
			h.logger.Warn("failed to bind ACP runtime to new session; first prompt will cold start",
				slog.String("session_id", sess.ID),
				slog.String("runtime_id", runtimeID),
				slog.Any("error", bindErr),
			)
		}
	}
	return c.JSON(http.StatusCreated, sess)
}

// ForkSession godoc
// @Summary Fork a chat session from an assistant reply
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Source session ID"
// @Param body body forkSessionRequest true "Fork source message"
// @Success 201 {object} session.Thread
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/fork [post].
func (h *SessionHandler) ForkSession(c echo.Context) error {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return apperror.Required("session_id")
	}
	bot, _, source, err := h.authorizeSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}
	if source.Type != session.TypeChat {
		return apperror.Conflict("fork session", nil)
	}

	var req forkSessionRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind session payload", err)
	}
	messageID := strings.TrimSpace(req.MessageID)
	if messageID == "" {
		return apperror.Required("message_id")
	}
	if _, err := uuid.Parse(messageID); err != nil {
		return apperror.Field("message_id", apperror.FieldInvalid)
	}

	forked, err := h.sessionService.ForkFromAssistantMessage(c.Request().Context(), session.ForkFromAssistantInput{
		BotID:           bot.ID,
		ThreadID:        source.ID,
		MessageID:       messageID,
		Title:           strings.TrimSpace(req.Title),
		CreatedByUserID: channelIdentityID,
	})
	if err != nil {
		return sessionForkError(err)
	}
	return c.JSON(http.StatusCreated, forked)
}

// ListSessions godoc
// @Summary List bot sessions
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param types query string false "Comma-separated session types to include. Defaults to user-facing types (chat,discuss,acp_agent), or subagent when parent_session_id is set."
// @Param parent_session_id query string false "Only include child sessions under this parent session."
// @Param limit query int false "Page size (1..200). Defaults to 50."
// @Param cursor query string false "Opaque cursor returned as next_cursor on a previous page."
// @Success 200 {object} listSessionsResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions [get].
func (h *SessionHandler) ListSessions(c echo.Context) error {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	record, perms, err := h.authorizeBotSessionAccess(c, channelIdentityID, botID)
	if err != nil {
		return err
	}
	limit, err := parseSessionLimitParam(c.QueryParam("limit"))
	if err != nil {
		return err
	}
	cursor, err := decodeSessionCursor(c.QueryParam("cursor"))
	if err != nil {
		return err
	}
	parentSessionID, err := parseSessionParentIDParam(c.QueryParam("parent_session_id"))
	if err != nil {
		return err
	}
	if parentSessionID != "" {
		if _, _, _, err := h.authorizeSession(c, channelIdentityID, record.ID, parentSessionID); err != nil {
			return err
		}
	}
	types, err := parseSessionTypesParam(c.QueryParam("types"), parentSessionID != "")
	if err != nil {
		return err
	}
	filter := session.ListFilter{ParentThreadID: parentSessionID}

	// Initialize to an empty slice so an empty page serializes as `"items": []`
	// rather than `"items": null`, sparing clients a null check.
	sessions := []session.Thread{}
	var (
		nextCursor   session.Cursor
		hasMorePages bool
	)
	// Probe one row past the requested page so we can answer "is there more?"
	// without forcing the client into a tail request that returns []. The
	// extra row is sliced off before the response is built.
	probeLimit := limit + 1
	if bot.HasPermission(perms, bot.PermissionManage) {
		var rows []session.Thread
		rows, err = h.sessionService.ListByBotPagedWithFilter(c.Request().Context(), record.ID, types, cursor, probeLimit, filter)
		if err == nil {
			sessions, hasMorePages = trimPagedSessions(rows, limit)
			if hasMorePages {
				last := sessions[len(sessions)-1]
				nextCursor = session.Cursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
			}
		}
	} else {
		var preFilter []session.Thread
		preFilter, err = h.sessionService.ListByBotAndCreatedByUserPagedWithFilter(c.Request().Context(), record.ID, channelIdentityID, types, cursor, probeLimit, filter)
		if err == nil {
			var page []session.Thread
			page, hasMorePages = trimPagedSessions(preFilter, limit)
			if hasMorePages {
				last := page[len(page)-1]
				nextCursor = session.Cursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
			}
			sessions = filterSessionsForPermissions(page, channelIdentityID, perms)
		}
	}
	if err != nil {
		return apperror.Internal("list sessions", err)
	}
	if h.threadEnricher != nil {
		sessions, err = h.threadEnricher.EnrichThreads(c.Request().Context(), record.ID, sessions)
		if err != nil {
			return apperror.Internal("list sessions", err)
		}
	}

	encoded := ""
	if hasMorePages {
		encoded = encodeSessionCursor(nextCursor)
	}
	return c.JSON(http.StatusOK, listSessionsResponse{Items: sessions, NextCursor: encoded})
}

// trimPagedSessions implements the limit+1 has-more probe: if the SQL layer
// returned the extra row, slice it off and signal hasMore; otherwise the
// caller has reached the end of the listing and next_cursor must stay empty.
//
// The returned page is the slice the caller should derive next_cursor from
// before any in-memory permission filtering. next_cursor must reflect the DB
// position to resume from, not filter survivorship — otherwise a page whose
// rows were all dropped by the permission filter would terminate pagination
// while older accessible rows still exist on disk.
func trimPagedSessions(rows []session.Thread, limit int64) ([]session.Thread, bool) {
	if int64(len(rows)) > limit {
		return rows[:limit], true
	}
	return rows, false
}

// listSessionsResponse carries one page of sessions. NextCursor is empty
// exactly when the caller has reached the end of the listing — clients should
// stop paging on an empty cursor and never expect a follow-up empty page.
type listSessionsResponse struct {
	Items      []session.Thread `json:"items"`
	NextCursor string           `json:"next_cursor"`
}

const (
	sessionListDefaultLimit = 50
	sessionListMaxLimit     = 200
)

func parseSessionTypesParam(raw string, hasParentFilter bool) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if hasParentFilter {
			return []string{session.TypeSubagent}, nil
		}
		return session.UserFacingSessionTypes(), nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		if !session.IsKnownType(token) {
			return nil, apperror.Field("types", apperror.FieldUnsupported)
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	if len(out) == 0 {
		return nil, apperror.Required("types")
	}
	return out, nil
}

func parseSessionLimitParam(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sessionListDefaultLimit, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, apperror.Field("limit", apperror.FieldInvalid)
	}
	if value < 1 || value > sessionListMaxLimit {
		return 0, apperror.Field("limit", apperror.FieldOutOfRange)
	}
	return value, nil
}

func parseSessionParentIDParam(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if _, err := uuid.Parse(value); err != nil {
		return "", apperror.Field("parent_session_id", apperror.FieldInvalid)
	}
	return value, nil
}

// encodeSessionCursor packs the keyset cursor as base64(updated_at|id) where
// the timestamp is RFC3339Nano. The id tiebreak in the SQL handles uniqueness
// when two rows share the same timestamp.
func encodeSessionCursor(c session.Cursor) string {
	raw := c.UpdatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeSessionCursor(raw string) (session.Cursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return session.Cursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return session.Cursor{}, apperror.Field("cursor", apperror.FieldInvalid)
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return session.Cursor{}, apperror.Field("cursor", apperror.FieldInvalid)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return session.Cursor{}, apperror.Field("cursor", apperror.FieldInvalid)
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return session.Cursor{}, apperror.Field("cursor", apperror.FieldInvalid)
	}
	return session.Cursor{UpdatedAt: updatedAt, ID: parts[1]}, nil
}

// GetSession godoc
// @Summary Get a session by ID
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} session.Thread
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id} [get].
func (h *SessionHandler) GetSession(c echo.Context) error {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return apperror.Required("session_id")
	}
	_, _, sess, err := h.authorizeSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, sess)
}

// UpdateSession godoc
// @Summary Update a session
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param body body updateSessionRequest true "Fields to update"
// @Success 200 {object} session.Thread
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id} [patch].
func (h *SessionHandler) UpdateSession(c echo.Context) error {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return apperror.Required("session_id")
	}

	record, perms, existing, err := h.authorizeSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}

	var req updateSessionRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind session payload", err)
	}

	// Mirror the create-path guard in session.ResolveDescriptor: the legacy
	// acp_agent type unambiguously means an ACP runtime, so an explicit non-ACP
	// runtime_type in the same PATCH is contradictory and must fail loudly
	// rather than silently downgrade the session to a plain model chat.
	if req.Type != nil && req.RuntimeType != nil {
		legacyType := strings.TrimSpace(*req.Type)
		runtimeType := strings.TrimSpace(*req.RuntimeType)
		if legacyType == session.TypeACPAgent && runtimeType != "" && runtimeType != session.RuntimeACPAgent {
			return apperror.Invalid("resolve session type conflict", nil)
		}
	}

	result := existing
	if req.Type != nil || req.SessionMode != nil || req.RuntimeType != nil || req.Metadata != nil || req.RuntimeMetadata != nil {
		targetType := existing.Type
		targetMode, targetRuntime := normalizedSessionDescriptor(existing)
		if req.Type != nil {
			targetType = strings.TrimSpace(*req.Type)
			if targetType == "" {
				targetType = session.TypeChat
			}
			targetMode, targetRuntime = session.DescriptorFromLegacyType(targetType)
		}
		if !session.IsKnownType(targetType) {
			return apperror.Field("type", apperror.FieldUnsupported)
		}
		if req.SessionMode != nil {
			targetMode = strings.TrimSpace(*req.SessionMode)
			if !session.IsKnownSessionMode(targetMode) {
				return apperror.Field("session_mode", apperror.FieldUnsupported)
			}
		}
		if req.RuntimeType != nil {
			targetRuntime = strings.TrimSpace(*req.RuntimeType)
			if !session.IsKnownRuntimeType(targetRuntime) {
				return apperror.Field("runtime_type", apperror.FieldUnsupported)
			}
		}
		targetType, targetMode, targetRuntime, err = session.ResolveDescriptor(targetType, targetMode, targetRuntime)
		if err != nil {
			return apperror.Invalid("resolve session descriptor", err)
		}
		if !bot.HasPermission(perms, requiredPermissionForSessionRuntime(targetMode, targetRuntime)) {
			return apperror.Forbidden("authorize session access", nil)
		}
		targetMetadata := cloneSessionMetadata(existing.Metadata)
		if req.Metadata != nil {
			targetMetadata = cloneSessionMetadata(req.Metadata)
		}
		targetRuntimeMetadata := cloneSessionMetadata(existing.RuntimeMetadata)
		if req.RuntimeMetadata != nil {
			targetRuntimeMetadata = cloneSessionMetadata(req.RuntimeMetadata)
		}
		if targetRuntime == session.RuntimeACPAgent {
			targetMetadata = session.ApplyACPMetadataDefaults(mergeSessionMetadata(targetMetadata, targetRuntimeMetadata))
			targetRuntimeMetadata = session.ApplyACPMetadataDefaults(mergeSessionMetadata(targetRuntimeMetadata, targetMetadata))
		}
		agentChanged := sessionAgentConfigChanged(existing, targetMode, targetRuntime, targetMetadata, targetRuntimeMetadata)
		if agentChanged {
			count, err := h.sessionService.MessageCount(c.Request().Context(), sessionID)
			if err != nil {
				return apperror.Internal("update session", err)
			}
			if count > 0 {
				return apperror.Conflict("update session agent", nil)
			}
		}
		if targetRuntime == session.RuntimeACPAgent {
			if err := validateACPCreate(record, targetMetadata); err != nil {
				return err
			}
		} else if session.IsACPRuntime(existing) || req.Type != nil || req.RuntimeType != nil || req.RuntimeMetadata != nil {
			targetMetadata = stripACPMetadata(targetMetadata)
			targetRuntimeMetadata = map[string]any{}
		}
		if targetType != existing.Type || targetMode != existing.SessionMode || targetRuntime != existing.RuntimeType || req.Metadata != nil || req.RuntimeMetadata != nil || req.SessionMode != nil || req.RuntimeType != nil {
			result, err = h.sessionService.UpdateDescriptorAndMetadataWithOwner(c.Request().Context(), sessionID, targetType, targetMode, targetRuntime, targetMetadata, targetRuntimeMetadata, channelIdentityID)
			if err != nil {
				return sessionServiceError(err)
			}
			if agentChanged && session.IsACPRuntime(existing) && h.acpPool != nil {
				if closeErr := h.acpPool.CloseSession(sessionID); closeErr != nil {
					h.logger.Warn("failed to close ACP runtime after session update", slog.String("session_id", sessionID), slog.Any("error", closeErr))
				}
			}
		}
	}
	if req.Title != nil {
		resultMode, resultRuntime := normalizedSessionDescriptor(result)
		if !bot.HasPermission(perms, requiredPermissionForSessionRuntime(resultMode, resultRuntime)) {
			return apperror.Forbidden("authorize session access", nil)
		}
		result, err = h.sessionService.UpdateTitle(c.Request().Context(), sessionID, *req.Title)
		if err != nil {
			return apperror.Internal("update session", err)
		}
	}
	if req.Title == nil && req.Metadata == nil && req.Type == nil && req.SessionMode == nil && req.RuntimeType == nil && req.RuntimeMetadata == nil {
		result = existing
	}
	return c.JSON(http.StatusOK, result)
}

// DeleteSession godoc
// @Summary Soft-delete a session
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 204
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id} [delete].
func (h *SessionHandler) DeleteSession(c echo.Context) error {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return apperror.Required("session_id")
	}
	_, perms, existing, err := h.authorizeSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}
	existingMode, existingRuntime := normalizedSessionDescriptor(existing)
	if !bot.HasPermission(perms, requiredPermissionForSessionRuntime(existingMode, existingRuntime)) {
		return apperror.Forbidden("authorize session access", nil)
	}
	if session.IsACPRuntime(existing) && h.acpPool != nil {
		if closeErr := h.acpPool.CloseSession(sessionID); closeErr != nil {
			h.logger.Warn("failed to close ACP runtime before session delete", slog.String("session_id", sessionID), slog.Any("error", closeErr))
		}
	}
	if err := h.sessionService.SoftDelete(c.Request().Context(), sessionID); err != nil {
		return apperror.Internal("delete session", err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *SessionHandler) authorizeBotSessionAccess(c echo.Context, channelIdentityID, botID string) (bot.Bot, []string, error) {
	record, err := httpx.AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bot.PermissionChat)
	if err != nil {
		record, err = httpx.AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bot.PermissionWorkspaceExec)
		if err != nil {
			return bot.Bot{}, nil, err
		}
	}
	perms, err := h.resolveCurrentUserPermissions(c, channelIdentityID, record.ID)
	if err != nil {
		return bot.Bot{}, nil, err
	}
	return record, perms, nil
}

func (h *SessionHandler) authorizeSession(c echo.Context, channelIdentityID, botID, sessionID string) (bot.Bot, []string, session.Thread, error) {
	record, perms, err := h.authorizeBotSessionAccess(c, channelIdentityID, botID)
	if err != nil {
		return bot.Bot{}, nil, session.Thread{}, err
	}
	sess, err := h.sessionService.Get(c.Request().Context(), sessionID)
	if err != nil || sess.BotID != record.ID {
		return bot.Bot{}, nil, session.Thread{}, apperror.NotFound("get session", nil)
	}
	if !canAccessSession(sess, channelIdentityID, perms) {
		return bot.Bot{}, nil, session.Thread{}, apperror.NotFound("get session", nil)
	}
	return record, perms, sess, nil
}

func (h *SessionHandler) resolveCurrentUserPermissions(c echo.Context, channelIdentityID, botID string) ([]string, error) {
	if h.botService == nil || h.accountService == nil {
		return nil, apperror.Internal("resolve user permissions", nil)
	}
	isAdmin, err := h.accountService.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return nil, apperror.Internal("resolve user permissions", err)
	}
	perms, err := h.botService.ResolveUserPermissions(c.Request().Context(), botID, channelIdentityID, isAdmin)
	if err != nil {
		return nil, apperror.Internal("resolve user permissions", err)
	}
	return perms, nil
}

func requiredReadPermissionForSessionType(sessionType string) string {
	switch strings.TrimSpace(sessionType) {
	case session.TypeChat:
		return bot.PermissionChat
	case session.TypeSubagent:
		return bot.PermissionChat
	case session.TypeACPAgent:
		return bot.PermissionWorkspaceExec
	default:
		return bot.PermissionManage
	}
}

func requiredWritePermissionForSessionType(sessionType string) string {
	switch strings.TrimSpace(sessionType) {
	case session.TypeChat:
		return bot.PermissionChat
	case session.TypeACPAgent:
		return bot.PermissionWorkspaceExec
	default:
		return bot.PermissionManage
	}
}

func requiredReadPermissionForSessionRuntime(sessionType, runtimeType string) string {
	sessionType = strings.TrimSpace(sessionType)
	if strings.TrimSpace(runtimeType) == session.RuntimeACPAgent {
		switch sessionType {
		case session.TypeACPAgent, session.TypeChat, session.TypeDiscuss:
			return bot.PermissionWorkspaceExec
		default:
			return requiredReadPermissionForSessionType(sessionType)
		}
	}
	return requiredReadPermissionForSessionType(sessionType)
}

func requiredPermissionForSessionRuntime(sessionType, runtimeType string) string {
	sessionType = strings.TrimSpace(sessionType)
	if strings.TrimSpace(runtimeType) == session.RuntimeACPAgent {
		switch sessionType {
		case session.TypeACPAgent, session.TypeChat, session.TypeDiscuss:
		default:
			return requiredWritePermissionForSessionType(sessionType)
		}
		return bot.PermissionWorkspaceExec
	}
	return requiredWritePermissionForSessionType(sessionType)
}

func canAccessSession(sess session.Thread, userID string, perms []string) bool {
	if bot.HasPermission(perms, bot.PermissionManage) {
		return true
	}
	if strings.TrimSpace(sess.CreatedByUserID) == "" || sess.CreatedByUserID != strings.TrimSpace(userID) {
		return false
	}
	sessionMode, runtimeType := normalizedSessionDescriptor(sess)
	return bot.HasPermission(perms, requiredReadPermissionForSessionRuntime(sessionMode, runtimeType))
}

func filterSessionsForPermissions(items []session.Thread, userID string, perms []string) []session.Thread {
	if bot.HasPermission(perms, bot.PermissionManage) {
		return items
	}
	out := make([]session.Thread, 0, len(items))
	for _, item := range items {
		if canAccessSession(item, userID, perms) {
			out = append(out, item)
		}
	}
	return out
}

func validateACPCreate(record bot.Bot, metadata map[string]any) error {
	agentID := httpx.SessionMetadataString(metadata, "acp_agent_id")
	if agentID == "" {
		return apperror.Required("acp_agent_id")
	}
	if httpx.SessionMetadataString(metadata, "project_path") == "" {
		return apperror.Required("project_path")
	}
	if err := agenthttp.AcpAgentSetupHTTPError(record.Metadata, agentID); err != nil {
		return err
	}
	return nil
}

func sessionServiceError(err error) error {
	switch {
	case errors.Is(err, session.ErrACPAgentIDRequired),
		errors.Is(err, session.ErrACPProjectPathMissing):
		feedback := agenthttp.AcpAgentNotConfiguredFeedback(err.Error())
		return apperror.OfKind(apperror.KindFromHTTPStatus(feedback.HTTPStatus), "validate acp session", feedback)
	case errors.Is(err, session.ErrACPUnknownAgent):
		feedback := agenthttp.AcpAgentNotFoundFeedback(err.Error())
		return apperror.OfKind(apperror.KindFromHTTPStatus(feedback.HTTPStatus), "validate acp session", feedback)
	case errors.Is(err, session.ErrACPRuntimeOwnerMissing):
		feedback := agenthttp.AcpRuntimeOwnerMissingFeedback()
		return apperror.OfKind(apperror.KindFromHTTPStatus(feedback.HTTPStatus), "validate acp session", feedback)
	case errors.Is(err, session.ErrACPAgentNotConfigured):
		feedback := agenthttp.AcpAgentNotConfiguredFeedback(err.Error())
		return apperror.OfKind(apperror.KindFromHTTPStatus(feedback.HTTPStatus), "validate acp session", feedback)
	case errors.Is(err, session.ErrACPAgentNotEnabled):
		feedback := agenthttp.AcpAgentNotEnabledFeedback(err.Error())
		return apperror.OfKind(apperror.KindFromHTTPStatus(feedback.HTTPStatus), "validate acp session", feedback)
	case errors.Is(err, session.ErrACPProjectModeInvalid):
		feedback := agenthttp.AcpProjectModeInvalidFeedback(err.Error())
		return apperror.OfKind(apperror.KindFromHTTPStatus(feedback.HTTPStatus), "validate acp session", feedback)
	default:
		return apperror.Internal("session service", err)
	}
}

func sessionForkError(err error) error {
	switch {
	case errors.Is(err, session.ErrForkSourceNotFound):
		return apperror.NotFound("get session", nil)
	case errors.Is(err, session.ErrForkSourceNotReply):
		return apperror.Conflict("fork session", nil)
	case errors.Is(err, session.ErrForkSourceNotChat):
		return apperror.Conflict("fork session", nil)
	default:
		return sessionServiceError(err)
	}
}

func normalizedSessionDescriptor(sess session.Thread) (string, string) {
	mode := strings.TrimSpace(sess.SessionMode)
	runtimeType := strings.TrimSpace(sess.RuntimeType)
	if !session.IsKnownSessionMode(mode) || !session.IsKnownRuntimeType(runtimeType) {
		derivedMode, derivedRuntime := session.DescriptorFromLegacyType(sess.Type)
		if !session.IsKnownSessionMode(mode) {
			mode = derivedMode
		}
		if !session.IsKnownRuntimeType(runtimeType) {
			runtimeType = derivedRuntime
		}
	}
	return mode, runtimeType
}

func sessionAgentConfigChanged(existing session.Thread, targetMode, targetRuntime string, targetMetadata, targetRuntimeMetadata map[string]any) bool {
	existingMode, existingRuntime := normalizedSessionDescriptor(existing)
	if existingMode != strings.TrimSpace(targetMode) || existingRuntime != strings.TrimSpace(targetRuntime) {
		return true
	}
	if strings.TrimSpace(targetRuntime) != session.RuntimeACPAgent {
		return false
	}
	existingMetadata := mergeSessionMetadata(existing.Metadata, existing.RuntimeMetadata)
	targetMetadata = mergeSessionMetadata(targetMetadata, targetRuntimeMetadata)
	for _, key := range []string{"acp_agent_id", "project_path", "acp_project_mode"} {
		if httpx.SessionMetadataString(existingMetadata, key) != httpx.SessionMetadataString(targetMetadata, key) {
			return true
		}
	}
	return false
}

func stripACPMetadata(metadata map[string]any) map[string]any {
	out := cloneSessionMetadata(metadata)
	for key := range out {
		if strings.HasPrefix(key, "acp_") || key == "project_path" {
			delete(out, key)
		}
	}
	return out
}

func mergeSessionMetadata(base, overlay map[string]any) map[string]any {
	out := cloneSessionMetadata(base)
	for key, value := range overlay {
		out[key] = value
	}
	return out
}

func cloneSessionMetadata(metadata map[string]any) map[string]any {
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}
