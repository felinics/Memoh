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
	"github.com/felinics/memoh/internal/agent/runtime/external"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/external/acp"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/runtimekind"
	"github.com/felinics/memoh/internal/workdir"
)

// SessionHandler handles bot session CRUD endpoints.
type SessionHandler struct {
	sessionService  *session.Service
	threadEnricher  threadEnricher
	attention       attentionService
	runtimeResets   sessionResetService
	runtimes        *external.Registry
	workdirs        sessionWorkdirService
	agentRuntimes   sessionAgentRuntimeService
	runtimeControls turn.RuntimeControlService
	botAgents       *botagents.Service
	botService      *bots.Service
	accountService  *accounts.Service
	modelPrefs      modelPreferenceService
	projectionCache sessionProjectionCache
	invocations     sessionInvocationLookup
	logger          *slog.Logger
}

type sessionProjectionCache interface {
	DropSession(sessionID string)
}

// sessionAgentRuntimeService owns external-runtime fork preparation;
// satisfied by the agent application service.
type sessionAgentRuntimeService interface {
	PrepareExternalFork(ctx context.Context, botID, sessionID, turnID string) (map[string]any, error)
}

// modelPreferenceService is the picker-pair write path (issue #879),
// satisfied by the agent application service.
type modelPreferenceService interface {
	PatchSessionModelPreference(ctx context.Context, botID, sessionID string, modelRef, effort *string, expectedRevision string) error
	ReconcileSessionModelPreference(ctx context.Context, botID, modelRef, effort string) (string, string, error)
}

// SetModelPreferenceService installs the agent-side preference write path.
// SetRuntimeRegistry installs the external runtimes, whose declarations say
// how their conversations fork and pick models.
func (h *SessionHandler) SetRuntimeRegistry(runtimes *external.Registry) {
	h.runtimes = runtimes
}

func (h *SessionHandler) SetModelPreferenceService(svc modelPreferenceService) {
	h.modelPrefs = svc
}

// sessionWorkdirService validates a workdir binding at session creation.
type sessionWorkdirService interface {
	RequireActive(ctx context.Context, botID, workdirID string) (workdir.Workdir, error)
}

// sessionResetService is the runtime-agnostic history reset boundary.
type sessionResetService interface {
	BeginSessionHistoryReset(ctx context.Context, botID, sessionID string) (resetCtx context.Context, release func(), err error)
}

type threadEnricher interface {
	EnrichThreads(ctx context.Context, botID string, threads []session.Thread) ([]session.Thread, error)
}

// NewSessionHandler creates a SessionHandler.
func NewSessionHandler(log *slog.Logger, sessionService *session.Service, runtimes sessionResetService, botService *bots.Service, accountService *accounts.Service) *SessionHandler {
	handler := &SessionHandler{
		sessionService: sessionService,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "session")),
	}
	if runtimes != nil {
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
	if controls, ok := service.(turn.RuntimeControlService); ok {
		h.runtimeControls = controls
	}
}

func (h *SessionHandler) SetProjectionCache(cache sessionProjectionCache) {
	h.projectionCache = cache
}

// Register registers session routes.
func (h *SessionHandler) Register(e *echo.Echo) {
	g := e.Group("/bots/:bot_id/sessions")
	g.POST("", h.CreateSession)
	g.GET("", h.ListSessions)
	g.GET("/model-preference-seed", h.ModelPreferenceSeed)
	g.GET("/:session_id", h.GetSession)
	g.GET("/:session_id/invocations/:invocation_id", h.GetSessionInvocation)
	g.GET("/:session_id/runtime-controls", h.GetRuntimeControls)
	g.PATCH("/:session_id/runtime-controls/mode", h.SetRuntimeMode)
	g.GET("/:session_id/runtime-controls/goal", h.GetRuntimeGoal)
	g.POST("/:session_id/runtime-controls/goal", h.ControlRuntimeGoal)
	g.POST("/:session_id/runtime-controls/commands", h.ExecuteRuntimeCommand)
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
	// WorkdirID immutably binds the new session to a bot workdir. The
	// workdir decides the session's workspace target and working directory
	// for its whole life; there is no way to change or clear it later.
	WorkdirID string `json:"workdir_id,omitempty"`
	// PreferredChatModelID / PreferredReasoningEffort carry the first-send
	// picker pair (issue #879 spec v2). The composer sends them only when the
	// pair has an explicit source (user pick or remembered session); omitted
	// fields leave the columns NULL so the session follows the bot default.
	PreferredChatModelID     *string `json:"preferred_chat_model_id,omitempty"`
	PreferredReasoningEffort *string `json:"preferred_reasoning_effort,omitempty"`
}

