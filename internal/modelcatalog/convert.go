// Package modelcatalog reads public models.dev metadata. Authenticated Cloud
// relay catalogs have a different contract and must not be decoded here.
package modelcatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/models"
)

var (
	ErrSourceNotFound = errors.New("model catalog source not found")
	ErrProtocol       = errors.New("unsupported model catalog protocol")
)

type sourceOption struct {
	Type   string    `json:"type"`
	Values []*string `json:"values"`
	Min    *int      `json:"min"`
	Max    *int      `json:"max"`
}
type sourceModel struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Type             string          `json:"type"`
	Status           string          `json:"status"`
	Reasoning        bool            `json:"reasoning"`
	Options          []sourceOption  `json:"reasoning_options"`
	Interleaved      json.RawMessage `json:"interleaved"`
	ToolCall         bool            `json:"tool_call"`
	StructuredOutput bool            `json:"structured_output"`
	Modalities       *struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Limit *struct {
		Context *int `json:"context"`
		Output  *int `json:"output"`
	} `json:"limit"`
	Provider *struct {
		NPM   string `json:"npm"`
		Shape string `json:"shape"`
	} `json:"provider"`
}
type sourceProvider struct {
	ID     string                     `json:"id"`
	NPM    string                     `json:"npm"`
	Models map[string]json.RawMessage `json:"models"`
}
type catalog map[string]json.RawMessage

// Model is a normalized preview/import entry, never a persisted tenant model.
type Model struct {
	ModelID string             `json:"model_id"`
	Name    string             `json:"name,omitempty"`
	Type    models.ModelType   `json:"type"`
	Config  models.ModelConfig `json:"config"`
}

// Decode only the catalog envelope here. A provider's schema is interpreted
// when that source is requested, so a broken neighbor cannot disable it.
func decode(data []byte) (catalog, error) {
	var out catalog
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}
	for _, raw := range out {
		var provider struct {
			Models map[string]json.RawMessage `json:"models"`
		}
		if json.Unmarshal(raw, &provider) == nil && provider.Models != nil {
			return out, nil
		}
	}
	return nil, errors.New("response contains no provider catalog")
}

func supportedProtocol(protocol string) bool {
	switch models.ClientType(protocol) {
	case models.ClientTypeOpenAICompletions, models.ClientTypeOpenAIResponses,
		models.ClientTypeAnthropicMessages, models.ClientTypeGoogleGenerativeAI, models.ClientTypeOpenCodeGo:
		return true
	default:
		return false
	}
}

func convert(data catalog, source, protocol string) ([]Model, error) {
	rawProvider, ok := data[source]
	if !ok {
		return nil, ErrSourceNotFound
	}
	var provider sourceProvider
	if err := json.Unmarshal(rawProvider, &provider); err != nil {
		return nil, fmt.Errorf("decode catalog source %q: %w", source, err)
	}
	if provider.Models == nil || (provider.ID != "" && provider.ID != source) {
		return nil, errors.New("invalid catalog provider identity")
	}
	out := make([]Model, 0, len(provider.Models))
	for id, encoded := range provider.Models {
		var raw sourceModel
		if err := json.Unmarshal(encoded, &raw); err != nil {
			return nil, fmt.Errorf("decode catalog model %q: %w", id, err)
		}
		if raw.Status == "deprecated" || (raw.Type != "" && raw.Type != "chat") || looksLikeEmbedding(raw.ID) {
			continue
		}
		if strings.TrimSpace(id) == "" || raw.ID != id {
			return nil, errors.New("invalid catalog model identity")
		}
		interleaved := bytes.TrimSpace(raw.Interleaved)
		if len(interleaved) > 0 && string(interleaved) != "null" && string(interleaved) != "true" && string(interleaved) != "false" {
			var replay struct {
				Field string `json:"field"`
			}
			if err := json.Unmarshal(interleaved, &replay); err != nil {
				return nil, fmt.Errorf("invalid catalog replay for %q: %w", id, err)
			}
		}
		if raw.Limit != nil && ((raw.Limit.Context != nil && *raw.Limit.Context < 0) || (raw.Limit.Output != nil && *raw.Limit.Output < 0)) {
			return nil, fmt.Errorf("negative catalog limit for %q", id)
		}
		cfg := modelConfig(raw, modelProtocol(raw, provider, protocol))
		if err := cfg.Validate(models.ModelTypeChat); err != nil {
			return nil, fmt.Errorf("invalid catalog model %q: %w", raw.ID, err)
		}
		name := strings.TrimSpace(raw.Name)
		if name == raw.ID {
			name = ""
		}
		out = append(out, Model{ModelID: raw.ID, Name: name, Type: models.ModelTypeChat, Config: cfg})
	}
	sort.Slice(out, func(i, j int) bool {
		label := func(m Model) string {
			if m.Name != "" {
				return m.Name
			}
			return m.ModelID
		}
		a, b := strings.ToLower(label(out[i])), strings.ToLower(label(out[j]))
		if a == b {
			return out[i].ModelID < out[j].ModelID
		}
		return a < b
	})
	return out, nil
}

