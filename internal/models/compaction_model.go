package models

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// Compaction model resolution failures. Automatic triggers map them to a
// silent skip; manual surfaces translate them into user-facing errors.
var (
	ErrCompactionModelNotConfigured     = errors.New("no compaction or chat model configured")
	ErrCompactionModelNotChat           = errors.New("compaction model is not a chat model")
	ErrCompactionModelDisabled          = errors.New("compaction model is disabled")
	ErrCompactionProviderDisabled       = errors.New("compaction model provider is disabled")
	ErrCompactionOutputLimitUnsupported = errors.New("compaction model provider does not enforce the output limit")
	ErrCompactionWindowUnknown          = errors.New("compaction model does not declare a context window")
)

// CompactionModelResolution is the resolved summarizer identity. Credentials
// stay with the caller: orchestration surfaces own auth context, the
// compaction engine only receives a completed contract.
type CompactionModelResolution struct {
	Model    GetResponse
	Provider sqlc.Provider
	// WindowTokens is the summarizer model's declared context window.
	WindowTokens int
}

// ResolveCompactionModel picks the first non-empty candidate model id and
// validates that it can act as a summarizer: a chat-type, enabled model on an
// enabled LLM provider that honors output caps and declares a context window
// (the summary budget derives from it, so an unknown window fails closed).
// Callers compose the candidate chain: automatic triggers pass the override
// and the turn's actually-resolved model; manual surfaces prefer the
// session's latest model before the bot default.
func ResolveCompactionModel(
	ctx context.Context,
	modelsService *Service,
	queries dbstore.Queries,
	candidates ...string,
) (CompactionModelResolution, error) {
	modelID := ""
	for _, candidate := range candidates {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			modelID = candidate
			break
		}
	}
	if modelID == "" {
		return CompactionModelResolution{}, ErrCompactionModelNotConfigured
	}
	model, err := modelsService.GetByID(ctx, modelID)
	if err != nil {
		return CompactionModelResolution{}, fmt.Errorf("resolve compaction model: %w", err)
	}
	if model.Type != ModelTypeChat {
		return CompactionModelResolution{}, fmt.Errorf("%w: %s", ErrCompactionModelNotChat, model.ModelID)
	}
	if !model.Enable {
		return CompactionModelResolution{}, fmt.Errorf("%w: %s", ErrCompactionModelDisabled, model.ModelID)
	}
	provider, err := FetchProviderByID(ctx, queries, model.ProviderID)
	if err != nil {
		return CompactionModelResolution{}, fmt.Errorf("resolve compaction provider: %w", err)
	}
	if !provider.Enable {
		return CompactionModelResolution{}, fmt.Errorf("%w: %s", ErrCompactionProviderDisabled, provider.Name)
	}
	if IsImageOnlyChatModel(model, provider) {
		return CompactionModelResolution{}, fmt.Errorf("%w: %s is a dedicated image model", ErrCompactionModelNotChat, model.ModelID)
	}
	clientType := ClientType(strings.TrimSpace(provider.ClientType))
	if !IsLLMClientType(clientType) {
		return CompactionModelResolution{}, fmt.Errorf("%w: %s", ErrCompactionModelNotChat, provider.ClientType)
	}
	if !EnforcesMaxOutputTokens(clientType) {
		return CompactionModelResolution{}, ErrCompactionOutputLimitUnsupported
	}
	if model.Config.ContextWindow == nil || *model.Config.ContextWindow <= 0 {
		return CompactionModelResolution{}, fmt.Errorf("%w: %s", ErrCompactionWindowUnknown, model.ModelID)
	}
	return CompactionModelResolution{
		Model:        model,
		Provider:     provider,
		WindowTokens: *model.Config.ContextWindow,
	}, nil
}

