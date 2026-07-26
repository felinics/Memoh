package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	echojwt "github.com/labstack/echo-jwt/v4"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/memohai/memoh/internal/apperror"
)

// UserSessionValidator revalidates an account-backed JWT against current
// server-side account state before the request reaches a handler.
type UserSessionValidator func(ctx context.Context, userID string) error

const (
	claimSubject           = "sub"
	claimUserID            = "user_id"
	claimChannelIdentityID = "channel_identity_id"
	claimType              = "typ"
	claimBotID             = "bot_id"
	claimChatID            = "chat_id"
	claimRouteID           = "route_id"
	chatTokenType          = "chat_route"
)

// JWTMiddleware returns a JWT auth middleware configured for HS256 tokens.
func JWTMiddleware(secret string, skipper middleware.Skipper, validators ...UserSessionValidator) echo.MiddlewareFunc {
	jwtMiddleware := echojwt.WithConfig(echojwt.Config{
		SigningKey:    []byte(secret),
		SigningMethod: "HS256",
		TokenLookup:   "header:Authorization:Bearer ,query:token",
		Skipper:       skipper,
		NewClaimsFunc: func(_ echo.Context) jwt.Claims {
			return jwt.MapClaims{}
		},
	})
	if len(validators) == 0 || validators[0] == nil {
		return jwtMiddleware
	}
	validateSession := validators[0]
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		validatedNext := func(c echo.Context) error {
			if skipper != nil && skipper(c) {
				return next(c)
			}
			if isChatRouteToken(c) {
				return next(c)
			}
			userID, err := UserIDFromContext(c)
			if err != nil {
				return err
			}
			if err := validateSession(c.Request().Context(), userID); err != nil {
				return apperror.Unauthenticated("validate user session", err)
			}
			return next(c)
		}
		return jwtMiddleware(validatedNext)
	}
}

func isChatRouteToken(c echo.Context) bool {
	token, ok := c.Get("user").(*jwt.Token)
	if !ok || token == nil || !token.Valid {
		return false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	return ok && claimString(claims, claimType) == chatTokenType
}

// UserIDFromContext extracts the user id from JWT claims.
func UserIDFromContext(c echo.Context) (string, error) {
	token, ok := c.Get("user").(*jwt.Token)
	if !ok || token == nil || !token.Valid {
		return "", apperror.Unauthenticated("parse access token", nil)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", apperror.Unauthenticated("parse access token claims", nil)
	}
	if userID := claimString(claims, claimUserID); userID != "" {
		return userID, nil
	}
	if userID := claimString(claims, claimSubject); userID != "" {
		return userID, nil
	}
	return "", apperror.Unauthenticated("resolve user id", nil)
}

// GenerateToken creates a signed JWT for the user.
func GenerateToken(userID, secret string, expiresIn time.Duration) (string, time.Time, error) {
	if strings.TrimSpace(userID) == "" {
		return "", time.Time{}, errors.New("user id is required")
	}
	if strings.TrimSpace(secret) == "" {
		return "", time.Time{}, errors.New("jwt secret is required")
	}
	if expiresIn <= 0 {
		return "", time.Time{}, errors.New("jwt expires in must be positive")
	}

	now := time.Now().UTC()
	expiresAt := now.Add(expiresIn)
	claims := jwt.MapClaims{
		claimSubject: userID,
		claimUserID:  userID,
		"iat":        now.Unix(),
		"exp":        expiresAt.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

// ChatToken holds the claims for a chat-based JWT used for route-based reply.
type ChatToken struct {
	BotID             string
	ChatID            string
	RouteID           string
	UserID            string
	ChannelIdentityID string
}

// GenerateChatToken creates a signed JWT for chat route reply.
func GenerateChatToken(info ChatToken, secret string, expiresIn time.Duration) (string, time.Time, error) {
	if strings.TrimSpace(info.BotID) == "" {
		return "", time.Time{}, errors.New("bot id is required")
	}
	if strings.TrimSpace(info.ChatID) == "" {
		return "", time.Time{}, errors.New("chat id is required")
	}
	if strings.TrimSpace(info.UserID) == "" {
		info.UserID = strings.TrimSpace(info.ChannelIdentityID)
	}
	if strings.TrimSpace(info.UserID) == "" {
		return "", time.Time{}, errors.New("user id is required")
	}
	if strings.TrimSpace(secret) == "" {
		return "", time.Time{}, errors.New("jwt secret is required")
	}
	if expiresIn <= 0 {
		return "", time.Time{}, errors.New("jwt expires in must be positive")
	}

	now := time.Now().UTC()
	expiresAt := now.Add(expiresIn)
	claims := jwt.MapClaims{
		claimType:              chatTokenType,
		claimBotID:             info.BotID,
		claimChatID:            info.ChatID,
		claimRouteID:           info.RouteID,
		claimUserID:            info.UserID,
		claimChannelIdentityID: info.ChannelIdentityID,
		"iat":                  now.Unix(),
		"exp":                  expiresAt.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

// ChatTokenFromContext extracts the chat token claims from context.
func ChatTokenFromContext(c echo.Context) (ChatToken, error) {
	token, ok := c.Get("user").(*jwt.Token)
	if !ok || token == nil || !token.Valid {
		return ChatToken{}, apperror.Unauthenticated("parse chat token", nil)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return ChatToken{}, apperror.Unauthenticated("parse chat token claims", nil)
	}
	if claimString(claims, claimType) != chatTokenType {
		return ChatToken{}, apperror.Unauthenticated("validate chat token", nil)
	}
	info := ChatToken{
		BotID:             claimString(claims, claimBotID),
		ChatID:            claimString(claims, claimChatID),
		RouteID:           claimString(claims, claimRouteID),
		UserID:            claimString(claims, claimUserID),
		ChannelIdentityID: claimString(claims, claimChannelIdentityID),
	}
	if strings.TrimSpace(info.UserID) == "" {
		info.UserID = strings.TrimSpace(info.ChannelIdentityID)
	}
	return info, nil
}

// RefreshTokenFromContext extracts the current token from context and issues a new one
// with the same claims but a renewed expiration time.
func RefreshTokenFromContext(c echo.Context, secret string, defaultExpiresIn time.Duration) (string, time.Time, error) {
	token, ok := c.Get("user").(*jwt.Token)
	if !ok || token == nil || !token.Valid {
		return "", time.Time{}, apperror.Unauthenticated("parse refresh token", nil)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", time.Time{}, apperror.Unauthenticated("parse refresh token claims", nil)
	}

	// Calculate original duration if possible
	expiresIn := defaultExpiresIn
	if expRaw, ok := claims["exp"].(float64); ok {
		if iatRaw, ok := claims["iat"].(float64); ok {
			duration := time.Duration(expRaw-iatRaw) * time.Second
			if duration > 0 {
				expiresIn = duration
			}
		}
	}

	now := time.Now().UTC()
	expiresAt := now.Add(expiresIn)

	// Create new claims, copying over existing ones but updating time bounds
	newClaims := jwt.MapClaims{}
	for k, v := range claims {
		newClaims[k] = v
	}
	newClaims["iat"] = now.Unix()
	newClaims["exp"] = expiresAt.Unix()

	newToken := jwt.NewWithClaims(jwt.SigningMethodHS256, newClaims)
	signed, err := newToken.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, err
	}

	return signed, expiresAt, nil
}

func claimString(claims jwt.MapClaims, key string) string {
	raw, ok := claims[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(raw)
	}
}
