package handlers

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/errs"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
)

// MemoryConfigHandler serves the team-level Built-in Memory configuration.
// Whether a bot uses memory is a per-bot `memory_enabled` setting.
type MemoryConfigHandler struct {
	service *memprovider.Service
	logger  *slog.Logger
}

func NewMemoryConfigHandler(log *slog.Logger, service *memprovider.Service) *MemoryConfigHandler {
	return &MemoryConfigHandler{
		service: service,
		logger:  log.With(slog.String("handler", "memory_config")),
	}
}

func (h *MemoryConfigHandler) Register(e *echo.Echo) {
	group := e.Group("/memory/config")
	group.GET("", h.Get)
	group.PUT("", h.Update)
}

// Get godoc
// @Summary Get Built-in Memory configuration
// @Description Get the team-level Built-in Memory configuration
// @Tags memory
// @Produce json
// @Success 200 {object} adapters.MemoryConfig
// @Failure 500 {object} server.Problem
// @Router /memory/config [get].
func (h *MemoryConfigHandler) Get(c echo.Context) error {
	cfg, err := h.service.GetConfig(c.Request().Context())
	if err != nil {
		return errs.Wrap(err, "get config")
	}
	return c.JSON(http.StatusOK, cfg)
}

// Update godoc
// @Summary Update Built-in Memory configuration
// @Description Update the team-level Built-in Memory configuration
// @Tags memory
// @Accept json
// @Produce json
// @Param request body adapters.MemoryConfigUpdateRequest true "Built-in Memory configuration"
// @Success 200 {object} adapters.MemoryConfig
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /memory/config [put].
func (h *MemoryConfigHandler) Update(c echo.Context) error {
	var req memprovider.MemoryConfigUpdateRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	cfg, err := h.service.UpdateConfig(c.Request().Context(), req)
	if err != nil {
		return errs.Wrap(err, "update config")
	}
	return c.JSON(http.StatusOK, cfg)
}
