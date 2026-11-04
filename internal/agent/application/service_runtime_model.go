package application

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
)

// RuntimeModel reports the model a direct runtime session will use next turn
// and the models its catalog offers. The session's remembered pair wins, then
// the Agent's configured model and effort, then the catalog default: the
// chain the Web composer displays. Runtimes that do not choose a model per
// session report Supported=false.
func (s *Service) RuntimeModel(ctx context.Context, request RuntimeControlRequest) (out turn.RuntimeModelState, err error) {
	defer func() { err = publicRuntimeControlError(err) }()
	sess, _, _, err := s.runtimeControlTarget(ctx, request)
	if err != nil {
		return out, err
	}
	state, _, err := s.runtimeModelState(ctx, sess)
	return state, err
}

// SetRuntimeModel remembers a model and effort for a direct runtime session.
// An empty ModelID keeps the session's current model, so a bare effort change
// on a session still following its default pins that default together with
// the effort, as the Web picker does. Picking a model without an effort takes
// that model's default effort rather than carrying the previous model's.
func (s *Service) SetRuntimeModel(ctx context.Context, request RuntimeControlRequest) (out turn.RuntimeModelState, err error) {
	defer func() { err = publicRuntimeControlError(err) }()
	sess, _, _, err := s.runtimeControlTarget(ctx, request)
	if err != nil {
		return out, err
	}
	state, catalog, err := s.runtimeModelState(ctx, sess)
	if err != nil || !state.Supported {
		if err == nil {
			err = external.ErrControlUnsupported
		}
		return out, err
	}
	modelID, effort := strings.TrimSpace(request.ModelID), strings.TrimSpace(request.ReasoningEffort)
	if modelID == "" {
		modelID = state.ModelID
		if effort == "" {
			effort = state.ReasoningEffort
		}
	}
	modelID, effort, err = reconcileDirectPair(catalog, modelID, effort)
	if err != nil {
		return out, apperror.Wrap(apperror.CodeRuntimeControlModeUnavailable, err, nil)
	}
	if requested := strings.TrimSpace(request.ReasoningEffort); requested != "" && requested != effort {
		return out, apperror.New(apperror.CodeRuntimeControlModeUnavailable, nil)
	}
	err = s.compareAndSetModelPreference(ctx, db.ParseUUIDOrEmpty(sess.ID), sess.RuntimeType, pgtype.UUID{},
		pgtype.Text{String: modelID, Valid: modelID != ""}, effort, sess.ModelPreferenceRevision)
	if errors.Is(err, ErrModelPreferenceConflict) {
		return out, apperror.New(apperror.CodeSessionModelPreferenceConflict, nil)
	}
	if err != nil {
		return out, err
	}
	state.ModelID, state.ReasoningEffort = modelID, effort
	return state, nil
}

// runtimeModelState reads the session's catalog once; the catalog is returned
// so a write can reconcile against the same snapshot the user was shown.
func (s *Service) runtimeModelState(ctx context.Context, sess session.Thread) (turn.RuntimeModelState, external.ModelCatalog, error) {
	out := turn.RuntimeModelState{Models: []turn.RuntimeModelOption{}}
	provider, ok := external.As[external.ModelCatalogProvider](s.runtimes, session.RuntimeTypeOf(sess))
	if !ok {
		return out, external.ModelCatalog{}, nil
	}
	remembered := strings.TrimSpace(sess.PreferredExternalModelID)
	catalog, err := provider.ModelCatalog(ctx, external.ModelCatalogRequest{
		BotID: sess.BotID, BotAgentID: sess.BotAgentID,
		ProjectPath: metadataString(runtimeSessionMeta(sess), "project_path"),
		ModelID:     remembered, ResolveDefaults: true,
	})
	if err != nil {
		return out, catalog, err
	}
	out.Supported = true
	for _, model := range catalog.Models {
		option := turn.RuntimeModelOption{
			ID: model.ID, Name: strings.TrimSpace(model.Name), Default: model.Default,
			DefaultReasoningEffort: model.DefaultReasoningEffort, ReasoningEfforts: []string{},
		}
		if option.Name == "" {
			option.Name = model.ID
		}
		for _, effort := range model.ReasoningEfforts {
			if id := strings.TrimSpace(effort.ID); id != "" {
				option.ReasoningEfforts = append(option.ReasoningEfforts, id)
			}
		}
		out.Models = append(out.Models, option)
	}
	if remembered != "" {
		out.ModelID = catalogModelID(catalog, remembered)
		out.ReasoningEffort = strings.TrimSpace(sess.PreferredReasoningEffort)
		return out, catalog, nil
	}
	// The driver applies the Agent's configured pair when a turn carries
	// none, so that is what an unremembered session runs with.
	defaultID, _, _ := reconcileDirectPair(catalog, "", "")
	out.ModelID = catalogModelID(catalog, defaultID)
	out.ReasoningEffort = strings.TrimSpace(catalog.ConfiguredReasoningEffort)
	if model, ok := out.Model(); ok && (out.ReasoningEffort == "" || !slices.Contains(model.ReasoningEfforts, out.ReasoningEffort)) {
		out.ReasoningEffort = model.DefaultReasoningEffort
	}
	return out, catalog, nil
}

// catalogModelID maps a remembered id, which may be the full name the
// runtime resolved an alias to, onto the picker id shown in the catalog.
func catalogModelID(catalog external.ModelCatalog, modelID string) string {
	for _, model := range catalog.Models {
		if model.ID == modelID || (model.ResolvedModelID != "" && model.ResolvedModelID == modelID) {
			return model.ID
		}
	}
	return modelID
}
