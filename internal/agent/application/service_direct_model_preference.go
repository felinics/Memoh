package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

var ErrDirectModelUnavailable = errors.New("direct runtime model unavailable")

// Direct runtimes use agent-owned model IDs, never models-table UUIDs. Keep
// their preference in a separate column so the native foreign key remains
// meaningful and runtime-owned conversation metadata is not read/modified.
func (s *Service) reconcileDirectModelPreference(ctx context.Context, botID, botAgentID, runtimeType, modelID, effort string) (string, string, error) {
	provider, ok := s.externalDrivers[runtimeType].(external.ModelCatalogProvider)
	if !ok {
		return "", "", external.ErrModelCatalogUnavailable
	}
	catalog, err := provider.ModelCatalog(ctx, botID, botAgentID)
	if err != nil {
		return "", "", err
	}
	return reconcileDirectPair(catalog, modelID, effort)
}

func reconcileDirectPair(catalog external.ModelCatalog, modelID, effort string) (string, string, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		modelID = strings.TrimSpace(catalog.ConfiguredModelID)
	}
	if modelID == "" {
		for _, model := range catalog.Models {
			if model.Default {
				modelID = model.ID
				break
			}
		}
	}
	for _, model := range catalog.Models {
		if model.ID != modelID && (modelID == "" || model.ResolvedModelID != modelID) {
			continue
		}
		effort = strings.TrimSpace(effort)
		for _, option := range model.ReasoningEfforts {
			if effort != "" && option.ID == effort {
				return modelID, effort, nil
			}
		}
		// The model's declared default wins when changing models. Do not carry
		// another model's effort or substitute the agent-wide configured effort.
		for _, option := range model.ReasoningEfforts {
			if option.ID == model.DefaultReasoningEffort {
				return modelID, option.ID, nil
			}
		}
		return modelID, "", nil
	}
	return "", "", fmt.Errorf("%w: %q", ErrDirectModelUnavailable, modelID)
}

func (s *Service) applyDirectModelPreference(ctx context.Context, req ChatRequest, sess session.Thread) (ChatRequest, error) {
	if !session.IsDirectRuntime(sess) || req.SessionType == sessionmode.Schedule {
		return req, nil
	}
	carries := strings.TrimSpace(req.Model) != "" || strings.TrimSpace(req.ReasoningEffort) != ""
	if !carries {
		req.Model = sess.PreferredExternalModelID
		if req.Model != "" {
			req.ReasoningEffort = sess.PreferredReasoningEffort
		}
		return req, nil
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = sess.PreferredExternalModelID
	}
	effort := strings.TrimSpace(req.ReasoningEffort)
	// Catalog discovery is expensive (Claude Code spawns a CLI and waits for
	// initialize; Codex calls model/list or the custom /models endpoint). A
	// remembered Web session repeats its stored pair on every send, so only
	// re-validate when the request actually differs from what the session
	// already reconciled. The revision still advances below so an older picker
	// PATCH cannot overwrite this send.
	if modelID == "" || modelID != sess.PreferredExternalModelID || effort != sess.PreferredReasoningEffort {
		var err error
		modelID, effort, err = s.reconcileDirectModelPreference(ctx, req.BotID, sess.BotAgentID, sess.RuntimeType, modelID, effort)
		if err != nil {
			return req, err
		}
	}
	req.Model = modelID
	req.ReasoningEffort = effort
	// Best-effort like the native write-back: a preference write must not be
	// able to break a turn. The request pair still applies to this turn and
	// the next send retries the persist.
	err := s.queries.UpdateSessionModelPreference(ctx, sqlc.UpdateSessionModelPreferenceParams{
		ID: db.ParseUUIDOrEmpty(sess.ID), PreferredExternalModelID: pgtype.Text{String: modelID, Valid: true}, PreferredReasoningEffort: pgtype.Text{String: effort, Valid: effort != ""},
	})
	if err != nil {
		s.logger.Warn("write-back direct model preference",
			slog.String("session_id", sess.ID),
			slog.Any("error", err),
		)
	}
	return req, nil
}
