package handlers

import (
	"context"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/shellenv"
)

var terminalUpgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

type terminalInfoResponse struct {
	Available bool   `json:"available"`
	Shell     string `json:"shell"`
}

// GetTerminalInfo godoc
// @Summary Check terminal availability for bot workspace
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} terminalInfoResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/container/terminal [get].
func (h *ContainerdHandler) GetTerminalInfo(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceExec)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()

	if h.manager == nil {
		return c.JSON(http.StatusOK, terminalInfoResponse{Available: false})
	}

	client, clientErr := h.manager.NativeMCPClient(ctx, botID)
	if clientErr != nil || client == nil {
		return c.JSON(http.StatusOK, terminalInfoResponse{Available: false})
	}

	shell := detectShell(ctx, client)
	return c.JSON(http.StatusOK, terminalInfoResponse{
		Available: true,
		Shell:     shell,
	})
}

// HandleTerminalWS godoc
// @Summary Interactive WebSocket terminal for bot workspace
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param cols query int false "Initial terminal columns" default(80)
// @Param rows query int false "Initial terminal rows" default(24)
// @Param token query string false "Auth token"
// @Success 101 "WebSocket upgrade"
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container/terminal/ws [get].
func (h *ContainerdHandler) HandleTerminalWS(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceExec)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()

	if h.manager == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "manager not configured")
	}

	client, err := h.manager.NativeMCPClient(ctx, botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "workspace is not reachable")
	}

	conn, err := terminalUpgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}

	shell := detectShell(ctx, client)
	h.serveTerminalSocket(ctx, botID, conn, bridgeTerminalDialer{client: client}, shell)
	return nil
}

// detectShell returns the interactive shell launcher used for browser terminals.
// It is shellenv's launch line so launchers that probe the user's shell PATH
// read the same rc files this terminal does.
func detectShell(_ context.Context, _ *bridge.Client) string {
	return shellenv.TerminalCommand()
}
