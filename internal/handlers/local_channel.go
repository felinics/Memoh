package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/apperror"
	attachmentpkg "github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/local"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/runtimefence"
	skillset "github.com/felinics/memoh/internal/skills"
	"github.com/felinics/memoh/internal/slash"
)

// localSpeechSynthesizer synthesizes text to speech audio.
type localSpeechSynthesizer interface {
	Synthesize(ctx context.Context, modelID string, text string, overrideCfg map[string]any) ([]byte, string, error)
}

// localSpeechModelResolver resolves speech model IDs for bots.
type localSpeechModelResolver interface {
	ResolveSpeechModelID(ctx context.Context, botID string) (string, error)
}

// LocalChannelHandler handles local channel routes (WebUI / API) backed by bot history.
type LocalChannelHandler struct {
	channelType         channel.ChannelType
	channelManager      *channel.Manager
	channelStore        *channel.Store
	routeHub            *local.RouteHub
	botService          *bots.Service
	accountService      *accounts.Service
	sessionService      *sessionpkg.Service
	agentService        *application.Service
	sessionRuntime      wsTurnAdmitter
	commandHandler      *command.Handler
	skillResolver       runtimeSkillResolver
	acpRuntimeStatus    acpRuntimeStatusReader
	mediaService        *media.Service
	speechService       localSpeechSynthesizer
	speechModelResolver localSpeechModelResolver
	wsSkillTurnsMu      sync.Mutex
	wsSkillTurns        *wsRequestedSkillTurnRegistry
	logger              *slog.Logger
	jwtSecret           string
	tokenTTL            time.Duration
}

type runtimeSkillResolver interface {
	ListSafeSkillCatalog(ctx context.Context, botID string) ([]skillset.SafeCatalogItem, error)
	ResolveTextRequestedSkills(ctx context.Context, botID string, names []string) ([]skillset.ResolvedSkill, error)
}

// acpRuntimeStatusReader is the live, server-owned capability snapshot used
// to recognize Agent-declared commands. The client never supplies this list:
// accepting a stale composer cache would let arbitrary slash text bypass the
// fail-closed skill classifier.
type acpRuntimeStatusReader interface {
	RuntimeStatus(sessionID, agentID, projectPath string) acpagent.RuntimeStatus
}

// wsTurnAdmitter is the durable admission this entry point depends on. It is
// declared as the one method the handler calls rather than as the manager type
// so a test can admit without a database, and so the handler cannot reach for
// snapshot or command routing that belongs to the runtime protocol instead.
type wsTurnAdmitter interface {
	Admit(ctx context.Context, in sessionruntime.AdmitInput) (sessionruntime.Admission, error)
	FinishRun(ctx context.Context, handle sessionruntime.RunHandle, status, message string) error
}

// NewLocalChannelHandler creates a local channel handler.
func NewLocalChannelHandler(channelType channel.ChannelType, channelManager *channel.Manager, channelStore *channel.Store, routeHub *local.RouteHub, botService *bots.Service, accountService *accounts.Service, sessionService *sessionpkg.Service) *LocalChannelHandler {
	return &LocalChannelHandler{
		channelType:    channelType,
		channelManager: channelManager,
		channelStore:   channelStore,
		routeHub:       routeHub,
		botService:     botService,
		accountService: accountService,
		sessionService: sessionService,
		wsSkillTurns:   newWSRequestedSkillTurnRegistry(),
		logger:         slog.Default().With(slog.String("handler", "local_channel")),
	}
}

// SetAgentService configures the application service used for WebSocket turns.
func (h *LocalChannelHandler) SetAgentService(service *application.Service) {
	h.agentService = service
}

// SetSessionRuntime installs the durable admission gate for turn-starting
// WebSocket messages.
func (h *LocalChannelHandler) SetSessionRuntime(admitter wsTurnAdmitter) {
	h.sessionRuntime = admitter
}

func (h *LocalChannelHandler) SetCommandHandler(handler *command.Handler) {
	h.commandHandler = handler
}

func (h *LocalChannelHandler) SetRuntimeSkillResolver(resolver runtimeSkillResolver) {
	h.skillResolver = resolver
}

// SetACPRuntimeStatusReader configures the authoritative live ACP capability
// source used by Web slash-command classification.
func (h *LocalChannelHandler) SetACPRuntimeStatusReader(reader acpRuntimeStatusReader) {
	h.acpRuntimeStatus = reader
}

// SetAuthTokenConfig configures runtime token minting for ACP-backed local WS streams.
func (h *LocalChannelHandler) SetAuthTokenConfig(jwtSecret string, ttl time.Duration) {
	h.jwtSecret = strings.TrimSpace(jwtSecret)
	h.tokenTTL = ttl
}

// SetMediaService sets the media service for WebSocket attachment ingestion.
func (h *LocalChannelHandler) SetMediaService(svc *media.Service) {
	h.mediaService = svc
}

// SetSpeechService configures speech synthesis for handling speech_delta events.
func (h *LocalChannelHandler) SetSpeechService(synth localSpeechSynthesizer, resolver localSpeechModelResolver) {
	h.speechService = synth
	h.speechModelResolver = resolver
}

// Register registers the local channel routes.
func (h *LocalChannelHandler) Register(e *echo.Echo) {
	prefix := fmt.Sprintf("/bots/:bot_id/%s", h.channelType.String())
	group := e.Group(prefix)
	group.GET("/stream", h.StreamMessages)
	group.POST("/messages", h.PostMessage)
	group.GET("/ws", h.HandleWebSocket)
	e.POST("/bots/:bot_id/quick-actions/execute", h.ExecuteQuickAction)
}

type QuickActionExecuteRequest struct {
	ActionID      string         `json:"action_id"`
	Params        map[string]any `json:"params,omitempty"`
	InvocationID  string         `json:"invocation_id,omitempty"`
	ComposerScope string         `json:"composer_scope,omitempty"`
	SessionID     string         `json:"session_id,omitempty"`
}

type CommandEventResponse struct {
	Type          string               `json:"type"`
	InvocationID  string               `json:"invocation_id,omitempty"`
	ComposerScope string               `json:"composer_scope,omitempty"`
	SessionID     string               `json:"session_id,omitempty"`
	ActionID      string               `json:"action_id,omitempty"`
	Terminal      bool                 `json:"terminal"`
	Result        *CommandActionResult `json:"result,omitempty"`
	Error         *CommandActionError  `json:"error,omitempty"`
}

type CommandActionResult struct {
	Kind  string                  `json:"kind"`
	Title string                  `json:"title,omitempty"`
	Text  string                  `json:"text,omitempty"`
	Items []CommandActionListItem `json:"items,omitempty"`
}

type CommandActionListItem struct {
	ID          string `json:"id,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind,omitempty"`
}

type CommandActionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ExecuteQuickAction godoc
// @Summary Execute a Web quick action
// @Description Runs a typed Web quick action such as help or skill.list and returns a command_result or command_error envelope.
// @Tags quick-actions
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param payload body QuickActionExecuteRequest true "Quick action payload"
// @Success 200 {object} CommandEventResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/quick-actions/execute [post].
func (h *LocalChannelHandler) ExecuteQuickAction(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	var req QuickActionExecuteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	actionID := strings.TrimSpace(req.ActionID)
	sessionID := strings.TrimSpace(req.SessionID)
	skillActivationAllowed := true
	if actionID == "permission" {
		// The permission action targets a live ACP session, so enforce session
		// visibility (chat or workspace_exec plus canAccessSession) instead of
		// the chat-only bot access check. An empty session falls through so the
		// executor returns its session-required command error.
		if sessionID != "" {
			if err := h.authorizeWSSession(c.Request().Context(), channelIdentityID, botID, sessionID); err != nil {
				return err
			}
		}
	} else {
		if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
			return err
		}
		if sessionID != "" {
			if err := h.authorizeWSSession(c.Request().Context(), channelIdentityID, botID, sessionID); err != nil {
				return err
			}
			supported, supportErr := h.wsSessionSupportsRequestedSkills(c.Request().Context(), sessionID)
			if supportErr != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, supportErr.Error())
			}
			skillActivationAllowed = supported
		}
	}
	if !quickActionSkillActivationAllowedHint(req.Params) {
		skillActivationAllowed = false
	}
	result, slashErr := h.executeWebQuickAction(c.Request().Context(), botID, actionID, skillActivationAllowed, webQuickActionContext{
		SessionID: sessionID,
		ActorID:   channelIdentityID,
		ModeID:    quickActionStringParam(req.Params, "mode_id"),
		ToolURL:   buildACPMCPToolsURL(c, botID),
	})
	event := commandEvent(req.InvocationID, req.ComposerScope, sessionID, actionID)
	if slashErr != nil {
		event.Type = "command_error"
		event.Error = &CommandActionError{Code: slashErr.Code, Message: slashUserMessage(slashErr.Code)}
		return c.JSON(http.StatusOK, event)
	}
	event.Type = "command_result"
	event.Result = result
	return c.JSON(http.StatusOK, event)
}

func quickActionSkillActivationAllowedHint(params map[string]any) bool {
	if params == nil {
		return true
	}
	allowed, ok := params["skill_activation_allowed"].(bool)
	return !ok || allowed
}

func quickActionStringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return value
}

func commandEvent(invocationID, composerScope, sessionID, actionID string) CommandEventResponse {
	return CommandEventResponse{
		InvocationID:  strings.TrimSpace(invocationID),
		ComposerScope: strings.TrimSpace(composerScope),
		SessionID:     strings.TrimSpace(sessionID),
		ActionID:      strings.TrimSpace(actionID),
		Terminal:      true,
	}
}

type webQuickActionContext struct {
	SessionID string
	ActorID   string
	ModeID    string
	ToolURL   string
}

func (h *LocalChannelHandler) executeWebQuickAction(ctx context.Context, botID, actionID string, skillActivationAllowed bool, control webQuickActionContext) (*CommandActionResult, *slash.Error) {
	switch strings.TrimSpace(actionID) {
	case "help":
		items := []CommandActionListItem{
			{ID: "help", Title: "/help", Description: "Show available quick actions", Kind: "quick_action"},
			{ID: "new", Title: "/new", Description: "Start a new session", Kind: "quick_action"},
			{ID: "compact", Title: "/compact", Description: "Compact the current session history", Kind: "quick_action"},
		}
		labels := []string{"/help", "/new", "/compact"}
		text := "Available Web quick actions: %s."
		// skillActivationAllowed already reflects a plain (non-ACP) chat
		// session, which is also the only context where the model picker
		// applies, so it doubles as the /model gate.
		if skillActivationAllowed {
			items = append(items,
				CommandActionListItem{ID: "skill.list", Title: "/skill list", Description: "Show runtime-usable skills", Kind: "quick_action"},
				CommandActionListItem{ID: "model", Title: "/model", Description: "Switch the chat model", Kind: "quick_action"},
			)
			labels = append(labels, "/skill list", "/model")
			text = "Available Web quick actions: %s. To activate a skill, send /<skill-name> or /<skill-name> <prompt>."
		}
		return &CommandActionResult{
			Kind:  "list",
			Title: "Quick actions",
			Items: items,
			Text:  fmt.Sprintf(text, strings.Join(labels, ", ")),
		}, nil
	case "skill.list":
		if !skillActivationAllowed {
			err := slash.Error{Code: slash.CodeUnsupportedSkillSlashContext}
			return nil, &err
		}
		if h.skillResolver == nil {
			err := slash.Error{Code: slash.CodeRequestedSkillNotRuntimeUsable, Msg: "skill resolver not configured"}
			return nil, &err
		}
		catalog, err := h.skillResolver.ListSafeSkillCatalog(ctx, botID)
		if err != nil {
			code := slashErrorCode(err)
			if code == "" {
				code = slash.CodeRequestedSkillNotRuntimeUsable
			}
			slashErr := slash.Error{Code: code, Msg: err.Error()}
			return nil, &slashErr
		}
		items := make([]CommandActionListItem, 0, len(catalog))
		for _, item := range catalog {
			items = append(items, CommandActionListItem{
				ID:          item.Name,
				Title:       item.Name,
				Description: item.Description,
				Kind:        "skill",
			})
		}
		return &CommandActionResult{Kind: "list", Title: "Skills", Items: items}, nil
	case "permission":
		return h.executeWebPermissionQuickAction(ctx, botID, control)
	default:
		err := slash.Error{Code: slash.CodeUnsupportedWebCommand}
		return nil, &err
	}
}

func (h *LocalChannelHandler) executeWebPermissionQuickAction(ctx context.Context, botID string, control webQuickActionContext) (*CommandActionResult, *slash.Error) {
	if h == nil || h.agentService == nil || strings.TrimSpace(control.SessionID) == "" {
		err := slash.Error{Code: slash.CodePermissionSessionRequired}
		return nil, &err
	}
	state, err := h.agentService.ConfigureACPMode(ctx, application.ACPModeRequest{
		BotID:                  strings.TrimSpace(botID),
		ThreadID:               strings.TrimSpace(control.SessionID),
		ActorChannelIdentityID: strings.TrimSpace(control.ActorID),
		ActorUserID:            strings.TrimSpace(control.ActorID),
		ModeID:                 control.ModeID,
		ToolHTTPURL:            strings.TrimSpace(control.ToolURL),
	})
	if err != nil {
		code := slash.CodePermissionModeFailed
		switch {
		case errors.Is(err, toolapproval.ErrForbidden):
			code = slash.CodePermissionDenied
		case errors.Is(err, application.ErrACPModeSessionRequired):
			code = slash.CodePermissionSessionRequired
		case errors.Is(err, application.ErrACPModeUnsupported):
			code = slash.CodePermissionModeUnsupported
		case errors.Is(err, application.ErrACPModeUnavailable):
			code = slash.CodePermissionModeUnavailable
		}
		slashErr := slash.Error{Code: code}
		return nil, &slashErr
	}
	items := make([]CommandActionListItem, 0, len(state.Available))
	for _, mode := range state.Available {
		kind := "acp_mode"
		if mode.ID == state.CurrentModeID {
			kind = "acp_mode_current"
		}
		items = append(items, CommandActionListItem{
			ID:          mode.ID,
			Title:       mode.Name,
			Description: mode.Description,
			Kind:        kind,
		})
	}
	resultKind := "permission_modes"
	if state.Changed {
		resultKind = "permission_mode_changed"
	}
	return &CommandActionResult{Kind: resultKind, Items: items}, nil
}

func slashErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var slashErr slash.Error
	if errors.As(err, &slashErr) {
		return slashErr.Code
	}
	return ""
}

func slashUserMessage(code string) string {
	switch code {
	case slash.CodeUnknownSlash:
		return "Unknown slash command."
	case slash.CodeUnsupportedWebCommand:
		return "This slash command is not available in Web."
	case slash.CodeInvalidSkillSlashSyntax:
		return "Invalid skill slash syntax. Use /<skill-name> [prompt] or pick a skill from the composer."
	case slash.CodeRequestedSkillNotFound:
		return "Requested skill was not found."
	case slash.CodeRequestedSkillAmbiguous:
		return "Requested skill is ambiguous."
	case slash.CodeRequestedSkillDisabled:
		return "Requested skill is disabled."
	case slash.CodeRequestedSkillNotRuntimeUsable:
		return "Requested skill is not available for chat."
	case slash.CodeTooManyRequestedSkills:
		return "Too many skills selected."
	case slash.CodeRequestedSkillContextTooLarge:
		return "Selected skill context is too large."
	case slash.CodeSlashAttachmentsUnsupported:
		return "Slash commands cannot be sent with attachments."
	case slash.CodeUnsupportedSkillSlashContext:
		return "Requested skills are not supported in this context."
	case slash.CodeUnsupportedLegacyEndpoint:
		return "Skill activation requires WebSocket. Reconnect and try again."
	case slash.CodePermissionDenied:
		return "You do not have permission to run this command."
	case slash.CodeReservedSkillMetadata:
		return "Reserved skill metadata cannot be supplied by clients."
	case slash.CodeInvalidQuickActionScope:
		return "This quick action cannot be scoped to a session."
	case slash.CodePermissionSessionRequired:
		return "Open an external-Agent session before using /permission."
	case slash.CodePermissionModeUnsupported:
		return "This Agent does not declare selectable session modes."
	case slash.CodePermissionModeUnavailable:
		return "That mode is not available for this Agent session."
	case slash.CodePermissionModeFailed:
		return "The Agent session mode could not be loaded or changed."
	default:
		return "Slash command failed."
	}
}

func (h *LocalChannelHandler) resolveWebRequestedSkillContexts(ctx context.Context, botID string, requested []webRequestedSkill) ([]turn.RequestedSkillContext, error) {
	names := make([]string, 0, len(requested))
	for _, item := range requested {
		names = append(names, strings.TrimSpace(item.Name))
	}
	return h.resolveWebTextRequestedSkillContexts(ctx, botID, names)
}

func (h *LocalChannelHandler) resolveWebTextRequestedSkillContexts(ctx context.Context, botID string, names []string) ([]turn.RequestedSkillContext, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if h.skillResolver == nil {
		return nil, slash.NewError(slash.CodeRequestedSkillNotRuntimeUsable)
	}
	resolved, err := h.skillResolver.ResolveTextRequestedSkills(ctx, botID, names)
	if err != nil {
		return nil, err
	}
	return skillset.RequestedSkillContexts(resolved), nil
}

func (h *LocalChannelHandler) classifyWebSlash(text string, hasAttachments bool, surface slash.Surface) slash.Decision {
	return slash.Classify(slash.ClassifyInput{
		Text:           text,
		HasAttachments: hasAttachments,
		Surface:        surface,
		IsGroup:        false,
		Directed:       true,
		SupportsMode:   false,
		KnownCommand: func(resource string) bool {
			if resource == "help" || resource == "skill" || resource == "permission" {
				return true
			}
			return h.commandHandler != nil && h.commandHandler.HasCommandResource(resource)
		},
		WebActionSupported: func(resource, action string) bool {
			return webActionID(resource, action) != ""
		},
	})
}

func (h *LocalChannelHandler) classifyWebSlashForSession(ctx context.Context, text string, hasAttachments bool, sessionID string) slash.Decision {
	decision := h.classifyWebSlash(text, hasAttachments, slash.SurfaceWebWS)
	selector := exactWebSlashSelector(text)
	liveCommand, liveACP := h.liveACPCommandAuthority(sessionID, selector)
	if liveCommand {
		// Preserve the parsed invocation for diagnostics, but deliberately leave
		// the message text alone. The ordinary message path below must send the
		// exact `/name args` input to the ACP prompt instead of turning it into a
		// Memoh command or stripping the selector like a skill activation.
		// AgentCommand carries the matched selector so the session pool can
		// re-validate it against the final runtime at prompt time.
		return slash.Decision{
			Kind:         slash.DecisionNormalChat,
			Directed:     decision.Directed,
			Invocation:   decision.Invocation,
			AgentCommand: selector,
		}
	}
	if isReservedWebACPControl(selector) ||
		(decision.Invocation != nil && isReservedWebACPControl(decision.Invocation.Parsed.Resource)) {
		return decision
	}
	pathProse := decision.Kind == slash.DecisionNormalChat && strings.Contains(selector, "/")
	if selector != "" && !pathProse &&
		(liveACP || h.isACPRuntimeSession(ctx, sessionID)) {
		// ACP sessions never reinterpret an unadvertised Agent command as a
		// Memoh skill activation. SessionPool's live full replacement is the sole
		// command authority, so stale or unknown selectors fail with one stable
		// command error (attachments included). One exception: the classifier's
		// prose carve-out for a Unix path or URL after the slash ("/etc/hosts
		// what does this line mean" — the head token itself contains a "/")
		// stays normal chat and reaches the agent as text. Opaque command-ish
		// tokens without a "/" (including case near-misses of advertised
		// commands) still fail closed here.
		return slash.Decision{
			Kind:       slash.DecisionUnknownSlash,
			Code:       slash.CodeUnknownSlash,
			Directed:   decision.Directed,
			Invocation: decision.Invocation,
		}
	}
	return decision
}

func exactWebSlashSelector(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	selector := strings.TrimPrefix(text, "/")
	if index := strings.IndexFunc(selector, unicode.IsSpace); index >= 0 {
		selector = selector[:index]
	}
	return selector
}

func (h *LocalChannelHandler) liveACPCommandAuthority(sessionID, selector string) (bool, bool) {
	if h == nil || h.acpRuntimeStatus == nil {
		return false, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || selector == "" || isReservedWebACPControl(strings.ToLower(selector)) {
		return false, false
	}

	status := h.acpRuntimeStatus.RuntimeStatus(sessionID, "", "")
	if strings.TrimSpace(status.SessionID) != sessionID {
		return false, false
	}
	liveACP := strings.TrimSpace(status.ACPSession) != ""
	if !liveACP {
		return false, false
	}
	for _, advertised := range status.AvailableCommands {
		// ACP command IDs are opaque and case-sensitive. Compare the exact selector
		// extracted from the original slash input; descriptions and input hints are
		// display metadata, never authority.
		if advertised.Name == selector {
			return true, true
		}
	}
	return false, true
}

func (h *LocalChannelHandler) isACPRuntimeSession(ctx context.Context, sessionID string) bool {
	if h == nil || h.sessionService == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	sess, err := h.sessionService.Get(ctx, strings.TrimSpace(sessionID))
	return err == nil && sessionpkg.IsACPRuntime(sess)
}

// wsSessionAuthAckCode maps a session pre-authorization failure onto the ack
// code the decision surfaces render. Authorization denials (403/404 masking)
// keep the forbidden code; infrastructure failures must not masquerade as "you
// do not have permission", so 5xx maps to the operation-failed code.
func wsSessionAuthAckCode(err error, forbidden, failed apperror.Code) apperror.Code {
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) && httpErr.Code >= http.StatusInternalServerError {
		return failed
	}
	return forbidden
}

func isReservedWebACPControl(resource string) bool {
	switch strings.ToLower(strings.TrimSpace(resource)) {
	case "help", "new", "permission", "skill":
		return true
	default:
		return false
	}
}

func webActionID(resource, action string) string {
	resource = strings.TrimSpace(strings.ToLower(resource))
	action = strings.TrimSpace(strings.ToLower(action))
	switch {
	case resource == "help" && action == "":
		return "help"
	case resource == "skill" && (action == "" || action == "list"):
		return "skill.list"
	case resource == "permission":
		return "permission"
	default:
		return ""
	}
}

func sendWSCommandError(writer *wsWriter, msg wsClientMessage, code string) {
	event := commandEvent(msg.InvocationID, msg.ComposerScope, msg.SessionID, "")
	event.Type = "command_error"
	event.Error = &CommandActionError{Code: code, Message: slashUserMessage(code)}
	writer.SendJSON(event)
}

func sendWSCommandResult(writer *wsWriter, msg wsClientMessage, actionID string, result *CommandActionResult) {
	event := commandEvent(msg.InvocationID, msg.ComposerScope, msg.SessionID, actionID)
	event.Type = "command_result"
	event.Result = result
	writer.SendJSON(event)
}

// StreamMessages godoc
// @Summary Subscribe to local channel events via SSE
// @Description Open a persistent SSE connection to receive real-time stream events for the given bot.
// @Tags local-channel
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Success 200 {string} string "SSE stream"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/web/stream [get].
func (h *LocalChannelHandler) StreamMessages(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	if h.routeHub == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "route hub not configured")
	}

	setSSEHeaders(c)
	c.Response().WriteHeader(http.StatusOK)

	flusher, ok := c.Response().Writer.(http.Flusher)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "streaming not supported")
	}
	writer := c.Response().Writer

	_, stream, cancel := h.routeHub.Subscribe(botID)
	defer cancel()

	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case msg, ok := <-stream:
			if !ok {
				return nil
			}
			data, err := formatLocalStreamEvent(msg.Event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(writer, "data: %s\n\n", string(data)); err != nil {
				return nil // client disconnected
			}
			flusher.Flush()
		}
	}
}

func formatLocalStreamEvent(event channel.StreamEvent) ([]byte, error) {
	return json.Marshal(event)
}

func jsonBodyHasKey(body []byte, key string) bool {
	if len(bytes.TrimSpace(body)) == 0 || strings.TrimSpace(key) == "" {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return false
	}
	_, ok := obj[key]
	return ok
}

// LocalChannelMessageRequest is the request body for posting a local channel message.
type LocalChannelMessageRequest struct {
	Message           channel.Message `json:"message" validate:"required"`
	ModelID           string          `json:"model_id,omitempty"`
	ReasoningEffort   string          `json:"reasoning_effort,omitempty"`
	WorkspaceTargetID string          `json:"workspace_target_id,omitempty"`
}

