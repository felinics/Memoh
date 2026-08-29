package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/auth"
)

type AuthHandler struct {
	accountService *accounts.Service
	jwtSecret      string
	expiresIn      time.Duration
	logger         *slog.Logger
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"` //nolint:gosec // intentional: JSON request field carrying a user-supplied credential
}

type LoginResponse struct {
	AccessToken string `json:"access_token"` //nolint:gosec // intentional: JWT is the purpose of this response field
	TokenType   string `json:"token_type"`
	ExpiresAt   string `json:"expires_at"`
	UserID      string `json:"user_id"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	Username    string `json:"username"`
	Timezone    string `json:"timezone,omitempty"`
}

func NewAuthHandler(log *slog.Logger, accountService *accounts.Service, jwtSecret string, expiresIn time.Duration) *AuthHandler {
	return &AuthHandler{
		accountService: accountService,
		jwtSecret:      jwtSecret,
		expiresIn:      expiresIn,
		logger:         log.With(slog.String("handler", "auth")),
	}
}

func (h *AuthHandler) Register(e *echo.Echo) {
	e.POST("/auth/login", h.Login)
	e.POST("/auth/refresh", h.Refresh)
}

// Login godoc
// @Summary Login
// @Description Validate user credentials and issue a JWT
// @Tags auth
// @Param payload body LoginRequest true "Login request"
// @Success 200 {object} LoginResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /auth/login [post].
func (h *AuthHandler) Login(c echo.Context) error {
	if h.accountService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "user service not configured")
	}
	if strings.TrimSpace(h.jwtSecret) == "" {
		return echo.NewHTTPError(http.StatusInternalServerError, "jwt secret not configured")
	}
	if h.expiresIn <= 0 {
		return echo.NewHTTPError(http.StatusInternalServerError, "jwt expiry not configured")
	}

	var req LoginRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || strings.TrimSpace(req.Password) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "username and password are required")
	}

	account, err := h.accountService.Login(c.Request().Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, accounts.ErrInvalidCredentials) {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
		}
		if errors.Is(err, accounts.ErrInactiveAccount) {
			return echo.NewHTTPError(http.StatusUnauthorized, "user is inactive")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	token, expiresAt, err := auth.GenerateToken(account.ID, h.jwtSecret, h.expiresIn)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, LoginResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresAt:   expiresAt.Format(time.RFC3339),
		UserID:      account.ID,
		Username:    account.Username,
		Role:        account.Role,
		DisplayName: account.DisplayName,
		Timezone:    account.Timezone,
	})
}

type RefreshResponse struct {
	AccessToken string `json:"access_token"` //nolint:gosec // intentional: JWT is the purpose of this response field
	TokenType   string `json:"token_type"`
	ExpiresAt   string `json:"expires_at"`
}

// Refresh godoc
// @Summary Refresh Token
// @Description Issue a new JWT using the existing claims with updated expiration
// @Tags auth
// @Security BearerAuth
// @Success 200 {object} RefreshResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /auth/refresh [post].
func (h *AuthHandler) Refresh(c echo.Context) error {
	if strings.TrimSpace(h.jwtSecret) == "" {
		return echo.NewHTTPError(http.StatusInternalServerError, "jwt secret not configured")
	}

	token, expiresAt, err := auth.RefreshTokenFromContext(c, h.jwtSecret, h.expiresIn)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, err.Error())
	}

	return c.JSON(http.StatusOK, RefreshResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresAt:   expiresAt.Format(time.RFC3339),
	})
}
