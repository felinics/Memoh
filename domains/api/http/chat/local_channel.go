package chat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/application"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
	sessionpkg "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/agent/command"
	"github.com/memohai/memoh/domains/agent/command/slash"
	skillset "github.com/memohai/memoh/domains/agent/extension/skills"
	chatview "github.com/memohai/memoh/domains/agent/view"
	"github.com/memohai/memoh/domains/api/auth"
	"github.com/memohai/memoh/domains/api/bot"
	agenthttp "github.com/memohai/memoh/domains/api/http/agent"
	"github.com/memohai/memoh/domains/api/http/chat/local"
	httpx "github.com/memohai/memoh/domains/api/http/httpx"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/domains/media"
	attachmentpkg "github.com/memohai/memoh/domains/media/attachment"
	runtimedomain "github.com/memohai/memoh/domains/runtime"
	"github.com/memohai/memoh/internal/apperror"
)

// localChannelMediaService is the narrow ingest surface LocalChannelHandler needs.
type localChannelMediaService interface {
	Ingest(ctx context.Context, input media.IngestInput) (media.Asset, error)
	IngestContainerFile(ctx context.Context, botID, containerPath string) (media.Asset, error)
}

// localSpeechSynthesizer synthesizes text to speech audio.
type localSpeechSynthesizer interface {
	Synthesize(ctx context.Context, modelID string, text string, overrideCfg map[string]any) ([]byte, string, error)
}

// localSpeechModelResolver resolves speech model IDs for bot.
type localSpeechModelResolver interface {
	ResolveSpeechModelID(ctx context.Context, botID string) (string, error)
}

// LocalChannelHandler handles local channel routes (WebUI / API) backed by bot history.
type LocalChannelHandler struct {
	channelType         gateway.ChannelType
	channelManager      *gateway.Manager
	channelStore        *gateway.Store
	routeHub            *local.RouteHub
	botService          *bot.Service
	accountService      *account.Service
	sessionService      *sessionpkg.Service
	agentService        *application.Service
	commandHandler      *command.Handler
	skillResolver       runtimeSkillResolver
	mediaService        localChannelMediaService
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

// NewLocalChannelHandler creates a local channel handler.
func NewLocalChannelHandler(channelType gateway.ChannelType, channelManager *gateway.Manager, channelStore *gateway.Store, routeHub *local.RouteHub, botService *bot.Service, accountService *account.Service, sessionService *sessionpkg.Service) *LocalChannelHandler {
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

func (h *LocalChannelHandler) SetCommandHandler(handler *command.Handler) {
	h.commandHandler = handler
}

func (h *LocalChannelHandler) SetRuntimeSkillResolver(resolver runtimeSkillResolver) {
	h.skillResolver = resolver
}

// SetAuthTokenConfig configures runtime token minting for ACP-backed local WS streams.
func (h *LocalChannelHandler) SetAuthTokenConfig(jwtSecret string, ttl time.Duration) {
	h.jwtSecret = strings.TrimSpace(jwtSecret)
	h.tokenTTL = ttl
}

// SetMediaService sets the media service for WebSocket attachment ingestion.
func (h *LocalChannelHandler) SetMediaService(svc localChannelMediaService) {
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
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	var req QuickActionExecuteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	actionID := strings.TrimSpace(req.ActionID)
	sessionID := strings.TrimSpace(req.SessionID)
	skillActivationAllowed := true
	if sessionID != "" {
		if err := h.authorizeWSSession(c, channelIdentityID, botID, sessionID); err != nil {
			return err
		}
		supported, supportErr := h.wsSessionSupportsRequestedSkills(c.Request().Context(), sessionID)
		if supportErr != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, supportErr.Error())
		}
		skillActivationAllowed = supported
	}
	if !quickActionSkillActivationAllowedHint(req.Params) {
		skillActivationAllowed = false
	}
	result, slashErr := h.executeWebQuickAction(c.Request().Context(), botID, actionID, skillActivationAllowed)
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

func commandEvent(invocationID, composerScope, sessionID, actionID string) CommandEventResponse {
	return CommandEventResponse{
		InvocationID:  strings.TrimSpace(invocationID),
		ComposerScope: strings.TrimSpace(composerScope),
		SessionID:     strings.TrimSpace(sessionID),
		ActionID:      strings.TrimSpace(actionID),
		Terminal:      true,
	}
}

func (h *LocalChannelHandler) executeWebQuickAction(ctx context.Context, botID, actionID string, skillActivationAllowed bool) (*CommandActionResult, *slash.Error) {
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
	default:
		err := slash.Error{Code: slash.CodeUnsupportedWebCommand}
		return nil, &err
	}
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
	default:
		return "Slash command failed."
	}
}

func (h *LocalChannelHandler) resolveWebRequestedSkillContexts(ctx context.Context, botID string, requested []webRequestedSkill) ([]agentdomain.RequestedSkillContext, error) {
	names := make([]string, 0, len(requested))
	for _, item := range requested {
		names = append(names, strings.TrimSpace(item.Name))
	}
	return h.resolveWebTextRequestedSkillContexts(ctx, botID, names)
}

