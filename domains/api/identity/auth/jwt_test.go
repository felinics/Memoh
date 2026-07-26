package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/memohai/memoh/internal/apperror"
)

func TestRefreshTokenFromContext(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	secret := "test-secret"
	userID := "user-123"

	// Create an initial token with a 5-minute lifespan
	initialDuration := 5 * time.Minute
	initialTokenStr, _, err := GenerateToken(userID, secret, initialDuration)
	require.NoError(t, err)

	// Parse the token to place it into the echo context
	token, err := jwt.Parse(initialTokenStr, func(_ *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})
	require.NoError(t, err)
	c.Set("user", token)

	// Simulate some time passing to ensure the new token has a different 'iat' and 'exp'
	time.Sleep(1 * time.Second)

	// Run the refresh function
	defaultDuration := 1 * time.Hour
	newTokenStr, newExpiresAt, err := RefreshTokenFromContext(c, secret, defaultDuration)
	require.NoError(t, err)
	assert.NotEmpty(t, newTokenStr)

	// Parse the original token claims for comparison
	originalClaims, ok := token.Claims.(jwt.MapClaims)
	assert.True(t, ok)
	origIat := int64(originalClaims["iat"].(float64))

	// Parse the new token
	newToken, err := jwt.Parse(newTokenStr, func(_ *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})
	require.NoError(t, err)
	assert.True(t, newToken.Valid)

	newClaims, ok := newToken.Claims.(jwt.MapClaims)
	assert.True(t, ok)

	// Ensure standard payload claims are retained
	assert.Equal(t, userID, newClaims[claimSubject])
	assert.Equal(t, userID, newClaims[claimUserID])

	// Validate the new time bounds
	newIat := int64(newClaims["iat"].(float64))
	newExp := int64(newClaims["exp"].(float64))

	// 1. Ensure time has advanced
	assert.Greater(t, newIat, origIat)

	// 2. Ensure the refreshed token has a positive lifetime and does not exceed the configured default duration
	lifetimeSeconds := newExp - newIat
	assert.Positive(t, lifetimeSeconds)
	assert.LessOrEqual(t, lifetimeSeconds, int64(defaultDuration.Seconds()))

	// 3. Ensure the return value matches the claim
	assert.Equal(t, newExp, newExpiresAt.Unix())
}

func TestRefreshTokenFromContext_MissingUser(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	secret := "test-secret"
	defaultDuration := 1 * time.Hour

	// Context without the "user" key
	_, _, err := RefreshTokenFromContext(c, secret, defaultDuration)
	require.Error(t, err)
	assert.Equal(t, apperror.KindUnauthenticated, apperror.KindOf(err))
}

func TestJWTMiddlewareRejectsInactiveAccountSession(t *testing.T) {
	const secret = "test-secret"
	token, _, err := GenerateToken("user-123", secret, time.Hour)
	require.NoError(t, err)

	validatedUserID := ""
	e := echo.New()
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		_ = c.NoContent(apperror.KindOf(err).HTTPStatus())
	}
	e.Use(JWTMiddleware(secret, func(echo.Context) bool { return false }, func(_ context.Context, userID string) error {
		validatedUserID = userID
		return errTestInactiveSession
	}))
	e.GET("/protected", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/protected", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "user-123", validatedUserID)
}

func TestJWTMiddlewareDoesNotValidateChatRouteTokenAsAccount(t *testing.T) {
	const secret = "test-secret"
	token, _, err := GenerateChatToken(ChatToken{
		BotID:  "bot-1",
		ChatID: "chat-1",
		UserID: "channel-identity-1",
	}, secret, time.Hour)
	require.NoError(t, err)

	validatorCalled := false
	e := echo.New()
	e.Use(JWTMiddleware(secret, func(echo.Context) bool { return false }, func(context.Context, string) error {
		validatorCalled = true
		return nil
	}))
	e.GET("/chat-route", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/chat-route", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.False(t, validatorCalled)
}

var errTestInactiveSession = errors.New("inactive test session")
