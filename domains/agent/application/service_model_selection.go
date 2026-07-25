package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/memohai/memoh/domains/api/setting"
	modeldomain "github.com/memohai/memoh/domains/model"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	"github.com/memohai/memoh/internal/db"
)

func (s *Service) selectChatModel(ctx context.Context, req ChatRequest, botSettings setting.Settings) (modelcatalog.GetResponse, modelcatalog.ResolvedProvider, error) {
	if s.modelsService == nil {
		return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, errors.New("models service not configured")
	}
	modelID := strings.TrimSpace(req.Model)
	providerFilter := strings.TrimSpace(req.Provider)

	// Priority: request model > bot settings > session history.
	if modelID == "" && providerFilter == "" {
		if value := strings.TrimSpace(botSettings.ChatModelID); value != "" {
			modelID = value
		} else {
			// Resumed turns (ask_user answers, tool approval decisions) carry no
			// request model, and the bot may have no default chat model when the
			// web client selects the model per request. Continue with the model
			// that produced the session's latest round.
			modelID = s.latestSessionModelID(ctx, req.ThreadID)
		}
	}

	if modelID == "" {
		return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, errors.New("chat model not configured: specify model in request or bot settings")
	}

	if providerFilter == "" {
		return s.fetchChatModel(ctx, modelID)
	}

	candidates, err := s.listCandidates(ctx, providerFilter)
	if err != nil {
		return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, err
	}
	for _, m := range candidates {
		if matchesModelReference(m, modelID) {
			prov, err := modelcatalog.FetchProviderByID(ctx, s.modelProviderResolver, m.ProviderID)
			if err != nil {
				return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, err
			}
			if err := validateSelectedChatModel(m, prov); err != nil {
				return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, err
			}
			if !prov.Enable {
				return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, fmt.Errorf("chat model provider %s is disabled", prov.Name)
			}
			return m, prov, nil
		}
	}
	return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, fmt.Errorf("chat model %q not found for provider %q", modelID, providerFilter)
}

// latestSessionModelID returns the modelcatalog.id UUID of the most recent history
// message in the session that recorded one, or "" when the session has no
// model-bearing history yet.
func (s *Service) latestSessionModelID(ctx context.Context, sessionID string) string {
	if s.latestSessionModels == nil || strings.TrimSpace(sessionID) == "" {
		return ""
	}
	modelID, err := s.latestSessionModels.LatestSessionModelID(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(modelID)
}

func (s *Service) fetchChatModel(ctx context.Context, modelID string) (modelcatalog.GetResponse, modelcatalog.ResolvedProvider, error) {
	modelRef := strings.TrimSpace(modelID)
	if modelRef == "" {
		return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, errors.New("model id is required")
	}

	// Support both model UUID and model_id slug. UUID-formatted slugs still
	// work because we fall back to GetByModelID when UUID lookup misses.
	var model modelcatalog.GetResponse
	var err error
	if _, parseErr := db.ParseUUID(modelRef); parseErr == nil {
		model, err = s.modelsService.GetByID(ctx, modelRef)
		if err == nil {
			goto resolved
		}
		if !errors.Is(err, modelcatalog.ErrModelNotFound) {
			return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, err
		}
	}
	model, err = s.modelsService.GetByModelID(ctx, modelRef)
	if err != nil {
		return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, err
	}

resolved:
	prov, err := modelcatalog.FetchProviderByID(ctx, s.modelProviderResolver, model.ProviderID)
	if err != nil {
		return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, err
	}
	if err := validateSelectedChatModel(model, prov); err != nil {
		return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, err
	}
	if !prov.Enable {
		return modelcatalog.GetResponse{}, modelcatalog.ResolvedProvider{}, fmt.Errorf("chat model provider %s is disabled", prov.Name)
	}
	return model, prov, nil
}

func validateSelectedChatModel(model modelcatalog.GetResponse, provider modelcatalog.ResolvedProvider) error {
	if model.Type != modeldomain.ModelTypeChat {
		return errors.New("model is not a chat model")
	}
	if !model.Enable {
		return fmt.Errorf("chat model %s is disabled", model.ModelID)
	}
	if isImageOnlyChatModel(model, provider) {
		return fmt.Errorf("chat model %s is an image generation model; configure it as the bot image model and use a chat model for conversation", model.ModelID)
	}
	return nil
}

func isImageOnlyChatModel(model modelcatalog.GetResponse, provider modelcatalog.ResolvedProvider) bool {
	// A model that advertises tool calling is usable as a chat model regardless
	// of its name — this is the escape hatch for the name heuristic below, so a
	// tool-capable model that merely looks like an image model (or a genuine
	// multimodal chat model) is never blocked.
	if model.HasCompatibility(modeldomain.CompatToolCall) {
		return false
	}
	lowerModel := strings.ToLower(strings.TrimSpace(model.ModelID))
	if lowerModel == "" {
		return false
	}
	if isKnownStandaloneImageModelID(lowerModel) {
		return true
	}
	lowerBase := strings.ToLower(strings.TrimSpace(provider.BaseURL))
	if strings.Contains(lowerBase, "dashscope") && strings.Contains(lowerModel, "image") {
		return true
	}
	if !model.HasCompatibility(modeldomain.CompatImageOutput) {
		return false
	}
	ct := provider.ClientType
	if ct != modeldomain.ClientTypeOpenAICompletions && ct != modeldomain.ClientTypeOpenAIResponses {
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
// bare "flux". A tool-calling model bypasses this check entirely (see
// isImageOnlyChatModel), which is the override when a name collision is wrong.
func isKnownStandaloneImageModelID(lowerModel string) bool {
	return strings.HasPrefix(lowerModel, "qwen-image") ||
		strings.HasPrefix(lowerModel, "wan2") ||
		strings.HasPrefix(lowerModel, "wanx") ||
		strings.HasPrefix(lowerModel, "z-image") ||
		strings.HasPrefix(lowerModel, "flux-") ||
		strings.HasPrefix(lowerModel, "flux.") ||
		strings.HasPrefix(lowerModel, "flux1") ||
		strings.HasPrefix(lowerModel, "stable-diffusion") ||
		strings.HasPrefix(lowerModel, "gpt-image") ||
		strings.HasPrefix(lowerModel, "dall-e") ||
		strings.Contains(lowerModel, "seedream")
}

func matchesModelReference(model modelcatalog.GetResponse, modelRef string) bool {
	ref := strings.TrimSpace(modelRef)
	if ref == "" {
		return false
	}
	return model.ID == ref || model.ModelID == ref
}

func (s *Service) listCandidates(ctx context.Context, providerFilter string) ([]modelcatalog.GetResponse, error) {
	var all []modelcatalog.GetResponse
	var err error
	if providerFilter != "" {
		all, err = s.modelsService.ListEnabledByProviderClientType(ctx, modeldomain.ClientType(providerFilter))
	} else {
		all, err = s.modelsService.ListEnabledByType(ctx, modeldomain.ModelTypeChat)
	}
	if err != nil {
		return nil, err
	}
	filtered := make([]modelcatalog.GetResponse, 0, len(all))
	for _, m := range all {
		if m.Type == modeldomain.ModelTypeChat {
			filtered = append(filtered, m)
		}
	}
	return filtered, nil
}
