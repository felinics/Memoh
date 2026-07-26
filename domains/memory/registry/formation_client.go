package registry

import (
	"time"

	"github.com/memohai/memoh/domains/memory/internal/formation"
	memprovider "github.com/memohai/memoh/domains/memory/provider"
)

// FormationClientConfig holds model resolution details for memory formation.
type FormationClientConfig struct {
	ModelID        string
	BaseURL        string
	APIKey         string `json:"-"`
	ClientType     string
	Timeout        time.Duration
	PromptCacheTTL string
}

// NewFormationClient constructs the Memory-owned formation LLM client.
//
// formation.Client speaks the public provider vocabulary directly, so this is
// a constructor rather than a translation layer.
func NewFormationClient(cfg FormationClientConfig) memprovider.LLM {
	return formation.New(formation.Config{
		ModelID:        cfg.ModelID,
		BaseURL:        cfg.BaseURL,
		APIKey:         cfg.APIKey,
		ClientType:     cfg.ClientType,
		Timeout:        cfg.Timeout,
		PromptCacheTTL: cfg.PromptCacheTTL,
	})
}
