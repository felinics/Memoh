package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// TokenConfig holds the HS256 signing material used by API auth and
// authenticated system triggers (schedule/heartbeat chat tokens).
type TokenConfig struct {
	Secret    string `json:"-"`
	ExpiresIn time.Duration
}

// NewTokenConfig validates and parses operator-facing JWT settings.
func NewTokenConfig(secret, expiresIn string) (TokenConfig, error) {
	if strings.TrimSpace(secret) == "" {
		return TokenConfig{}, errors.New("jwt secret is required")
	}
	duration, err := time.ParseDuration(expiresIn)
	if err != nil {
		return TokenConfig{}, fmt.Errorf("invalid jwt expires in: %w", err)
	}
	return TokenConfig{
		Secret:    secret,
		ExpiresIn: duration,
	}, nil
}
