package capabilities

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/memohai/memoh/internal/reasoning"
)

// OpenRouter answers two questions no other catalog does, because it is a gateway
// rather than a description: it has to translate one request shape into every
// upstream's, so it knows which models refuse to stop thinking and which think
// unless told otherwise. Both facts are derived from its own routing code, which
// makes them production-tested rather than curated.
//
//	mandatory        the model cannot be turned off at all
//	default_enabled  omitting the control leaves thinking running
//
// models.dev implies the first through the shape of its option list and cannot
// express the second; LiteLLM has neither. The overlap with models.dev is a free
// cross-check: where both speak, they agree on every model we sync today.
type openRouterReasoning struct {
	Mandatory        *bool    `json:"mandatory"`
	DefaultEnabled   *bool    `json:"default_enabled"`
	SupportedEfforts []string `json:"supported_efforts"`
	DefaultEffort    string   `json:"default_effort"`
}

type openRouterModel struct {
	ID        string               `json:"id"`
	Reasoning *openRouterReasoning `json:"reasoning"`
}

// OpenRouterSource resolves off-ability from an OpenRouter /api/v1/models payload.
//
// Its coverage is deliberately narrow — only models OpenRouter resells — so it is
// a supplement, never a base. Absence here means "no opinion", not "no capability".
type OpenRouterSource struct {
	entries map[string]openRouterReasoning
}

// NewOpenRouterSource parses an OpenRouter models payload.
func NewOpenRouterSource(body []byte) (*OpenRouterSource, error) {
	var payload struct {
		Data []openRouterModel `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse openrouter models: %w", err)
	}
	src := &OpenRouterSource{entries: make(map[string]openRouterReasoning, len(payload.Data))}
	for _, model := range payload.Data {
		if model.Reasoning == nil {
			continue
		}
		src.entries[normalizeOpenRouterID(model.ID)] = *model.Reasoning
	}
	if len(src.entries) == 0 {
		return nil, fmt.Errorf("openrouter payload carried no reasoning data")
	}
	return src, nil
}

// Count reports how many models carry reasoning data.
func (s *OpenRouterSource) Count() int { return len(s.entries) }

// Resolve returns the off-ability facts for a model, and whether OpenRouter has an
// opinion about it. vendorID is the model as OpenRouter names it — for our
// openrouter template that is the model id verbatim; for a first-party template it
// is the vendor-prefixed form, which callers build.
func (s *OpenRouterSource) Resolve(vendorID string) (Capabilities, bool) {
	entry, ok := s.entries[normalizeOpenRouterID(vendorID)]
	if !ok {
		return Capabilities{}, false
	}

	caps := Capabilities{ReasoningDefaultOn: entry.DefaultEnabled}
	switch {
	case entry.Mandatory == nil:
		// Silent: say nothing rather than guess. A wrong "cannot be turned off"
		// removes a working control.
	case *entry.Mandatory:
		caps.ReasoningOffSupport = reasoning.OffSupportRejected
	default:
		caps.ReasoningOffSupport = reasoning.OffSupportAccepted
	}
	return caps, true
}

// normalizeOpenRouterID lowercases and drops the routing suffixes OpenRouter
// appends to variants (":free", ":thinking", ":nitro"), which name a routing
// choice rather than a different model's capabilities.
func normalizeOpenRouterID(id string) string {
	out := strings.ToLower(strings.TrimSpace(id))
	if idx := strings.IndexByte(out, ':'); idx > 0 {
		out = out[:idx]
	}
	return out
}