// IsCompactionModelUnavailable reports whether the resolution failure means
// "this bot cannot compact right now" rather than an infrastructure error.
func IsCompactionModelUnavailable(err error) bool {
	return errors.Is(err, ErrCompactionModelNotConfigured) ||
		errors.Is(err, ErrCompactionModelNotChat) ||
		errors.Is(err, ErrCompactionModelDisabled) ||
		errors.Is(err, ErrCompactionProviderDisabled) ||
		errors.Is(err, ErrCompactionOutputLimitUnsupported) ||
		errors.Is(err, ErrCompactionWindowUnknown)
}

// LatestSessionModelID returns the models.id UUID of the most recent history
// message in the session that recorded one, or "" when the session has no
// model-bearing history yet.
func LatestSessionModelID(ctx context.Context, queries dbstore.Queries, sessionID string) string {
	if queries == nil {
		return ""
	}
	parsed, err := db.ParseUUID(sessionID)
	if err != nil {
		return ""
	}
	modelID, err := queries.GetLatestSessionModelID(ctx, parsed)
	if err != nil || !modelID.Valid {
		return ""
	}
	return modelID.String()
}

// IsImageOnlyChatModel reports whether a chat-typed model is actually a
// dedicated image generator that cannot summarize text.
func IsImageOnlyChatModel(model GetResponse, provider sqlc.Provider) bool {
	// A model that advertises tool calling is usable as a chat model regardless
	// of its name — this is the escape hatch for the name heuristic below, so a
	// tool-capable model that merely looks like an image model (or a genuine
	// multimodal chat model) is never blocked.
	if model.HasCompatibility(CompatToolCall) {
		return false
	}
	lowerModel := strings.ToLower(strings.TrimSpace(model.ModelID))
	if lowerModel == "" {
		return false
	}
	if isKnownStandaloneImageModelID(lowerModel) {
		return true
	}
	lowerBase := strings.ToLower(providerConfigString(provider.Config, "base_url"))
	if strings.Contains(lowerBase, "dashscope") && strings.Contains(lowerModel, "image") {
		return true
	}
	if !model.HasCompatibility(CompatImageOutput) {
		return false
	}
	ct := ClientType(provider.ClientType)
	if ct != ClientTypeOpenAICompletions && ct != ClientTypeOpenAIResponses {
		return false
	}
	return strings.Contains(lowerBase, "maas.aliyuncs.com") ||
		strings.Contains(lowerBase, "api.openai.com") ||
		strings.Contains(lowerBase, "volces.com") ||
		strings.Contains(lowerBase, "bytepluses.com") ||
		strings.Contains(lowerBase, "siliconflow")
}

// isKnownStandaloneImageModelID matches the naming conventions of dedicated
// text-to-image model families. Prefixes are kept specific enough not to catch
// ordinary chat models that merely share a leading token — e.g. "wan2"/"wanx"
// (Alibaba Wan image/video) rather than a bare "wan" that would also match a
// chat model like "wanjuan-chat", and "flux-"/"flux."/"flux1" rather than a
// bare "flux". The family name lives in the last path segment for namespaced
// IDs like accounts/fireworks/models/flux-1-dev, so prefixes match against
// that segment — never against a namespace, which could collide the other
// way. A tool-calling model bypasses this check entirely (see
// IsImageOnlyChatModel), which is the override when a name collision is wrong.
func isKnownStandaloneImageModelID(lowerModel string) bool {
	base := lowerModel
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	return strings.HasPrefix(base, "qwen-image") ||
		strings.HasPrefix(base, "wan2") ||
		strings.HasPrefix(base, "wanx") ||
		strings.HasPrefix(base, "z-image") ||
		strings.HasPrefix(base, "flux-") ||
		strings.HasPrefix(base, "flux.") ||
		strings.HasPrefix(base, "flux1") ||
		strings.HasPrefix(base, "stable-diffusion") ||
		strings.HasPrefix(base, "gpt-image") ||
		strings.HasPrefix(base, "dall-e") ||
		strings.Contains(base, "seedream")
}
