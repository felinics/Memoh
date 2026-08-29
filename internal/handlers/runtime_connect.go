package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"google.golang.org/grpc/connectivity"

	"github.com/felinics/memoh/internal/userruntime"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	runtimeProtocolGRPC      = "memoh.runtime.v1.grpc"
	runtimeReadinessTimeout  = 10 * time.Second
	runtimeActivationTimeout = 10 * time.Second
	runtimeMessageLimit      = userruntime.RuntimeGRPCMessageLimit
)

type RuntimeConnectHandler struct {
	log     *slog.Logger
	service *userruntime.Service
	pipe    userruntime.Pipe
}

func NewRuntimeConnectHandler(log *slog.Logger, service *userruntime.Service, pipe userruntime.Pipe) *RuntimeConnectHandler {
	if log == nil {
		log = slog.Default()
	}
	return &RuntimeConnectHandler{
		log:     log.With(slog.String("handler", "runtime_connect")),
		service: service,
		pipe:    pipe,
	}
}

func (h *RuntimeConnectHandler) Register(e *echo.Echo) {
	e.GET("/runtimes/connect", h.Connect)
}

func (h *RuntimeConnectHandler) Connect(c echo.Context) error {
	key, err := bearerToken(c.Request().Header.Get(echo.HeaderAuthorization))
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "runtime key is required")
	}
	runtime, err := h.service.AuthenticateKey(c.Request().Context(), key)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid runtime key")
	}
	info, err := userruntime.ParseHandshakeMetadata(c.Request().Header.Get(userruntime.RuntimeMetadataHeader))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if !offersSubprotocol(c.Request(), runtimeProtocolGRPC) {
		c.Response().Header().Set("Sec-WebSocket-Protocol", runtimeProtocolGRPC)
		return echo.NewHTTPError(http.StatusUpgradeRequired, "unsupported runtime subprotocol")
	}
	if h.pipe == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, userruntime.ErrPipeNotConfigured.Error())
	}
	conn, err := websocket.Accept(c.Response().Writer, c.Request(), &websocket.AcceptOptions{
		Subprotocols: []string{runtimeProtocolGRPC},
	})
	if err != nil {
		return err
	}
	if conn.Subprotocol() != runtimeProtocolGRPC {
		_ = conn.CloseNow()
		return nil
	}
	connectionID := uuid.NewString()
	ctx := c.Request().Context()
	lifetimeCtx, cancelLifetime := context.WithCancel(ctx)
	netConn := runtimeWebSocketNetConn(lifetimeCtx, conn)
	defer func() {
		cancelLifetime()
		_ = conn.CloseNow()
	}()
	transportCtx, cancelTransport := context.WithTimeout(ctx, runtimeReadinessTimeout)
	grpcConn, err := h.pipe.ClientConn(transportCtx, netConn)
	cancelTransport()
	if err != nil {
		h.log.Warn("create runtime transport failed", slog.String("runtime_id", runtime.ID), slog.Any("error", err))
		return nil
	}
	client := bridge.NewClientFromConn(grpcConn)
	defer func() {
		_ = client.Close()
	}()

	readyCtx, cancelReady := context.WithTimeout(ctx, runtimeReadinessTimeout)
	rootEntry, err := client.Stat(readyCtx, "/")
	cancelReady()
	if err == nil && rootEntry == nil {
		err = errors.New("runtime readiness returned no root entry")
	}
	if err == nil && !rootEntry.GetIsDir() {
		err = errors.New("runtime readiness root is not a directory")
	}
	if err != nil {
		h.log.Warn("runtime readiness probe failed", slog.String("runtime_id", runtime.ID), slog.Any("error", err))
		return nil
	}
	activationCtx, cancelActivation := context.WithTimeout(ctx, runtimeActivationTimeout)
	transportLost := make(chan connectivity.State, 1)
	transportGuard := newRuntimeTransportCommitGuard(grpcConn, cancelActivation)
	// Start observing immediately after readiness. Persistence may block, and a
	// connection lost in that interval must never replace a healthy generation.
	go monitorRuntimeTransport(ctx, grpcConn, transportLost, transportGuard.MarkLost)

	connection := &userruntime.Connection{
		ConnectionID: connectionID,
		Client:       client,
		// Hub close paths synchronously cancel the byte stream. The handler's
		// defer owns the potentially blocking WebSocket teardown.
		Close: func(string) { cancelLifetime() },
	}
	activationErr := h.service.ActivateConnection(activationCtx, key, runtime.ID, info, connection, transportGuard.Check)
	cancelActivation()
	if activationErr != nil {
		h.log.Warn("activate runtime connection failed", slog.String("runtime_id", runtime.ID), slog.Any("error", activationErr))
		return nil
	}
	disconnectReason := "runtime connection closed"
	defer func() { h.service.DeactivateConnection(runtime.ID, connection, disconnectReason) }()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			disconnectReason = "request context canceled"
			return nil
		case state := <-transportLost:
			disconnectReason = "gRPC transport lost"
			h.log.Warn("runtime gRPC transport lost",
				slog.String("runtime_id", runtime.ID),
				slog.String("state", state.String()),
			)
			return nil
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				disconnectReason = "websocket ping failed"
				h.log.Warn("runtime websocket ping failed", slog.String("runtime_id", runtime.ID), slog.Any("error", err))
				return nil
			}
		}
	}
}

