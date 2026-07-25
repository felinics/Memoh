package capability

import "strings"

// resellerPrefixes are third-party cloud / gateway repackagers. Their LiteLLM
// entries routinely carry stripped or wrong reasoning flags and a truncated
// context window (e.g. heroku/claude-3-7-sonnet has no reasoning flag, azure_ai
// gpt-oss differs from ovhcloud's). Matching an official model to one of these
// shells injects bad capabilities, so they are pruned from the snapshot before
// indexing: only first-party canonical entries remain. Vendor-owned prefixes
// (xai/, mistral/, moonshot/, deepseek/, ...) are intentionally NOT listed —
// those are the authoritative source for their own models.
var resellerPrefixes = map[string]struct{}{
	"github_copilot": {}, "copilot": {}, "github": {},
	"azure": {}, "azure_ai": {}, "bedrock": {}, "vertex": {}, "vertex_ai": {},
	"openrouter": {}, "groq": {}, "heroku": {}, "ovhcloud": {}, "novita": {},
	"sambanova": {}, "deepinfra": {}, "publicai": {}, "perplexity": {},
	"together": {}, "together_ai": {}, "togethercomputer": {},
	"fireworks": {}, "fireworks_ai": {}, "nscale": {}, "nebius": {},
	"hyperbolic": {}, "lambda": {}, "lambda_ai": {}, "crusoe": {},
	"gradient_ai": {}, "watsonx": {}, "databricks": {}, "friendliai": {},
}

// isResellerKey reports whether a registry key is served by a third-party
// repackager (its leading slash- or dot-delimited segment is a known reseller).
func isResellerKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	head := k
	if i := strings.IndexAny(head, "/."); i >= 0 {
		head = head[:i]
	}
	_, ok := resellerPrefixes[head]
	return ok
}

// Resolver resolves model capabilities from a LiteLLM registry snapshot. It is a
// BUILD-TIME helper (used by cmd/synccaps) and is not part of the runtime path.
type Resolver struct {
	entries map[string]litellmEntry
	idx     *index
}

// NewResolver parses a LiteLLM registry JSON body, prunes third-party reseller
// shells, and builds the lookup index.
func NewResolver(body []byte) (*Resolver, error) {
	entries, err := parseRegistry(body)
	if err != nil {
		return nil, err
	}
	for k := range entries {
		if isResellerKey(k) {
			delete(entries, k)
		}
	}
	return &Resolver{entries: entries, idx: buildIndex(keysOf(entries))}, nil
}

// Resolve returns derived capabilities for a (canonical) model identifier. The
// bool is false on a miss; callers should leave the template's existing values
// untouched in that case.
func (r *Resolver) Resolve(modelID string) (Capabilities, bool) {
	key, ok := r.idx.match(modelID)
	if !ok {
		return Capabilities{}, false
	}
	return derive(r.entries[key]), true
}

// Count reports the number of (post-prune) registry entries indexed.
func (r *Resolver) Count() int { return len(r.entries) }
