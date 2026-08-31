package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/runtimekind"
	"github.com/felinics/memoh/internal/workdir"
)

// SessionHandler handles bot session CRUD endpoints.
type SessionHandler struct {
	sessionService *session.Service
	threadEnricher threadEnricher
	acpRuntimes    acpSessionRuntimeService
	runtimeResets  sessionResetService
	workdirs       sessionWorkdirService
	agentRuntimes  sessionAgentRuntimeService
	botAgents      *botagents.Service
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

// sessionAgentRuntimeService owns external-runtime fork preparation and active
// run shutdown; satisfied by the agent application service.
type sessionAgentRuntimeService interface {
	PrepareExternalFork(ctx context.Context, botID, sessionID, turnID string) (map[string]any, error)
	AbortSessionRuns(ctx context.Context, botID, sessionID string) error
}

// sessionWorkdirService validates a workdir binding at session creation.
type sessionWorkdirService interface {
	RequireActive(ctx context.Context, botID, workdirID string) (workdir.Workdir, error)
}

// acpSessionRuntimeService owns the ACP-specific runtime lifecycle: closing a
// warm agent process and binding a session to a runtime.
type acpSessionRuntimeService interface {
	CloseSession(sessionID string) error
	BindRuntime(ctx context.Context, botID, runtimeID, sessionID, agentID, projectPath, runtimeOwnerAccountID string) error
}

// sessionResetService is the runtime-agnostic history reset boundary. It is a
// separate interface so generic reset call sites never depend on ACP naming.
type sessionResetService interface {
	BeginSessionHistoryReset(ctx context.Context, botID, sessionID string) (resetCtx context.Context, release func(), err error)
}

// sessionRuntimeServices is the single dependency the ACP pool satisfies; the
// handler splits it into the two narrow roles above at construction.
type sessionRuntimeServices interface {
	acpSessionRuntimeService
	sessionResetService
}

type threadEnricher interface {
	EnrichThreads(ctx context.Context, botID string, threads []session.Thread) ([]session.Thread, error)
}

// NewSessionHandler creates a SessionHandler.
func NewSessionHandler(log *slog.Logger, sessionService *session.Service, runtimes sessionRuntimeServices, botService *bots.Service, accountService *accounts.Service) *SessionHandler {
	handler := &SessionHandler{
		sessionService: sessionService,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "session")),
	}
	if runtimes != nil {
		handler.acpRuntimes = runtimes
		handler.runtimeResets = runtimes
	}
	return handler
}

// SetThreadEnricher installs the Channel-owned route projection used by list
// responses. Thread persistence stays independent from the route table.
func (h *SessionHandler) SetThreadEnricher(enricher threadEnricher) {
	h.threadEnricher = enricher
}

// SetWorkdirService installs the workdir domain used to validate workdir
// bindings at session creation.
func (h *SessionHandler) SetWorkdirService(workdirs sessionWorkdirService) {
	h.workdirs = workdirs
}

func (h *SessionHandler) SetBotAgents(service *botagents.Service) {
	h.botAgents = service
}

// SetAgentRuntimeService installs the agent application service used for
// external-runtime fork preparation and active-run shutdown.
func (h *SessionHandler) SetAgentRuntimeService(service sessionAgentRuntimeService) {
	h.agentRuntimes = service
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
	BotAgentID      string         `json:"bot_agent_id,omitempty"`
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
	// WorkdirID immutably binds the new session to a bot workdir. The
	// workdir decides the session's workspace target and working directory
	// for its whole life; there is no way to change or clear it later.
	WorkdirID string `json:"workdir_id,omitempty"`
}