var embeddingID = regexp.MustCompile("(?i)(?:^|[^a-z])embed|(?:^|[/_.:-])(?:bge|gte|e5|m3e|voyage|jina-clip)(?:[-_.:]|$)")

func looksLikeEmbedding(id string) bool {
	return embeddingID.MatchString(id) && !strings.Contains(strings.ToLower(id), "rerank")
}

func modelProtocol(m sourceModel, p sourceProvider, protocol string) string {
	if protocol != string(models.ClientTypeOpenCodeGo) {
		return protocol
	}
	npm := p.NPM
	if m.Provider != nil {
		if m.Provider.Shape == "responses" {
			return string(models.ClientTypeOpenAIResponses)
		}
		if m.Provider.Shape == "completions" {
			return string(models.ClientTypeOpenAICompletions)
		}
		if m.Provider.NPM != "" {
			npm = m.Provider.NPM
		}
	}
	switch npm {
	case "@ai-sdk/openai":
		return string(models.ClientTypeOpenAIResponses)
	case "@ai-sdk/anthropic":
		return string(models.ClientTypeAnthropicMessages)
	case "@ai-sdk/google":
		return string(models.ClientTypeGoogleGenerativeAI)
	default:
		return string(models.ClientTypeOpenAICompletions)
	}
}

func knownModalities(values []string) []models.Modality {
	out := []models.Modality{}
	for _, v := range values {
		switch v {
		case "text", "image", "audio", "video", "pdf":
			value := models.Modality(v)
			if !slices.Contains(out, value) {
				out = append(out, value)
			}
		}
	}
	if len(out) == 0 {
		return []models.Modality{models.ModalityText}
	}
	return out
}

func modelConfig(m sourceModel, protocol string) models.ModelConfig {
	cfg := models.ModelConfig{
		SchemaVersion: models.ModelConfigSchemaVersion,
		ToolCall:      m.ToolCall, StructuredOutput: m.StructuredOutput,
		Modalities: &models.ModelModalities{Input: knownModalities(nil), Output: knownModalities(nil)},
	}
	if m.Modalities != nil {
		cfg.Modalities = &models.ModelModalities{Input: knownModalities(m.Modalities.Input), Output: knownModalities(m.Modalities.Output)}
	}
	if desc := strings.TrimSpace(m.Description); desc != "" {
		cfg.Description = &desc
	}
	if m.Limit != nil {
		cfg.ContextWindow = atLeast(m.Limit.Context, 1)
		cfg.MaxOutputTokens = atLeast(m.Limit.Output, 1)
	}
	cfg.Reasoning = reasoningConfig(m, protocol)
	return cfg
}

func atLeast(value *int, floor int) *int {
	if value != nil && *value >= floor {
		n := *value
		return &n
	}
	return nil
}

