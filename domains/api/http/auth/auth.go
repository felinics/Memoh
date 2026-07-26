package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/identity/auth"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/apperror"
)

type AuthHandler struct {
	accountService *account.Service
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

func NewAuthHandler(log *slog.Logger, accountService *account.Service, jwtSecret string, expiresIn time.Duration) *AuthHandler {
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
// @Failure 400 {object} apperror.Problem
// @Failure 401 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /auth/login [post].
func (h *AuthHandler) Login(c echo.Context) error {
	if h.accountService == nil {
		return apperror.Internal("configure user service", nil)
	}
	if strings.TrimSpace(h.jwtSecret) == "" {
		return apperror.Internal("configure jwt secret", nil)
	}
	if h.expiresIn <= 0 {
		return apperror.Internal("configure jwt expiry", nil)
	}

	var req LoginRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind login payload", err)
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		return apperror.Required("username")
	}
	if strings.TrimSpace(req.Password) == "" {
		return apperror.Required("password")
	}

	record, err := h.accountService.Login(c.Request().Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, account.ErrInvalidCredentials) {
			return apperror.Unauthenticated("authenticate user", err)
		}
		if errors.Is(err, account.ErrInactiveAccount) {
			return apperror.Unauthenticated("authenticate user", err)
		}
		return apperror.Internal("authenticate user", err)
	}
	token, expiresAt, err := auth.GenerateToken(record.ID, h.jwtSecret, h.expiresIn)
	if err != nil {
		return apperror.Internal("generate access token", err)
	}

	return c.JSON(http.StatusOK, LoginResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresAt:   expiresAt.Format(time.RFC3339),
		UserID:      record.ID,
		Username:    record.Username,
		Role:        record.Role,
		DisplayName: record.DisplayName,
		Timezone:    record.Timezone,
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
// @Failure 401 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /auth/refresh [post].
func (h *AuthHandler) Refresh(c echo.Context) error {
	if strings.TrimSpace(h.jwtSecret) == "" {
		return apperror.Internal("configure jwt secret", nil)
	}

	token, expiresAt, err := auth.RefreshTokenFromContext(c, h.jwtSecret, h.expiresIn)
	if err != nil {
		return apperror.Unauthenticated("refresh access token", err)
	}

	return c.JSON(http.StatusOK, RefreshResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresAt:   expiresAt.Format(time.RFC3339),
	})
}
