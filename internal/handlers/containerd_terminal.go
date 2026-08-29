package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// terminalIdleTimeout closes inactive terminal WebSocket sessions to
// prevent leaked PTY processes. Reset on every inbound WebSocket message.
const terminalIdleTimeout = 30 * time.Minute

var terminalUpgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

type terminalInfoResponse struct {
	Available bool   `json:"available"`
	Shell     string `json:"shell"`
}

type terminalControlMessage struct {
	Type string `json:"type"`
	Cols uint32 `json:"cols,omitempty"`
	Rows uint32 `json:"rows,omitempty"`
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

	cols := parseUint32Query(c, "cols", 80)
	rows := parseUint32Query(c, "rows", 24)

	conn, err := terminalUpgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	shell := detectShell(ctx, client)
	execStream, err := client.ExecStreamPTY(ctx, shell, "/data", cols, rows)
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "exec failed"))
		return nil
	}
	defer func() { _ = execStream.Close() }()

	done := make(chan struct{})

	// Idle timer: closes the connection if no client activity for terminalIdleTimeout.
	var idleMu sync.Mutex
	idleTimer := time.AfterFunc(terminalIdleTimeout, func() {
		h.logger.Info("terminal idle timeout reached, closing", slog.String("bot_id", botID))
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "idle timeout"),
			time.Now().Add(5*time.Second))
		_ = conn.Close()
	})
	defer idleTimer.Stop()
	resetIdle := func() {
		idleMu.Lock()
		idleTimer.Reset(terminalIdleTimeout)
		idleMu.Unlock()
	}

	// gRPC output -> WebSocket
	go func() {
		defer close(done)
		for {
			output, recvErr := execStream.Recv()
			if recvErr != nil {
				return
			}
			switch output.GetStream() {
			case pb.ExecOutput_STDOUT, pb.ExecOutput_STDERR:
				if data := output.GetData(); len(data) > 0 {
					if writeErr := conn.WriteMessage(websocket.BinaryMessage, data); writeErr != nil {
						return
					}
				}
			case pb.ExecOutput_EXIT:
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
					time.Now().Add(5*time.Second))
				return
			}
		}
	}()

	// WebSocket -> gRPC stdin/resize
	go func() {
		for {
			msgType, data, readErr := conn.ReadMessage()
			if readErr != nil {
				_ = execStream.Close()
				return
			}
			resetIdle() // client is active
			switch msgType {
			case websocket.BinaryMessage:
				if len(data) > 0 {
					if sendErr := execStream.SendStdin(data); sendErr != nil {
						return
					}
				}
			case websocket.TextMessage:
				var ctrl terminalControlMessage
				if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "resize" && ctrl.Cols > 0 && ctrl.Rows > 0 {
					if resizeErr := execStream.Resize(ctrl.Cols, ctrl.Rows); resizeErr != nil {
						h.logger.Warn("terminal resize failed",
							slog.String("bot_id", botID), slog.Any("error", resizeErr))
					}
				}
			}
		}
	}()

	<-done
	return nil
}

// detectShell returns the interactive shell launcher used for browser terminals.
// The bash-vs-sh decision happens inside the PTY process so terminal startup
// does not depend on a separate, potentially flaky probe exec.
func detectShell(_ context.Context, _ *bridge.Client) string {
	return `if [ -x /bin/bash ]; then exec /bin/bash; elif [ -x /usr/bin/bash ]; then exec /usr/bin/bash; elif command -v bash >/dev/null 2>&1; then exec bash; else exec /bin/sh; fi`
}

func parseUint32Query(c echo.Context, name string, fallback uint32) uint32 {
	raw := c.QueryParam(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || v == 0 {
		return fallback
	}
	return uint32(v) //nolint:gosec // G115
}
