package handlers

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/auth"
	mcpgw "github.com/felinics/memoh/internal/mcp"
)

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
	// A runtime credential is an authentication mechanism independent of a
	// user JWT. Resolve it before the ordinary bot-access path, but fail closed
	// whenever either runtime header is present: malformed or stale runtime
	// credentials must never fall back to public header-derived tool identity.
	if hasAnyRuntimeToolCredential(c.Request()) {
		return h.handleMCPToolsWithBotID(c, strings.TrimSpace(c.Param("bot_id")))
	}
	if h.toolGateway == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "tool gateway not configured")
	}
	botID, err := h.requireBotAccessWithGuest(c)
	if err != nil {
		return err
	}
	return h.handleMCPToolsWithBotID(c, botID)
}

func (h *ContainerdHandler) handleMCPToolsWithBotID(c echo.Context, botID string) error {
	// ACP runtimes reference themselves by stable, server-generated runtime
	// identity; their per-prompt context resolves from the live handle and
	// the bot in the path must own the runtime. Fails closed: a dead or
	// foreign runtime never falls back to header-supplied identity.
	if hasAnyRuntimeToolCredential(c.Request()) {
		session, err := h.resolveRuntimeToolContext(c, botID)
		if err != nil {
			return err
		}
		if h.toolGateway == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "tool gateway not configured")
		}
		mcpgw.ServeToolMCPHTTP(c.Response().Writer, c.Request(), h.logger, h.toolGateway, h.toolContexts, session)
		return nil
	}
	if h.toolGateway == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "tool gateway not configured")
	}
	session := h.buildToolSessionContext(c, botID)
	mcpgw.ServeToolMCPHTTP(c.Response().Writer, c.Request(), h.logger, h.toolGateway, h.toolContexts, session)
	return nil
}

func hasAnyRuntimeToolCredential(req *http.Request) bool {
	if req == nil {
		return false
	}
	return strings.TrimSpace(req.Header.Get(mcpgw.ToolHeaderRuntimeID)) != "" ||
		strings.TrimSpace(req.Header.Get(mcpgw.ToolHeaderRuntimeToken)) != ""
}

func (h *ContainerdHandler) resolveRuntimeToolContext(c echo.Context, botID string) (mcpgw.ToolSessionContext, error) {
	runtimeID := strings.TrimSpace(c.Request().Header.Get(mcpgw.ToolHeaderRuntimeID))
	runtimeToken := strings.TrimSpace(c.Request().Header.Get(mcpgw.ToolHeaderRuntimeToken))
	if botID == "" || runtimeID == "" || runtimeToken == "" || h.acpRuntimes == nil {
		return mcpgw.ToolSessionContext{}, apperror.New(apperror.CodeACPRuntimeNotFound, nil)
	}
	session, ok := h.acpRuntimes.ResolveRuntimeToolContext(botID, runtimeID, runtimeToken)
	if !ok || strings.TrimSpace(session.BotID) != botID || strings.TrimSpace(session.RuntimeID) != runtimeID {
		return mcpgw.ToolSessionContext{}, apperror.New(apperror.CodeACPRuntimeNotFound, nil)
	}
	return session, nil
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