// PostMessage godoc
// @Summary Send a message to a local channel
// @Description Post a user message (with optional attachments) through the local channel pipeline.
// @Tags local-channel
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param payload body LocalChannelMessageRequest true "Message payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/web/messages [post].
func (h *LocalChannelHandler) PostMessage(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	if h.channelManager == nil || h.channelStore == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "channel manager not configured")
	}
	body, readErr := io.ReadAll(c.Request().Body)
	if readErr != nil {
		return echo.NewHTTPError(http.StatusBadRequest, readErr.Error())
	}
	c.Request().Body = io.NopCloser(bytes.NewReader(body))
	if jsonBodyHasKey(body, "requested_skills") {
		event := commandEvent("", "", "", "")
		event.Type = "command_error"
		event.Error = &CommandActionError{Code: slash.CodeUnsupportedLegacyEndpoint, Message: slashUserMessage(slash.CodeUnsupportedLegacyEndpoint)}
		return c.JSON(http.StatusBadRequest, event)
	}
	var req LocalChannelMessageRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if req.Message.IsEmpty() {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required")
	}
	workspaceTargetID := strings.TrimSpace(req.WorkspaceTargetID)
	if workspaceTargetID != "" {
		perms, permissionErr := h.resolveCurrentUserPermissions(c.Request().Context(), channelIdentityID, botID)
		if permissionErr != nil {
			return permissionErr
		}
		if err := authorizeWorkspaceTargetSelection(perms, workspaceTargetID); err != nil {
			return err
		}
		if h.agentService == nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "resolver not configured")
		}
		if err := h.agentService.ValidateWorkspaceTarget(c.Request().Context(), botID, workspaceTargetID); err != nil {
			return echo.NewHTTPError(http.StatusConflict, err.Error())
		}
	}
	if err := channel.RejectReservedSkillMetadata(req.Message); err != nil {
		event := commandEvent("", "", "", "")
		event.Type = "command_error"
		event.Error = &CommandActionError{Code: slash.CodeReservedSkillMetadata, Message: slashUserMessage(slash.CodeReservedSkillMetadata)}
		return c.JSON(http.StatusBadRequest, event)
	}
	// Slash CONTROL input (commands, skill activation) is WS-only; the legacy
	// REST endpoint rejects it instead of degrading. Run the shared classifier
	// rather than a bare "/" prefix check so prose that merely starts with a
	// slash ("/etc/nginx.conf keeps failing…") still reaches the model.
	if decision := h.classifyWebSlash(strings.TrimSpace(req.Message.PlainText()), len(req.Message.Attachments) > 0, slash.SurfaceWebWS); decision.Kind != slash.DecisionNormalChat {
		event := commandEvent("", "", "", "")
		event.Type = "command_error"
		event.Error = &CommandActionError{Code: slash.CodeUnsupportedLegacyEndpoint, Message: slashUserMessage(slash.CodeUnsupportedLegacyEndpoint)}
		return c.JSON(http.StatusOK, event)
	}
	cfg, err := h.channelStore.ResolveEffectiveConfig(c.Request().Context(), botID, h.channelType)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	routeKey := botID
	msg := channel.InboundMessage{
		Channel:     h.channelType,
		Message:     req.Message,
		BotID:       botID,
		ReplyTarget: routeKey,
		RouteKey:    routeKey,
		Sender: channel.Identity{
			SubjectID: channelIdentityID,
			Attributes: map[string]string{
				"user_id": channelIdentityID,
			},
		},
		Conversation: channel.Conversation{
			ID:   routeKey,
			Type: channel.ConversationTypePrivate,
		},
		ReceivedAt: time.Now().UTC(),
		Source:     "local",
	}
	if mid := strings.TrimSpace(req.ModelID); mid != "" {
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]any)
		}
		msg.Metadata["model_id"] = mid
	}
	if re := strings.TrimSpace(req.ReasoningEffort); re != "" {
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]any)
		}
		msg.Metadata["reasoning_effort"] = re
	}
	if workspaceTargetID != "" {
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]any)
		}
		msg.Metadata["workspace_target_id"] = workspaceTargetID
	}
	if err := h.channelManager.HandleInbound(c.Request().Context(), cfg, msg); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// wsClientMessage carries the three identifiers a client is allowed to name,
// and they are not interchangeable.
//
// invocation_id is minted by the client and names an *intent*: it is the
// idempotency key for starting a turn, so a redelivered send resolves to the
// run it already started instead of a second one.
//
// run_id is minted by the server and names a *run*: it is the only way to
// address work that is executing, which is why abort and decision responses
// carry it.
//
// turn_id is minted by the server at admission and names a *round*: it is how
// a client addresses a round it wants replaced. It is published while the run
// is still streaming, which is what separates it from a stored message id —
// that only exists once the round has been persisted, so a client that had to
// name one could not act on the round it is looking at.
//
// A client can never name a run or a turn it has not been told about.
type wsClientMessage struct {
	Type          string `json:"type"`
	RunID         string `json:"run_id,omitempty"`
	Text          string `json:"text,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	InvocationID  string `json:"invocation_id,omitempty"`
	ComposerScope string `json:"composer_scope,omitempty"`
	// TurnID names an existing round the client wants replaced (retry, edit).
	TurnID string `json:"turn_id,omitempty"`
	// MessageID is the pre-turn spelling of TurnID.
	//
	// Deprecated: accepted so a client shipped against the message-id contract
	// keeps working after a server upgrade — the desktop app is distributed on
	// its own cadence and does not update in lockstep with the server it
	// connects to. The server resolves it to the round that contains it.
	// Remove once the compatibility window closes.
	MessageID         string                     `json:"message_id,omitempty"`
	Attachments       []json.RawMessage          `json:"attachments,omitempty"`
	RequestedSkills   []webRequestedSkill        `json:"requested_skills,omitempty"`
	ModelID           string                     `json:"model_id,omitempty"`
	ReasoningEffort   string                     `json:"reasoning_effort,omitempty"`
	WorkspaceTargetID string                     `json:"workspace_target_id,omitempty"`
	DecisionID        string                     `json:"decision_id,omitempty"`
	Decision          string                     `json:"decision,omitempty"`
	OptionID          string                     `json:"option_id,omitempty"`
	Reason            string                     `json:"reason,omitempty"`
	Answers           []userinput.QuestionAnswer `json:"answers,omitempty"`
	Canceled          bool                       `json:"canceled,omitempty"`
	// Cursor is the subscriber's last observed position. It is carried here
	// rather than in a separate message type because runtime_subscribe shares
	// this envelope with every other inbound message.
	Cursor *runtimeCursor `json:"cursor,omitempty"`
	// ControlID is the client's name for a control request such as abort. It is
	// the idempotency key: a client that retries an abort — or reissues one after
	// reconnecting to a different server — is answered with the first attempt's
	// outcome instead of executing a second control, and the ack it receives
	// carries this id back so it can be matched to the request that caused it.
	ControlID string `json:"control_id,omitempty"`
}

type webRequestedSkill struct {
	Name string `json:"name"`
}

type wsOutboundEvent struct {
	Type string `json:"type"`
	// RunID is set on everything that describes an existing run. InvocationID
	// is set only where no run exists yet — acceptance, rejection, session
	// creation, and validation failures — because those are the moments when the
	// client's own name for the turn is the only one both sides share.
	RunID        string `json:"run_id,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Code         string `json:"code,omitempty"`
	// TurnID names the turn the run belongs to. Every subscriber of the session
	// is shown the same one (SR-OBS-003), so it is what lets the client that sent
	// the turn and one that only watches agree on which turn a run is executing.
	TurnID string `json:"turn_id,omitempty"`
	// Epoch and Seq are the session's authoritative position at the moment the
	// run became observable. They use the same names as the subscription frames
	// so a client orders acceptance against those frames with one comparison
	// instead of two vocabularies.
	Epoch string `json:"epoch,omitempty"`
	Seq   int64  `json:"seq,omitempty"`
	// Duplicate marks an acceptance that named a run the invocation had already
	// started, which is what makes a redelivered send safe to answer.
	Duplicate bool   `json:"duplicate,omitempty"`
	Data      any    `json:"data,omitempty"`
	Message   string `json:"message,omitempty"`
	Feedback  any    `json:"feedback,omitempty"`
	// Control, ControlID and Applied describe the outcome of a control request.
	// Applied is reported separately from Code because "the run was already over"
	// is not a failure: the control was resolved, it simply changed nothing, and a
	// client must be able to tell that apart from a control that never landed.
	Control   string `json:"control,omitempty"`
	ControlID string `json:"control_id,omitempty"`
	Applied   bool   `json:"applied,omitempty"`
}

// wsTurnRef names the turn an outbound event belongs to. It exists because that
// name changes exactly once — from the client's invocation to the server's run —
// and threading two strings through every send site invited them to drift apart.
type wsTurnRef struct {
	RunID        string
	InvocationID string
	SessionID    string
}

func wsTurn(invocationID, sessionID string) wsTurnRef {
	return wsTurnRef{InvocationID: strings.TrimSpace(invocationID), SessionID: strings.TrimSpace(sessionID)}
}

func (r wsTurnRef) withRun(runID string) wsTurnRef {
	r.RunID = strings.TrimSpace(runID)
	return r
}

func (r wsTurnRef) withSession(sessionID string) wsTurnRef {
	r.SessionID = strings.TrimSpace(sessionID)
	return r
}

// event names the turn the way the client can resolve it: once a run exists its
// id is authoritative and the invocation is redundant, because a second
// subscriber that never sent the invocation still has to recognise this run.
func (r wsTurnRef) event(eventType string) wsOutboundEvent {
	out := wsOutboundEvent{Type: eventType, SessionID: r.SessionID}
	if r.RunID != "" {
		out.RunID = r.RunID
		return out
	}
	out.InvocationID = r.InvocationID
	return out
}

type wsRequestedSkillTurnRegistry struct {
	mu     sync.Mutex
	active map[string]int
}

func newWSRequestedSkillTurnRegistry() *wsRequestedSkillTurnRegistry {
	return &wsRequestedSkillTurnRegistry{active: make(map[string]int)}
}

func wsRequestedSkillTurnKey(botID, sessionID string) string {
	return strings.TrimSpace(botID) + ":" + strings.TrimSpace(sessionID)
}

func (r *wsRequestedSkillTurnRegistry) reserve(botID, sessionID, invocationID string) (func(), bool) {
	key := wsRequestedSkillTurnKey(botID, sessionID)
	if r == nil || strings.TrimSpace(botID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(invocationID) == "" {
		return func() {}, true
	}

	r.mu.Lock()
	if r.active == nil {
		r.active = make(map[string]int)
	}
	if r.active[key] > 0 {
		r.mu.Unlock()
		return nil, false
	}
	r.active[key] = 1
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			switch refs := r.active[key] - 1; {
			case refs > 0:
				r.active[key] = refs
			default:
				delete(r.active, key)
			}
			r.mu.Unlock()
		})
	}, true
}

func (r *wsRequestedSkillTurnRegistry) enter(botID, sessionID, invocationID string) func() {
	key := wsRequestedSkillTurnKey(botID, sessionID)
	if r == nil || strings.TrimSpace(botID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(invocationID) == "" {
		return func() {}
	}
	r.mu.Lock()
	if r.active == nil {
		r.active = make(map[string]int)
	}
	r.active[key]++
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			switch refs := r.active[key] - 1; {
			case refs > 0:
				r.active[key] = refs
			default:
				delete(r.active, key)
			}
			r.mu.Unlock()
		})
	}
}

func (h *LocalChannelHandler) reserveWSRequestedSkillTurn(botID, sessionID, invocationID string) (func(), bool) {
	if h == nil {
		return func() {}, true
	}
	h.wsSkillTurnsMu.Lock()
	if h.wsSkillTurns == nil {
		h.wsSkillTurns = newWSRequestedSkillTurnRegistry()
	}
	registry := h.wsSkillTurns
	h.wsSkillTurnsMu.Unlock()
	return registry.reserve(botID, sessionID, invocationID)
}

func (h *LocalChannelHandler) enterWSMessageTurn(botID, sessionID, invocationID string) func() {
	if h == nil {
		return func() {}
	}
	h.wsSkillTurnsMu.Lock()
	if h.wsSkillTurns == nil {
		h.wsSkillTurns = newWSRequestedSkillTurnRegistry()
	}
	registry := h.wsSkillTurns
	h.wsSkillTurnsMu.Unlock()
	return registry.enter(botID, sessionID, invocationID)
}

// wsWriter serialises all WebSocket writes through a single goroutine to
// avoid concurrent write panics with gorilla/websocket.
type wsWriter struct {
	conn      *websocket.Conn
	ch        chan []byte
	closeOnce sync.Once
	stop      chan struct{}
	done      chan struct{}
}

func newWSWriter(conn *websocket.Conn) *wsWriter {
	w := &wsWriter{
		conn: conn,
		ch:   make(chan []byte, 128),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go w.loop()
	return w
}

func (w *wsWriter) loop() {
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			return
		default:
		}

		select {
		case data := <-w.ch:
			_ = w.conn.WriteMessage(websocket.TextMessage, data)
		case <-w.stop:
			return
		}
	}
}

func (w *wsWriter) Send(data []byte) {
	select {
	case <-w.stop:
		return
	default:
	}

	select {
	case w.ch <- data:
	case <-w.stop:
	}
}

func (w *wsWriter) SendJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	w.Send(data)
}

func (w *wsWriter) Close() {
	w.closeOnce.Do(func() {
		close(w.stop)
	})
	<-w.done
}

// extractRawBearerToken returns the raw JWT token suitable for passing to the
// gateway. The gateway WS handler receives the token directly (not as an HTTP
// header), so we must strip the "Bearer " prefix if present.
func extractRawBearerToken(c echo.Context) string {
	auth := strings.TrimSpace(c.Request().Header.Get("Authorization"))
	if auth != "" {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return strings.TrimSpace(c.QueryParam("token"))
}

func (h *LocalChannelHandler) issueRuntimeOwnerBearerToken(runtimeOwnerAccountID, fallbackBearerToken string) string {
	runtimeOwnerAccountID = strings.TrimSpace(runtimeOwnerAccountID)
	if h == nil || strings.TrimSpace(h.jwtSecret) == "" || runtimeOwnerAccountID == "" || h.tokenTTL <= 0 {
		return fallbackBearerToken
	}
	signed, _, err := auth.GenerateToken(runtimeOwnerAccountID, h.jwtSecret, h.tokenTTL)
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("issue ACP runtime token failed", slog.Any("error", err))
		}
		return fallbackBearerToken
	}
	return "Bearer " + signed
}

// resolveWSTargetTurnID settles which round a replacement names. A turn id is
// the contract; a message id is the pre-turn spelling and is resolved to its
// round here, at the transport boundary, so nothing downstream has to know two
// ways of naming a round.
//
// It reads session history, so every caller MUST authorize the session first.
// Resolving before that would answer "does this message id belong to this
// session" for a caller who cannot read the session at all, which is a
// cross-session existence oracle even though no write follows it.
//
// The storage error is deliberately not returned: it names rows, and the
// caller only needs to know that the id did not resolve. The cause is logged.
//
// Deprecated behaviour: the message_id branch exists only for clients shipped
// before the turn-id contract. Remove it with the field.
func (h *LocalChannelHandler) resolveWSTargetTurnID(ctx context.Context, sessionID, turnID, legacyMessageID string) (string, error) {
	if turnID != "" {
		return turnID, nil
	}
	legacy := strings.TrimSpace(legacyMessageID)
	if legacy == "" {
		return "", errors.New("turn_id is required")
	}
	resolved, err := h.agentService.ResolveTurnIDForMessage(ctx, sessionID, legacy)
	if err != nil {
		h.logger.Warn("resolve deprecated message_id failed",
			slog.String("session_id", sessionID),
			slog.Any("error", err),
		)
		return "", errors.New("message_id does not name a turn in this session")
	}
	return resolved, nil
}

