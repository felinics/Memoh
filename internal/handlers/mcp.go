package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/mcp"
)

type MCPHandler struct {
	service        *mcp.ConnectionService
	botService     *bots.Service
	accountService *accounts.Service
	fedGateway     *MCPFederationGateway
	logger         *slog.Logger
}

func NewMCPHandler(log *slog.Logger, service *mcp.ConnectionService, botService *bots.Service, accountService *accounts.Service, fedGateway *MCPFederationGateway) *MCPHandler {
	return &MCPHandler{
		service:        service,
		botService:     botService,
		accountService: accountService,
		fedGateway:     fedGateway,
		logger:         log.With(slog.String("handler", "mcp")),
	}
}

func (h *MCPHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/mcp")
	group.GET("", h.List)
	group.POST("", h.Create)
	group.GET("/:id", h.Get)
	group.PUT("/:id", h.Update)
	group.DELETE("/:id", h.Delete)
	group.POST("/:id/probe", h.Probe)

	ops := e.Group("/bots/:bot_id/mcp-ops")
	ops.PUT("/import", h.Import)
	ops.GET("/export", h.Export)
	ops.POST("/batch-delete", h.BatchDelete)
}

// List godoc
// @Summary List MCP connections
// @Description List MCP connections for a bot
// @Tags mcp
// @Success 200 {object} mcp.ListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp [get].
func (h *MCPHandler) List(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	items, err := h.service.ListByBot(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, mcp.ListResponse{Items: items})
}

// Create godoc
// @Summary Create MCP connection
// @Description Create a MCP connection for a bot
// @Tags mcp
// @Param payload body mcp.UpsertRequest true "MCP payload"
// @Success 201 {object} mcp.Connection
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp [post].
func (h *MCPHandler) Create(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	var req mcp.UpsertRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.service.Create(c.Request().Context(), botID, req)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusCreated, resp)
}

// Get godoc
// @Summary Get MCP connection
// @Description Get a MCP connection by ID
// @Tags mcp
// @Param id path string true "MCP ID"
// @Success 200 {object} mcp.Connection
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp/{id} [get].
func (h *MCPHandler) Get(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	resp, err := h.service.Get(c.Request().Context(), botID, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "mcp connection not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// Update godoc
// @Summary Update MCP connection
// @Description Update a MCP connection by ID
// @Tags mcp
// @Param id path string true "MCP ID"
// @Param payload body mcp.UpsertRequest true "MCP payload"
// @Success 200 {object} mcp.Connection
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp/{id} [put].
func (h *MCPHandler) Update(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	var req mcp.UpsertRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.service.Update(c.Request().Context(), botID, id, req)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "mcp connection not found")
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// Delete godoc
// @Summary Delete MCP connection
// @Description Delete a MCP connection by ID
// @Tags mcp
// @Param id path string true "MCP ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp/{id} [delete].
func (h *MCPHandler) Delete(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	if err := h.service.Delete(c.Request().Context(), botID, id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// ProbeResponse is the response for a probe operation.
type ProbeResponse struct {
	Status       string               `json:"status"`
	Tools        []mcp.ToolDescriptor `json:"tools"`
	Error        string               `json:"error,omitempty"`
	AuthRequired bool                 `json:"auth_required,omitempty"`
}

// Probe godoc
// @Summary Probe MCP connection
// @Description Probe a MCP connection to discover tools and verify connectivity
// @Tags mcp
// @Param id path string true "MCP connection ID"
// @Success 200 {object} ProbeResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp/{id}/probe [post].
func (h *MCPHandler) Probe(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	conn, err := h.service.Get(c.Request().Context(), botID, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "mcp connection not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if h.fedGateway == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "federation gateway not configured")
	}

	ctx := c.Request().Context()
	var tools []mcp.ToolDescriptor
	var probeErr error

	switch strings.ToLower(strings.TrimSpace(conn.Type)) {
	case "http":
		tools, probeErr = h.fedGateway.ListHTTPConnectionTools(ctx, conn)
	case "sse":
		tools, probeErr = h.fedGateway.ListSSEConnectionTools(ctx, conn)
	case "stdio":
		tools, probeErr = h.fedGateway.ListStdioConnectionTools(ctx, botID, conn)
	default:
		probeErr = fmt.Errorf("unsupported connection type: %s", conn.Type)
	}

	resp := ProbeResponse{}
	if probeErr != nil {
		resp.Status = "error"
		resp.Error = probeErr.Error()
		resp.Tools = []mcp.ToolDescriptor{}
		authRequired := strings.Contains(probeErr.Error(), "401") || strings.Contains(strings.ToLower(probeErr.Error()), "unauthorized")
		resp.AuthRequired = authRequired
		_ = h.service.UpdateProbeResult(ctx, botID, id, "error", []mcp.ToolDescriptor{}, probeErr.Error())
	} else {
		resp.Status = "connected"
		if tools == nil {
			tools = []mcp.ToolDescriptor{}
		}
		resp.Tools = tools
		_ = h.service.UpdateProbeResult(ctx, botID, id, "connected", tools, "")
	}
	return c.JSON(http.StatusOK, resp)
}

// Import godoc
// @Summary Import MCP connections
// @Description Batch import MCP connections from standard mcpServers format. Existing connections (matched by name) get config updated with is_active preserved. New connections are created as active.
// @Tags mcp
// @Param payload body mcp.ImportRequest true "mcpServers dict"
// @Success 200 {object} mcp.ListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp-ops/import [put].
func (h *MCPHandler) Import(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	var req mcp.ImportRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	items, err := h.service.Import(c.Request().Context(), botID, req)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, mcp.ListResponse{Items: items})
}

// BatchDeleteRequest is the body for batch delete.
type BatchDeleteRequest struct {
	IDs []string `json:"ids"`
}

// BatchDelete godoc
// @Summary Batch delete MCP connections
// @Description Delete multiple MCP connections by IDs.
// @Tags mcp
// @Param payload body BatchDeleteRequest true "IDs to delete"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp-ops/batch-delete [post].
func (h *MCPHandler) BatchDelete(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	var req BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if len(req.IDs) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "ids are required")
	}
	if err := h.service.BatchDelete(c.Request().Context(), botID, req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// Export godoc
// @Summary Export MCP connections
// @Description Export all MCP connections for a bot in standard mcpServers format.
// @Tags mcp
// @Success 200 {object} mcp.ExportResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp-ops/export [get].
func (h *MCPHandler) Export(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	resp, err := h.service.ExportByBot(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

func (*MCPHandler) requireChannelIdentityID(c echo.Context) (string, error) {
	return RequireChannelIdentityID(c)
}

func (h *MCPHandler) authorizeBotAccess(ctx context.Context, channelIdentityID, botID string) (bots.Bot, error) {
	return AuthorizeBotAccess(ctx, h.botService, h.accountService, channelIdentityID, botID)
}
