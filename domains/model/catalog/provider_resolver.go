package catalog

import (
	"context"

	modeldomain "github.com/memohai/memoh/domains/model"
)

// ResolvedProvider is the provider state needed by model selection and probing.
// Credential material is supplied by the Providers owner through a consumer
// port; the catalog package never reads provider or OAuth rows directly.
type ResolvedProvider struct {
	ID                    string
	Name                  string
	ClientType            modeldomain.ClientType
	Enable                bool
	BaseURL               string
	APIKey                string //nolint:gosec // runtime credential passed to an SDK boundary
	CodexAccountID        string
	PromptCacheTTL        string
	ChatCompletionsCompat string
}

// ProviderResolver is the explicit Providers-owned lookup/credential port
// consumed by model probing and memory-model selection.
type ProviderResolver interface {
	ResolveModelProvider(context.Context, string) (ResolvedProvider, error)
}