func (h *LocalChannelHandler) resolveWebTextRequestedSkillContexts(ctx context.Context, botID string, names []string) ([]agentdomain.RequestedSkillContext, error) {
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
			if resource == "help" || resource == "skill" {
				return true
			}
			return h.commandHandler != nil && h.commandHandler.HasCommandResource(resource)
		},
		WebActionSupported: func(resource, action string) bool {
			return webActionID(resource, action) != ""
		},
	})
}

func webActionID(resource, action string) string {
	resource = strings.TrimSpace(strings.ToLower(resource))
	action = strings.TrimSpace(strings.ToLower(action))
	switch {
	case resource == "help" && action == "":
		return "help"
	case resource == "skill" && (action == "" || action == "list"):
		return "skill.list"
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

	if err := httpx.PrepareSSE(c); err != nil {
		return err
	}
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

func formatLocalStreamEvent(event gateway.StreamEvent) ([]byte, error) {
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
	Message           gateway.Message `json:"message" validate:"required"`
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
	if err := gateway.RejectReservedSkillMetadata(req.Message); err != nil {
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
	msg := gateway.InboundMessage{
		Channel:     h.channelType,
		Message:     req.Message,
		BotID:       botID,
		ReplyTarget: routeKey,
		RouteKey:    routeKey,
		Sender: gateway.Identity{
			SubjectID: channelIdentityID,
			Attributes: map[string]string{
				"user_id": channelIdentityID,
			},
		},
		Conversation: gateway.Conversation{
			ID:   routeKey,
			Type: gateway.ConversationTypePrivate,
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

type wsClientMessage struct {
	Type              string                       `json:"type"`
	StreamID          string                       `json:"stream_id,omitempty"`
	Text              string                       `json:"text,omitempty"`
	SessionID         string                       `json:"session_id,omitempty"`
	InvocationID      string                       `json:"invocation_id,omitempty"`
	ComposerScope     string                       `json:"composer_scope,omitempty"`
	MessageID         string                       `json:"message_id,omitempty"`
	Attachments       []json.RawMessage            `json:"attachments,omitempty"`
	RequestedSkills   []webRequestedSkill          `json:"requested_skills,omitempty"`
	ModelID           string                       `json:"model_id,omitempty"`
	ReasoningEffort   string                       `json:"reasoning_effort,omitempty"`
	WorkspaceTargetID string                       `json:"workspace_target_id,omitempty"`
	ApprovalID        string                       `json:"approval_id,omitempty"`
	UserInputID       string                       `json:"user_input_id,omitempty"`
	ShortID           int                          `json:"short_id,omitempty"`
	ToolCallID        string                       `json:"tool_call_id,omitempty"`
	Decision          string                       `json:"decision,omitempty"`
	Reason            string                       `json:"reason,omitempty"`
	Answers           []agentdomain.QuestionAnswer `json:"answers,omitempty"`
	Canceled          bool                         `json:"canceled,omitempty"`
}

func turnQuestionAnswers(in []agentdomain.QuestionAnswer) []agentdomain.QuestionAnswer {
	if in == nil {
		return nil
	}
	return append([]agentdomain.QuestionAnswer(nil), in...)
}

type webRequestedSkill struct {
	Name string `json:"name"`
}

type wsOutboundEvent struct {
	Type      string `json:"type"`
	StreamID  string `json:"stream_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Data      any    `json:"data,omitempty"`
	Message   string `json:"message,omitempty"`
	Feedback  any    `json:"feedback,omitempty"`
}

type activeWSStream struct {
	streamID  string
	sessionID string
	cancel    context.CancelFunc
	abortCh   chan struct{}
}

type wsStreamRegistry struct {
	mu   sync.Mutex
	byID map[string]*activeWSStream
}

type wsRequestedSkillTurnRegistry struct {
	mu     sync.Mutex
	active map[string]int
}

func newWSStreamRegistry() *wsStreamRegistry {
	return &wsStreamRegistry{
		byID: make(map[string]*activeWSStream),
	}
}

func newWSRequestedSkillTurnRegistry() *wsRequestedSkillTurnRegistry {
	return &wsRequestedSkillTurnRegistry{active: make(map[string]int)}
}

func wsRequestedSkillTurnKey(botID, sessionID string) string {
	return strings.TrimSpace(botID) + ":" + strings.TrimSpace(sessionID)
}

func (r *wsRequestedSkillTurnRegistry) reserve(botID, sessionID, streamID string) (func(), bool) {
	key := wsRequestedSkillTurnKey(botID, sessionID)
	if r == nil || strings.TrimSpace(botID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(streamID) == "" {
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

func (r *wsRequestedSkillTurnRegistry) enter(botID, sessionID, streamID string) func() {
	key := wsRequestedSkillTurnKey(botID, sessionID)
	if r == nil || strings.TrimSpace(botID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(streamID) == "" {
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

func (h *LocalChannelHandler) reserveWSRequestedSkillTurn(botID, sessionID, streamID string) (func(), bool) {
	if h == nil {
		return func() {}, true
	}
	h.wsSkillTurnsMu.Lock()
	if h.wsSkillTurns == nil {
		h.wsSkillTurns = newWSRequestedSkillTurnRegistry()
	}
	registry := h.wsSkillTurns
	h.wsSkillTurnsMu.Unlock()
	return registry.reserve(botID, sessionID, streamID)
}

func (h *LocalChannelHandler) enterWSMessageTurn(botID, sessionID, streamID string) func() {
	if h == nil {
		return func() {}
	}
	h.wsSkillTurnsMu.Lock()
	if h.wsSkillTurns == nil {
		h.wsSkillTurns = newWSRequestedSkillTurnRegistry()
	}
	registry := h.wsSkillTurns
	h.wsSkillTurnsMu.Unlock()
	return registry.enter(botID, sessionID, streamID)
}

func (r *wsStreamRegistry) register(stream *activeWSStream) error {
	streamID := strings.TrimSpace(stream.streamID)
	if streamID == "" {
		return errors.New("stream_id is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[streamID]; exists {
		return fmt.Errorf("stream_id %q is already active", streamID)
	}
	stream.streamID = streamID
	stream.sessionID = strings.TrimSpace(stream.sessionID)
	r.byID[streamID] = stream
	return nil
}

func (r *wsStreamRegistry) hasSession(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if r == nil || sessionID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, stream := range r.byID {
		if stream != nil && stream.sessionID == sessionID {
			return true
		}
	}
	return false
}

type sessionTurnActiveChecker interface {
	SessionTurnActive(botID, sessionID string) bool
}

func shouldRejectWSSkillActivationForActiveStream(activeStreams *wsStreamRegistry, activeTurns sessionTurnActiveChecker, botID, sessionID string, hasSkillActivation bool) bool {
	if !hasSkillActivation {
		return false
	}
	if activeStreams != nil && activeStreams.hasSession(sessionID) {
		return true
	}
	return activeTurns != nil && activeTurns.SessionTurnActive(botID, sessionID)
}

func (r *wsStreamRegistry) finish(streamID string) {
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stream := r.byID[streamID]
	if stream == nil {
		return
	}
	delete(r.byID, streamID)
}

func (r *wsStreamRegistry) abort(streamID string) bool {
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return false
	}

	r.mu.Lock()
	stream := r.byID[streamID]
	r.mu.Unlock()
	if stream == nil {
		return false
	}
	select {
	case stream.abortCh <- struct{}{}:
	default:
	}
	stream.cancel()
	return true
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

func sendWSError(writer *wsWriter, streamID, sessionID, message string) {
	writer.SendJSON(wsOutboundEvent{
		Type:      "error",
		StreamID:  strings.TrimSpace(streamID),
		SessionID: strings.TrimSpace(sessionID),
		Message:   message,
	})
}

func newWSAppErrorEvent(streamID, sessionID string, err error) (wsOutboundEvent, bool) {
	public, ok := apperror.PublicFrom(err, "")
	if !ok {
		return wsOutboundEvent{}, false
	}
	return wsOutboundEvent{
		Type:      "error",
		StreamID:  strings.TrimSpace(streamID),
		SessionID: strings.TrimSpace(sessionID),
		Message:   public.Detail,
		Feedback:  public,
	}, true
}

func sendWSErrorFromError(writer *wsWriter, streamID, sessionID string, err error) {
	if event, ok := newWSAppErrorEvent(streamID, sessionID, err); ok {
		writer.SendJSON(event)
		return
	}
	feedback := agenthttp.AcpFeedbackError(err)
	if feedback == nil {
		sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
		return
	}
	writer.SendJSON(wsOutboundEvent{
		Type:      "error",
		StreamID:  strings.TrimSpace(streamID),
		SessionID: strings.TrimSpace(sessionID),
		Message:   strings.TrimSpace(feedback.Message),
		Feedback:  feedback,
	})
}

func (h *LocalChannelHandler) forwardWSStreamEvents(ctx, assetCtx context.Context, writer *wsWriter, botID, sessionID, streamID string, eventCh <-chan application.WSStreamEvent) {
	converter := chatview.NewUIMessageStreamConverter()
	outboundAssetRefs := make([]messagepkg.AssetRef, 0)
	for event := range eventCh {
		processed := h.processWSEvent(ctx, botID, event)
		for _, p := range processed {
			if refs := extractAssetRefsFromProcessedEvent(p); len(refs) > 0 {
				outboundAssetRefs = append(outboundAssetRefs, refs...)
			}

			var streamEvent agentdomain.StreamEvent
			if err := json.Unmarshal(p, &streamEvent); err != nil {
				continue
			}

			switch streamEvent.Type {
			case agentdomain.AgentStart:
				writer.SendJSON(wsOutboundEvent{
					Type:      "start",
					StreamID:  streamID,
					SessionID: sessionID,
				})
				continue
			case agentdomain.AgentEnd, agentdomain.AgentAbort:
				for _, uiMessage := range converter.ConvertTerminalMessages(streamEvent.Messages) {
					writer.SendJSON(wsOutboundEvent{
						Type:      "message",
						StreamID:  streamID,
						SessionID: sessionID,
						Data:      uiMessage,
					})
				}
				writer.SendJSON(wsOutboundEvent{
					Type:      "end",
					StreamID:  streamID,
					SessionID: sessionID,
				})
				continue
			case agentdomain.Error:
				message := strings.TrimSpace(streamEvent.Error)
				if message == "" {
					message = "stream error"
				}
				sendWSError(writer, streamID, sessionID, message)
				continue
			}

			uiEvents := converter.HandleEvent(chatview.UIStreamEventFromAgentEvent(streamEvent))
			for _, uiMessage := range uiEvents {
				writer.SendJSON(wsOutboundEvent{
					Type:      "message",
					StreamID:  streamID,
					SessionID: sessionID,
					Data:      uiMessage,
				})
			}
		}
	}
	if len(outboundAssetRefs) > 0 {
		h.agentService.LinkOutboundAssets(assetCtx, botID, sessionID, outboundAssetRefs)
	}
}

type wsStreamRunner func(ctx context.Context, eventCh chan<- application.WSStreamEvent, abortCh <-chan struct{}) error

func (h *LocalChannelHandler) startWSStream(baseCtx, connCtx context.Context, activeStreams *wsStreamRegistry, writer *wsWriter, botID, sessionID, streamID, logLabel string, onFinish func(), runner wsStreamRunner) {
	streamCtx, streamCancel := context.WithCancel(baseCtx)
	abortCh := make(chan struct{}, 1)
	if err := activeStreams.register(&activeWSStream{
		streamID:  streamID,
		sessionID: sessionID,
		cancel:    streamCancel,
		abortCh:   abortCh,
	}); err != nil {
		streamCancel()
		if onFinish != nil {
			onFinish()
		}
		sendWSError(writer, streamID, sessionID, err.Error())
		return
	}

	eventCh := make(chan application.WSStreamEvent, 64)
	releaseCompaction := h.agentService.DeferSessionCompaction(botID, sessionID, streamID)
	go func() {
		defer streamCancel()
		err := func() error {
			defer activeStreams.finish(streamID)
			if onFinish != nil {
				defer onFinish()
			}
			defer close(eventCh)
			return runner(streamCtx, eventCh, abortCh)
		}()
		if err != nil && connCtx.Err() == nil {
			privateErr := err
			if cause := apperror.CauseOf(err); cause != nil {
				privateErr = cause
			}
			h.logger.ErrorContext(baseCtx, "ws stream error",
				slog.String("operation", logLabel),
				slog.String("error_code", string(apperror.CodeOf(err))),
				slog.Any("error", privateErr),
				slog.String("bot_id", botID),
				slog.String("session_id", sessionID))
			sendWSErrorFromError(writer, streamID, sessionID, err)
		}
	}()

	go func() {
		defer releaseCompaction()
		h.forwardWSStreamEvents(streamCtx, baseCtx, writer, botID, sessionID, streamID, eventCh)
	}()
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
	activeStreams := newWSStreamRegistry()

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

		switch msg.Type {
		case "abort":
			streamID := strings.TrimSpace(msg.StreamID)
			if streamID == "" {
				sendWSError(writer, "", strings.TrimSpace(msg.SessionID), "stream_id is required")
				continue
			}
			activeStreams.abort(streamID)

		case "tool_approval_response":
			sessionID := strings.TrimSpace(msg.SessionID)
			if sessionID == "" {
				sendWSError(writer, strings.TrimSpace(msg.StreamID), "", "session_id is required")
				continue
			}
			streamID := strings.TrimSpace(msg.StreamID)
			if streamID == "" {
				sendWSError(writer, "", sessionID, "stream_id is required")
				continue
			}
			if err := h.authorizeWSSession(c, channelIdentityID, botID, sessionID); err != nil {
				sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
				continue
			}
			explicitID := strings.TrimSpace(msg.ApprovalID)
			if explicitID == "" && msg.ShortID > 0 {
				explicitID = strconv.Itoa(msg.ShortID)
			}
			suppressActivePromptAttach := activeStreams.hasSession(sessionID)
			releaseWSMessageTurn := h.enterWSMessageTurn(botID, sessionID, streamID)

			h.startWSStream(streamBaseCtx, connCtx, activeStreams, writer, botID, sessionID, streamID, "ws approval stream error", releaseWSMessageTurn,
				func(ctx context.Context, eventCh chan<- application.WSStreamEvent, _ <-chan struct{}) error {
					return h.agentService.RespondToolApproval(ctx, agentdomain.ToolApprovalResponse{
						BotID:                      botID,
						ThreadID:                   sessionID,
						ActorChannelIdentityID:     channelIdentityID,
						ActorUserID:                channelIdentityID,
						ApprovalID:                 strings.TrimSpace(msg.ApprovalID),
						ExplicitID:                 explicitID,
						Decision:                   strings.TrimSpace(msg.Decision),
						Reason:                     strings.TrimSpace(msg.Reason),
						ChatToken:                  bearerToken,
						SuppressActivePromptAttach: suppressActivePromptAttach,
					}, eventCh)
				},
			)

		case "user_input_response":
			sessionID := strings.TrimSpace(msg.SessionID)
			if sessionID == "" {
				sendWSError(writer, strings.TrimSpace(msg.StreamID), "", "session_id is required")
				continue
			}
			streamID := strings.TrimSpace(msg.StreamID)
			if streamID == "" {
				sendWSError(writer, "", sessionID, "stream_id is required")
				continue
			}
			if err := h.authorizeWSSession(c, channelIdentityID, botID, sessionID); err != nil {
				sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
				continue
			}
			explicitID := strings.TrimSpace(msg.UserInputID)
			if explicitID == "" && msg.ShortID > 0 {
				explicitID = strconv.Itoa(msg.ShortID)
			}
			suppressActivePromptAttach := activeStreams.hasSession(sessionID)
			releaseWSMessageTurn := h.enterWSMessageTurn(botID, sessionID, streamID)

			h.startWSStream(streamBaseCtx, connCtx, activeStreams, writer, botID, sessionID, streamID, "ws user input stream error", releaseWSMessageTurn,
				func(ctx context.Context, eventCh chan<- application.WSStreamEvent, _ <-chan struct{}) error {
					return h.agentService.RespondUserInput(ctx, agentdomain.UserInputResponse{
						BotID:                      botID,
						ThreadID:                   sessionID,
						ActorChannelIdentityID:     channelIdentityID,
						ActorUserID:                channelIdentityID,
						UserInputID:                strings.TrimSpace(msg.UserInputID),
						ExplicitID:                 explicitID,
						Answers:                    turnQuestionAnswers(msg.Answers),
						Canceled:                   msg.Canceled,
						Reason:                     strings.TrimSpace(msg.Reason),
						ChatToken:                  bearerToken,
						SuppressActivePromptAttach: suppressActivePromptAttach,
					}, eventCh)
				},
			)

		case "message":
			text := strings.TrimSpace(msg.Text)
			sessionID := strings.TrimSpace(msg.SessionID)
			streamID := strings.TrimSpace(msg.StreamID)
			workspaceTargetID := strings.TrimSpace(msg.WorkspaceTargetID)

			if streamID == "" {
				sendWSError(writer, "", sessionID, "stream_id is required")
				continue
			}
			if sessionID != "" {
				if err := h.authorizeWSSession(c, channelIdentityID, botID, sessionID); err != nil {
					sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
					continue
				}
			}
			if err := authorizeWorkspaceTargetSelection(perms, workspaceTargetID); err != nil {
				sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
				continue
			}
			if workspaceTargetID != "" {
				if err := h.agentService.ValidateWorkspaceTarget(streamBaseCtx, botID, workspaceTargetID); err != nil {
					sendWSError(writer, streamID, sessionID, err.Error())
					continue
				}
			}

			hasRequestedSkills := len(msg.RequestedSkills) > 0
			if hasRequestedSkills && strings.HasPrefix(text, "/") {
				sendWSCommandError(writer, msg, slash.CodeInvalidSkillSlashSyntax)
				continue
			}
			decision := h.classifyWebSlash(text, len(msg.Attachments) > 0, slash.SurfaceWebWS)
			var pendingSkillIntent *slash.SkillIntent
			switch decision.Kind {
			case slash.DecisionNormalChat:
			case slash.DecisionCommandAction:
				if err := h.authorizeWSChatAccess(streamBaseCtx, channelIdentityID, botID); err != nil {
					sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
					continue
				}
				actionID := webActionID(decision.Command.Resource, decision.Command.Action)
				skillActivationAllowed := true
				if strings.TrimSpace(sessionID) != "" {
					supported, supportErr := h.wsSessionSupportsRequestedSkills(streamBaseCtx, sessionID)
					if supportErr != nil {
						sendWSErrorFromError(writer, streamID, sessionID, supportErr)
						continue
					}
					skillActivationAllowed = supported
				}
				result, slashErr := h.executeWebQuickAction(streamBaseCtx, botID, actionID, skillActivationAllowed)
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
				sendWSError(writer, streamID, sessionID, "message text or attachments required")
				continue
			}
			if sessionID == "" || hasSkillActivation {
				if err := h.authorizeWSChatAccess(streamBaseCtx, channelIdentityID, botID); err != nil {
					sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
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

			var requestedSkillContexts []agentdomain.RequestedSkillContext
			var skillActivation *agentdomain.SkillActivation
			userMessageKind := ""
			userVisibleText := ""
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
					if shouldRejectWSSkillActivationForActiveStream(activeStreams, h.agentService, botID, sessionID, true) {
						sendWSCommandError(writer, msg, slash.CodeUnsupportedSkillSlashContext)
						continue
					}
					supported, supportErr := h.wsSessionSupportsRequestedSkills(streamBaseCtx, sessionID)
					if supportErr != nil {
						sendWSErrorFromError(writer, streamID, sessionID, supportErr)
						continue
					}
					if !supported {
						sendWSCommandError(writer, msg, slash.CodeUnsupportedSkillSlashContext)
						continue
					}
					var reserved bool
					releaseActiveWSTurn, reserved = h.reserveWSRequestedSkillTurn(botID, sessionID, streamID)
					if !reserved {
						sendWSCommandError(writer, msg, slash.CodeUnsupportedSkillSlashContext)
						continue
					}
				} else {
					releaseActiveWSTurn = h.enterWSMessageTurn(botID, sessionID, streamID)
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
				skillActivation = agentdomain.NewSkillActivation(requestedSkillContexts, prompt)
				streamText = strings.TrimSpace(prompt)
				streamModelText = strings.TrimSpace(agentdomain.SkillActivationModelQuery(skillActivation))
				userMessageKind = agentdomain.UserMessageKindSkillActivation
				userVisibleText = strings.TrimSpace(prompt)
			}

			if sessionID == "" {
				if h.sessionService == nil {
					sendWSError(writer, streamID, "", "session service not configured")
					releaseActiveWSTurnNow()
					continue
				}
				created, createErr := h.createWSChatSession(streamBaseCtx, botID, channelIdentityID)
				if createErr != nil {
					sendWSError(writer, streamID, "", createErr.Error())
					releaseActiveWSTurnNow()
					continue
				}
				sessionID = created.ID
				msg.SessionID = sessionID
				if hasSkillActivation {
					var reserved bool
					releaseActiveWSTurn, reserved = h.reserveWSRequestedSkillTurn(botID, sessionID, streamID)
					if !reserved {
						sendWSCommandError(writer, msg, slash.CodeUnsupportedSkillSlashContext)
						continue
					}
				} else {
					releaseActiveWSTurn = h.enterWSMessageTurn(botID, sessionID, streamID)
				}
				writer.SendJSON(map[string]any{
					"type":       "session_created",
					"stream_id":  streamID,
					"session_id": sessionID,
				})
			}
			if !sessionAuthorized {
				if err := h.authorizeWSSession(c, channelIdentityID, botID, sessionID); err != nil {
					sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
					releaseActiveWSTurnNow()
					continue
				}
			}
			acpInfo, err := h.authorizeWSACPExecution(c.Request().Context(), channelIdentityID, botID, sessionID)
			if err != nil {
				sendWSErrorFromError(writer, streamID, sessionID, err)
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
			var ingestedActivationAttachments []agentdomain.Attachment
			userMessagePersisted := false
			persistedUserMessageID := ""
			externalMessageID := ""
			var preparedActivationReq *application.ChatRequest
			if hasSkillActivation {
				externalMessageID = streamID
				ingestedActivationAttachments = h.ingestWSInboundAttachments(streamBaseCtx, botID, chatAttachments)
				userReq := application.ChatRequest{
					BotID:                   botID,
					ChatID:                  botID,
					ThreadID:                sessionID,
					StreamID:                streamID,
					UserID:                  channelIdentityID,
					SourceChannelIdentityID: channelIdentityID,
					ExternalMessageID:       externalMessageID,
					ConversationType:        gateway.ConversationTypePrivate,
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
					sendWSErrorFromError(writer, streamID, sessionID, persistErr)
					releaseActiveWSTurnNow()
					continue
				}
				preparedActivationReq = &preparedReq
				persistedUserMessageID = persisted.ID
				turns := chatview.ConvertMessagesToUITurns([]messagepkg.Message{persisted})
				if len(turns) > 0 {
					writer.SendJSON(wsOutboundEvent{
						Type:      "user_message",
						StreamID:  streamID,
						SessionID: sessionID,
						Data:      turns[0],
					})
				}
				userMessagePersisted = true
			}
			h.startWSStream(streamBaseCtx, connCtx, activeStreams, writer, botID, sessionID, streamID, "ws stream error", releaseActiveWSTurn,
				func(ctx context.Context, eventCh chan<- application.WSStreamEvent, abortCh <-chan struct{}) error {
					// Persist inbound attachments into the media store first so each
					// carries a content_hash. Without one the file is still inlined
					// for the model to see, but it is never linked to the stored user
					// message and would vanish from history once the session refreshes.
					ingestedAttachments := ingestedActivationAttachments
					if !hasSkillActivation {
						ingestedAttachments = h.ingestWSInboundAttachments(ctx, botID, chatAttachments)
					}
					req := application.ChatRequest{
						BotID:                   botID,
						ChatID:                  botID,
						ThreadID:                sessionID,
						StreamID:                streamID,
						UserID:                  channelIdentityID,
						SourceChannelIdentityID: channelIdentityID,
						ExternalMessageID:       externalMessageID,
						ConversationType:        gateway.ConversationTypePrivate,
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
						Attachments:             ingestedAttachments,
						RequestedSkills:         requestedSkillContexts,
						SkipMemoryExtraction:    hasSkillActivation && userVisibleText == "",
						SkipTitleGeneration:     hasSkillActivation && userVisibleText == "",
						Model:                   strings.TrimSpace(msg.ModelID),
						ReasoningEffort:         strings.TrimSpace(msg.ReasoningEffort),
						WorkspaceTargetID:       workspaceTargetID,
						ToolHTTPURL:             agenthttp.BuildACPMCPToolsURL(c, botID),
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
			streamID := strings.TrimSpace(msg.StreamID)
			messageID := strings.TrimSpace(msg.MessageID)
			workspaceTargetID := strings.TrimSpace(msg.WorkspaceTargetID)
			if streamID == "" {
				sendWSError(writer, "", sessionID, "stream_id is required")
				continue
			}
			if sessionID == "" {
				sendWSError(writer, streamID, "", "session_id is required")
				continue
			}
			if messageID == "" {
				sendWSError(writer, streamID, sessionID, "message_id is required")
				continue
			}
			if err := h.authorizeWSSession(c, channelIdentityID, botID, sessionID); err != nil {
				sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
				continue
			}
			if err := authorizeWorkspaceTargetSelection(perms, workspaceTargetID); err != nil {
				sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
				continue
			}
			if workspaceTargetID != "" {
				if err := h.agentService.ValidateWorkspaceTarget(streamBaseCtx, botID, workspaceTargetID); err != nil {
					sendWSError(writer, streamID, sessionID, err.Error())
					continue
				}
			}

			h.startWSStream(streamBaseCtx, connCtx, activeStreams, writer, botID, sessionID, streamID, "ws retry stream error", nil,
				func(ctx context.Context, eventCh chan<- application.WSStreamEvent, abortCh <-chan struct{}) error {
					return h.agentService.RetryLatestMessageWS(ctx, application.RetryLatestMessageInput{
						BotID:                  botID,
						SessionID:              sessionID,
						StreamID:               streamID,
						MessageID:              messageID,
						ActorChannelIdentityID: channelIdentityID,
						ActorUserID:            channelIdentityID,
						ChatToken:              bearerToken,
						Model:                  strings.TrimSpace(msg.ModelID),
						ReasoningEffort:        strings.TrimSpace(msg.ReasoningEffort),
						WorkspaceTargetID:      workspaceTargetID,
						ToolHTTPURL:            agenthttp.BuildACPMCPToolsURL(c, botID),
					}, eventCh, abortCh)
				},
			)

		case "edit_message":
			text := strings.TrimSpace(msg.Text)
			sessionID := strings.TrimSpace(msg.SessionID)
			streamID := strings.TrimSpace(msg.StreamID)
			messageID := strings.TrimSpace(msg.MessageID)
			workspaceTargetID := strings.TrimSpace(msg.WorkspaceTargetID)
			if streamID == "" {
				sendWSError(writer, "", sessionID, "stream_id is required")
				continue
			}
			if sessionID == "" {
				sendWSError(writer, streamID, "", "session_id is required")
				continue
			}
			if messageID == "" {
				sendWSError(writer, streamID, sessionID, "message_id is required")
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
				sendWSError(writer, streamID, sessionID, "message text or attachments required")
				continue
			}
			if err := h.authorizeWSSession(c, channelIdentityID, botID, sessionID); err != nil {
				sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
				continue
			}
			if err := authorizeWorkspaceTargetSelection(perms, workspaceTargetID); err != nil {
				sendWSError(writer, streamID, sessionID, wsErrorMessage(err))
				continue
			}
			if workspaceTargetID != "" {
				if err := h.agentService.ValidateWorkspaceTarget(streamBaseCtx, botID, workspaceTargetID); err != nil {
					sendWSError(writer, streamID, sessionID, err.Error())
					continue
				}
			}

			h.startWSStream(streamBaseCtx, connCtx, activeStreams, writer, botID, sessionID, streamID, "ws edit stream error", nil,
				func(ctx context.Context, eventCh chan<- application.WSStreamEvent, abortCh <-chan struct{}) error {
					ingestedAttachments := h.ingestWSInboundAttachments(ctx, botID, chatAttachments)
					return h.agentService.EditLatestMessageWS(ctx, application.EditLatestMessageInput{
						BotID:                  botID,
						SessionID:              sessionID,
						StreamID:               streamID,
						MessageID:              messageID,
						Text:                   text,
						Attachments:            ingestedAttachments,
						ActorChannelIdentityID: channelIdentityID,
						ActorUserID:            channelIdentityID,
						ChatToken:              bearerToken,
						Model:                  strings.TrimSpace(msg.ModelID),
						ReasoningEffort:        strings.TrimSpace(msg.ReasoningEffort),
						WorkspaceTargetID:      workspaceTargetID,
						ToolHTTPURL:            agenthttp.BuildACPMCPToolsURL(c, botID),
					}, eventCh, abortCh)
				},
			)

		default:
			sendWSError(writer, strings.TrimSpace(msg.StreamID), strings.TrimSpace(msg.SessionID), "unknown message type: "+msg.Type)
		}
	}
	return nil
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
		feedback := agenthttp.AcpRuntimeOwnerMissingFeedback()
		return info, echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	record, err := httpx.AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bot.PermissionWorkspaceExec)
	if err != nil {
		if agenthttp.IsHTTPStatus(err, http.StatusForbidden) {
			feedback := agenthttp.AcpNoWorkspaceExecFeedback("missing_workspace_exec", "You do not have permission to run workspace commands for this bot.")
			return info, echo.NewHTTPError(feedback.HTTPStatus, feedback)
		}
		return info, err
	}
	if strings.TrimSpace(info.BotID) != "" && info.BotID != record.ID {
		return info, echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	perms, err := h.resolveCurrentUserPermissions(ctx, channelIdentityID, record.ID)
	if err != nil {
		return info, err
	}
	if err := agenthttp.AuthorizeACPRuntimeSessionAccess(channelIdentityID, perms, info.RuntimeOwnerAccountID); err != nil {
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
	return bot.HasPermission(perms, bot.PermissionWorkspaceExec) || bot.HasPermission(perms, bot.PermissionManage)
}

func authorizeWorkspaceTargetSelection(perms []string, targetID string) error {
	if strings.TrimSpace(targetID) == "" {
		return nil
	}
	if bot.HasPermission(perms, bot.PermissionWorkspaceRead) {
		return nil
	}
	return echo.NewHTTPError(http.StatusForbidden, "workspace_read permission is required to select a computer")
}

func (*LocalChannelHandler) requireChannelIdentityID(c echo.Context) (string, error) {
	return httpx.RequireChannelIdentityID(c)
}

func (h *LocalChannelHandler) authorizeBotAccess(ctx context.Context, channelIdentityID, botID string) (bot.Bot, error) {
	return httpx.AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bot.PermissionChat)
}

func (h *LocalChannelHandler) authorizeWSChatAccess(ctx context.Context, channelIdentityID, botID string) error {
	_, err := h.authorizeBotAccess(ctx, channelIdentityID, botID)
	return err
}

func (h *LocalChannelHandler) authorizeWSSession(c echo.Context, channelIdentityID, botID, sessionID string) error {
	if h.sessionService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "session service not configured")
	}
	bot, perms, err := h.authorizeBotSessionAccess(c.Request().Context(), channelIdentityID, botID)
	if err != nil {
		return err
	}
	sess, err := h.sessionService.Get(c.Request().Context(), sessionID)
	if err != nil || sess.BotID != bot.ID {
		return echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	if !canAccessSession(sess, channelIdentityID, perms) {
		return echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	return nil
}

func (h *LocalChannelHandler) authorizeBotSessionAccess(ctx context.Context, channelIdentityID, botID string) (bot.Bot, []string, error) {
	record, err := httpx.AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bot.PermissionChat)
	if err != nil {
		record, err = httpx.AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bot.PermissionWorkspaceExec)
		if err != nil {
			return bot.Bot{}, nil, err
		}
	}
	perms, err := h.resolveCurrentUserPermissions(ctx, channelIdentityID, record.ID)
	if err != nil {
		return bot.Bot{}, nil, err
	}
	return record, perms, nil
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

	h.logger.DebugContext(ctx, "ws event", slog.String("type", envelope.Type), slog.String("bot_id", botID))

	switch envelope.Type {
	case "attachment_delta":
		h.logger.InfoContext(ctx, "ws processing attachment_delta", slog.String("bot_id", botID))
		return h.wsIngestAttachments(ctx, botID, event)
	case "speech_delta":
		h.logger.InfoContext(ctx, "ws processing speech_delta", slog.String("bot_id", botID))
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
		h.logger.DebugContext(ctx, "ws attachment_delta: no items needed ingestion", slog.String("bot_id", botID))
		return []json.RawMessage{original}
	}

	h.logger.InfoContext(ctx, "ws attachment_delta: ingested attachments", slog.String("bot_id", botID), slog.Int("count", len(rawItems)))

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
			h.logger.WarnContext(ctx, "ws ingest container file failed", slog.String("path", bundle.Path), slog.Any("error", err))
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
			h.logger.WarnContext(ctx, "ws decode data url failed", slog.Any("error", err))
			return attachmentpkg.Bundle{}, false
		}
		asset, err := h.mediaService.Ingest(ctx, media.IngestInput{
			BotID:    botID,
			Mime:     mimeType,
			Reader:   decoded,
			MaxBytes: media.MaxAssetBytes,
		})
		if err != nil {
			h.logger.WarnContext(ctx, "ws ingest data url failed", slog.Any("error", err))
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
		h.logger.WarnContext(ctx, "speech_delta received but TTS service not configured")
		return nil
	}

	modelID, err := h.speechModelResolver.ResolveSpeechModelID(ctx, botID)
	if err != nil || strings.TrimSpace(modelID) == "" {
		h.logger.WarnContext(ctx, "speech_delta: bot has no TTS model configured", slog.String("bot_id", botID))
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
			h.logger.WarnContext(ctx, "speech synthesis failed", slog.String("bot_id", botID), slog.Any("error", synthErr))
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
		h.logger.WarnContext(ctx, "ws tts ingest failed", slog.Any("error", err))
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
func (h *LocalChannelHandler) ingestWSInboundAttachments(ctx context.Context, botID string, attachments []agentdomain.Attachment) []agentdomain.Attachment {
	if len(attachments) == 0 || h.mediaService == nil || strings.TrimSpace(botID) == "" {
		return attachments
	}
	result := make([]agentdomain.Attachment, 0, len(attachments))
	for _, att := range attachments {
		if strings.TrimSpace(att.ContentHash) != "" {
			result = append(result, att)
			continue
		}
		bundle := bundleFromTurnAttachment(att)
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
		result = append(result, turnAttachmentFromBundle(ingested))
	}
	return result
}

func parseWSClientAttachments(rawAttachments []json.RawMessage) ([]agentdomain.Attachment, error) {
	if len(rawAttachments) == 0 {
		return nil, nil
	}
	attachments := make([]agentdomain.Attachment, 0, len(rawAttachments))
	for _, rawAtt := range rawAttachments {
		var decoded any
		if err := json.Unmarshal(rawAtt, &decoded); err != nil {
			continue
		}
		bundles, ok := attachmentpkg.ParseToolInputBundles(decoded, runtimedomain.DataMountPath)
		if !ok {
			continue
		}
		for _, bundle := range bundles {
			attachment := turnAttachmentFromBundle(bundle)
			if err := slash.RejectReservedSkillMetadataValue(attachment.Metadata); err != nil {
				return nil, err
			}
			attachments = append(attachments, attachment)
		}
	}
	return attachments, nil
}