func sendWSError(writer *wsWriter, ref wsTurnRef, message string) {
	event := ref.event("error")
	event.Message = message
	writer.SendJSON(event)
}

func sendWSAgentError(writer *wsWriter, ref wsTurnRef, streamEvent native.StreamEvent) {
	event := ref.event("error")
	event.Code = strings.TrimSpace(streamEvent.Code)
	event.Message = strings.TrimSpace(streamEvent.Error)
	if event.Message == "" {
		event.Message = "stream error"
	}
	writer.SendJSON(event)
}

// wsRunAcceptance is what a client needs beyond the run id to reconcile the turn
// it rendered optimistically with the authoritative one: which turn the run
// executes, and where in the session's stream it became observable.
type wsRunAcceptance struct {
	TurnID string
	Cursor sessionruntime.Cursor
	// Duplicate marks an acceptance that named a run the invocation had already
	// started, so a redelivered send attaches to that turn instead of expecting
	// a second one.
	Duplicate bool
}

// sendWSRunAccepted tells the client the server's name for the turn it asked
// for. It is the first event of every run and the only place a run id is
// introduced, so it has to be written before anything that carries one.
func sendWSRunAccepted(writer *wsWriter, ref wsTurnRef, accepted wsRunAcceptance) {
	writer.SendJSON(wsOutboundEvent{
		Type:         "run_accepted",
		RunID:        ref.RunID,
		InvocationID: ref.InvocationID,
		SessionID:    ref.SessionID,
		TurnID:       accepted.TurnID,
		Epoch:        accepted.Cursor.Epoch,
		Seq:          accepted.Cursor.Seq,
		Duplicate:    accepted.Duplicate,
	})
}

// sendWSRunRejected answers a submission that will not become a run. The code is
// a stable apperror code rather than prose because the client branches on it: one
// of these two is worth retrying unchanged and the other never is.
func sendWSRunRejected(writer *wsWriter, ref wsTurnRef, code apperror.Code, message string) {
	writer.SendJSON(wsOutboundEvent{
		Type:         "run_rejected",
		InvocationID: ref.InvocationID,
		SessionID:    ref.SessionID,
		Code:         string(code),
		Message:      message,
	})
}

// sendWSControlAck answers a control the client named. It is sent from whichever
// connection received the request, which is frequently not the one executing the
// run, so it reports what the control did rather than what this connection did.
//
// Applied false with an empty code is a resolved control that changed nothing —
// the run had already finished — and is a different answer from a code, which
// means the control never reached an owner and is worth retrying.
func sendWSControlAck(writer *wsWriter, ref wsTurnRef, control, controlID string, applied bool, code string) {
	writer.SendJSON(wsOutboundEvent{
		Type:      "control_ack",
		RunID:     ref.RunID,
		SessionID: ref.SessionID,
		Control:   control,
		ControlID: controlID,
		Applied:   applied,
		Code:      code,
	})
}

// wsRunRejectionCode maps the admission sentinels to the stable codes clients
// branch on. Everything else is a stream error, not a refusal to run.
func wsRunRejectionCode(err error) (apperror.Code, bool) {
	switch {
	case errors.Is(err, sessionruntime.ErrSessionBusy):
		return apperror.CodeSessionBusy, true
	case errors.Is(err, sessionruntime.ErrInvocationConflict):
		return apperror.CodeSessionInvocationConflict, true
	default:
		return "", false
	}
}

func newWSAppErrorEvent(ref wsTurnRef, err error) (wsOutboundEvent, bool) {
	public, ok := apperror.PublicFrom(err, "")
	if !ok {
		return wsOutboundEvent{}, false
	}
	event := ref.event("error")
	event.Message = public.Detail
	event.Feedback = public
	return event, true
}

func sendWSErrorFromError(writer *wsWriter, ref wsTurnRef, err error) {
	// A refusal to run is not a stream failure: the turn never started, so the
	// client is told which invocation was refused and why, by code.
	if code, ok := wsRunRejectionCode(err); ok {
		sendWSRunRejected(writer, ref.withRun(""), code, err.Error())
		return
	}
	if event, ok := newWSAppErrorEvent(ref, err); ok {
		writer.SendJSON(event)
		return
	}
	feedback := acpFeedbackError(err)
	if feedback == nil {
		sendWSError(writer, ref, wsErrorMessage(err))
		return
	}
	event := ref.event("error")
	event.Message = strings.TrimSpace(feedback.Message)
	event.Feedback = feedback
	writer.SendJSON(event)
}

// forwardWSStreamEvents publishes the run's events to the session runtime,
// which folds each one into the run's authoritative state so every subscriber
// of the session sees it — including one attached to a different connection or
// a different server, and including this client after it reconnects
// (SR-OBS-003).
//
// The initiating socket is not a special case and receives nothing here beyond
// errors. It reads the run the same way every other subscriber does, through
// the snapshot and deltas of the session it subscribed to; rendering a second,
// connection-local copy of the same events would be a projection only this
// client can see, which is exactly the divergence SR-OBS-003 forbids.
func (h *LocalChannelHandler) forwardWSStreamEvents(ctx, assetCtx context.Context, writer *wsWriter, botID string, ref wsTurnRef, handle sessionruntime.RunHandle, eventCh <-chan application.WSStreamEvent) {
	publisher := h.sessionRuntimePublisher()
	if handle.FencingToken <= 0 {
		// No admitted ownership, so there is nothing this process is allowed to
		// write for the run. Only a caller that bypasses admission gets here.
		publisher = nil
	}
	outboundAssetRefs := make([]messagepkg.AssetRef, 0)
	for event := range eventCh {
		processed := h.processWSEvent(ctx, botID, event)
		for _, p := range processed {
			if refs := extractAssetRefsFromProcessedEvent(p); len(refs) > 0 {
				outboundAssetRefs = append(outboundAssetRefs, refs...)
			}

			var streamEvent native.StreamEvent
			if err := json.Unmarshal(p, &streamEvent); err != nil {
				continue
			}

			if publisher != nil {
				// Published before anything is said on this socket, so the
				// session's state never lags behind what a client has already
				// been told: a reconnecting subscriber must not be handed less
				// than it saw.
				//
				// assetCtx, not ctx: an aborted run cancels ctx and its last
				// events arrive after that, which is exactly when publishing them
				// matters most.
				if _, err := publisher.HandleAgentEvent(assetCtx, handle, streamEvent); err != nil {
					if errors.Is(err, sessionruntime.ErrRunOwnershipLost) {
						// A superseded owner has nothing left to publish and every
						// later event would fail identically.
						publisher = nil
					}
					h.logger.Warn("publish runtime run event failed",
						slog.Any("error", err),
						slog.String("bot_id", botID),
						slog.String("run_id", ref.RunID),
						slog.String("session_id", ref.SessionID))
				}
			}

			// An error is the one thing the run's published state cannot carry on
			// its own: it names the run as failed but not what to tell this
			// caller, which is still waiting on the send it made.
			if streamEvent.Type == native.EventError {
				sendWSAgentError(writer, ref, streamEvent)
			}
		}
	}
	if len(outboundAssetRefs) > 0 {
		h.agentService.LinkOutboundAssets(assetCtx, botID, ref.SessionID, outboundAssetRefs)
	}
}

// wsStreamRunner receives the run it is executing as, because the run id is
// minted at admission and everything downstream — compaction barriers, ACP
// sessions, interactive tool headers — has to agree on that one name.
//
// turn is the turn admission allocated for this run. It is separate from ref
// because ref is how the *client* names the turn, while this is how the
// *database* does: the same value would otherwise have to be threaded through
// every send site that only cares about the client's name for it.
type wsStreamRunner func(ctx context.Context, ref wsTurnRef, turn wsAdmittedTurn, eventCh chan<- application.WSStreamEvent, abortCh <-chan struct{}) error

type wsRunAdmissionBuilder func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error)

// wsAdmittedTurn is the turn identity a run was admitted with. A zero value
// means no admission decided the turn, so the history layer allocates one.
type wsAdmittedTurn struct {
	TurnID   string
	Position *int64
}