func runtimeWebSocketNetConn(ctx context.Context, conn *websocket.Conn) net.Conn {
	netConn := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	// NetConn disables coder/websocket's default read limit, so restore the
	// Runtime protocol ceiling before returning the stream to grpc-go.
	conn.SetReadLimit(runtimeMessageLimit)
	return netConn
}

type runtimeTransportCommitGuard struct {
	conn interface {
		GetState() connectivity.State
	}
	lost   atomic.Bool
	cancel context.CancelFunc
}

func newRuntimeTransportCommitGuard(
	conn interface{ GetState() connectivity.State },
	cancel context.CancelFunc,
) *runtimeTransportCommitGuard {
	return &runtimeTransportCommitGuard{conn: conn, cancel: cancel}
}

func (g *runtimeTransportCommitGuard) MarkLost(connectivity.State) {
	if g == nil {
		return
	}
	g.lost.Store(true)
	if g.cancel != nil {
		g.cancel()
	}
}

func (g *runtimeTransportCommitGuard) Check() error {
	if g == nil || g.conn == nil {
		return userruntime.ErrRuntimeConnectionNotReady
	}
	state := g.conn.GetState()
	if g.lost.Load() || state != connectivity.Ready {
		return fmt.Errorf("%w: gRPC state %s", userruntime.ErrRuntimeConnectionNotReady, state)
	}
	return nil
}

// monitorRuntimeTransport turns the first departure from Ready into a terminal
// connection event. DirectPipe is intentionally single-use, so there is no
// valid reconnect transition for this ClientConn; the Runtime client must open
// a fresh WebSocket instead.
func monitorRuntimeTransport(
	ctx context.Context,
	conn interface {
		GetState() connectivity.State
		WaitForStateChange(context.Context, connectivity.State) bool
	},
	lost chan<- connectivity.State,
	onLost func(connectivity.State),
) {
	if conn == nil {
		return
	}
	state := conn.GetState()
	if state == connectivity.Ready {
		if !conn.WaitForStateChange(ctx, state) {
			return
		}
		state = conn.GetState()
	}
	if onLost != nil {
		onLost(state)
	}
	select {
	case lost <- state:
	case <-ctx.Done():
	}
}

func bearerToken(header string) (string, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", userruntime.ErrInvalidKey
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", userruntime.ErrInvalidKey
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return "", userruntime.ErrInvalidKey
	}
	return token, nil
}

func offersSubprotocol(req *http.Request, required string) bool {
	for _, value := range req.Header.Values("Sec-WebSocket-Protocol") {
		for _, candidate := range strings.Split(value, ",") {
			if strings.TrimSpace(candidate) == required {
				return true
			}
		}
	}
	return false
}
