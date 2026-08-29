package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

func (s *Service) selectChatModel(ctx context.Context, req ChatRequest, botSettings settings.Settings) (models.GetResponse, sqlc.Provider, error) {
	if s.modelsService == nil {
		return models.GetResponse{}, sqlc.Provider{}, errors.New("models service not configured")
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
		return models.GetResponse{}, sqlc.Provider{}, errors.New("chat model not configured: specify model in request or bot settings")
	}

	if providerFilter == "" {
		return s.fetchChatModel(ctx, modelID)
	}

	candidates, err := s.listCandidates(ctx, providerFilter)
	if err != nil {
		return models.GetResponse{}, sqlc.Provider{}, err
	}
	for _, m := range candidates {
		if matchesModelReference(m, modelID) {
			prov, err := models.FetchProviderByID(ctx, s.queries, m.ProviderID)
			if err != nil {
				return models.GetResponse{}, sqlc.Provider{}, err
			}
			if err := validateSelectedChatModel(m, prov); err != nil {
				return models.GetResponse{}, sqlc.Provider{}, err
			}
			if !prov.Enable {
				return models.GetResponse{}, sqlc.Provider{}, fmt.Errorf("chat model provider %s is disabled", prov.Name)
			}
			return m, prov, nil
		}
	}
	return models.GetResponse{}, sqlc.Provider{}, fmt.Errorf("chat model %q not found for provider %q", modelID, providerFilter)
}

// latestSessionModelID returns the models.id UUID of the most recent history
// message in the session that recorded one, or "" when the session has no
// model-bearing history yet.
func (s *Service) latestSessionModelID(ctx context.Context, sessionID string) string {
	return models.LatestSessionModelID(ctx, s.queries, sessionID)
}

func (s *Service) fetchChatModel(ctx context.Context, modelID string) (models.GetResponse, sqlc.Provider, error) {
	modelRef := strings.TrimSpace(modelID)
	if modelRef == "" {
		return models.GetResponse{}, sqlc.Provider{}, errors.New("model id is required")
	}

	// Support both model UUID and model_id slug. UUID-formatted slugs still
	// work because we fall back to GetByModelID when UUID lookup misses.
	var model models.GetResponse
	var err error
	if _, parseErr := db.ParseUUID(modelRef); parseErr == nil {
		model, err = s.modelsService.GetByID(ctx, modelRef)
		if err == nil {
			goto resolved
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return models.GetResponse{}, sqlc.Provider{}, err
		}
	}
	model, err = s.modelsService.GetByModelID(ctx, modelRef)
	if err != nil {
		return models.GetResponse{}, sqlc.Provider{}, err
	}

resolved:
	prov, err := models.FetchProviderByID(ctx, s.queries, model.ProviderID)
	if err != nil {
		return models.GetResponse{}, sqlc.Provider{}, err
	}
	if err := validateSelectedChatModel(model, prov); err != nil {
		return models.GetResponse{}, sqlc.Provider{}, err
	}
	if !prov.Enable {
		return models.GetResponse{}, sqlc.Provider{}, fmt.Errorf("chat model provider %s is disabled", prov.Name)
	}
	return model, prov, nil
}

func validateSelectedChatModel(model models.GetResponse, provider sqlc.Provider) error {
	if model.Type != models.ModelTypeChat {
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

func isImageOnlyChatModel(model models.GetResponse, provider sqlc.Provider) bool {
	return models.IsImageOnlyChatModel(model, provider)
}

func matchesModelReference(model models.GetResponse, modelRef string) bool {
	ref := strings.TrimSpace(modelRef)
	if ref == "" {
		return false
	}
	return model.ID == ref || model.ModelID == ref
}

func (s *Service) listCandidates(ctx context.Context, providerFilter string) ([]models.GetResponse, error) {
	var all []models.GetResponse
	var err error
	if providerFilter != "" {
		all, err = s.modelsService.ListEnabledByProviderClientType(ctx, models.ClientType(providerFilter))
	} else {
		all, err = s.modelsService.ListEnabledByType(ctx, models.ModelTypeChat)
	}
	if err != nil {
		return nil, err
	}
	filtered := make([]models.GetResponse, 0, len(all))
	for _, m := range all {
		if m.Type == models.ModelTypeChat {
			filtered = append(filtered, m)
		}
	}
	return filtered, nil
}