// wsSubmission is the canonical form of what a client sent. Its bytes decide
// whether a repeated invocation is the same submit or a conflicting one, so it
// carries only what the user actually submitted: no timestamps, no tokens, no
// per-attempt ids. Field order is the encoding order, so equal submissions
// encode equal.
type wsSubmission struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	Text      string `json:"text,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	// Attachments is a digest rather than the files themselves. Two submissions
	// differ if their attachments differ, which is all the fingerprint needs,
	// and the durable row does not carry inlined uploads to learn it.
	Attachments string `json:"attachments,omitempty"`
}

// digestWSAttachments summarises the attachments exactly as the client sent
// them. The raw bytes are the identity: a redelivery repeats them, and any edit
// to what was attached changes them.
func digestWSAttachments(attachments []json.RawMessage) string {
	if len(attachments) == 0 {
		return ""
	}
	sum := sha256.New()
	for _, attachment := range attachments {
		sum.Write(attachment)
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// encode renders the submission for fingerprinting. A marshal error cannot come
// from these field types, so it collapses to a payload that still differs per
// invocation rather than to an admission that silently matches everything.
func (s wsSubmission) encode() []byte {
	payload, err := json.Marshal(s)
	if err != nil {
		return []byte(s.Kind + "\x00" + s.SessionID + "\x00" + s.Text + "\x00" + s.TurnID)
	}
	return payload
}

// wsRunAdmission is the outcome of naming one WebSocket turn.
type wsRunAdmission struct {
	RunID string
	// TurnID and TurnPosition are the turn admission allocated. They are carried
	// to the runner so the persisted user turn lands under the id the client was
	// given, rather than a second one minted by the history layer.
	TurnID       string
	TurnPosition *int64
	// Accepted is what the client is told once the run is about to execute. It
	// is carried from admission rather than rebuilt at the send site because the
	// turn id and the cursor exist only there: the ledger decides the first and
	// the live reservation decides the second.
	Accepted wsRunAcceptance
	// Handle is the durable ownership this process holds, carrying the fencing
	// token its terminal write must present.
	Handle sessionruntime.RunHandle
	// Execute is false when the run already exists under another owner or has
	// already finished. The client has been told which run its invocation
	// resolved to, and a second execution of it is the duplication admission
	// exists to prevent.
	Execute bool
}

// admitWSTurn turns a submission into a run this process may execute.
func (h *LocalChannelHandler) admitWSTurn(ctx context.Context, writer *wsWriter, botID string, ref wsTurnRef, submission []byte, admissionBuilder wsRunAdmissionBuilder, abortCh chan struct{}, cancel context.CancelFunc, ownershipCancel context.CancelCauseFunc) (wsRunAdmission, bool) {
	if h.sessionRuntime == nil {
		sendWSError(writer, ref, "session runtime is not configured")
		return wsRunAdmission{}, false
	}
	if admissionBuilder == nil {
		admissionBuilder = func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
			return sessionruntime.RunAdmissionView{}, nil
		}
	}
	admission, err := h.sessionRuntime.Admit(ctx, sessionruntime.AdmitInput{
		BotID:        botID,
		SessionID:    ref.SessionID,
		InvocationID: ref.InvocationID,
		Payload:      submission,
		Execution: sessionruntime.Execution{
			Admission: admissionBuilder,
			AbortCh:   abortCh,
			Cancel:    cancel,
			// Revoking ownership cancels the run with a cause, which is what lets
			// the execution below tell "this run was stopped" apart from "this run
			// was taken away". Only the second must leave no durable trace.
			OwnershipCancel: ownershipCancel,
		},
	})
	if err != nil {
		// ErrSessionBusy and ErrInvocationConflict arrive here as themselves and
		// leave as run_rejected with the stable code the client branches on.
		sendWSErrorFromError(writer, ref, err)
		return wsRunAdmission{}, false
	}
	if !admission.Started {
		// The run exists and its turn is already named, but this call reserved
		// nothing, so there is no cursor to report: where the session stands now
		// is what a subscription's snapshot answers.
		sendWSRunAccepted(writer, ref.withRun(admission.RunID), wsRunAcceptance{TurnID: admission.TurnID, Duplicate: true})
		return wsRunAdmission{RunID: admission.RunID}, true
	}
	return wsRunAdmission{
		RunID:        admission.RunID,
		TurnID:       admission.TurnID,
		TurnPosition: &admission.TurnPosition,
		Accepted:     wsRunAcceptance{TurnID: admission.TurnID, Cursor: admission.Cursor},
		Handle:       admission.Handle,
		Execute:      true,
	}, true
}

// finishWSRun writes the run's terminal state under the token this process was
// admitted with. It is the release of the session's single active slot: without
// it the durable row stays active and the next submission is told the session is
// busy until the reaper times the lease out.
//
// Only the stable error code reaches the run's published state. The underlying
// error is a private diagnostic and stays in the log.
func (h *LocalChannelHandler) finishWSRun(ctx context.Context, admission wsRunAdmission, runErr error) {
	if h.sessionRuntime == nil || admission.Handle.FencingToken <= 0 {
		return
	}
	// This process reports only what it actually knows, which is whether the run
	// failed: it holds the error. It does not know why an execution it was told
	// to stop was stopped. Every abort now arrives as a routed control, which
	// unblocks the runner and cancels its context, so the owner sees either a
	// clean return or a bare cancellation and neither says "aborted".
	//
	// Anything that is not a failure is therefore left unnamed, and the manager
	// resolves it from the intent already recorded against the run. Reporting a
	// clean return as `completed` is what made a routed abort finalize as a
	// successful turn (SR-CTL-001); a cancellation reported as `errored` blames
	// the symptom.
	status, message := "", ""
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		status = sessionruntime.RunStatusErrored
		message = string(apperror.CodeOf(runErr))
	}
	switch err := h.sessionRuntime.FinishRun(ctx, admission.Handle, status, message); {
	case err == nil:
		if h.agentService != nil && runErr != nil && !errors.Is(runErr, context.Canceled) {
			h.agentService.EnsureTerminalContextLifecycle(
				ctx,
				admission.RunID,
				admission.Handle.BotID,
				admission.Handle.SessionID,
				runErr,
			)
		}
	case errors.Is(err, sessionruntime.ErrRunOwnershipLost):
		// Expected, not a failure: this process was superseded mid-run, so the
		// terminal write was refused and the reaper names the outcome instead.
		h.logger.Warn("skip finishing runtime run after ownership loss",
			slog.String("run_id", admission.RunID))
	default:
		h.logger.Error("finish runtime run failed",
			slog.Any("error", err),
			slog.String("run_id", admission.RunID),
			slog.String("status", status))
	}
}

// abortWSRun stops a run the client named.
//
// It routes through the session runtime, which is the only way that works: a
// run belongs to a session, not to a socket, so a client that reconnected — in
// a cluster, routinely onto a server that has never heard of the run — has no
// local execution to cancel. The manager knows which process owns the run and
// can reach it (SR-CTL-001).
//
// Routing is also what decides the run's terminal state. The manager records
// the abort intent and moves the run to `aborting` before anything is
// cancelled, so the cancellation that follows is already explained; a local
// cancel would race that write and let the run finish as `completed`.
func (h *LocalChannelHandler) abortWSRun(ctx context.Context, writer *wsWriter, botID string, msg wsClientMessage, runID string) {
	sessionID := strings.TrimSpace(msg.SessionID)
	controlID := strings.TrimSpace(msg.ControlID)
	ref := wsTurn(msg.InvocationID, sessionID).withRun(runID)

	controller := h.sessionRuntimeController()
	if sessionID == "" || (h.agentService == nil && controller == nil) {
		if controlID != "" {
			sendWSControlAck(writer, ref, "abort", controlID, false, "")
		}
		return
	}
	var (
		applied bool
		err     error
	)
	if h.agentService != nil {
		applied, err = h.agentService.AbortRuntimeRun(ctx, botID, sessionID, runID, controlID)
	} else {
		applied, err = controller.AbortControl(ctx, botID, sessionID, runID, controlID)
	}
	if err != nil {
		h.logger.Warn("route ws abort failed",
			slog.Any("error", err),
			slog.String("bot_id", botID),
			slog.String("run_id", runID),
			slog.String("session_id", sessionID))
		if controlID != "" {
			sendWSControlAck(writer, ref, "abort", controlID, false, string(apperror.CodeOf(err)))
		}
		return
	}
	if controlID != "" {
		sendWSControlAck(writer, ref, "abort", controlID, applied, "")
	}
}

// startWSStream is where a submission becomes a run: admission names the run,
// the server publishes that name, and only then does anything execute. The id
// comes from the ledger rather than from the client, which is what makes run
// identity unforgeable and lets a second subscriber address the same run.
//
// submission is the canonical bytes that identify what was sent. The ledger
// arbitrates every new turn: a repeated invocation resolves to its original
// run, a session that is already running one rejects this, and the winner
// receives a fencing token its durable writes carry. Decision responses do not
// enter this path; they route to the existing run's owner.
//
// The returned ref carries the run id so callers describing the same turn — a
// persisted user message, a late error — name it the way the client will.
func (h *LocalChannelHandler) startWSStream(baseCtx, connCtx context.Context, writer *wsWriter, botID string, ref wsTurnRef, logLabel string, submission []byte, admissionBuilder wsRunAdmissionBuilder, onFinish func(), runner wsStreamRunner) (wsTurnRef, bool) {
	// Cause-carrying so a lost owner lease is distinguishable downstream from an
	// ordinary stop: both cancel this context, and only one of them means this
	// process may no longer write for the run.
	streamCtx, streamCancelCause := context.WithCancelCause(baseCtx)
	streamCancel := func() { streamCancelCause(context.Canceled) }
	abortCh := make(chan struct{}, 1)

	admission, ok := h.admitWSTurn(streamCtx, writer, botID, ref, submission, admissionBuilder, abortCh, streamCancel, streamCancelCause)
	if !ok {
		streamCancel()
		if onFinish != nil {
			onFinish()
		}
		return ref, false
	}
	ref = ref.withRun(admission.RunID)
	if !admission.Execute {
		streamCancel()
		if onFinish != nil {
			onFinish()
		}
		return ref, false
	}
	if admission.Handle.FencingToken > 0 {
		streamCtx = runtimefence.WithContext(streamCtx, runtimefence.Fence{
			BotID:     admission.Handle.BotID,
			SessionID: admission.Handle.SessionID,
			Token:     admission.Handle.FencingToken,
		})
	}

	sendWSRunAccepted(writer, ref, admission.Accepted)

	eventCh := make(chan application.WSStreamEvent, 64)
	forwarded := make(chan struct{})
	releaseCompaction := h.agentService.DeferSessionCompaction(botID, ref.SessionID, ref.RunID)
	go func() {
		defer streamCancel()
		err := func() error {
			if onFinish != nil {
				defer onFinish()
			}
			defer close(eventCh)
			return runner(streamCtx, ref, wsAdmittedTurn{TurnID: admission.TurnID, Position: admission.TurnPosition}, eventCh, abortCh)
		}()
		// Every event this run produced has to be published before the run is
		// declared finished, or a subscriber is shown the terminal state and then
		// handed output that supposedly preceded it. The forwarder cannot outlive
		// its channel — the runner closed it above — and its writes stop when the
		// writer closes, so this waits on a drain that is already bounded.
		<-forwarded
		// The terminal write outlives the connection: a client that disappears
		// mid-turn must still free the session, and baseCtx is already gone by
		// the time an aborted run returns.
		h.finishWSRun(context.WithoutCancel(baseCtx), admission, err)
		if err != nil && connCtx.Err() == nil {
			privateErr := err
			if cause := apperror.CauseOf(err); cause != nil {
				privateErr = cause
			}
			h.logger.Error("ws stream error",
				slog.String("operation", logLabel),
				slog.String("error_code", string(apperror.CodeOf(err))),
				slog.Any("error", privateErr),
				slog.String("bot_id", botID),
				slog.String("run_id", ref.RunID),
				slog.String("session_id", ref.SessionID))
			sendWSErrorFromError(writer, ref, err)
		}
	}()

	go func() {
		defer close(forwarded)
		defer releaseCompaction()
		h.forwardWSStreamEvents(streamCtx, baseCtx, writer, botID, ref, admission.Handle, eventCh)
	}()
	return ref, true
}

// HandleWebSocket godoc
// @Summary WebSocket chat endpoint
// @Description Upgrade to WebSocket for bidirectional chat streaming with abort support.
// @Tags local-channel
// @Param bot_id path string true "Bot ID"
// @Success 101 {string} string "Switching Protocols"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/web/ws [get].
func (h *LocalChannelHandler) HandleWebSocket(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	_, perms, err := h.authorizeBotSessionAccess(c.Request().Context(), channelIdentityID, botID)
	if err != nil {
		return err
	}
	if !canOpenLocalWebSocket(perms) {
		return echo.NewHTTPError(http.StatusForbidden, "bot access denied")
	}
	if h.agentService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "resolver not configured")
	}

	conn, err := wsUpgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	rawToken := extractRawBearerToken(c)
	bearerToken := "Bearer " + rawToken

	writer := newWSWriter(conn)
	defer writer.Close()

	connCtx, connCancel := context.WithCancel(context.Background())
	defer connCancel()
	streamBaseCtx := context.WithoutCancel(c.Request().Context())

	// Subscriptions observe sessions; they never own the runs behind them, so
	// closing them on disconnect leaves a run executing for its other
	// subscribers (SR-OBS-003).
	subscriptions := newRuntimeSubscriptions(h.sessionRuntimeObserver(), writer, h.logger)
	defer subscriptions.close()
	authorizeSubscription := func(ctx context.Context, sessionID string) error {
		return h.authorizeWSSession(ctx, channelIdentityID, botID, sessionID)
	}

	for {
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			connCancel()
			h.logger.Debug("ws disconnected; active stream can finish in background",
				slog.String("bot_id", botID),
				slog.Any("error", readErr),
			)
			break
		}
		var msg wsClientMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			h.logger.Warn("ws: unmarshal failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
			writer.SendJSON(map[string]string{"type": "error", "message": "invalid message format"})
			continue
		}

		if subscriptions.handle(streamBaseCtx, botID, runtimeClientMessage{
			Type:      msg.Type,
			SessionID: msg.SessionID,
			Cursor:    msg.Cursor,
		}, authorizeSubscription) {
			continue
		}

		switch msg.Type {
		case "abort":
			// Abort is the one inbound message that addresses existing work, so
			// it is the one that takes a run_id.
			runID := strings.TrimSpace(msg.RunID)
			if runID == "" {
				sendWSError(writer, wsTurn("", msg.SessionID), "run_id is required")
				continue
			}
			h.abortWSRun(streamBaseCtx, writer, botID, msg, runID)

		case "tool_approval_response":
			sessionID := strings.TrimSpace(msg.SessionID)
			runID := strings.TrimSpace(msg.RunID)
			ref := wsTurn("", sessionID).withRun(runID)
			if sessionID == "" {
				sendWSError(writer, ref, "session_id is required")
				continue
			}
			if runID == "" {
				sendWSError(writer, ref, "run_id is required")
				continue
			}
			decisionID := strings.TrimSpace(msg.DecisionID)
			if decisionID == "" {
				sendWSError(writer, ref, "decision_id is required")
				continue
			}
			controlID := strings.TrimSpace(msg.ControlID)
			if controlID == "" {
				sendWSError(writer, ref, "control_id is required")
				continue
			}
			if err := h.authorizeWSSession(c.Request().Context(), channelIdentityID, botID, sessionID); err != nil {
				sendWSControlAck(writer, ref, msg.Type, controlID, false, string(wsSessionAuthAckCode(err, apperror.CodeToolApprovalForbidden, apperror.CodeToolApprovalOperationFailed)))
				continue
			}
			controller := h.sessionRuntimeController()
			if controller == nil {
				sendWSControlAck(writer, ref, msg.Type, controlID, false, string(apperror.CodeToolApprovalOperationFailed))
				continue
			}
			payload, err := json.Marshal(application.ToolApprovalResponseInput{
				ControlID:                  controlID,
				BotID:                      botID,
				ThreadID:                   sessionID,
				ActorChannelIdentityID:     channelIdentityID,
				ActorUserID:                channelIdentityID,
				ApprovalID:                 decisionID,
				ExplicitID:                 decisionID,
				Decision:                   strings.TrimSpace(msg.Decision),
				OptionID:                   msg.OptionID,
				Reason:                     strings.TrimSpace(msg.Reason),
				SuppressActivePromptAttach: true,
			})
			if err != nil {
				h.logger.Warn("encode ws tool approval response failed", slog.Any("error", err))
				sendWSControlAck(writer, ref, msg.Type, controlID, false, string(apperror.CodeToolApprovalOperationFailed))
				continue
			}
			result, err := controller.RouteDecisionResponse(streamBaseCtx, sessionruntime.DecisionResponse{
				ControlID: controlID, Type: sessionruntime.CommandToolApprovalResponse,
				DecisionID: decisionID, BotID: botID, SessionID: sessionID, RunID: runID,
				Payload: payload,
			})
			code := ""
			if err != nil {
				code = string(apperror.CodeOf(toolApprovalHTTPError(err)))
				h.logger.Warn("route ws tool approval response failed",
					slog.Any("error", err),
					slog.String("bot_id", botID),
					slog.String("run_id", runID),
					slog.String("session_id", sessionID),
					slog.String("decision_id", decisionID))
			}
			sendWSControlAck(writer, ref, msg.Type, controlID, result.Applied && err == nil, code)

		case "user_input_response":
			sessionID := strings.TrimSpace(msg.SessionID)
			runID := strings.TrimSpace(msg.RunID)
			ref := wsTurn("", sessionID).withRun(runID)
			if sessionID == "" {
				sendWSError(writer, ref, "session_id is required")
				continue
			}
			if runID == "" {
				sendWSError(writer, ref, "run_id is required")
				continue
			}
			decisionID := strings.TrimSpace(msg.DecisionID)
			if decisionID == "" {
				sendWSError(writer, ref, "decision_id is required")
				continue
			}
			controlID := strings.TrimSpace(msg.ControlID)
			if controlID == "" {
				sendWSError(writer, ref, "control_id is required")
				continue
			}
			if err := h.authorizeWSSession(c.Request().Context(), channelIdentityID, botID, sessionID); err != nil {
				sendWSControlAck(writer, ref, msg.Type, controlID, false, string(wsSessionAuthAckCode(err, apperror.CodeUserInputForbidden, apperror.CodeUserInputOperationFailed)))
				continue
			}
			controller := h.sessionRuntimeController()
			if controller == nil {
				sendWSControlAck(writer, ref, msg.Type, controlID, false, string(apperror.CodeUserInputOperationFailed))
				continue
			}
			payload, err := json.Marshal(application.UserInputResponseInput{
				ControlID:                  controlID,
				BotID:                      botID,
				ThreadID:                   sessionID,
				ActorChannelIdentityID:     channelIdentityID,
				ActorUserID:                channelIdentityID,
				UserInputID:                decisionID,
				ExplicitID:                 decisionID,
				Answers:                    msg.Answers,
				Canceled:                   msg.Canceled,
				Reason:                     strings.TrimSpace(msg.Reason),
				SuppressActivePromptAttach: true,
			})
			if err != nil {
				h.logger.Warn("encode ws user input response failed", slog.Any("error", err))
				sendWSControlAck(writer, ref, msg.Type, controlID, false, string(apperror.CodeUserInputOperationFailed))
				continue
			}
			result, err := controller.RouteDecisionResponse(streamBaseCtx, sessionruntime.DecisionResponse{
				ControlID: controlID, Type: sessionruntime.CommandUserInputResponse,
				DecisionID: decisionID, BotID: botID, SessionID: sessionID, RunID: runID,
				Payload: payload,
			})
			code := ""
			if err != nil {
				code = string(apperror.CodeOf(userInputResponseAppError(err)))
				h.logger.Warn("route ws user input response failed",
					slog.Any("error", err),
					slog.String("bot_id", botID),
					slog.String("run_id", runID),
					slog.String("session_id", sessionID),
					slog.String("decision_id", decisionID))
			}
			sendWSControlAck(writer, ref, msg.Type, controlID, result.Applied && err == nil, code)

		case "message":
			text := strings.TrimSpace(msg.Text)
			sessionID := strings.TrimSpace(msg.SessionID)
			ref := wsTurn(msg.InvocationID, sessionID)
			workspaceTargetID := strings.TrimSpace(msg.WorkspaceTargetID)

			if ref.InvocationID == "" {
				sendWSError(writer, ref, "invocation_id is required")
				continue
			}
			if sessionID != "" {
				if err := h.authorizeWSSession(c.Request().Context(), channelIdentityID, botID, sessionID); err != nil {
					sendWSError(writer, ref, wsErrorMessage(err))
					continue
				}
			}
			if err := authorizeWorkspaceTargetSelection(perms, workspaceTargetID); err != nil {
				sendWSError(writer, ref, wsErrorMessage(err))
				continue
			}
			if workspaceTargetID != "" {
				if err := h.agentService.ValidateWorkspaceTarget(streamBaseCtx, botID, workspaceTargetID); err != nil {
					sendWSError(writer, ref, err.Error())
					continue
				}
			}

			hasRequestedSkills := len(msg.RequestedSkills) > 0
			if hasRequestedSkills && strings.HasPrefix(text, "/") {
				sendWSCommandError(writer, msg, slash.CodeInvalidSkillSlashSyntax)
				continue
			}
			decision := h.classifyWebSlashForSession(streamBaseCtx, text, len(msg.Attachments) > 0, sessionID)
			var pendingSkillIntent *slash.SkillIntent
			switch decision.Kind {
			case slash.DecisionNormalChat:
			case slash.DecisionCommandAction:
				if len(msg.Attachments) > 0 {
					// Server-side twin of the Web composer guard: a quick action
					// consumes only the command text, so accepting the message
					// would silently drop the files.
					sendWSCommandError(writer, msg, slash.CodeSlashAttachmentsUnsupported)
					continue
				}
				actionID := webActionID(decision.Command.Resource, decision.Command.Action)
				permissionAction := actionID == "permission"
				if permissionAction {
					// The permission action targets a live ACP session, so enforce
					// session visibility with the same check the WS approval path
					// uses. An empty session falls through so the executor returns
					// its session-required command error.
					if strings.TrimSpace(sessionID) != "" {
						if err := h.authorizeWSSession(streamBaseCtx, channelIdentityID, botID, sessionID); err != nil {
							sendWSError(writer, ref, wsErrorMessage(err))
							continue
						}
					}
				} else {
					if err := h.authorizeWSChatAccess(streamBaseCtx, channelIdentityID, botID); err != nil {
						sendWSError(writer, ref, wsErrorMessage(err))
						continue
					}
				}
				skillActivationAllowed := true
				if strings.TrimSpace(sessionID) != "" && !permissionAction {
					supported, supportErr := h.wsSessionSupportsRequestedSkills(streamBaseCtx, sessionID)
					if supportErr != nil {
						sendWSErrorFromError(writer, ref, supportErr)
						continue
					}
					skillActivationAllowed = supported
				}
				control := webQuickActionContext{
					SessionID: sessionID,
					ActorID:   channelIdentityID,
					ToolURL:   buildACPMCPToolsURL(c, botID),
				}
				if decision.Invocation != nil {
					control.ModeID = decision.Invocation.Rest
				}
				result, slashErr := h.executeWebQuickAction(streamBaseCtx, botID, actionID, skillActivationAllowed, control)
				if slashErr != nil {
					sendWSCommandError(writer, msg, slashErr.Code)
				} else {
					sendWSCommandResult(writer, msg, actionID, result)
				}
				continue
			case slash.DecisionSkillIntent:
				intent := decision.SkillIntent
				pendingSkillIntent = &intent
			case slash.DecisionUnsupportedCommand, slash.DecisionUnknownSlash, slash.DecisionReject:
				code := decision.Code
				if code == "" {
					code = slash.CodeUnknownSlash
				}
				sendWSCommandError(writer, msg, code)
				continue
			default:
				sendWSCommandError(writer, msg, slash.CodeUnknownSlash)
				continue
			}

			hasSkillActivation := hasRequestedSkills || pendingSkillIntent != nil
			if text == "" && len(msg.Attachments) == 0 && !hasSkillActivation {
				sendWSError(writer, ref, "message text or attachments required")
				continue
			}
			if sessionID == "" || hasSkillActivation {
				if err := h.authorizeWSChatAccess(streamBaseCtx, channelIdentityID, botID); err != nil {
					sendWSError(writer, ref, wsErrorMessage(err))
					continue
				}
			}
			chatAttachments, attachmentErr := parseWSClientAttachments(msg.Attachments)
			if attachmentErr != nil {
				code := slashErrorCode(attachmentErr)
				if code == "" {
					code = slash.CodeReservedSkillMetadata
				}
				sendWSCommandError(writer, msg, code)
				continue
			}

			var requestedSkillContexts []turn.RequestedSkillContext
			var skillActivation *turn.SkillActivation
			userMessageKind := ""
			userVisibleText := text
			streamText := text
			streamModelText := text
			var err error
			sessionAuthorized := false
			var releaseActiveWSTurn func()
			releaseActiveWSTurnNow := func() {
				if releaseActiveWSTurn != nil {
					releaseActiveWSTurn()
					releaseActiveWSTurn = nil
				}
			}
			if sessionID != "" {
				sessionAuthorized = true
				if hasSkillActivation {
					supported, supportErr := h.wsSessionSupportsRequestedSkills(streamBaseCtx, sessionID)
					if supportErr != nil {
						sendWSErrorFromError(writer, ref, supportErr)
						continue
					}
					if !supported {
						sendWSCommandError(writer, msg, slash.CodeUnsupportedSkillSlashContext)
						continue
					}
					var reserved bool
					releaseActiveWSTurn, reserved = h.reserveWSRequestedSkillTurn(botID, sessionID, ref.InvocationID)
					if !reserved {
						sendWSCommandError(writer, msg, slash.CodeUnsupportedSkillSlashContext)
						continue
					}
				} else {
					releaseActiveWSTurn = h.enterWSMessageTurn(botID, sessionID, ref.InvocationID)
				}
			}

			if hasRequestedSkills {
				requestedSkillContexts, err = h.resolveWebRequestedSkillContexts(streamBaseCtx, botID, msg.RequestedSkills)
				if err != nil {
					code := slashErrorCode(err)
					if code == "" {
						code = slash.CodeUnsupportedSkillSlashContext
					}
					sendWSCommandError(writer, msg, code)
					releaseActiveWSTurnNow()
					continue
				}
			}
			if pendingSkillIntent != nil {
				requestedSkillContexts, err = h.resolveWebTextRequestedSkillContexts(streamBaseCtx, botID, pendingSkillIntent.Names)
				if err != nil {
					code := slashErrorCode(err)
					if code == "" {
						code = slash.CodeUnsupportedSkillSlashContext
					}
					sendWSCommandError(writer, msg, code)
					releaseActiveWSTurnNow()
					continue
				}
			}
			if hasSkillActivation {
				prompt := text
				if pendingSkillIntent != nil {
					prompt = pendingSkillIntent.Prompt
				}
				skillActivation = turn.NewSkillActivation(requestedSkillContexts, prompt)
				streamText = strings.TrimSpace(prompt)
				streamModelText = strings.TrimSpace(turn.SkillActivationModelQuery(skillActivation))
				userMessageKind = turn.UserMessageKindSkillActivation
				userVisibleText = strings.TrimSpace(prompt)
			}

			if sessionID == "" {
				if h.sessionService == nil {
					sendWSError(writer, ref, "session service not configured")
					releaseActiveWSTurnNow()
					continue
				}
				created, createErr := h.createWSChatSession(streamBaseCtx, botID, channelIdentityID)
				if createErr != nil {
					sendWSError(writer, ref, createErr.Error())
					releaseActiveWSTurnNow()
					continue
				}
				sessionID = created.ID
				msg.SessionID = sessionID
				ref = ref.withSession(sessionID)
				if hasSkillActivation {
					var reserved bool
					releaseActiveWSTurn, reserved = h.reserveWSRequestedSkillTurn(botID, sessionID, ref.InvocationID)
					if !reserved {
						sendWSCommandError(writer, msg, slash.CodeUnsupportedSkillSlashContext)
						continue
					}
				} else {
					releaseActiveWSTurn = h.enterWSMessageTurn(botID, sessionID, ref.InvocationID)
				}
				// The session exists before the run does, so this is announced
				// under the client's own name for the turn.
				writer.SendJSON(ref.event("session_created"))
			}
			if !sessionAuthorized {
				if err := h.authorizeWSSession(c.Request().Context(), channelIdentityID, botID, sessionID); err != nil {
					sendWSError(writer, ref, wsErrorMessage(err))
					releaseActiveWSTurnNow()
					continue
				}
			}
			acpInfo, err := h.authorizeWSACPExecution(c.Request().Context(), channelIdentityID, botID, sessionID)
			if err != nil {
				sendWSErrorFromError(writer, ref, err)
				releaseActiveWSTurnNow()
				continue
			}
			if acpInfo.IsACP && len(requestedSkillContexts) > 0 {
				sendWSCommandError(writer, msg, slash.CodeUnsupportedSkillSlashContext)
				releaseActiveWSTurnNow()
				continue
			}
			streamToken := bearerToken
			if acpInfo.IsACP {
				streamToken = h.issueRuntimeOwnerBearerToken(acpInfo.RuntimeOwnerAccountID, bearerToken)
			}
			var ingestedActivationAttachments []turn.Attachment
			userMessagePersisted := false
			persistedUserMessageID := ""
			externalMessageID := ""
			var preparedActivationReq *application.ChatRequest
			if hasSkillActivation {
				// The persisted user turn is deduplicated by the client's
				// invocation id: it exists before any run does, and the
				// invocation is precisely the key a redelivery would repeat.
				externalMessageID = ref.InvocationID
				ingestedActivationAttachments = h.ingestWSInboundAttachments(streamBaseCtx, botID, chatAttachments)
				userReq := application.ChatRequest{
					BotID:                   botID,
					ChatID:                  botID,
					ThreadID:                sessionID,
					UserID:                  channelIdentityID,
					SourceChannelIdentityID: channelIdentityID,
					ExternalMessageID:       externalMessageID,
					ConversationType:        channel.ConversationTypePrivate,
					Query:                   streamText,
					ModelQuery:              streamModelText,
					RawQuery:                userVisibleText,
					UserMessageKind:         userMessageKind,
					UserVisibleText:         userVisibleText,
					SkillActivation:         skillActivation,
					CurrentChannel:          h.channelType.String(),
					ReplyTarget:             botID,
					Attachments:             ingestedActivationAttachments,
					RequestedSkills:         requestedSkillContexts,
					WorkspaceTargetID:       workspaceTargetID,
				}
				preparedReq, persisted, persistErr := h.agentService.ApplyUserMessageHookAndPersistUserTurn(streamBaseCtx, userReq)
				if persistErr != nil {
					sendWSErrorFromError(writer, ref, persistErr)
					releaseActiveWSTurnNow()
					continue
				}
				preparedActivationReq = &preparedReq
				persistedUserMessageID = persisted.ID
				userMessagePersisted = true
			}
			submission := wsSubmission{
				Kind:        "message",
				SessionID:   sessionID,
				Text:        userVisibleText,
				Attachments: digestWSAttachments(msg.Attachments),
			}.encode()
			messageAdmission := &wsMessageAdmission{
				handler: h,
				botID:   botID,
				request: chatview.UITurn{
					Role:              "user",
					Text:              userVisibleText,
					UserMessageKind:   userMessageKind,
					SkillActivation:   skillActivation,
					Timestamp:         time.Now().UTC(),
					Platform:          h.channelType.String(),
					SenderUserID:      channelIdentityID,
					ExternalMessageID: ref.InvocationID,
				},
				attachments: chatAttachments,
			}
			if hasSkillActivation {
				messageAdmission.attachments = ingestedActivationAttachments
				messageAdmission.attachmentsPrepared = true
			}
			h.startWSStream(streamBaseCtx, connCtx, writer, botID, ref, "ws stream error", submission, messageAdmission.build, releaseActiveWSTurn,
				func(ctx context.Context, runRef wsTurnRef, admittedTurn wsAdmittedTurn, eventCh chan<- application.WSStreamEvent, abortCh <-chan struct{}) error {
					req := application.ChatRequest{
						BotID:                   botID,
						ChatID:                  botID,
						ThreadID:                sessionID,
						RunID:                   runRef.RunID,
						TurnID:                  admittedTurn.TurnID,
						TurnPosition:            admittedTurn.Position,
						UserID:                  channelIdentityID,
						SourceChannelIdentityID: channelIdentityID,
						ExternalMessageID:       externalMessageID,
						ConversationType:        channel.ConversationTypePrivate,
						Query:                   streamText,
						ModelQuery:              streamModelText,
						RawQuery:                userVisibleText,
						UserMessageKind:         userMessageKind,
						UserVisibleText:         userVisibleText,
						SkillActivation:         skillActivation,
						UserMessagePersisted:    userMessagePersisted,
						PersistedUserMessageID:  persistedUserMessageID,
						Token:                   streamToken,
						ChatToken:               bearerToken,
						CurrentChannel:          h.channelType.String(),
						ReplyTarget:             botID,
						Channels:                []string{h.channelType.String()},
						Attachments:             messageAdmission.preparedAttachments(),
						RequestedSkills:         requestedSkillContexts,
						SkipMemoryExtraction:    hasSkillActivation && userVisibleText == "",
						SkipTitleGeneration:     hasSkillActivation && userVisibleText == "",
						Model:                   strings.TrimSpace(msg.ModelID),
						ReasoningEffort:         strings.TrimSpace(msg.ReasoningEffort),
						WorkspaceTargetID:       workspaceTargetID,
						ToolHTTPURL:             buildACPMCPToolsURL(c, botID),
						AgentCommand:            decision.AgentCommand,
					}
					if preparedActivationReq != nil {
						req.Messages = preparedActivationReq.Messages
						req.Query = preparedActivationReq.Query
						req.ModelQuery = preparedActivationReq.ModelQuery
						req.RawQuery = preparedActivationReq.RawQuery
						req.UserVisibleText = preparedActivationReq.UserVisibleText
						req.SkillActivation = preparedActivationReq.SkillActivation
						req.RequestedSkills = preparedActivationReq.RequestedSkills
						req.PersistedUserMessageID = preparedActivationReq.PersistedUserMessageID
						req.WorkspaceTargetID = preparedActivationReq.WorkspaceTargetID
						req.WorkspaceTarget = preparedActivationReq.WorkspaceTarget
					}
					return h.agentService.StreamChatWS(ctx, req, eventCh, abortCh)
				},
			)

		case "retry_message":
			sessionID := strings.TrimSpace(msg.SessionID)
			ref := wsTurn(msg.InvocationID, sessionID)
			targetTurnID := strings.TrimSpace(msg.TurnID)
			workspaceTargetID := strings.TrimSpace(msg.WorkspaceTargetID)
			if ref.InvocationID == "" {
				sendWSError(writer, ref, "invocation_id is required")
				continue
			}
			if sessionID == "" {
				sendWSError(writer, ref, "session_id is required")
				continue
			}
			if err := h.authorizeWSSession(c.Request().Context(), channelIdentityID, botID, sessionID); err != nil {
				sendWSError(writer, ref, wsErrorMessage(err))
				continue
			}
			if err := authorizeWorkspaceTargetSelection(perms, workspaceTargetID); err != nil {
				sendWSError(writer, ref, wsErrorMessage(err))
				continue
			}
			if workspaceTargetID != "" {
				if err := h.agentService.ValidateWorkspaceTarget(streamBaseCtx, botID, workspaceTargetID); err != nil {
					sendWSError(writer, ref, err.Error())
					continue
				}
			}
			targetTurnID, resolveErr := h.resolveWSTargetTurnID(c.Request().Context(), sessionID, targetTurnID, msg.MessageID)
			if resolveErr != nil {
				sendWSError(writer, ref, resolveErr.Error())
				continue
			}

			retrySubmission := wsSubmission{
				Kind:      "retry_message",
				SessionID: sessionID,
				TurnID:    targetTurnID,
			}.encode()
			retryInput := application.RetryLatestMessageInput{
				BotID:                  botID,
				SessionID:              sessionID,
				TargetTurnID:           targetTurnID,
				ActorChannelIdentityID: channelIdentityID,
				ActorUserID:            channelIdentityID,
				ChatToken:              bearerToken,
				Model:                  strings.TrimSpace(msg.ModelID),
				ReasoningEffort:        strings.TrimSpace(msg.ReasoningEffort),
				WorkspaceTargetID:      workspaceTargetID,
				ToolHTTPURL:            buildACPMCPToolsURL(c, botID),
			}
			retryAdmission := &wsReplacementAdmission{
				kind: sessionruntime.RunOperationRetry,
				prepareAnchor: func(ctx context.Context) (string, error) {
					return h.agentService.PrepareRetryLatestTurnOperation(ctx, sessionID, targetTurnID)
				},
			}
			h.startWSStream(streamBaseCtx, connCtx, writer, botID, ref, "ws retry stream error", retrySubmission, retryAdmission.build, nil,
				func(ctx context.Context, runRef wsTurnRef, admittedTurn wsAdmittedTurn, eventCh chan<- application.WSStreamEvent, abortCh <-chan struct{}) error {
					input := retryInput
					input.RunID = runRef.RunID
					input.TurnID = admittedTurn.TurnID
					input.TurnPosition = admittedTurn.Position
					return h.agentService.RetryLatestMessageWS(ctx, input, eventCh, abortCh)
				},
			)

		case "edit_message":
			text := strings.TrimSpace(msg.Text)
			sessionID := strings.TrimSpace(msg.SessionID)
			ref := wsTurn(msg.InvocationID, sessionID)
			targetTurnID := strings.TrimSpace(msg.TurnID)
			workspaceTargetID := strings.TrimSpace(msg.WorkspaceTargetID)
			if ref.InvocationID == "" {
				sendWSError(writer, ref, "invocation_id is required")
				continue
			}
			if sessionID == "" {
				sendWSError(writer, ref, "session_id is required")
				continue
			}
			chatAttachments, attachmentErr := parseWSClientAttachments(msg.Attachments)
			if attachmentErr != nil {
				code := slashErrorCode(attachmentErr)
				if code == "" {
					code = slash.CodeReservedSkillMetadata
				}
				sendWSCommandError(writer, msg, code)
				continue
			}
			if text == "" && len(chatAttachments) == 0 {
				sendWSError(writer, ref, "message text or attachments required")
				continue
			}
			if err := h.authorizeWSSession(c.Request().Context(), channelIdentityID, botID, sessionID); err != nil {
				sendWSError(writer, ref, wsErrorMessage(err))
				continue
			}
			if err := authorizeWorkspaceTargetSelection(perms, workspaceTargetID); err != nil {
				sendWSError(writer, ref, wsErrorMessage(err))
				continue
			}
			if workspaceTargetID != "" {
				if err := h.agentService.ValidateWorkspaceTarget(streamBaseCtx, botID, workspaceTargetID); err != nil {
					sendWSError(writer, ref, err.Error())
					continue
				}
			}
			targetTurnID, resolveErr := h.resolveWSTargetTurnID(c.Request().Context(), sessionID, targetTurnID, msg.MessageID)
			if resolveErr != nil {
				sendWSError(writer, ref, resolveErr.Error())
				continue
			}

			editSubmission := wsSubmission{
				Kind:        "edit_message",
				SessionID:   sessionID,
				Text:        text,
				TurnID:      targetTurnID,
				Attachments: digestWSAttachments(msg.Attachments),
			}.encode()
			editInput := application.EditLatestMessageInput{
				BotID:                  botID,
				SessionID:              sessionID,
				TargetTurnID:           targetTurnID,
				Text:                   text,
				ActorChannelIdentityID: channelIdentityID,
				ActorUserID:            channelIdentityID,
				ChatToken:              bearerToken,
				Model:                  strings.TrimSpace(msg.ModelID),
				ReasoningEffort:        strings.TrimSpace(msg.ReasoningEffort),
				WorkspaceTargetID:      workspaceTargetID,
				ToolHTTPURL:            buildACPMCPToolsURL(c, botID),
			}
			editAdmission := &wsReplacementAdmission{
				handler: h,
				botID:   botID,
				kind:    sessionruntime.RunOperationEdit,
				prepareAnchor: func(ctx context.Context) (string, error) {
					return h.agentService.PrepareEditLatestTurnOperation(ctx, sessionID, targetTurnID)
				},
				replacementUserTurn: &chatview.UITurn{
					Role:         "user",
					Text:         text,
					Timestamp:    time.Now().UTC(),
					Platform:     h.channelType.String(),
					SenderUserID: channelIdentityID,
				},
				attachments: chatAttachments,
			}
			h.startWSStream(streamBaseCtx, connCtx, writer, botID, ref, "ws edit stream error", editSubmission, editAdmission.build, nil,
				func(ctx context.Context, runRef wsTurnRef, admittedTurn wsAdmittedTurn, eventCh chan<- application.WSStreamEvent, abortCh <-chan struct{}) error {
					input := editInput
					input.RunID = runRef.RunID
					input.TurnID = admittedTurn.TurnID
					input.TurnPosition = admittedTurn.Position
					input.Attachments = editAdmission.preparedAttachments()
					return h.agentService.EditLatestMessageWS(ctx, input, eventCh, abortCh)
				},
			)

		default:
			sendWSError(writer, wsTurn(msg.InvocationID, msg.SessionID), "unknown message type: "+msg.Type)
		}
	}
	return nil
}

func userInputResponseAppError(err error) error {
	if err == nil || apperror.CodeOf(err) != "" {
		return err
	}
	switch {
	case errors.Is(err, userinput.ErrForbidden):
		return apperror.New(apperror.CodeUserInputForbidden, nil)
	case errors.Is(err, userinput.ErrNotFound), errors.Is(err, userinput.ErrAlreadyDecided):
		return apperror.New(apperror.CodeUserInputExpired, nil)
	default:
		return apperror.Wrap(apperror.CodeUserInputOperationFailed, err, nil)
	}
}

func (h *LocalChannelHandler) createWSChatSession(ctx context.Context, botID, channelIdentityID string) (sessionpkg.Thread, error) {
	if h == nil || h.sessionService == nil {
		return sessionpkg.Thread{}, errors.New("session service not configured")
	}
	return h.sessionService.Create(ctx, sessionpkg.CreateInput{
		BotID:           strings.TrimSpace(botID),
		ChannelType:     h.channelType.String(),
		Type:            sessionpkg.TypeChat,
		CreatedByUserID: strings.TrimSpace(channelIdentityID),
	})
}

func (h *LocalChannelHandler) wsSessionSupportsRequestedSkills(ctx context.Context, sessionID string) (bool, error) {
	if h == nil || h.sessionService == nil || strings.TrimSpace(sessionID) == "" {
		return false, nil
	}
	sess, err := h.sessionService.Get(ctx, sessionID)
	if err != nil {
		return false, err
	}
	return sessionpkg.SupportsSkillActivation(sess.SessionMode, sess.Type, sess.RuntimeType), nil
}

func (h *LocalChannelHandler) authorizeWSACPExecution(ctx context.Context, channelIdentityID, botID, sessionID string) (application.ACPSessionExecutionInfo, error) {
	if h == nil || h.agentService == nil {
		return application.ACPSessionExecutionInfo{}, nil
	}
	info, err := h.agentService.ACPSessionExecutionInfo(ctx, sessionID)
	if err != nil || !info.IsACP {
		return info, err
	}
	if strings.TrimSpace(info.RuntimeOwnerAccountID) == "" {
		feedback := acpRuntimeOwnerMissingFeedback()
		return info, echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	bot, err := AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bots.PermissionWorkspaceExec)
	if err != nil {
		if isHTTPStatus(err, http.StatusForbidden) {
			feedback := acpNoWorkspaceExecFeedback("missing_workspace_exec", "You do not have permission to run workspace commands for this bot.")
			return info, echo.NewHTTPError(feedback.HTTPStatus, feedback)
		}
		return info, err
	}
	if strings.TrimSpace(info.BotID) != "" && info.BotID != bot.ID {
		return info, echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	perms, err := h.resolveCurrentUserPermissions(ctx, channelIdentityID, bot.ID)
	if err != nil {
		return info, err
	}
	if err := authorizeACPRuntimeSessionAccess(channelIdentityID, perms, info.RuntimeOwnerAccountID); err != nil {
		return info, err
	}
	return info, nil
}

func wsErrorMessage(err error) string {
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		switch msg := httpErr.Message.(type) {
		case interface{ Error() string }:
			return msg.Error()
		case string:
			return msg
		default:
			if msg != nil {
				return fmt.Sprint(msg)
			}
		}
	}
	return err.Error()
}

func canOpenLocalWebSocket(perms []string) bool {
	return bots.HasPermission(perms, bots.PermissionWorkspaceExec) || bots.HasPermission(perms, bots.PermissionManage)
}

func authorizeWorkspaceTargetSelection(perms []string, targetID string) error {
	if strings.TrimSpace(targetID) == "" {
		return nil
	}
	if bots.HasPermission(perms, bots.PermissionWorkspaceRead) {
		return nil
	}
	return echo.NewHTTPError(http.StatusForbidden, "workspace_read permission is required to select a computer")
}

func (*LocalChannelHandler) requireChannelIdentityID(c echo.Context) (string, error) {
	return RequireChannelIdentityID(c)
}

func (h *LocalChannelHandler) authorizeBotAccess(ctx context.Context, channelIdentityID, botID string) (bots.Bot, error) {
	return AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bots.PermissionChat)
}

func (h *LocalChannelHandler) authorizeWSChatAccess(ctx context.Context, channelIdentityID, botID string) error {
	_, err := h.authorizeBotAccess(ctx, channelIdentityID, botID)
	return err
}

func (h *LocalChannelHandler) authorizeWSSession(ctx context.Context, channelIdentityID, botID, sessionID string) error {
	if h.sessionService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "session service not configured")
	}
	bot, perms, err := h.authorizeBotSessionAccess(ctx, channelIdentityID, botID)
	if err != nil {
		return err
	}
	sess, err := h.sessionService.Get(ctx, sessionID)
	if err != nil || sess.BotID != bot.ID {
		return echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	if !canAccessSession(sess, channelIdentityID, perms) {
		return echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	return nil
}

func (h *LocalChannelHandler) authorizeBotSessionAccess(ctx context.Context, channelIdentityID, botID string) (bots.Bot, []string, error) {
	bot, err := AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bots.PermissionChat)
	if err != nil {
		bot, err = AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bots.PermissionWorkspaceExec)
		if err != nil {
			return bots.Bot{}, nil, err
		}
	}
	perms, err := h.resolveCurrentUserPermissions(ctx, channelIdentityID, bot.ID)
	if err != nil {
		return bots.Bot{}, nil, err
	}
	return bot, perms, nil
}

func (h *LocalChannelHandler) resolveCurrentUserPermissions(ctx context.Context, channelIdentityID, botID string) ([]string, error) {
	if h.botService == nil || h.accountService == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "bot services not configured")
	}
	isAdmin, err := h.accountService.IsAdmin(ctx, channelIdentityID)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	perms, err := h.botService.ResolveUserPermissions(ctx, botID, channelIdentityID, isAdmin)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return perms, nil
}

// ---------------------------------------------------------------------------
// WebSocket event processing — attachment ingestion + TTS extraction
// ---------------------------------------------------------------------------

type wsEventEnvelope struct {
	Type     string          `json:"type"`
	ToolName string          `json:"toolName,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
}

