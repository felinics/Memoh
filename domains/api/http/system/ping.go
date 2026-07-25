package system

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/runtime/container"
	"github.com/memohai/memoh/internal/version"
)

type PingResponse struct {
	Status            string `json:"status"`
	ContainerBackend  string `json:"container_backend"`
	SnapshotSupported bool   `json:"snapshot_supported"`
	Version           string `json:"version"`
	CommitHash        string `json:"commit_hash"`
}

type PingHandler struct {
	logger  *slog.Logger
	backend container.Backend
}

func NewPingHandler(log *slog.Logger, backend container.Backend) *PingHandler {
	return &PingHandler{
		logger:  log.With(slog.String("handler", "ping")),
		backend: backend,
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
		ContainerBackend:  h.backend.String(),
		SnapshotSupported: h.snapshotSupported(),
		Version:           version.Version,
		CommitHash:        version.ShortCommitHash(),
	})
}

func (*PingHandler) PingHead(c echo.Context) error {
	return c.NoContent(http.StatusOK)
}

func (h *PingHandler) snapshotSupported() bool {
	return h.backend != container.BackendApple
}
