package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	modeldomain "github.com/memohai/memoh/domains/model"
)

// GenerateState returns a random OAuth state token.
func GenerateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateCodeVerifier returns a PKCE code verifier.
func GenerateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ComputeCodeChallenge returns the S256 PKCE challenge for verifier.
func ComputeCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ExpiresAtFromNow converts an OAuth expires_in seconds value to an absolute time.
func ExpiresAtFromNow(expiresIn int64) time.Time {
	if expiresIn <= 0 {
		return time.Time{}
	}
	return time.Now().Add(time.Duration(expiresIn) * time.Second)
}

// ValidateTokenURL enforces fail-closed token endpoint allowlisting.
func ValidateTokenURL(clientType modeldomain.ClientType, raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid oauth token url: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("oauth token url must use https")
	}

	switch clientType {
	case modeldomain.ClientTypeOpenAICodex:
		if !strings.EqualFold(parsed.Hostname(), "auth.openai.com") {
			return errors.New("oauth token url host must be auth.openai.com")
		}
	case modeldomain.ClientTypeGitHubCopilot:
		if !strings.EqualFold(parsed.Hostname(), "github.com") {
			return errors.New("oauth token url host must be github.com")
		}
	default:
		return errors.New("unsupported oauth client type")
	}

	return nil
}
