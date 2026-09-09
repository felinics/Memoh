package handlers

import (
	"net/http"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/channel"
)

type ChannelHandler struct {
	store    *channel.Store
	registry *channel.Registry
}

func NewChannelHandler(store *channel.Store, registry *channel.Registry) *ChannelHandler {
	return &ChannelHandler{store: store, registry: registry}
}

func (h *ChannelHandler) Register(e *echo.Echo) {
	group := e.Group("/users/me/channels")
	group.GET("/:platform", h.GetChannelIdentityConfig)
	group.PUT("/:platform", h.UpsertChannelIdentityConfig)

	metaGroup := e.Group("/channels")
	metaGroup.GET("", h.ListChannels)
	metaGroup.GET("/:platform", h.GetChannel)
}

// GetChannelIdentityConfig godoc
// @Summary Get channel user config
// @Description Get channel binding configuration for current user
// @Tags channel
// @Param platform path string true "Channel platform"
// @Success 200 {object} channel.ChannelIdentityBinding
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me/channels/{platform} [get].
func (h *ChannelHandler) GetChannelIdentityConfig(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	channelType, err := h.registry.ParseChannelType(c.Param("platform"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.store.GetChannelIdentityConfig(c.Request().Context(), channelIdentityID, channelType)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// UpsertChannelIdentityConfig godoc
// @Summary Update channel user config
// @Description Update channel binding configuration for current user
// @Tags channel
// @Param platform path string true "Channel platform"
// @Param payload body channel.UpsertChannelIdentityConfigRequest true "Channel user config payload"
// @Success 200 {object} channel.ChannelIdentityBinding
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me/channels/{platform} [put].
func (h *ChannelHandler) UpsertChannelIdentityConfig(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	channelType, err := h.registry.ParseChannelType(c.Param("platform"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	var req channel.UpsertChannelIdentityConfigRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	resp, err := h.store.UpsertChannelIdentityConfig(c.Request().Context(), channelIdentityID, channelType, req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

type ChannelMeta struct {
	Type             string                      `json:"type"`
	DisplayName      string                      `json:"display_name"`
	Configless       bool                        `json:"configless"`
	Capabilities     channel.ChannelCapabilities `json:"capabilities"`
	ConfigSchema     channel.ConfigSchema        `json:"config_schema"`
	UserConfigSchema channel.ConfigSchema        `json:"user_config_schema"`
	TargetSpec       channel.TargetSpec          `json:"target_spec"`
}

// ListChannels godoc
// @Summary List channel capabilities and schemas
// @Description List channel meta information including capabilities and schemas
// @Tags channel
// @Success 200 {array} ChannelMeta
// @Failure 500 {object} ErrorResponse
// @Router /channels [get].
func (h *ChannelHandler) ListChannels(c echo.Context) error {
	descs := h.registry.ListDescriptors()
	items := make([]ChannelMeta, 0, len(descs))
	for _, desc := range descs {
		items = append(items, ChannelMeta{
			Type:             desc.Type.String(),
			DisplayName:      desc.DisplayName,
			Configless:       desc.Configless,
			Capabilities:     desc.Capabilities,
			ConfigSchema:     desc.ConfigSchema,
			UserConfigSchema: desc.UserConfigSchema,
			TargetSpec:       desc.TargetSpec,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Type < items[j].Type
	})
	return c.JSON(http.StatusOK, items)
}

// GetChannel godoc
// @Summary Get channel capabilities and schemas
// @Description Get channel meta information including capabilities and schemas
// @Tags channel
// @Param platform path string true "Channel platform"
// @Success 200 {object} ChannelMeta
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /channels/{platform} [get].
func (h *ChannelHandler) GetChannel(c echo.Context) error {
	channelType, err := h.registry.ParseChannelType(c.Param("platform"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	desc, ok := h.registry.GetDescriptor(channelType)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "channel not found")
	}
	resp := ChannelMeta{
		Type:             desc.Type.String(),
		DisplayName:      desc.DisplayName,
		Configless:       desc.Configless,
		Capabilities:     desc.Capabilities,
		ConfigSchema:     desc.ConfigSchema,
		UserConfigSchema: desc.UserConfigSchema,
		TargetSpec:       desc.TargetSpec,
	}
	return c.JSON(http.StatusOK, resp)
}

func (*ChannelHandler) requireChannelIdentityID(c echo.Context) (string, error) {
	return RequireChannelIdentityID(c)
}