type updateSessionRequest struct {
	BotAgentID      *string        `json:"bot_agent_id,omitempty"`
	Title           *string        `json:"title,omitempty"`
	Type            *string        `json:"type,omitempty"`
	SessionMode     *string        `json:"session_mode,omitempty"`
	RuntimeType     *string        `json:"runtime_type,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	RuntimeMetadata map[string]any `json:"runtime_metadata,omitempty"`
}

type forkSessionRequest struct {
	TurnID string `json:"turn_id" format:"uuid"`
	// MessageID is the pre-turn spelling of TurnID, resolved server-side to the
	// round that contains it. Deprecated: send turn_id. A client holds a turn id
	// from admission onward, while a stored message id exists only once the
	// round has been persisted.
	MessageID string `json:"message_id,omitempty" format:"uuid"`
	Title     string `json:"title,omitempty"`
}

// CreateSession godoc
// @Summary Create a new chat session
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param body body createSessionRequest true "Session data"
// @Success 201 {object} session.Thread
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions [post].
func (h *SessionHandler) CreateSession(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	var req createSessionRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	sessionType := strings.TrimSpace(req.Type)
	if sessionType == "" {
		sessionType = session.TypeChat
	}
	if !session.IsKnownType(sessionType) {
		return echo.NewHTTPError(http.StatusBadRequest, "unknown session type")
	}
	botAgentID := strings.TrimSpace(req.BotAgentID)
	var botAgent botagents.BotAgent
	var botAgentDescriptor botagents.Descriptor
	if botAgentID != "" {
		if h.botAgents == nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "bot agent service not configured")
		}
		agent, resolveErr := h.botAgents.GetActive(c.Request().Context(), botID, botAgentID)
		if resolveErr != nil {
			if publicErr := botAgentHTTPError(resolveErr); publicErr != nil {
				return publicErr
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to resolve bot Agent")
		}
		descriptor, descriptorErr := botagents.DescriptorFor(agent)
		if descriptorErr != nil {
			if publicErr := botAgentHTTPError(descriptorErr); publicErr != nil {
				return publicErr
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to resolve bot Agent runtime")
		}
		botAgent = agent
		botAgentDescriptor = descriptor
		// The persisted BotAgent descriptor is authoritative for the runtime:
		// ACP rows keep their provider identity in metadata, direct runtimes
		// (codex) map straight to their session runtime type.
		switch descriptor.Runtime {
		case botagents.RuntimeACP:
			req.RuntimeType = session.RuntimeACPAgent
		case botagents.RuntimeCodex:
			req.RuntimeType = session.RuntimeCodex
		case botagents.RuntimeClaudeCode:
			req.RuntimeType = session.RuntimeClaudeCode
		default:
			return apperror.New(apperror.CodeBotAgentInvalidRuntime, nil)
		}
	}
	targetType, targetMode, targetRuntimeType, err := session.ResolveDescriptor(sessionType, req.SessionMode, req.RuntimeType)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := rejectSystemACPRuntime(targetMode, targetRuntimeType); err != nil {
		return err
	}
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, requiredPermissionForSessionRuntime(targetMode, targetRuntimeType))
	if err != nil {
		return err
	}
	if botAgentID != "" {
		if configErr := h.botAgents.ValidateConfiguration(c.Request().Context(), botAgent, bot.Metadata); configErr != nil {
			if publicErr := botAgentHTTPError(configErr); publicErr != nil {
				return publicErr
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to validate bot Agent")
		}
		if botAgentDescriptor.Runtime == botagents.RuntimeACP {
			req.Metadata = mergeSessionMetadata(req.Metadata, map[string]any{"acp_agent_id": botAgentDescriptor.Provider})
			req.RuntimeMetadata = mergeSessionMetadata(req.RuntimeMetadata, map[string]any{"acp_agent_id": botAgentDescriptor.Provider})
		}
	}
	boundWorkdir, err := h.resolveCreateSessionWorkdir(c.Request().Context(), bot.ID, req.WorkdirID, targetRuntimeType)
	if err != nil {
		return err
	}
	if targetRuntimeType == session.RuntimeACPAgent {
		req.Metadata = session.ApplyACPMetadataDefaults(mergeSessionMetadata(req.Metadata, req.RuntimeMetadata))
		req.RuntimeMetadata = session.ApplyACPMetadataDefaults(mergeSessionMetadata(req.RuntimeMetadata, req.Metadata))
		if botAgentID == "" {
			if err := validateACPCreate(bot, req.Metadata); err != nil {
				return err
			}
		} else if sessionMetadataString(req.Metadata, "project_path") == "" {
			return echo.NewHTTPError(http.StatusBadRequest, session.ErrACPProjectPathMissing.Error())
		}
	}
	createInput := session.CreateInput{
		BotID:           bot.ID,
		BotAgentID:      botAgentID,
		ChannelType:     req.ChannelType,
		Type:            targetType,
		SessionMode:     targetMode,
		RuntimeType:     targetRuntimeType,
		Title:           req.Title,
		Metadata:        req.Metadata,
		RuntimeMetadata: req.RuntimeMetadata,
		CreatedByUserID: channelIdentityID,
	}
	if boundWorkdir != nil {
		createInput.WorkdirID = boundWorkdir.ID
		createInput.WorkdirPath = boundWorkdir.Path
	}
	sess, err := h.sessionService.Create(c.Request().Context(), createInput)
	if err != nil {
		return sessionServiceError(err)
	}
	// Best-effort bind of a warm pre-session runtime: the session lives in
	// the database and the runtime in memory, so this is sequenced (bind only
	// after a successful create), not transactional. A failed bind keeps the
	// session — the first prompt simply cold starts a runtime.
	if runtimeID := strings.TrimSpace(req.ACPRuntimeID); runtimeID != "" && session.IsACPRuntime(sess) && h.acpRuntimes != nil {
		if bindErr := h.acpRuntimes.BindRuntime(
			c.Request().Context(),
			bot.ID,
			runtimeID,
			sess.ID,
			sessionMetadataString(sess.Metadata, "acp_agent_id"),
			sessionMetadataString(sess.Metadata, "project_path"),
			sessionMetadataString(sess.Metadata, "runtime_owner_account_id"),
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
// @Param body body forkSessionRequest true "Fork source turn"
// @Success 201 {object} session.Thread
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions/{session_id}/fork [post].
func (h *SessionHandler) ForkSession(c echo.Context) error {
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
	bot, perms, source, err := h.authorizeSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}
	if source.Type != session.TypeChat {
		return echo.NewHTTPError(http.StatusConflict, "only chat sessions can be forked")
	}

	var req forkSessionRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	turnID := strings.TrimSpace(req.TurnID)
	legacyMessageID := strings.TrimSpace(req.MessageID)
	if turnID == "" && legacyMessageID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "turn_id is required")
	}
	if turnID != "" {
		if _, err := uuid.Parse(turnID); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid turn_id")
		}
	} else if _, err := uuid.Parse(legacyMessageID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid message_id")
	}

	// External runtimes fork their own conversation first; the returned
	// runtime metadata binds the Memoh fork to the runtime-side fork.
	var runtimeMetadataOverride map[string]any
	agentRuntimeSource := session.IsACPRuntime(source) || session.IsDirectRuntime(source)
	if agentRuntimeSource {
		// Forking an agent-runtime session creates a new workspace execution
		// surface; read access to the source is not enough.
		if !bots.HasPermission(perms, bots.PermissionWorkspaceExec) {
			feedback := externalAgentNoWorkspaceExecFeedback("missing_workspace_exec", "You do not have permission to run workspace commands for this bot.")
			return echo.NewHTTPError(feedback.HTTPStatus, feedback)
		}
	}
	if session.IsDirectRuntime(source) {
		if h.agentRuntimes == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "agent runtime service unavailable")
		}
		if turnID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "turn_id is required to fork this session")
		}
		override, forkErr := h.agentRuntimes.PrepareExternalFork(c.Request().Context(), bot.ID, sessionID, turnID)
		if forkErr != nil {
			switch {
			case errors.Is(forkErr, application.ErrExternalForkUnsupported):
				return echo.NewHTTPError(http.StatusConflict, "this session's runtime does not support forking")
			case errors.Is(forkErr, application.ErrExternalForkAnchorMissing):
				return echo.NewHTTPError(http.StatusConflict, "fork source turn has no runtime anchor")
			default:
				return forkErr
			}
		}
		runtimeMetadataOverride = override
	}
	if agentRuntimeSource {
		// The fork belongs to its creator. The runtime owner is the principal
		// offline fires and turn-level exec checks authorize against, so
		// inheriting the source's owner would let the forker keep executing
		// on the owner's grants after their own are revoked.
		if runtimeMetadataOverride == nil {
			runtimeMetadataOverride = make(map[string]any, len(source.RuntimeMetadata)+1)
			for key, value := range source.RuntimeMetadata {
				runtimeMetadataOverride[key] = value
			}
		}
		runtimeMetadataOverride["runtime_owner_account_id"] = channelIdentityID
	}

	forked, err := h.sessionService.ForkFromAssistantTurn(c.Request().Context(), session.ForkFromAssistantInput{
		BotID:                   bot.ID,
		ThreadID:                source.ID,
		TurnID:                  turnID,
		MessageID:               legacyMessageID,
		Title:                   strings.TrimSpace(req.Title),
		CreatedByUserID:         channelIdentityID,
		RuntimeMetadataOverride: runtimeMetadataOverride,
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
// @Param workdir_id query string false "Only include sessions bound to this workdir. The literal none selects sessions with no workdir."
// @Param limit query int false "Page size (1..200). Defaults to 50."
// @Param cursor query string false "Opaque cursor returned as next_cursor on a previous page."
// @Success 200 {object} listSessionsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions [get].
func (h *SessionHandler) ListSessions(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	bot, perms, err := h.authorizeBotSessionAccess(c, channelIdentityID, botID)
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
		if _, _, _, err := h.authorizeSession(c, channelIdentityID, bot.ID, parentSessionID); err != nil {
			return err
		}
	}
	types, defaultVisibility, err := parseSessionTypesParam(c.QueryParam("types"), parentSessionID != "")
	if err != nil {
		return err
	}
	filter := session.ListFilter{ParentThreadID: parentSessionID}
	if defaultVisibility {
		filter.Visibility = session.VisibilityUser
	}
	if workdirParam := strings.TrimSpace(c.QueryParam("workdir_id")); workdirParam != "" {
		// The literal "none" selects the unassigned bucket so the sidebar can
		// page ungrouped sessions with the same cursor machinery.
		if strings.EqualFold(workdirParam, "none") {
			filter.WorkdirUnassigned = true
		} else {
			if _, parseErr := uuid.Parse(workdirParam); parseErr != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid workdir_id")
			}
			filter.WorkdirID = workdirParam
		}
	}

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
	if bots.HasPermission(perms, bots.PermissionManage) {
		var rows []session.Thread
		rows, err = h.sessionService.ListByBotPagedWithFilter(c.Request().Context(), bot.ID, types, cursor, probeLimit, filter)
		if err == nil {
			sessions, hasMorePages = trimPagedSessions(rows, limit)
			if hasMorePages {
				last := sessions[len(sessions)-1]
				nextCursor = session.Cursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
			}
		}
	} else {
		var preFilter []session.Thread
		preFilter, err = h.sessionService.ListByBotAndCreatedByUserPagedWithFilter(c.Request().Context(), bot.ID, channelIdentityID, types, cursor, probeLimit, filter)
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
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if h.threadEnricher != nil {
		sessions, err = h.threadEnricher.EnrichThreads(c.Request().Context(), bot.ID, sessions)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
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

// parseSessionTypesParam resolves the types filter. The second return says
// whether the default user-facing listing applies: no explicit types and no
// parent filter. That default filters by stored visibility rather than by a
// type list, so schedule-created sessions marked user-visible surface too.
// rejectSystemACPRuntime keeps system-managed session modes out of the HTTP
// session API's ACP surface. The thread domain itself allows
// schedule+acp_agent — the schedule trigger path creates those sessions —
// but interactive session creation stays limited to chat and discuss.
func rejectSystemACPRuntime(mode, runtimeType string) error {
	// Agent runtimes (ACP and direct): the capability table also allows
	// schedule mode, but those sessions are created by the schedule service —
	// this user-facing endpoint stays limited to chat and discuss.
	if runtimekind.IsExternal(runtimeType) && mode != session.TypeChat && mode != session.TypeDiscuss {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("runtime type %q is only supported for %s or %s sessions here", runtimeType, session.TypeChat, session.TypeDiscuss))
	}
	return nil
}

func parseSessionTypesParam(raw string, hasParentFilter bool) ([]string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if hasParentFilter {
			return []string{session.TypeSubagent}, false, nil
		}
		return session.AllSessionTypes(), true, nil
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
			return nil, false, echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("unknown session type %q", token))
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	if len(out) == 0 {
		return nil, false, echo.NewHTTPError(http.StatusBadRequest, "types must contain at least one session type")
	}
	return out, false, nil
}

func parseSessionLimitParam(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sessionListDefaultLimit, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "limit must be an integer")
	}
	if value < 1 || value > sessionListMaxLimit {
		return 0, echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", sessionListMaxLimit))
	}
	return value, nil
}

func parseSessionParentIDParam(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if _, err := uuid.Parse(value); err != nil {
		return "", echo.NewHTTPError(http.StatusBadRequest, "parent_session_id must be a UUID")
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
		return session.Cursor{}, echo.NewHTTPError(http.StatusBadRequest, "invalid cursor")
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return session.Cursor{}, echo.NewHTTPError(http.StatusBadRequest, "invalid cursor")
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return session.Cursor{}, echo.NewHTTPError(http.StatusBadRequest, "invalid cursor")
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return session.Cursor{}, echo.NewHTTPError(http.StatusBadRequest, "invalid cursor")
	}
	return session.Cursor{UpdatedAt: updatedAt, ID: parts[1]}, nil
}

// GetSession godoc
// @Summary Get a session by ID
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} session.Thread
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions/{session_id} [get].
func (h *SessionHandler) GetSession(c echo.Context) error {
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
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions/{session_id} [patch].
func (h *SessionHandler) UpdateSession(c echo.Context) error {
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

	bot, perms, existing, err := h.authorizeSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}

	var req updateSessionRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// Mirror the create-path guard in session.ResolveDescriptor: the legacy
	// acp_agent type unambiguously means an ACP runtime, so an explicit non-ACP
	// runtime_type in the same PATCH is contradictory and must fail loudly
	// rather than silently downgrade the session to a plain model chat.
	if req.Type != nil && req.RuntimeType != nil {
		legacyType := strings.TrimSpace(*req.Type)
		runtimeType := strings.TrimSpace(*req.RuntimeType)
		if legacyType == session.TypeACPAgent && runtimeType != "" && runtimeType != session.RuntimeACPAgent {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("session type %q conflicts with runtime_type %q", session.TypeACPAgent, runtimeType))
		}
	}

	result := existing
	if req.BotAgentID != nil || req.Type != nil || req.SessionMode != nil || req.RuntimeType != nil || req.Metadata != nil || req.RuntimeMetadata != nil {
		targetType := existing.Type
		targetMode, targetRuntime := normalizedSessionDescriptor(existing)
		targetBotAgentID := strings.TrimSpace(existing.BotAgentID)
		botAgentSelectionExplicit := req.BotAgentID != nil
		if botAgentSelectionExplicit {
			targetBotAgentID = strings.TrimSpace(*req.BotAgentID)
		}
		if req.Type != nil {
			targetType = strings.TrimSpace(*req.Type)
			if targetType == "" {
				targetType = session.TypeChat
			}
			targetMode, targetRuntime = session.DescriptorFromLegacyType(targetType)
		}
		if !session.IsKnownType(targetType) {
			return echo.NewHTTPError(http.StatusBadRequest, "unknown session type")
		}
		if req.SessionMode != nil {
			targetMode = strings.TrimSpace(*req.SessionMode)
			if !session.IsKnownSessionMode(targetMode) {
				return echo.NewHTTPError(http.StatusBadRequest, "unknown session mode")
			}
		}
		if req.RuntimeType != nil {
			targetRuntime = strings.TrimSpace(*req.RuntimeType)
			if !session.IsKnownRuntimeType(targetRuntime) {
				return echo.NewHTTPError(http.StatusBadRequest, "unknown runtime type")
			}
		}
		var targetAgent botagents.BotAgent
		var targetAgentDescriptor botagents.Descriptor
		if targetBotAgentID != "" {
			if h.botAgents == nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "bot agent service not configured")
			}
			var resolveErr error
			if botAgentSelectionExplicit {
				targetAgent, resolveErr = h.botAgents.GetActive(c.Request().Context(), bot.ID, targetBotAgentID)
			} else {
				targetAgent, resolveErr = h.botAgents.Get(c.Request().Context(), bot.ID, targetBotAgentID)
			}
			if resolveErr != nil {
				if publicErr := botAgentHTTPError(resolveErr); publicErr != nil {
					return publicErr
				}
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to resolve bot Agent")
			}
			descriptor, descriptorErr := botagents.DescriptorFor(targetAgent)
			if descriptorErr != nil {
				if publicErr := botAgentHTTPError(descriptorErr); publicErr != nil {
					return publicErr
				}
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to resolve bot Agent runtime")
			}
			targetAgentDescriptor = descriptor
			// The persisted BotAgent descriptor is authoritative for the runtime.
			switch descriptor.Runtime {
			case botagents.RuntimeACP:
				targetRuntime = session.RuntimeACPAgent
			case botagents.RuntimeCodex:
				targetRuntime = session.RuntimeCodex
			case botagents.RuntimeClaudeCode:
				targetRuntime = session.RuntimeClaudeCode
			default:
				return apperror.New(apperror.CodeBotAgentInvalidRuntime, nil)
			}
		} else if botAgentSelectionExplicit {
			targetRuntime = session.RuntimeModel
			if targetType == session.TypeACPAgent {
				targetType = targetMode
			}
		}
		targetType, targetMode, targetRuntime, err = session.ResolveDescriptor(targetType, targetMode, targetRuntime)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := rejectSystemACPRuntime(targetMode, targetRuntime); err != nil {
			return err
		}
		if !bots.HasPermission(perms, requiredPermissionForSessionRuntime(targetMode, targetRuntime)) {
			return echo.NewHTTPError(http.StatusForbidden, "bot access denied")
		}
		targetMetadata := cloneSessionMetadata(existing.Metadata)
		if req.Metadata != nil {
			targetMetadata = cloneSessionMetadata(req.Metadata)
		}
		targetRuntimeMetadata := cloneSessionMetadata(existing.RuntimeMetadata)
		if req.RuntimeMetadata != nil {
			targetRuntimeMetadata = cloneSessionMetadata(req.RuntimeMetadata)
		}
		if targetBotAgentID != "" {
			if configErr := h.botAgents.ValidateConfiguration(c.Request().Context(), targetAgent, bot.Metadata); configErr != nil {
				if publicErr := botAgentHTTPError(configErr); publicErr != nil {
					return publicErr
				}
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to validate bot Agent")
			}
			if targetAgentDescriptor.Runtime == botagents.RuntimeACP {
				targetMetadata = mergeSessionMetadata(targetMetadata, map[string]any{"acp_agent_id": targetAgentDescriptor.Provider})
				targetRuntimeMetadata = mergeSessionMetadata(targetRuntimeMetadata, map[string]any{"acp_agent_id": targetAgentDescriptor.Provider})
			}
		}
		if targetRuntime == session.RuntimeACPAgent {
			targetMetadata = session.ApplyACPMetadataDefaults(mergeSessionMetadata(targetMetadata, targetRuntimeMetadata))
			targetRuntimeMetadata = session.ApplyACPMetadataDefaults(mergeSessionMetadata(targetRuntimeMetadata, targetMetadata))
		}
		agentChanged := strings.TrimSpace(existing.BotAgentID) != targetBotAgentID || sessionAgentConfigChanged(existing, targetMode, targetRuntime, targetMetadata, targetRuntimeMetadata)
		if agentChanged {
			// Advisory pre-check with zero side effects: a doomed request must
			// not destroy the warm runtime or abort an in-flight turn before
			// being rejected. The authoritative check re-runs inside the
			// fenced update transaction below.
			if count, countErr := h.sessionService.MessageCount(c.Request().Context(), sessionID); countErr == nil && count > 0 {
				return echo.NewHTTPError(http.StatusConflict, "session agent cannot be changed after messages are sent")
			}
			if h.runtimeResets == nil {
				return apperror.Wrap(
					apperror.CodeSessionHistoryInconsistent,
					errors.New("runtime reset is not configured"),
					nil,
				)
			}
			resetCtx, releaseRuntimeReset, err := h.runtimeResets.BeginSessionHistoryReset(c.Request().Context(), botID, sessionID)
			if err != nil {
				return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
			}
			defer releaseRuntimeReset()
			c.SetRequest(c.Request().WithContext(resetCtx))
		}
		switch {
		case targetRuntime == session.RuntimeACPAgent:
			if targetBotAgentID == "" {
				if err := validateACPCreate(bot, targetMetadata); err != nil {
					return err
				}
			}
		case session.IsDirectRuntimeType(targetRuntime):
			// Driver-owned keys (the codex thread id) live in runtime
			// metadata; wiping them silently discards the runtime-side
			// conversation, so an external target keeps both maps.
			targetMetadata = session.ApplyExternalMetadataDefaults(targetMetadata)
			targetRuntimeMetadata = session.ApplyExternalMetadataDefaults(targetRuntimeMetadata)
		case session.IsACPRuntime(existing) || session.IsDirectRuntime(existing) || req.Type != nil || req.RuntimeType != nil || req.RuntimeMetadata != nil:
			targetMetadata = stripACPMetadata(targetMetadata)
			targetRuntimeMetadata = map[string]any{}
		}
		if targetType != existing.Type || targetMode != existing.SessionMode || targetRuntime != existing.RuntimeType || targetBotAgentID != strings.TrimSpace(existing.BotAgentID) || req.Metadata != nil || req.RuntimeMetadata != nil || req.SessionMode != nil || req.RuntimeType != nil || req.BotAgentID != nil {
			targetBotAgentIDValue := targetBotAgentID
			if agentChanged {
				result, err = h.sessionService.UpdateEmptyDescriptorAndMetadataWithOwner(c.Request().Context(), sessionID, targetType, targetMode, targetRuntime, targetMetadata, targetRuntimeMetadata, &targetBotAgentIDValue, channelIdentityID)
			} else {
				result, err = h.sessionService.UpdateDescriptorAndMetadataWithOwner(c.Request().Context(), sessionID, targetType, targetMode, targetRuntime, targetMetadata, targetRuntimeMetadata, &targetBotAgentIDValue, channelIdentityID)
			}
			if err != nil {
				if errors.Is(err, session.ErrSessionHasMessages) {
					return echo.NewHTTPError(http.StatusConflict, "session agent cannot be changed after messages are sent")
				}
				if leaseErr := runtimefence.ResetLeaseFailure(c.Request().Context(), err); leaseErr != nil {
					return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, leaseErr, nil)
				}
				return sessionServiceError(err)
			}
		}
	}
	if req.Title != nil {
		resultMode, resultRuntime := normalizedSessionDescriptor(result)
		if !bots.HasPermission(perms, requiredPermissionForSessionRuntime(resultMode, resultRuntime)) {
			return echo.NewHTTPError(http.StatusForbidden, "bot access denied")
		}
		result, err = h.sessionService.UpdateTitle(c.Request().Context(), sessionID, *req.Title)
		if err != nil {
			if leaseErr := runtimefence.ResetLeaseFailure(c.Request().Context(), err); leaseErr != nil {
				return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, leaseErr, nil)
			}
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
	}
	if req.Title == nil && req.BotAgentID == nil && req.Metadata == nil && req.Type == nil && req.SessionMode == nil && req.RuntimeType == nil && req.RuntimeMetadata == nil {
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
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions/{session_id} [delete].
func (h *SessionHandler) DeleteSession(c echo.Context) error {
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
	_, perms, existing, err := h.authorizeSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}
	existingMode, existingRuntime := normalizedSessionDescriptor(existing)
	if !bots.HasPermission(perms, requiredPermissionForSessionRuntime(existingMode, existingRuntime)) {
		return echo.NewHTTPError(http.StatusForbidden, "bot access denied")
	}
	var releaseRuntimeReset func()
	if session.IsACPRuntime(existing) && h.runtimeResets == nil {
		return apperror.Wrap(
			apperror.CodeSessionHistoryInconsistent,
			errors.New("runtime reset is not configured"),
			nil,
		)
	}
	if session.IsACPRuntime(existing) {
		var resetCtx context.Context
		resetCtx, releaseRuntimeReset, err = h.runtimeResets.BeginSessionHistoryReset(c.Request().Context(), botID, sessionID)
		if err != nil {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
		}
		defer releaseRuntimeReset()
		c.SetRequest(c.Request().WithContext(resetCtx))
	}
	if session.IsDirectRuntime(existing) && h.agentRuntimes == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "agent runtime service unavailable")
	}
	if err := h.sessionService.SoftDelete(c.Request().Context(), sessionID); err != nil {
		if releaseRuntimeReset != nil {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if session.IsDirectRuntime(existing) {
		// A direct-runtime session being deleted must not leave its active
		// turn executing against the workspace. Soft-delete FIRST: every new
		// turn admission starts with a session lookup, so the tombstone is
		// the admission barrier — then abort whatever is already in flight
		// (including anything admitted before the tombstone landed) and wait
		// for the driver call to actually finish.
		if err := h.agentRuntimes.AbortSessionRuns(c.Request().Context(), botID, sessionID); err != nil {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
		}
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *SessionHandler) authorizeBotSessionAccess(c echo.Context, channelIdentityID, botID string) (bots.Bot, []string, error) {
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bots.PermissionChat)
	if err != nil {
		bot, err = AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bots.PermissionWorkspaceExec)
		if err != nil {
			return bots.Bot{}, nil, err
		}
	}
	perms, err := h.resolveCurrentUserPermissions(c, channelIdentityID, bot.ID)
	if err != nil {
		return bots.Bot{}, nil, err
	}
	return bot, perms, nil
}

func (h *SessionHandler) authorizeSession(c echo.Context, channelIdentityID, botID, sessionID string) (bots.Bot, []string, session.Thread, error) {
	bot, perms, err := h.authorizeBotSessionAccess(c, channelIdentityID, botID)
	if err != nil {
		return bots.Bot{}, nil, session.Thread{}, err
	}
	sess, err := h.sessionService.Get(c.Request().Context(), sessionID)
	if err != nil || sess.BotID != bot.ID {
		return bots.Bot{}, nil, session.Thread{}, echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	if !canAccessSession(sess, channelIdentityID, perms) {
		return bots.Bot{}, nil, session.Thread{}, echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	return bot, perms, sess, nil
}

func (h *SessionHandler) resolveCurrentUserPermissions(c echo.Context, channelIdentityID, botID string) ([]string, error) {
	if h.botService == nil || h.accountService == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "bot services not configured")
	}
	isAdmin, err := h.accountService.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	perms, err := h.botService.ResolveUserPermissions(c.Request().Context(), botID, channelIdentityID, isAdmin)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return perms, nil
}

func requiredReadPermissionForSessionType(sessionType string) string {
	switch strings.TrimSpace(sessionType) {
	case session.TypeChat:
		return bots.PermissionChat
	case session.TypeSubagent:
		return bots.PermissionChat
	case session.TypeACPAgent:
		return bots.PermissionWorkspaceExec
	default:
		return bots.PermissionManage
	}
}

func requiredWritePermissionForSessionType(sessionType string) string {
	switch strings.TrimSpace(sessionType) {
	case session.TypeChat:
		return bots.PermissionChat
	case session.TypeACPAgent:
		return bots.PermissionWorkspaceExec
	default:
		return bots.PermissionManage
	}
}

func requiredReadPermissionForSessionRuntime(sessionType, runtimeType string) string {
	sessionType = strings.TrimSpace(sessionType)
	if runtimekind.RequiresWorkspaceExec(runtimeType) {
		switch sessionType {
		case session.TypeACPAgent, session.TypeChat, session.TypeDiscuss:
			return bots.PermissionWorkspaceExec
		}
	}
	return requiredReadPermissionForSessionType(sessionType)
}

func requiredPermissionForSessionRuntime(sessionType, runtimeType string) string {
	sessionType = strings.TrimSpace(sessionType)
	if runtimekind.RequiresWorkspaceExec(runtimeType) {
		switch sessionType {
		case session.TypeACPAgent, session.TypeChat, session.TypeDiscuss:
			return bots.PermissionWorkspaceExec
		}
	}
	return requiredWritePermissionForSessionType(sessionType)
}

func canAccessSession(sess session.Thread, userID string, perms []string) bool {
	if bots.HasPermission(perms, bots.PermissionManage) {
		return true
	}
	if strings.TrimSpace(sess.CreatedByUserID) == "" || sess.CreatedByUserID != strings.TrimSpace(userID) {
		return false
	}
	sessionMode, runtimeType := normalizedSessionDescriptor(sess)
	return bots.HasPermission(perms, requiredReadPermissionForSessionRuntime(sessionMode, runtimeType))
}

func authorizeExternalAgentSessionAccess(actorUserID string, perms []string, runtimeOwnerAccountID string) error {
	actorUserID = strings.TrimSpace(actorUserID)
	runtimeOwnerAccountID = strings.TrimSpace(runtimeOwnerAccountID)
	if runtimeOwnerAccountID == "" {
		feedback := externalAgentRuntimeOwnerMissingFeedback()
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	// The runtime owner has no standing beyond their live grants: owner and
	// members alike must hold workspace_exec, so a revoked owner loses
	// runtime access at decision time (same model as the application-layer
	// External Agent decision authorizers).
	if actorUserID == "" || !bots.HasPermission(perms, bots.PermissionWorkspaceExec) {
		feedback := externalAgentNoWorkspaceExecFeedback("missing_workspace_exec", "You do not have permission to run workspace commands for this bot.")
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	return nil
}

func filterSessionsForPermissions(items []session.Thread, userID string, perms []string) []session.Thread {
	if bots.HasPermission(perms, bots.PermissionManage) {
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

// resolveCreateSessionWorkdir validates a requested workdir binding. External
// Agent sessions can only bind native-workspace workdirs until their runtime
// lifecycles are scoped by workspace target.
func (h *SessionHandler) resolveCreateSessionWorkdir(ctx context.Context, botID, workdirID, runtimeType string) (*workdir.Workdir, error) {
	workdirID = strings.TrimSpace(workdirID)
	if workdirID == "" {
		return nil, nil
	}
	if h.workdirs == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "workdir service not configured")
	}
	bound, err := h.workdirs.RequireActive(ctx, botID, workdirID)
	if err != nil {
		return nil, workdirHTTPError(h.logger, err)
	}
	if (runtimeType == session.RuntimeACPAgent || session.IsDirectRuntimeType(runtimeType)) && bound.TargetKind == workdir.TargetKindRemote {
		return nil, echo.NewHTTPError(http.StatusBadRequest,
			"external agent sessions cannot use a remote computer workdir yet; bind a native workspace workdir instead")
	}
	return &bound, nil
}

func validateACPCreate(bot bots.Bot, metadata map[string]any) error {
	agentID := sessionMetadataString(metadata, "acp_agent_id")
	if agentID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, session.ErrACPAgentIDRequired.Error())
	}
	if sessionMetadataString(metadata, "project_path") == "" {
		return echo.NewHTTPError(http.StatusBadRequest, session.ErrACPProjectPathMissing.Error())
	}
	if err := acpAgentSetupHTTPError(bot.Metadata, agentID); err != nil {
		return err
	}
	return nil
}

func sessionServiceError(err error) error {
	switch {
	case errors.Is(err, session.ErrACPAgentIDRequired),
		errors.Is(err, session.ErrACPProjectPathMissing):
		feedback := acpAgentNotConfiguredFeedback(err.Error())
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	case errors.Is(err, session.ErrACPUnknownAgent):
		feedback := acpAgentNotFoundFeedback(err.Error())
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	case errors.Is(err, session.ErrACPRuntimeOwnerMissing):
		feedback := externalAgentRuntimeOwnerMissingFeedback()
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	case errors.Is(err, session.ErrACPAgentNotConfigured):
		feedback := acpAgentNotConfiguredFeedback(err.Error())
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	case errors.Is(err, session.ErrACPAgentNotEnabled):
		feedback := acpAgentNotEnabledFeedback(err.Error())
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	case errors.Is(err, session.ErrACPProjectModeInvalid):
		feedback := acpProjectModeInvalidFeedback(err.Error())
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
}

func sessionForkError(err error) error {
	switch {
	case errors.Is(err, session.ErrForkSourceNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "session not found")
	case errors.Is(err, session.ErrForkSourceNotReply):
		return echo.NewHTTPError(http.StatusConflict, "fork source is not a visible assistant reply")
	case errors.Is(err, session.ErrForkSourceNotChat):
		return echo.NewHTTPError(http.StatusConflict, "only chat sessions can be forked")
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
	trimmedRuntime := strings.TrimSpace(targetRuntime)
	if trimmedRuntime != session.RuntimeACPAgent && !session.IsDirectRuntimeType(trimmedRuntime) {
		return false
	}
	existingMetadata := mergeSessionMetadata(existing.Metadata, existing.RuntimeMetadata)
	targetMetadata = mergeSessionMetadata(targetMetadata, targetRuntimeMetadata)
	keys := []string{"acp_agent_id", "project_path", "acp_project_mode"}
	if session.IsDirectRuntimeType(trimmedRuntime) {
		keys = []string{"project_path"}
	}
	for _, key := range keys {
		if sessionMetadataString(existingMetadata, key) != sessionMetadataString(targetMetadata, key) {
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

func sessionMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
