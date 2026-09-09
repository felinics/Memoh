package handlers

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/boot"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/version"
)

type PingResponse struct {
	Status            string `json:"status"`
	ContainerBackend  string `json:"container_backend"`
	SnapshotSupported bool   `json:"snapshot_supported"`
	Connectors        bool   `json:"connectors"`
	Version           string `json:"version"`
	CommitHash        string `json:"commit_hash"`
}

type PingHandler struct {
	logger  *slog.Logger
	runtime *boot.RuntimeConfig
	cfg     config.Config
}

func NewPingHandler(log *slog.Logger, rc *boot.RuntimeConfig, cfg config.Config) *PingHandler {
	return &PingHandler{
		logger:  log.With(slog.String("handler", "ping")),
		runtime: rc,
		cfg:     cfg,
	}
}

func (h *PingHandler) Register(e *echo.Echo) {
	e.GET("/ping", h.Ping)
	e.HEAD("/health", h.PingHead)
}

// Ping godoc
// @Summary Health check with server capabilities
// @Tags system
// @Success 200 {object} PingResponse
// @Router /ping [get].
func (h *PingHandler) Ping(c echo.Context) error {
	return c.JSON(http.StatusOK, PingResponse{
		Status:            "ok",
		ContainerBackend:  h.runtime.ContainerBackend,
		SnapshotSupported: h.snapshotSupported(),
		Connectors:        h.cfg.ConnectIt.Configured(),
		Version:           version.Version,
		CommitHash:        version.ShortCommitHash(),
	})
}

func (*PingHandler) PingHead(c echo.Context) error {
	return c.NoContent(http.StatusOK)
}

func (h *PingHandler) snapshotSupported() bool {
	return h.runtime.ContainerBackend != "apple"
}