// processWSEvent transforms a raw WS event, ingesting attachments and
// extracting TTS audio so the web frontend receives content_hash references.
func (h *LocalChannelHandler) processWSEvent(ctx context.Context, botID string, event json.RawMessage) []json.RawMessage {
	var envelope wsEventEnvelope
	if err := json.Unmarshal(event, &envelope); err != nil {
		return []json.RawMessage{event}
	}

	h.logger.Debug("ws event", slog.String("type", envelope.Type), slog.String("bot_id", botID))

	switch envelope.Type {
	case "attachment_delta":
		h.logger.Info("ws processing attachment_delta", slog.String("bot_id", botID))
		return h.wsIngestAttachments(ctx, botID, event)
	case "speech_delta":
		h.logger.Info("ws processing speech_delta", slog.String("bot_id", botID))
		return h.wsSynthesizeSpeech(ctx, botID, event)
	default:
		return []json.RawMessage{event}
	}
}

// wsIngestAttachments persists attachment data (container paths / data URLs)
// and rewrites them with content_hash so the web frontend can resolve them.
func (h *LocalChannelHandler) wsIngestAttachments(ctx context.Context, botID string, original json.RawMessage) []json.RawMessage {
	if h.mediaService == nil {
		return []json.RawMessage{original}
	}

	var event map[string]any
	if err := json.Unmarshal(original, &event); err != nil {
		return []json.RawMessage{original}
	}

	rawItems, _ := event["attachments"].([]any)
	if len(rawItems) == 0 {
		return []json.RawMessage{original}
	}

	changed := false
	for i, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		bundle := attachmentpkg.BundleFromMap(item)
		if strings.TrimSpace(bundle.ContentHash) != "" {
			continue
		}
		if bundle.Path == "" && bundle.Base64 == "" {
			continue
		}
		if ingested, ok := h.ingestSingleAttachment(ctx, botID, bundle); ok {
			rawItems[i] = applyBundleToItemMap(maps.Clone(item), ingested)
			changed = true
		}
	}

	if !changed {
		h.logger.Debug("ws attachment_delta: no items needed ingestion", slog.String("bot_id", botID))
		return []json.RawMessage{original}
	}

	h.logger.Info("ws attachment_delta: ingested attachments", slog.String("bot_id", botID), slog.Int("count", len(rawItems)))

	out, err := json.Marshal(event)
	if err != nil {
		return []json.RawMessage{original}
	}
	return []json.RawMessage{out}
}