type updateSessionRequest struct {
	ExpectedModelPreferenceRevision *string        `json:"expected_model_preference_revision,omitempty"`
	BotAgentID                      *string        `json:"bot_agent_id,omitempty"`
	Title                           *string        `json:"title,omitempty"`
	Type                            *string        `json:"type,omitempty"`
	SessionMode                     *string        `json:"session_mode,omitempty"`
	RuntimeType                     *string        `json:"runtime_type,omitempty"`
	Metadata                        map[string]any `json:"metadata,omitempty"`
	RuntimeMetadata                 map[string]any `json:"runtime_metadata,omitempty"`
	// PreferredChatModelID / PreferredReasoningEffort are the picker pair
	// (issue #879). The composer always patches the pair together; either one
	// alone is reconciled against the model the session would actually use.
	// Changing either requires ExpectedModelPreferenceRevision ("" when the
	// session has none yet): picker writes are always compare-and-set.
	PreferredChatModelID     *string `json:"preferred_chat_model_id,omitempty"`
	PreferredReasoningEffort *string `json:"preferred_reasoning_effort,omitempty"`
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
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Router /bots/{bot_id}/sessions [post].
func (h *SessionHandler) CreateSession(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	var req createSessionRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	sessionType := strings.TrimSpace(req.Type)
	if sessionType == "" {
		sessionType = session.TypeChat
	}
	if !session.IsKnownType(sessionType) {
		return apperror.FieldInvalid("type", nil)
	}
	botAgentID := strings.TrimSpace(req.BotAgentID)
	var botAgent botagents.BotAgent
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
		// The persisted BotAgent descriptor is authoritative for the runtime:
		// ACP rows keep their provider identity in metadata, direct runtimes
		// map straight to their session runtime type.
		req.RuntimeType = botagents.SessionRuntime(descriptor.Runtime)
	}
	targetType, targetMode, targetRuntimeType, err := session.ResolveDescriptor(sessionType, req.SessionMode, req.RuntimeType)
	if err != nil {
		return descriptorFieldError(err)
	}
	if err := rejectSystemExternalRuntime(targetMode, targetRuntimeType); err != nil {
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
	}
	boundWorkdir, err := resolveSessionWorkdirBinding(c.Request().Context(), h.workdirs, bot.ID, req.WorkdirID, targetRuntimeType)
	if err != nil {
		return err
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
	// First-send pair (issue #879, P9′): reconcile BEFORE the INSERT so the
	// row is born with a legal pair and the session_created broadcast already
	// carries it. Native-only: ACP/external-runtime sessions keep the columns
	// NULL (their model concept lives in runtime_metadata). An unresolvable
	// model is a 400; an illegal effort silently lands on the model default.
	if (req.PreferredChatModelID != nil || req.PreferredReasoningEffort != nil) && targetRuntimeType == session.RuntimeModel {
		if h.modelPrefs == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "model preference service unavailable")
		}
		modelRef, effortRef := "", ""
		if req.PreferredChatModelID != nil {
			modelRef = *req.PreferredChatModelID
		}
		if req.PreferredReasoningEffort != nil {
			effortRef = *req.PreferredReasoningEffort
		}
		prefModelID, prefEffort, prefErr := h.modelPrefs.ReconcileSessionModelPreference(c.Request().Context(), bot.ID, modelRef, effortRef)
		if prefErr != nil {
			return modelPreferenceError(prefErr, "reconcile session model preference")
		}
		createInput.PreferredChatModelID = prefModelID
		createInput.PreferredReasoningEffort = prefEffort
	}
	sess, err := h.sessionService.Create(c.Request().Context(), createInput)
	if err != nil {
		return sessionServiceError(err)
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
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/fork [post].
func (h *SessionHandler) ForkSession(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	sessionID, err := httpx.RequiredParam(c, "session_id")
	if err != nil {
		return err
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
		return err
	}
	turnID := strings.TrimSpace(req.TurnID)
	legacyMessageID := strings.TrimSpace(req.MessageID)
	if turnID == "" && legacyMessageID == "" {
		return apperror.FieldRequired("turn_id")
	}
	if turnID != "" {
		if _, err := uuid.Parse(turnID); err != nil {
			return apperror.FieldInvalid("turn_id", err)
		}
	} else if _, err := uuid.Parse(legacyMessageID); err != nil {
		return apperror.FieldInvalid("message_id", err)
	}

	// External runtimes fork their own conversation first; the returned
	// runtime metadata binds the Memoh fork to the runtime-side fork.
	var runtimeMetadataOverride map[string]any
	agentRuntimeSource := session.IsExternalRuntime(source)
	sourceCaps, _ := h.runtimes.Caps(session.RuntimeTypeOf(source))
	if agentRuntimeSource {
		// Forking an agent-runtime session creates a new workspace execution
		// surface; read access to the source is not enough.
		if !bots.HasPermission(perms, bots.PermissionWorkspaceExec) {
			return apperror.New(apperror.CodeNoWorkspaceExec, nil)
		}
	}
	if sourceCaps.ForkNeedsRuntime {
		if h.agentRuntimes == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "agent runtime service unavailable")
		}
		if turnID == "" {
			return apperror.FieldRequired("turn_id")
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
		RuntimeForkRequired:     sourceCaps.ForkNeedsRuntime,
	})
	if err != nil {
		return sessionForkError(err)
	}
	return c.JSON(http.StatusCreated, forked)
}