func reasoningConfig(m sourceModel, protocol string) *models.ModelReasoning {
	if !m.Reasoning {
		return nil
	}
	r := &models.ModelReasoning{}
	if m.Options == nil {
		r.Efforts = []string{"low", "medium", "high"}
		return withReplay(r, m)
	}
	if len(m.Options) == 0 {
		return withReplay(r, m)
	}
	var toggle, budget, offByEffort bool
	var rawMin *int
	for _, option := range m.Options {
		switch option.Type {
		case "toggle":
			toggle = true
		case "effort":
			r.Efforts = nil
			for _, value := range option.Values {
				if value == nil || *value == "" || *value == "default" {
					continue
				}
				if *value == "none" {
					offByEffort = true
					continue
				}
				if !slices.Contains(r.Efforts, *value) {
					r.Efforts = append(r.Efforts, *value)
				}
			}
		case "budget_tokens":
			budget = true
			rawMin = option.Min
			lo, hi := atLeast(option.Min, 0), atLeast(option.Max, 1)
			if lo != nil || hi != nil {
				r.Budget = &models.ReasoningBudget{Min: lo, Max: hi}
			}
		}
	}
	major, minor, claude := claudeVersion(m.ID)
	adaptive := major >= 5 || (major == 4 && minor >= 6)
	switch {
	case protocol == string(models.ClientTypeAnthropicMessages) && claude:
		switch {
		case len(r.Efforts) > 0 && !budget:
			r.Dialect = "adaptive"
		case budget && len(r.Efforts) == 0:
			r.Dialect = "budget"
		case budget && len(r.Efforts) > 0:
			if adaptive {
				r.Dialect = "adaptive"
			} else {
				r.Dialect = "budget"
			}
		}
		if toggle {
			r.Toggle = true
			r.Off = "accepted"
		} else if major == 4 || !adaptive {
			r.Off = "accepted"
		}
	case protocol == string(models.ClientTypeGoogleGenerativeAI):
		if len(r.Efforts) > 0 {
			r.Dialect = "tier"
		} else if budget {
			r.Dialect = "budget"
		}
		r.Off = "rejected"
		if budget && rawMin != nil && *rawMin == 0 {
			r.Toggle = true
			r.Off = "accepted"
		}
	default:
		switch {
		case len(r.Efforts) > 0:
			r.Dialect = "tier"
		case budget:
			r.Dialect = "budget"
		case toggle:
			r.Dialect = "toggle"
		}
		r.Toggle = toggle || offByEffort
		if toggle {
			r.Off = "accepted"
		}
	}
	return withReplay(r, m)
}

func withReplay(r *models.ModelReasoning, m sourceModel) *models.ModelReasoning {
	var value struct {
		Field string `json:"field"`
	}
	if len(m.Interleaved) > 0 && m.Interleaved[0] == '{' && json.Unmarshal(m.Interleaved, &value) == nil {
		if value.Field == "reasoning_content" || value.Field == "reasoning_details" {
			r.Replay = &models.ReasoningReplay{Field: value.Field}
			id := strings.ToLower(m.ID)
			r.Replay.Required = value.Field == "reasoning_content" && (strings.Contains(id, "deepseek") || strings.Contains(id, "kimi"))
		}
	}
	return r
}

var claudePattern = regexp.MustCompile("claude-(?:[a-z]+-)?([0-9]+)(?:[.-]([0-9]{1,2}))?(?:[.:@-]|$)")

func claudeVersion(id string) (int, int, bool) {
	id = strings.ToLower(id)
	if !strings.Contains(id, "claude") {
		return 0, 0, false
	}
	match := claudePattern.FindStringSubmatch(id)
	if len(match) == 0 {
		return 999, 0, true
	}
	var major, minor int
	_, _ = fmt.Sscanf(match[1], "%d", &major)
	if match[2] != "" {
		_, _ = fmt.Sscanf(match[2], "%d", &minor)
	}
	return major, minor, true
}