func (h *LocalChannelHandler) ingestSingleAttachment(ctx context.Context, botID string, bundle attachmentpkg.Bundle) (attachmentpkg.Bundle, bool) {
	bundle = bundle.Normalize()
	if bundle.Path != "" {
		asset, err := h.mediaService.IngestContainerFile(ctx, botID, bundle.Path)
		if err != nil {
			h.logger.Warn("ws ingest container file failed", slog.String("path", bundle.Path), slog.Any("error", err))
			return attachmentpkg.Bundle{}, false
		}
		return bundle.WithAsset(botID, asset), true
	}

	if bundle.Base64 != "" {
		mimeType := bundle.Mime
		if mimeType == "" {
			mimeType = attachmentpkg.MimeFromDataURL(bundle.Base64)
		}
		decoded, err := attachmentpkg.DecodeBase64(bundle.Base64, media.MaxAssetBytes)
		if err != nil {
			h.logger.Warn("ws decode data url failed", slog.Any("error", err))
			return attachmentpkg.Bundle{}, false
		}
		asset, err := h.mediaService.Ingest(ctx, media.IngestInput{
			BotID:    botID,
			Mime:     mimeType,
			Reader:   decoded,
			MaxBytes: media.MaxAssetBytes,
		})
		if err != nil {
			h.logger.Warn("ws ingest data url failed", slog.Any("error", err))
			return attachmentpkg.Bundle{}, false
		}
		return bundle.WithAsset(botID, asset), true
	}

	return attachmentpkg.Bundle{}, false
}

