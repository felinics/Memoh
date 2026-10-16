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
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/shellenv"
)

// terminalIdleTimeout closes inactive terminal WebSocket sessions to
// prevent leaked PTY processes. Reset on every inbound WebSocket message.
const terminalIdleTimeout = 30 * time.Minute

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
// @Failure 404 {object} server.Problem
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

	return c.JSON(http.StatusOK, terminalInfoResponse{
		Available: true,
		Shell:     shellenv.TerminalCommand(),
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
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
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
		return echo.NewHTTPError(http.StatusInternalServerError).WithInternal(err)
	}

	cols := parseUint32Query(c, "cols", 80)
	rows := parseUint32Query(c, "rows", 24)

	conn, err := wsUpgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	execStream, err := client.ExecStreamPTY(ctx, shellenv.TerminalCommand(), "/data", cols, rows)
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "exec failed"))
		return nil
	}
	defer func() { _ = execStream.Close() }()

	h.serveTerminal(ctx, conn, execStream, botID)
	return nil
}

// terminalStream is the PTY exec stream a terminal socket drives.
type terminalStream interface {
	Recv() (*pb.ExecOutput, error)
	SendStdin(data []byte) error
	Resize(cols, rows uint32) error
	Close() error
}

// serveTerminal relays between the socket and the PTY until either side ends.
// The caller closes both.
func (h *ContainerdHandler) serveTerminal(ctx context.Context, conn *websocket.Conn, execStream terminalStream, botID string) {
	heartbeat := h.wsHeartbeat.orDefault()
	idleTimeout := h.terminalIdleTimeout
	if idleTimeout == 0 {
		idleTimeout = terminalIdleTimeout
	}
	// The heartbeat keeps the path open and notices a vanished peer; it is not
	// activity. Pongs extend the read deadline only, so a terminal left open
	// in a background tab still reaches the idle timeout.
	keepalive := heartbeat.start(conn)
	defer keepalive.Stop()

	done := make(chan struct{})

	// Idle timer: closes the connection if no client activity for idleTimeout.
	var idleMu sync.Mutex
	idleTimer := time.AfterFunc(idleTimeout, func() {
		h.logger.InfoContext(ctx, "terminal idle timeout reached, closing", slog.String("bot_id", botID))
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "idle timeout"),
			time.Now().Add(5*time.Second))
		_ = conn.Close()
	})
	defer idleTimer.Stop()
	resetIdle := func() {
		idleMu.Lock()
		idleTimer.Reset(idleTimeout)
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
					// Output can outpace a slow reader. Without a deadline
					// the write waits on a peer that stopped reading, and the
					// handler waits on the write.
					_ = conn.SetWriteDeadline(time.Now().Add(heartbeat.writeTimeout))
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
			keepalive.extend()
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
						h.logger.WarnContext(ctx, "terminal resize failed",
							slog.String("bot_id", botID), slog.Any("error", resizeErr))
					}
				}
			}
		}
	}()

	<-done
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
