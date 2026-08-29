package handlers

import (
	"errors"
	"net/http"
	"strings"

	connectsdk "github.com/felinics/connect-it/sdk/go"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/connectors"
)

type ConnectorsHandler struct {
	service        *connectors.Service
	botService     *bots.Service
	accountService *accounts.Service
}

func NewConnectorsHandler(
	service *connectors.Service,
	botService *bots.Service,
	accountService *accounts.Service,
) *ConnectorsHandler {
	return &ConnectorsHandler{
		service:        service,
		botService:     botService,
		accountService: accountService,
	}
}

func (h *ConnectorsHandler) Register(e *echo.Echo) {
	e.GET("/connectors/catalog", h.ListCatalog)

	group := e.Group("/bots/:bot_id/connectors")
	group.GET("", h.List)
	group.GET("/:connection_id", h.Get)
	group.POST("/oauth", h.BeginOAuth)
	group.POST("/api-key", h.CreateCredential)
	group.PATCH("/:connection_id", h.SetEnabled)
	group.DELETE("/:connection_id", h.Delete)
	group.POST("/:connection_id/reauth", h.Reauthorize)
}

// Get godoc
// @Summary Get a bot connector
// @Description Get one Connect-It connection bound to a bot.
// @Tags connectors
// @Param bot_id path string true "Bot ID"
// @Param connection_id path string true "Connect-It connection ID"
// @Success 200 {object} connectors.Connector
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/connectors/{connection_id} [get].
func (h *ConnectorsHandler) Get(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	item, err := h.service.Get(
		c.Request().Context(), botID, strings.TrimSpace(c.Param("connection_id")),
	)
	if err != nil {
		return connectorHTTPError(err)
	}
	return c.JSON(http.StatusOK, item)
}

type ConnectorOAuthRequest struct {
	ConnectorType string `json:"connector_type"`
	AuthMethod    string `json:"auth_method"`
}

type ConnectorCredentialRequest struct {
	ConnectorType string            `json:"connector_type"`
	AuthMethod    string            `json:"auth_method"`
	Fields        map[string]string `json:"fields"`
}

type ConnectorEnabledRequest struct {
	// Pointer so an empty or mistyped body fails validation instead of
	// silently disabling the connector.
	Enabled *bool `json:"enabled" validate:"required"`
}

// List godoc
// @Summary List bot connectors
// @Description List Connect-It connections enabled or disabled for a bot.
// @Tags connectors
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} connectors.ListResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/connectors [get].
func (h *ConnectorsHandler) List(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	items, err := h.service.List(c.Request().Context(), botID)
	if err != nil {
		return connectorHTTPError(err)
	}
	return c.JSON(http.StatusOK, connectors.ListResponse{Items: items})
}

// ListCatalog godoc
// @Summary List connector catalog
// @Description List providers available from the configured Connect-It deployment.
// @Tags connectors
// @Success 200 {array} connectsdk.Connector
// @Failure 403 {object} ErrorResponse
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /connectors/catalog [get].
func (h *ConnectorsHandler) ListCatalog(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	items, err := h.service.ListCatalog(c.Request().Context())
	if err != nil {
		return connectorHTTPError(err)
	}
	return c.JSON(http.StatusOK, items)
}

// BeginOAuth godoc
// @Summary Connect an OAuth provider
// @Description Create a bot connector and return the end-user authorization URL.
// @Tags connectors
// @Param bot_id path string true "Bot ID"
// @Param payload body ConnectorOAuthRequest true "OAuth request"
// @Success 201 {object} connectsdk.OAuthAuthorization
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/connectors/oauth [post].
func (h *ConnectorsHandler) BeginOAuth(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	var request ConnectorOAuthRequest
	if err := c.Bind(&request); err != nil {
		return apperror.Wrap(apperror.CodeConnectorRequestInvalid, err, nil)
	}
	result, err := h.service.BeginOAuth(
		c.Request().Context(),
		botID,
		request.ConnectorType,
		request.AuthMethod,
	)
	if err != nil {
		return connectorHTTPError(err)
	}
	return c.JSON(http.StatusCreated, result)
}

