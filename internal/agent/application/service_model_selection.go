package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

func (s *Service) selectChatModel(ctx context.Context, req ChatRequest, botSettings settings.Settings, sessionPrefModelID string) (models.GetResponse, sqlc.Provider, error) {
	if s.modelsService == nil {
		return models.GetResponse{}, sqlc.Provider{}, errors.New("models service not configured")
	}
	modelID := strings.TrimSpace(req.Model)
	providerFilter := strings.TrimSpace(req.Provider)

	// Priority: request model > session preference (issue #879) > bot settings
	// > session history.
	if modelID == "" && providerFilter == "" {
		if value := strings.TrimSpace(sessionPrefModelID); value != "" {
			modelID = value
		} else if value := strings.TrimSpace(botSettings.ChatModelID); value != "" {
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

// sessionModelPreference loads the session's persisted (model, effort) pair
// (issue #879). Both components come back trimmed; empty means "no memory"
// and lets resolution fall through the chain. Missing session / unreadable
// id are treated as no memory, never as an error: the preference must not be
// able to break a turn.
func (s *Service) sessionModelPreference(ctx context.Context, sessionID string) (string, string) {
	id, err := db.ParseUUID(sessionID)
	if err != nil {
		return "", ""
	}
	row, err := s.queries.GetSessionByID(ctx, id)
	if err != nil {
		return "", ""
	}
	modelID := ""
	if row.PreferredChatModelID.Valid {
		modelID = row.PreferredChatModelID.String()
	}
	effort := ""
	if row.PreferredReasoningEffort.Valid {
		effort = strings.TrimSpace(row.PreferredReasoningEffort.String)
	}
	// The pair is one value: a half pair (NULL model, surviving effort) arises
	// when ON DELETE SET NULL clears only the model column, and honoring the
	// effort alone would pin it onto whatever model the chain falls back to —
	// the cross-model effort memory the spec rules out (§3.7). The frontend
	// already treats a NULL model as no memory; match it here.
	if modelID == "" {
		effort = ""
	}
	return modelID, effort
}

// writeBackSessionModelPreference is the per-turn write-back (issue #879,
// spec v2 §3.3). The gate is data, not entry: carriesPair is true only when
// the request itself carried model/effort — the web composer omits them when
// the pair is default-sourced (never picked), and channel requests
// structurally never carry them, so both keep the session's columns NULL and
// follow the bot default live. The written value is the RESOLVED pair (the
// chain above has already reconciled it: request wins, so chatModel IS the
// carried model, validated). The UPDATE is skipped when the stored pair
// (loaded once at resolve time) already matches, so steady state costs no
// writes. Best-effort: a failed write is logged, never fails the turn — the
// next send is the retry (P7′).
func (s *Service) writeBackSessionModelPreference(ctx context.Context, sessionIDRaw string, carriesPair bool, chatModel models.GetResponse, reasoning *models.ReasoningConfig, storedModelID, storedEffort string) {
	if !carriesPair {
		return
	}
	sessionID, err := db.ParseUUID(sessionIDRaw)
	if err != nil {
		return
	}
	modelID, err := db.ParseUUID(chatModel.ID)
	if err != nil {
		return
	}
	effort := ""
	if reasoning != nil {
		switch {
		case reasoning.Active:
			effort = strings.TrimSpace(reasoning.Effort)
		case reasoning.Disabled:
			// Same vocabulary as bot settings ("disable" = thinking off) so a
			// seeded picker round-trips the user's choice instead of falling
			// back to the model's default-on tier.
			effort = "disable"
		}
	}
	if strings.EqualFold(storedModelID, modelID.String()) && storedEffort == effort {
		return
	}
	if err := s.queries.UpdateSessionModelPreference(ctx, sqlc.UpdateSessionModelPreferenceParams{
		ID:                       sessionID,
		PreferredChatModelID:     modelID,
		PreferredReasoningEffort: pgtype.Text{String: effort, Valid: effort != ""},
	}); err != nil {
		s.logger.Warn("write-back session model preference",
			slog.String("session_id", sessionIDRaw),
			slog.Any("error", err),
		)
	}
}

// ReconcileSessionModelPreference validates and normalizes a candidate pair
// (issue #879, spec v2 §3.3): the model must exist on an enabled provider
// (empty ref falls back to the bot default); an illegal or empty effort
// silently resolves to the model's default tier, so the DB never stores an
// illegal pair (S6). Returns the model's UUID and the normalized effort.
// Shared by the picker PATCH and the first-send INSERT so both write points
// reconcile identically.
func (s *Service) ReconcileSessionModelPreference(ctx context.Context, botID, modelRef, effort string) (string, string, error) {
	modelID := strings.TrimSpace(modelRef)
	if modelID == "" {
		botSettings, err := s.loadBotSettings(ctx, botID)
		if err != nil {
			return "", "", err
		}
		modelID = strings.TrimSpace(botSettings.ChatModelID)
	}
	if modelID == "" {
		return "", "", errors.New("chat model is required to set session model preference")
	}
	chatModel, provider, err := s.fetchChatModel(ctx, modelID)
	if err != nil {
		return "", "", err
	}
	reconciled := ""
	if rc := resolveReasoningConfig(chatModel, settings.Settings{}, effort, "", provider.ClientType); rc != nil {
		switch {
		case rc.Active:
			reconciled = strings.TrimSpace(rc.Effort)
		case rc.Disabled:
			reconciled = "disable"
		}
	}
	return chatModel.ID, reconciled, nil
}

// PatchSessionModelPreference handles the picker PATCH (issue #879, spec
// §3.3 / §5-2): reconcile the pair and write it. Reconcile = the effort must
// be legal for the target model; an illegal or empty tier silently resolves
// to the model default, so the DB never stores an illegal pair.
//
// Target model resolution when the request omits it: existing session
// preference, then bot default. Effort-only patches therefore reconcile
// against the model the session would actually use. An explicit but
// unresolvable/empty model reference is an error (the FK would reject it
// anyway; fail loudly instead of degrading).
func (s *Service) PatchSessionModelPreference(ctx context.Context, botID, sessionID string, modelRef, effort *string) error {
	sessionUUID, err := db.ParseUUID(sessionID)
	if err != nil {
		return fmt.Errorf("invalid session id: %w", err)
	}
	sess, err := s.queries.GetSessionByID(ctx, sessionUUID)
	if err != nil {
		return err
	}

	modelID := ""
	if modelRef != nil {
		modelID = strings.TrimSpace(*modelRef)
	}
	if modelID == "" && sess.PreferredChatModelID.Valid {
		modelID = sess.PreferredChatModelID.String()
	}
	targetEffort := ""
	if effort != nil {
		targetEffort = strings.TrimSpace(*effort)
	} else if sess.PreferredReasoningEffort.Valid {
		targetEffort = strings.TrimSpace(sess.PreferredReasoningEffort.String)
	}

	reconciledModelID, reconciledEffort, err := s.ReconcileSessionModelPreference(ctx, botID, modelID, targetEffort)
	if err != nil {
		return err
	}
	return s.queries.UpdateSessionModelPreference(ctx, sqlc.UpdateSessionModelPreferenceParams{
		ID:                       sessionUUID,
		PreferredChatModelID:     db.ParseUUIDOrEmpty(reconciledModelID),
		PreferredReasoningEffort: pgtype.Text{String: reconciledEffort, Valid: reconciledEffort != ""},
	})
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