// modelPreferenceSeedResponse is the welcome composer seed (issue #879):
// the pair of the bot's most recent native session, empty when none exists.
type modelPreferenceSeedResponse struct {
	ModelID         string `json:"model_id"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// ModelPreferenceSeed godoc
// @Summary Welcome composer model seed
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} modelPreferenceSeedResponse
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Router /bots/{bot_id}/sessions/model-preference-seed [get].
func (h *SessionHandler) ModelPreferenceSeed(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	if _, _, err := h.authorizeBotSessionAccess(c, channelIdentityID, botID); err != nil {
		return err
	}
	modelID, effort, err := h.sessionService.LatestModelPreferenceSeed(c.Request().Context(), botID, channelIdentityID)
	if err != nil {
		return errs.Wrap(err, "load model preference seed")
	}
	return c.JSON(http.StatusOK, modelPreferenceSeedResponse{ModelID: modelID, ReasoningEffort: effort})
}

// ListSessions godoc
// @Summary List bot sessions
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param types query string false "Comma-separated session types to include. Defaults to user-facing types (chat,discuss,acp_agent), or subagent when parent_session_id is set."
// @Param parent_session_id query string false "Only include child sessions under this parent session."
// @Param workdir_id query string false "Only include sessions bound to this workdir. The literal none selects sessions with no workdir."
// @Param hide_schedule_runs query bool false "Leave out sessions schedule runs created, except one a schedule still runs in. The sidebar lists them under their schedule."
// @Param limit query int false "Page size (1..200). Defaults to 50."
// @Param cursor query string false "Opaque cursor returned as next_cursor on a previous page."
// @Success 200 {object} listSessionsResponse
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Router /bots/{bot_id}/sessions [get].
func (h *SessionHandler) ListSessions(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
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
	filter := session.ListFilter{
		ParentThreadID:   parentSessionID,
		HideScheduleRuns: strings.EqualFold(c.QueryParam("hide_schedule_runs"), "true"),
	}
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
				return apperror.FieldInvalid("workdir_id", parseErr)
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
		return errs.Wrap(err, "list sessions")
	}
	if h.threadEnricher != nil {
		sessions, err = h.threadEnricher.EnrichThreads(c.Request().Context(), bot.ID, sessions)
		if err != nil {
			return errs.Wrap(err, "enrich sessions")
		}
	}
	if h.attention != nil {
		sessions, err = h.attention.Annotate(c.Request().Context(), channelIdentityID, sessions)
		if err != nil {
			return errs.Wrap(err, "annotate session attention")
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

// rejectSystemExternalRuntime keeps system-managed session modes out of the HTTP
// session API's external-agent surface. The schedule trigger path can create
// external-agent sessions in schedule mode; interactive session creation
// stays limited to chat and discuss.
func rejectSystemExternalRuntime(mode, runtimeType string) error {
	// Agent runtimes (ACP and direct): the capability table also allows
	// schedule mode, but those sessions are created by the schedule service —
	// this user-facing endpoint stays limited to chat and discuss.
	if runtimekind.IsExternal(runtimeType) && mode != session.TypeChat && mode != session.TypeDiscuss {
		return apperror.FieldInvalid("session_mode", fmt.Errorf("runtime type %q is only supported for %s or %s sessions here", runtimeType, session.TypeChat, session.TypeDiscuss))
	}
	return nil
}

// parseSessionTypesParam resolves the types filter. The second return says
// whether the default user-facing listing applies: no explicit types and no
// parent filter. That default filters by stored visibility rather than by a
// type list, so schedule-created sessions marked user-visible surface too.
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
			return nil, false, apperror.FieldInvalid("types", nil)
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	if len(out) == 0 {
		return nil, false, apperror.FieldInvalid("types", nil)
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
		return 0, apperror.FieldInvalid("limit", err)
	}
	if value < 1 || value > sessionListMaxLimit {
		return 0, apperror.FieldInvalid("limit", fmt.Errorf("limit must be between 1 and %d", sessionListMaxLimit))
	}
	return value, nil
}

func parseSessionParentIDParam(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if _, err := uuid.Parse(value); err != nil {
		return "", apperror.FieldInvalid("parent_session_id", err)
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
		return session.Cursor{}, apperror.FieldInvalid("cursor", err)
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return session.Cursor{}, apperror.FieldInvalid("cursor", nil)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return session.Cursor{}, apperror.FieldInvalid("cursor", err)
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return session.Cursor{}, apperror.FieldInvalid("cursor", err)
	}
	return session.Cursor{UpdatedAt: updatedAt, ID: parts[1]}, nil
}

// GetSession godoc
// @Summary Get a session by ID
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} session.Thread
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Router /bots/{bot_id}/sessions/{session_id} [get].
func (h *SessionHandler) GetSession(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	sessionID, err := httpx.RequiredParam(c, "session_id")
	if err != nil {
		return err
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
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Router /bots/{bot_id}/sessions/{session_id} [patch].
func (h *SessionHandler) UpdateSession(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	sessionID, err := httpx.RequiredParam(c, "session_id")
	if err != nil {
		return err
	}

	bot, perms, existing, err := h.authorizeSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}

	var req updateSessionRequest
	if err := c.Bind(&req); err != nil {
		return err
	}

	// Mirror the create-path guard in session.ResolveDescriptor: the legacy
	// acp_agent type unambiguously means an ACP runtime, so an explicit non-ACP
	// runtime_type in the same PATCH is contradictory and must fail loudly
	// rather than silently downgrade the session to a plain model chat.
	if req.Type != nil && req.RuntimeType != nil {
		legacyType := strings.TrimSpace(*req.Type)
		runtimeType := strings.TrimSpace(*req.RuntimeType)
		if legacyType == session.TypeACPAgent && runtimeType != "" && runtimeType != session.RuntimeACPAgent {
			return apperror.FieldInvalid("runtime_type", fmt.Errorf("session type %q conflicts with runtime_type %q", session.TypeACPAgent, runtimeType))
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
			return apperror.FieldInvalid("type", nil)
		}
		if req.SessionMode != nil {
			targetMode = strings.TrimSpace(*req.SessionMode)
			if !session.IsKnownSessionMode(targetMode) {
				return apperror.FieldInvalid("session_mode", nil)
			}
		}
		if req.RuntimeType != nil {
			targetRuntime = strings.TrimSpace(*req.RuntimeType)
			if !session.IsKnownRuntimeType(targetRuntime) {
				return apperror.FieldInvalid("runtime_type", nil)
			}
		}
		var targetAgent botagents.BotAgent
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
			// The persisted BotAgent descriptor is authoritative for the runtime.
			targetRuntime = botagents.SessionRuntime(descriptor.Runtime)
		} else if botAgentSelectionExplicit {
			targetRuntime = session.RuntimeModel
			if targetType == session.TypeACPAgent {
				targetType = targetMode
			}
		}
		targetType, targetMode, targetRuntime, err = session.ResolveDescriptor(targetType, targetMode, targetRuntime)
		if err != nil {
			return descriptorFieldError(err)
		}
		if err := rejectSystemExternalRuntime(targetMode, targetRuntime); err != nil {
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
		}
		if targetRuntime == session.RuntimeACPAgent {
			// A copy of the stored session in metadata would be merged back
			// after the runtime copy is cleared.
			delete(targetMetadata, acpagent.NativeSessionKey)
		}
		agentChanged := strings.TrimSpace(existing.BotAgentID) != targetBotAgentID || sessionAgentConfigChanged(existing, targetMode, targetRuntime, targetMetadata, targetRuntimeMetadata)
		if agentChanged {
			// The stored session belongs to the agent and project that wrote it.
			delete(targetRuntimeMetadata, acpagent.NativeSessionKey)
			// Advisory pre-check with zero side effects: a doomed request must
			// not destroy the warm runtime or abort an in-flight turn before
			// being rejected. The authoritative check re-runs inside the
			// fenced update transaction below.
			if count, countErr := h.sessionService.MessageCount(c.Request().Context(), sessionID); countErr == nil && count > 0 {
				return echo.NewHTTPError(http.StatusConflict, "session agent cannot be changed after messages are sent")
			}
			if h.runtimeResets == nil {
				return apperror.Wrap(
					apperror.CodeSessionResetUnavailable,
					errors.New("runtime reset is not configured"),
					nil,
				)
			}
			resetCtx, releaseRuntimeReset, err := h.runtimeResets.BeginSessionHistoryReset(c.Request().Context(), botID, sessionID)
			if err != nil {
				return historyResetError(err)
			}
			defer releaseRuntimeReset()
			c.SetRequest(c.Request().WithContext(resetCtx))
		}
		// Driver-owned keys (the codex thread id) live in runtime metadata;
		// wiping them silently discards the runtime-side conversation, so an
		// external target keeps both maps.
		if !runtimekind.IsExternal(targetRuntime) && (session.IsExternalRuntime(existing) || req.Type != nil || req.RuntimeType != nil || req.RuntimeMetadata != nil) {
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
					return apperror.Wrap(apperror.CodeSessionResetConflict, leaseErr, nil)
				}
				return sessionServiceError(err)
			}
		}
	}
	// Picker pair (issue #879): reconcile and persist. The agent service owns
	// model resolution and effort legality; the handler only routes.
	if req.PreferredChatModelID != nil || req.PreferredReasoningEffort != nil {
		if h.modelPrefs == nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "model preference service not configured")
		}
		if req.ExpectedModelPreferenceRevision == nil {
			return apperror.FieldRequired("expected_model_preference_revision")
		}
		if prefErr := h.modelPrefs.PatchSessionModelPreference(c.Request().Context(), botID, sessionID, req.PreferredChatModelID, req.PreferredReasoningEffort, *req.ExpectedModelPreferenceRevision); prefErr != nil {
			if errors.Is(prefErr, application.ErrModelPreferenceConflict) {
				return apperror.New(apperror.CodeSessionModelPreferenceConflict, nil)
			}
			if _, catalog := external.As[external.ModelCatalogProvider](h.runtimes, session.RuntimeTypeOf(result)); catalog {
				if errors.Is(prefErr, application.ErrDirectModelUnavailable) {
					return apperror.New(apperror.CodeRuntimeControlModeUnavailable, nil)
				}
				return application.RuntimeControlError(prefErr)
			}
			return modelPreferenceError(prefErr, "patch session model preference")
		}
		result, err = h.sessionService.Get(c.Request().Context(), sessionID)
		if err != nil {
			return sessionServiceError(err)
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
				return apperror.Wrap(apperror.CodeSessionResetConflict, leaseErr, nil)
			}
			return errs.Wrap(err, "update session")
		}
	}
	if req.Title == nil && req.BotAgentID == nil && req.Metadata == nil && req.Type == nil && req.SessionMode == nil && req.RuntimeType == nil && req.RuntimeMetadata == nil && req.PreferredChatModelID == nil && req.PreferredReasoningEffort == nil {
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
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Router /bots/{bot_id}/sessions/{session_id} [delete].
func (h *SessionHandler) DeleteSession(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	sessionID, err := httpx.RequiredParam(c, "session_id")
	if err != nil {
		return err
	}
	_, perms, existing, err := h.authorizeSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}
	existingMode, existingRuntime := normalizedSessionDescriptor(existing)
	if !bots.HasPermission(perms, requiredPermissionForSessionRuntime(existingMode, existingRuntime)) {
		return echo.NewHTTPError(http.StatusForbidden, "bot access denied")
	}
	// Every runtime goes through the one reset: the session's runs stop, its
	// agent process closes, and none starts again until the tombstone lands.
	if h.runtimeResets == nil {
		return apperror.Wrap(
			apperror.CodeSessionResetUnavailable,
			errors.New("runtime reset is not configured"),
			nil,
		)
	}
	resetCtx, releaseRuntimeReset, err := h.runtimeResets.BeginSessionHistoryReset(c.Request().Context(), botID, sessionID)
	if err != nil {
		return historyResetError(err)
	}
	defer releaseRuntimeReset()
	c.SetRequest(c.Request().WithContext(resetCtx))
	if err := h.sessionService.SoftDelete(c.Request().Context(), sessionID); err != nil {
		if leaseErr := runtimefence.ResetLeaseFailure(c.Request().Context(), err); leaseErr != nil {
			return apperror.Wrap(apperror.CodeSessionResetConflict, leaseErr, nil)
		}
		return errs.Wrap(err, "reset session")
	}
	if h.projectionCache != nil {
		h.projectionCache.DropSession(sessionID)
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
		return bots.Bot{}, nil, session.Thread{}, apperror.New(apperror.CodeSessionNotFound, nil)
	}
	if !canAccessSession(sess, channelIdentityID, perms) {
		return bots.Bot{}, nil, session.Thread{}, apperror.New(apperror.CodeSessionNotFound, nil)
	}
	return bot, perms, sess, nil
}

func (h *SessionHandler) resolveCurrentUserPermissions(c echo.Context, channelIdentityID, botID string) ([]string, error) {
	if h.botService == nil || h.accountService == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "bot services not configured")
	}
	isAdmin, err := h.accountService.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return nil, errs.Wrap(err, "check admin")
	}
	perms, err := h.botService.ResolveUserPermissions(c.Request().Context(), botID, channelIdentityID, isAdmin)
	if err != nil {
		return nil, errs.Wrap(err, "resolve bot permissions")
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
		return apperror.New(apperror.CodeACPRuntimeOwnerMissing, nil)
	}
	// The runtime owner has no standing beyond their live grants: owner and
	// members alike must hold workspace_exec, so a revoked owner loses
	// runtime access at decision time (same model as the application-layer
	// External Agent decision authorizers).
	if actorUserID == "" || !bots.HasPermission(perms, bots.PermissionWorkspaceExec) {
		return apperror.New(apperror.CodeNoWorkspaceExec, nil)
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

// resolveSessionWorkdirBinding validates a workdir binding requested at
// session creation. Both creation entry points use it — the REST create and
// the WebSocket first send that creates its session in-band — so a session is
// only ever born bound to an active workdir of its own bot. An empty id means
// no binding and returns nil. External Agent sessions can only bind
// native-workspace workdirs until their runtime lifecycles are scoped by
// workspace target.
//
// Errors are echo.HTTPError values; the WebSocket caller renders them with
// wsErrorMessage.
func resolveSessionWorkdirBinding(ctx context.Context, workdirs sessionWorkdirService, botID, workdirID, runtimeType string) (*workdir.Workdir, error) {
	workdirID = strings.TrimSpace(workdirID)
	if workdirID == "" {
		return nil, nil
	}
	if workdirs == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "workdir service not configured")
	}
	bound, err := workdirs.RequireActive(ctx, botID, workdirID)
	if err != nil {
		return nil, workdirHTTPError(err)
	}
	if runtimekind.IsExternal(runtimeType) && bound.TargetKind == workdir.TargetKindRemote {
		return nil, apperror.New(apperror.CodeWorkdirRemoteUnsupportedForAgent, nil)
	}
	return &bound, nil
}

// descriptorFieldError answers an unusable session descriptor with the request
// field the caller has to change.
func descriptorFieldError(err error) error {
	var descErr *session.DescriptorError
	if errors.As(err, &descErr) {
		switch descErr.Field {
		case "session_mode":
			return apperror.FieldInvalid("session_mode", err)
		case "runtime_type":
			return apperror.FieldInvalid("runtime_type", err)
		}
	}
	return errs.Wrap(err, "resolve session descriptor")
}

// modelPreferenceError splits the picker's errors: what the caller can fix by
// choosing again is a field error, store and settings failures are internal.
func modelPreferenceError(err error, op string) error {
	switch {
	case errors.Is(err, application.ErrModelPreferenceInvalid):
		return apperror.FieldInvalid("preferred_chat_model_id", err)
	case errors.Is(err, application.ErrModelPreferenceRevisionInvalid):
		return apperror.FieldInvalid("expected_model_preference_revision", err)
	case errors.Is(err, application.ErrACPPreferenceUnsupported):
		return apperror.Wrap(apperror.CodeACPModelSelectionUnsupported, err, nil)
	default:
		return errs.Wrap(err, op)
	}
}

func sessionServiceError(err error) error {
	switch {
	case errors.Is(err, session.ErrACPAgentIDRequired), errors.Is(err, session.ErrACPAgentNotConfigured):
		return apperror.Wrap(apperror.CodeACPAgentNotConfigured, err, nil)
	case errors.Is(err, session.ErrACPUnknownAgent):
		return apperror.Wrap(apperror.CodeACPAgentNotFound, err, nil)
	case errors.Is(err, session.ErrACPAgentNotEnabled):
		return apperror.Wrap(apperror.CodeACPAgentNotEnabled, err, nil)
	case errors.Is(err, session.ErrRuntimeOwnerMissing):
		return apperror.New(apperror.CodeACPRuntimeOwnerMissing, nil)
	default:
		return errs.Wrap(err, "map session service error")
	}
}

func sessionForkError(err error) error {
	switch {
	case errors.Is(err, session.ErrForkSourceNotFound):
		return apperror.Wrap(apperror.CodeSessionNotFound, err, nil)
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
	if !runtimekind.IsExternal(targetRuntime) {
		return false
	}
	// The ACP agent profile is the Bot Agent instance's, compared by the
	// caller.
	before := session.ExternalIdentityOf(existing)
	after := session.ExternalIdentityOf(session.Thread{Metadata: session.ApplyExternalMetadataDefaults(targetMetadata), RuntimeMetadata: session.ApplyExternalMetadataDefaults(targetRuntimeMetadata)})
	return before.ProjectPath != after.ProjectPath
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

func cloneSessionMetadata(metadata map[string]any) map[string]any {
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}
