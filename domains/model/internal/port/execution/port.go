package execution

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	modeldomain "github.com/memohai/memoh/domains/model"
)

var ErrProviderNotFound = errors.New("model provider not found")

// ProviderDescriptor is the credential-free provider state used by catalogs.
type ProviderDescriptor struct {
	Name string
}

// Provider is the execution-ready provider state consumed by Resolver.
type Provider struct {
	Name                  string
	ClientType            modeldomain.ClientType
	Enable                bool
	BaseURL               string
	APIKey                string //nolint:gosec // runtime credential passed to the SDK boundary
	CodexAccountID        string
	PromptCacheTTL        string
	ChatCompletionsCompat string
	SensitiveValues       []string
}

// ProviderSource separates credential-free catalog reads from execution
// resolution so listing models never performs OAuth or credential work.
type ProviderSource interface {
	LookupProvider(context.Context, string) (ProviderDescriptor, error)
	ResolveProvider(context.Context, string) (Provider, error)
}

type sanitizedError struct {
	message string
	cause   error
}

func (e sanitizedError) Error() string { return e.message }
func (e sanitizedError) Unwrap() error { return e.cause }

// SanitizeError redacts the supplied credential values while preserving the
// original error for errors.Is/errors.As classification.
func SanitizeError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, secret := range compactSecrets(secrets...) {
		for _, variant := range secretVariants(secret) {
			message = strings.ReplaceAll(message, variant, strings.Repeat("*", utf8.RuneCountInString(variant)))
		}
	}
	if message == err.Error() {
		return err
	}
	return sanitizedError{message: message, cause: err}
}

func compactSecrets(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func secretVariants(secret string) []string {
	variants := []string{secret}
	runes := []rune(secret)
	half := len(runes) / 2
	if half > 5 {
		variants = append(variants, string(runes[:half]), string(runes[len(runes)-half:]))
	}
	if encoded := url.QueryEscape(secret); encoded != secret {
		variants = append(variants, encoded)
	}
	return variants
}
