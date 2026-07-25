// Package capability derives model reasoning capabilities (thinking mode and
// effort tiers) from the LiteLLM model registry. It is a BUILD-TIME helper used
// by cmd/synccaps to enrich the hand-maintained conf/providers templates; it is
// not wired into the runtime server (the runtime reads capabilities straight
// from the provider templates).
//
// Matching is intentionally LIGHT: model identifiers enumerated in the official
// provider templates are already canonical (e.g. "claude-opus-4-6", "gpt-5.4",
// "grok-4"), so a canonical-equality match against the registry is enough. We
// deliberately avoid token-subset / version-veto fuzzing: a canonical miss
// simply falls back to the template's own legacy toggle default, which is safer
// than borrowing a sibling/repackaged entry's capabilities.
package capability

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
)

// prefixSegments are vendor / gateway / region tokens that may lead a model id.
// They are stripped only when leading, so version numbers (which never match
// these words) survive. Used to canonicalize both registry keys and template
// ids to the same bare slug ("xai/grok-4" and "grok-4" -> "grok-4").
var prefixSegments = map[string]struct{}{
	"openrouter": {}, "anthropic": {}, "openai": {}, "google": {}, "gemini": {},
	"azure": {}, "azure_ai": {}, "vertex": {}, "vertex_ai": {}, "bedrock": {},
	"deepseek": {}, "fireworks": {}, "fireworks_ai": {}, "groq": {}, "mistral": {},
	"cohere": {}, "xai": {}, "together": {}, "together_ai": {}, "togethercomputer": {},
	"perplexity": {}, "deepinfra": {}, "nscale": {}, "nebius": {}, "hyperbolic": {},
	"lambda": {}, "lambda_ai": {}, "crusoe": {}, "gradient_ai": {}, "watsonx": {},
	"databricks": {}, "github_copilot": {}, "copilot": {}, "github": {}, "moonshot": {},
	"moonshotai": {}, "meta": {}, "meta_llama": {}, "meta-llama": {}, "qwen": {},
	"alibaba": {}, "dashscope": {}, "minimax": {}, "zai": {}, "novita": {},
	"sambanova": {}, "ovhcloud": {}, "heroku": {}, "publicai": {},
	"us": {}, "eu": {}, "au": {}, "jp": {}, "apac": {}, "global": {}, "ap": {},
	"sa": {}, "ca": {}, "me": {}, "emea": {},
}

// modelFamilySegments are vendor words that can be part of a bare model family
// slug (deepseek-coder, qwen-coder, mistral-large). They may be stripped when
// they are unambiguously route prefixes in slash/dot notation, but not after
// everything has been folded into a final hyphenated model slug.
var modelFamilySegments = map[string]struct{}{
	"anthropic": {}, "openai": {}, "google": {}, "gemini": {},
	"deepseek": {}, "mistral": {}, "cohere": {}, "xai": {},
	"qwen": {}, "alibaba": {}, "meta": {}, "meta_llama": {},
	"meta-llama": {}, "moonshot": {}, "moonshotai": {}, "minimax": {},
}

var (
	reDateCompact = regexp.MustCompile(`-\d{8}$`)
	reDateDashed  = regexp.MustCompile(`-20\d{2}-\d{2}-\d{2}$`)
)

// canonical reduces a raw model identifier to a bare, separator-normalized slug.
func canonical(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	// Drop slash-delimited gateway prefixes ("openrouter/anthropic/x") by keeping
	// the final path segment.
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	} else if strings.Contains(s, ".") {
		// Strip dotted route prefixes ("us.anthropic.claude-...") while the dot
		// boundaries still distinguish routing segments from model-family tokens.
		segs := strings.Split(s, ".")
		start := 0
		for start < len(segs)-1 {
			if _, ok := prefixSegments[segs[start]]; !ok {
				break
			}
			start++
		}
		s = strings.Join(segs[start:], ".")
	}
	// Unify separators to '-'.
	s = strings.NewReplacer(".", "-", "_", "-", " ", "-", ":", "-", "@", "-").Replace(s)
	s = strings.Trim(s, "-")
	// Strip trailing date suffixes (repeated, best-effort).
	for {
		before := s
		s = reDateCompact.ReplaceAllString(s, "")
		s = reDateDashed.ReplaceAllString(s, "")
		s = strings.Trim(s, "-")
		if s == before {
			break
		}
	}
	// Strip leading vendor/gateway prefix tokens.
	tokens := splitNonEmpty(s, "-")
	for len(tokens) > 1 {
		if _, ok := prefixSegments[tokens[0]]; !ok {
			break
		}
		if _, family := modelFamilySegments[tokens[0]]; family {
			break
		}
		tokens = tokens[1:]
	}
	return strings.Join(tokens, "-")
}

func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// index maps canonical slugs and exact keys to registry keys.
type index struct {
	byExact     map[string]string
	byCanonical map[string]string
}

func buildIndex(keys []string) *index {
	idx := &index{
		byExact:     make(map[string]string, len(keys)),
		byCanonical: make(map[string]string, len(keys)),
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for _, k := range sorted {
		exact := strings.ToLower(strings.TrimSpace(k))
		if exact != "" {
			if _, exists := idx.byExact[exact]; !exists {
				idx.byExact[exact] = k
			}
		}
		c := canonical(k)
		if c == "" {
			continue
		}
		// Prefer the shortest source key for a canonical form (least noise);
		// ties broken by sorted order.
		if existing, ok := idx.byCanonical[c]; !ok || len(k) < len(existing) {
			idx.byCanonical[c] = k
		}
	}
	return idx
}

// match resolves a raw model id to a registry key via exact then canonical
// equality. No fuzzy fallback: a miss is a miss.
func (idx *index) match(raw string) (string, bool) {
	if exact := strings.ToLower(strings.TrimSpace(raw)); exact != "" {
		if key, ok := idx.byExact[exact]; ok {
			return key, true
		}
	}
	if c := canonical(raw); c != "" {
		if key, ok := idx.byCanonical[c]; ok {
			return key, true
		}
	}
	return "", false
}

// parseRegistry decodes the LiteLLM registry JSON. It skips the non-model
// "sample_spec" sentinel and tolerates per-entry decode noise.
func parseRegistry(body []byte) (map[string]litellmEntry, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]litellmEntry, len(raw))
	for key, rawEntry := range raw {
		if key == "sample_spec" {
			continue
		}
		var entry litellmEntry
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			continue
		}
		out[key] = entry
	}
	if len(out) == 0 {
		return nil, errors.New("registry contained no usable entries")
	}
	return out, nil
}

func keysOf(m map[string]litellmEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