// wsSynthesizeSpeech handles speech_delta events by synthesizing audio and
// injecting attachment_delta events with the resulting voice attachments.
func (h *LocalChannelHandler) wsSynthesizeSpeech(ctx context.Context, botID string, original json.RawMessage) []json.RawMessage {
	if h.speechService == nil || h.speechModelResolver == nil {
		h.logger.Warn("speech_delta received but TTS service not configured")
		return nil
	}

	modelID, err := h.speechModelResolver.ResolveSpeechModelID(ctx, botID)
	if err != nil || strings.TrimSpace(modelID) == "" {
		h.logger.Warn("speech_delta: bot has no TTS model configured", slog.String("bot_id", botID))
		return nil
	}

	var event struct {
		Speeches []struct {
			Text string `json:"text"`
		} `json:"speeches"`
	}
	if err := json.Unmarshal(original, &event); err != nil || len(event.Speeches) == 0 {
		return nil
	}

	var results []json.RawMessage
	for _, speech := range event.Speeches {
		text := strings.TrimSpace(speech.Text)
		if text == "" {
			continue
		}

		audioData, contentType, synthErr := h.speechService.Synthesize(ctx, modelID, text, nil)
		if synthErr != nil {
			h.logger.Warn("speech synthesis failed", slog.String("bot_id", botID), slog.Any("error", synthErr))
			continue
		}

		att := h.buildTtsAttachment(ctx, botID, contentType, audioData)
		attachmentEvent, _ := json.Marshal(map[string]any{
			"type":        "attachment_delta",
			"attachments": []any{att},
		})
		results = append(results, attachmentEvent)
	}
	return results
}

func (h *LocalChannelHandler) buildTtsAttachment(ctx context.Context, botID, contentType string, audioData []byte) map[string]any {
	bundle := attachmentpkg.Bundle{
		Type: "voice",
		Mime: contentType,
		Size: int64(len(audioData)),
	}

	mimeType := attachmentpkg.NormalizeMime(contentType)
	if h.mediaService != nil {
		asset, err := h.mediaService.Ingest(ctx, media.IngestInput{
			BotID:    botID,
			Mime:     mimeType,
			Reader:   bytes.NewReader(audioData),
			MaxBytes: media.MaxAssetBytes,
		})
		if err == nil {
			return bundle.WithAsset(botID, asset).ToMap()
		}
		h.logger.Warn("ws tts ingest failed", slog.Any("error", err))
	}

	bundle.Base64 = "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(audioData)
	return bundle.Normalize().ToMap()
}

// extractAssetRefsFromProcessedEvent parses a processed attachment_delta
// event to collect asset refs for post-persist linking.
func extractAssetRefsFromProcessedEvent(event json.RawMessage) []messagepkg.AssetRef {
	var envelope struct {
		Type        string           `json:"type"`
		ToolCallID  string           `json:"toolCallId"`
		Attachments []map[string]any `json:"attachments"`
	}
	if err := json.Unmarshal(event, &envelope); err != nil || envelope.Type != "attachment_delta" {
		return nil
	}
	toolCallID := strings.TrimSpace(envelope.ToolCallID)
	var refs []messagepkg.AssetRef
	for i, att := range envelope.Attachments {
		bundle := attachmentpkg.BundleFromMap(att)
		ch := strings.TrimSpace(bundle.ContentHash)
		if ch == "" {
			continue
		}
		name := strings.TrimSpace(bundle.Name)
		if name == "" && bundle.Metadata != nil {
			name, _ = bundle.Metadata["name"].(string)
		}
		metadata := bundle.Metadata
		if toolCallID != "" {
			metadata = maps.Clone(metadata)
			if metadata == nil {
				metadata = map[string]any{}
			}
			metadata["tool_call_id"] = toolCallID
		}
		ref := messagepkg.AssetRef{
			ContentHash: ch,
			Role:        "attachment",
			Ordinal:     i,
			Name:        name,
			Mime:        strings.TrimSpace(bundle.Mime),
			SizeBytes:   bundle.Size,
			Metadata:    metadata,
		}
		ref.StorageKey = attachmentpkg.MetadataString(bundle.Metadata, attachmentpkg.MetadataKeyStorageKey)
		refs = append(refs, ref)
	}
	return refs
}

func applyBundleToItemMap(item map[string]any, bundle attachmentpkg.Bundle) map[string]any {
	return bundle.MergeIntoMap(item)
}

// ingestWSInboundAttachments persists inbound user attachments (data URLs or
// container paths) into the media store so each carries a content_hash. The
// gateway can inline a raw base64 payload for the model to see, but only
// attachments with a content_hash are linked to the persisted user message —
// so without this step the file shows up in the live turn yet disappears from
// history after the session refreshes. Mirrors the channel inbound path's
// ingestInboundAttachments behaviour for the WebSocket transport.
func (h *LocalChannelHandler) ingestWSInboundAttachments(ctx context.Context, botID string, attachments []turn.Attachment) []turn.Attachment {
	if len(attachments) == 0 || h.mediaService == nil || strings.TrimSpace(botID) == "" {
		return attachments
	}
	result := make([]turn.Attachment, 0, len(attachments))
	for _, att := range attachments {
		if strings.TrimSpace(att.ContentHash) != "" {
			result = append(result, att)
			continue
		}
		bundle := att.Bundle()
		if strings.TrimSpace(bundle.Base64) == "" && strings.TrimSpace(bundle.Path) == "" {
			result = append(result, att)
			continue
		}
		ingested, ok := h.ingestSingleAttachment(ctx, botID, bundle)
		if !ok {
			// Keep the original so the model can still see the inlined payload
			// even if persistence failed.
			result = append(result, att)
			continue
		}
		result = append(result, turn.AttachmentFromBundle(ingested))
	}
	return result
}

func parseWSClientAttachments(rawAttachments []json.RawMessage) ([]turn.Attachment, error) {
	if len(rawAttachments) == 0 {
		return nil, nil
	}
	attachments := make([]turn.Attachment, 0, len(rawAttachments))
	for _, rawAtt := range rawAttachments {
		var decoded any
		if err := json.Unmarshal(rawAtt, &decoded); err != nil {
			continue
		}
		bundles, ok := attachmentpkg.ParseToolInputBundles(decoded)
		if !ok {
			continue
		}
		for _, bundle := range bundles {
			attachment := turn.AttachmentFromBundle(bundle)
			if err := slash.RejectReservedSkillMetadataValue(attachment.Metadata); err != nil {
				return nil, err
			}
			attachments = append(attachments, attachment)
		}
	}
	return attachments, nil
}