// CreateCredential godoc
// @Summary Connect an API credential provider
// @Description Send credential fields to Connect-It and store only its connection ID in Memoh.
// @Tags connectors
// @Param bot_id path string true "Bot ID"
// @Param payload body ConnectorCredentialRequest true "Credential request"
// @Success 201 {object} connectors.Connector
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/connectors/api-key [post].
func (h *ConnectorsHandler) CreateCredential(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	var request ConnectorCredentialRequest
	if err := c.Bind(&request); err != nil {
		return apperror.Wrap(apperror.CodeConnectorRequestInvalid, err, nil)
	}
	result, err := h.service.CreateCredential(
		c.Request().Context(),
		botID,
		request.ConnectorType,
		request.AuthMethod,
		request.Fields,
	)
	if err != nil {
		return connectorHTTPError(err)
	}
	return c.JSON(http.StatusCreated, result)
}

// SetEnabled godoc
// @Summary Enable or disable a connector
// @Description Disabled connectors are omitted when Memoh signs the bot's next aggregate MCP session.
// @Tags connectors
// @Param bot_id path string true "Bot ID"
// @Param connection_id path string true "Connect-It connection ID"
// @Param payload body ConnectorEnabledRequest true "Enabled state"
// @Success 204
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/connectors/{connection_id} [patch].
func (h *ConnectorsHandler) SetEnabled(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	var request ConnectorEnabledRequest
	if err := c.Bind(&request); err != nil {
		return apperror.Wrap(apperror.CodeConnectorRequestInvalid, err, nil)
	}
	if request.Enabled == nil {
		return apperror.New(apperror.CodeConnectorRequestInvalid, nil)
	}
	if err := h.service.SetEnabled(
		c.Request().Context(), botID, strings.TrimSpace(c.Param("connection_id")), *request.Enabled,
	); err != nil {
		return connectorHTTPError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Delete godoc
// @Summary Disconnect a connector
// @Description Delete the Connect-It credential and remove its bot binding.
// @Tags connectors
// @Param bot_id path string true "Bot ID"
// @Param connection_id path string true "Connect-It connection ID"
// @Success 204
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/connectors/{connection_id} [delete].
func (h *ConnectorsHandler) Delete(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	if err := h.service.Delete(
		c.Request().Context(), botID, strings.TrimSpace(c.Param("connection_id")),
	); err != nil {
		return connectorHTTPError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Reauthorize godoc
// @Summary Reauthorize a connector
// @Description Start OAuth again while preserving the same Connect-It connection ID.
// @Tags connectors
// @Param bot_id path string true "Bot ID"
// @Param connection_id path string true "Connect-It connection ID"
// @Success 200 {object} connectsdk.OAuthAuthorization
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/connectors/{connection_id}/reauth [post].
func (h *ConnectorsHandler) Reauthorize(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	result, err := h.service.Reauthorize(
		c.Request().Context(),
		botID,
		strings.TrimSpace(c.Param("connection_id")),
	)
	if err != nil {
		return connectorHTTPError(err)
	}
	return c.JSON(http.StatusOK, result)
}

func (h *ConnectorsHandler) authorize(c echo.Context) (string, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", apperror.New(apperror.CodeConnectorRequestInvalid, nil)
	}
	bot, err := AuthorizeBotAccess(
		c.Request().Context(),
		h.botService,
		h.accountService,
		channelIdentityID,
		botID,
	)
	if err != nil {
		return "", err
	}
	// The path parameter may be a name slug; the service layer and every
	// Connect-It call need the canonical bot UUID, resolved before any
	// remote side effect can happen with an unusable identifier.
	return bot.ID, nil
}

func connectorHTTPError(err error) error {
	switch {
	case errors.Is(err, connectors.ErrInvalidInput):
		return apperror.Wrap(apperror.CodeConnectorRequestInvalid, err, nil)
	case errors.Is(err, connectors.ErrNotConfigured):
		return apperror.New(apperror.CodeConnectorNotConfigured, nil)
	case errors.Is(err, pgx.ErrNoRows):
		return apperror.New(apperror.CodeConnectorNotFound, nil)
	}
	var apiErr *connectsdk.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return apperror.Wrap(apperror.CodeConnectorRequestRejected, err, nil)
		case http.StatusNotFound:
			return apperror.Wrap(apperror.CodeConnectorNotFound, err, nil)
		case http.StatusConflict:
			return apperror.Wrap(apperror.CodeConnectorConflict, err, nil)
		}
		return apperror.Wrap(apperror.CodeConnectorUpstreamUnavailable, err, nil)
	}
	if errors.Is(err, connectors.ErrUpstreamUnavailable) {
		return apperror.Wrap(apperror.CodeConnectorUpstreamUnavailable, err, nil)
	}
	return apperror.Wrap(apperror.CodeConnectorOperationFailed, err, nil)
}
