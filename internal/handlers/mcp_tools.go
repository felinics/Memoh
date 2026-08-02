package handlers

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/memoh/internal/agent/sessionmode"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/auth"
	mcpgw "github.com/memohai/memoh/internal/mcp"
)

type ToolCatalogItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ToolCatalogResponse struct {
	Tools []ToolCatalogItem `json:"tools"`
}

const (
	headerBotID             = mcpgw.ToolHeaderBotID
	headerChatID            = mcpgw.ToolHeaderChatID
	headerSessionID         = mcpgw.ToolHeaderSessionID
	headerRunID             = mcpgw.ToolHeaderRunID
	headerSessionType       = mcpgw.ToolHeaderSessionType
	headerRouteID           = mcpgw.ToolHeaderRouteID
	headerChannelIdentityID = mcpgw.ToolHeaderChannelIdentityID
	headerSessionToken      = mcpgw.ToolHeaderSessionToken
	headerCurrentPlatform   = mcpgw.ToolHeaderCurrentPlatform
	headerReplyTarget       = mcpgw.ToolHeaderReplyTarget
	headerConversationType  = mcpgw.ToolHeaderConversationType
	headerIsSubagent        = mcpgw.ToolHeaderIsSubagent
)

func (h *ContainerdHandler) SetToolGatewayService(service *mcpgw.ToolGatewayService) {
	h.toolGateway = service
}

func (h *ContainerdHandler) SetToolSessionContextStore(store *mcpgw.ToolSessionContextStore) {
	h.toolContexts = store
}

// acpRuntimeContextResolver resolves the trusted tool context of a live ACP
// runtime from its stable, server-generated runtime ID.
type acpRuntimeContextResolver interface {
	ResolveRuntimeToolContext(botID, runtimeID, toolToken string) (mcpgw.ToolSessionContext, bool)
}

func (h *ContainerdHandler) SetACPRuntimeResolver(resolver acpRuntimeContextResolver) {
	h.acpRuntimes = resolver
}

// HandleMCPTools godoc
// @Summary Unified MCP tools gateway
// @Description MCP endpoint for tool discovery and invocation.
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body object true "JSON-RPC request"
// @Success 200 {object} object "JSON-RPC response: {jsonrpc,id,result|error}"
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/tools [post].
func (h *ContainerdHandler) HandleMCPTools(c echo.Context) error {
	if h.toolGateway == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "tool gateway not configured")
	}
	botID, err := h.requireBotAccessWithGuest(c)
	if err != nil {
		return err
	}
	return h.handleMCPToolsWithBotID(c, botID)
}

// ListToolCatalog godoc
// @Summary List tools available for channel configuration
// @Description Lists the tools the bot can currently expose before applying its manual channel allowlist.
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param platform query string false "Channel platform" default(telegram)
// @Success 200 {object} ToolCatalogResponse
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/tools/catalog [get].
func (h *ContainerdHandler) ListToolCatalog(c echo.Context) error {
	if h.toolGateway == nil {
		return apperror.New(apperror.CodeToolCatalogUnavailable, nil)
	}
	botID, err := h.requireBotAccessWithGuest(c)
	if err != nil {
		return err
	}
	platform := strings.ToLower(strings.TrimSpace(c.QueryParam("platform")))
	if platform == "" {
		platform = "telegram"
	}
	descriptors, err := h.toolGateway.ListTools(c.Request().Context(), mcpgw.ToolSessionContext{
		BotID:               botID,
		ChatID:              botID,
		SessionType:         sessionmode.Chat,
		CurrentPlatform:     platform,
		ConversationType:    "group",
		CanRequestUserInput: true,
		CanListUserInput:    true,
		IgnoreToolPolicy:    true,
	})
	if err != nil {
		return apperror.Wrap(apperror.CodeToolCatalogUnavailable, err, nil)
	}
	items := make([]ToolCatalogItem, 0, len(descriptors))
	for _, descriptor := range descriptors {
		items = append(items, ToolCatalogItem{
			Name:        descriptor.Name,
			Description: descriptor.Description,
		})
	}
	return c.JSON(http.StatusOK, ToolCatalogResponse{Tools: items})
}

func (h *ContainerdHandler) handleMCPToolsWithBotID(c echo.Context, botID string) error {
	// ACP runtimes reference themselves by stable, server-generated runtime
	// identity; their per-prompt context resolves from the live handle and
	// the bot in the path must own the runtime. Fails closed: a dead or
	// foreign runtime never falls back to header-supplied identity.
	if runtimeID := strings.TrimSpace(c.Request().Header.Get(mcpgw.ToolHeaderRuntimeID)); runtimeID != "" {
		if h.acpRuntimes == nil {
			return echo.NewHTTPError(http.StatusNotFound, "runtime not found")
		}
		session, ok := h.acpRuntimes.ResolveRuntimeToolContext(botID, runtimeID, c.Request().Header.Get(mcpgw.ToolHeaderRuntimeToken))
		if !ok {
			return echo.NewHTTPError(http.StatusNotFound, "runtime not found")
		}
		mcpgw.ServeToolMCPHTTPWithoutContextMerge(c.Response().Writer, c.Request(), h.logger, h.toolGateway, h.toolContexts, session)
		return nil
	}
	session := h.buildToolSessionContext(c, botID)
	mcpgw.ServeToolMCPHTTPWithoutContextMerge(c.Response().Writer, c.Request(), h.logger, h.toolGateway, h.toolContexts, session)
	return nil
}

func buildToolCallPayloadFromRaw(params *sdkmcp.CallToolParamsRaw) (mcpgw.ToolCallPayload, error) {
	return mcpgw.BuildToolCallPayloadFromRaw(params)
}

func (*ContainerdHandler) buildToolSessionContext(c echo.Context, botID string) mcpgw.ToolSessionContext {
	botID = strings.TrimSpace(botID)
	session := mcpgw.ToolSessionContextFromHTTP(c.Request(), botID)
	session.BotID = botID
	session.RuntimeToken = ""
	session.ChannelIdentityID = ""
	session.SessionToken = ""
	// Public MCP clients cannot prove model-native image transport support.
	// Keep this capability limited to server-resolved runtime contexts until
	// the native MCP image path is wired end-to-end.
	session.SupportsImageInput = false
	if ctxIdentityID, err := auth.UserIDFromContext(c); err == nil {
		session.ChannelIdentityID = strings.TrimSpace(ctxIdentityID)
	}
	return session
}
