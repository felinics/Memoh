package application

import (
	"context"
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	session "github.com/felinics/memoh/internal/chat/thread"
)

// SessionModelState is the model a session's runtime will use and the choices
// the runtime offers. It serves channel commands, which address the session
// the conversation is on rather than the bot: a direct runtime chooses its
// model per session, from its own catalog.
type SessionModelState struct {
	// Runtime is the session's runtime type ("model", "acp_agent", "codex", …).
	Runtime string
	// BotAgentID is the Agent the session runs on; empty for the native runtime.
	BotAgentID string
	// Direct marks a direct external runtime (Codex, Claude Code). Only these
	// carry a catalog; the native runtime keeps the bot-level settings path and
	// ACP keeps its own runtime controls.
	Direct bool
	// ModelID and ReasoningEffort are what the next turn uses: the session's
	// remembered pair, else the Agent's configured model, else the catalog
	// default — the same chain the Web composer displays.
	ModelID         string
	ReasoningEffort string
	// Remembered reports that ModelID came from the session preference rather
	// than a default.
	Remembered bool
	// Revision is the session preference revision a write must name.
	Revision string
	Catalog  external.ModelCatalog
}

// ErrSessionNotInBot means the session id names another bot's session.
var ErrSessionNotInBot = errors.New("session does not belong to bot")

// CurrentModel returns the catalog entry for ModelID, matching either its
// picker id or the full model name the runtime resolved it to.
func (s SessionModelState) CurrentModel() (external.ModelOption, bool) {
	return findCatalogModel(s.Catalog, s.ModelID)
}

func findCatalogModel(catalog external.ModelCatalog, modelID string) (external.ModelOption, bool) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return external.ModelOption{}, false
	}
	for _, model := range catalog.Models {
		if model.ID == modelID || (model.ResolvedModelID != "" && model.ResolvedModelID == modelID) {
			return model, true
		}
	}
	return external.ModelOption{}, false
}

// SessionRuntime returns a session's runtime type, checked against the bot.
func (s *Service) SessionRuntime(ctx context.Context, botID, sessionID string) (string, error) {
	sess, err := s.botSession(ctx, botID, sessionID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sess.RuntimeType), nil
}

func (s *Service) botSession(ctx context.Context, botID, sessionID string) (session.Thread, error) {
	if s.sessionService == nil {
		return session.Thread{}, errors.New("session service not configured")
	}
	sess, err := s.sessionService.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return session.Thread{}, err
	}
	if strings.TrimSpace(sess.BotID) != strings.TrimSpace(botID) {
		return session.Thread{}, ErrSessionNotInBot
	}
	return sess, nil
}

// SessionModelState reads the model a session will use. Native sessions return
// only Runtime: their model is bot-level and already resolved by the caller.
// A direct runtime whose catalog cannot be read returns the catalog error as
// its public error, so the command can answer the user (sign-in expired,
// dependency missing) the way the Web picker does.
func (s *Service) SessionModelState(ctx context.Context, botID, sessionID string) (SessionModelState, error) {
	sess, err := s.botSession(ctx, botID, sessionID)
	if err != nil {
		return SessionModelState{}, err
	}
	state := SessionModelState{
		Runtime:    strings.TrimSpace(sess.RuntimeType),
		BotAgentID: strings.TrimSpace(sess.BotAgentID),
		Revision:   strings.TrimSpace(sess.ModelPreferenceRevision),
	}
	if !session.IsDirectRuntime(sess) {
		return state, nil
	}
	state.Direct = true
	provider, ok := s.externalDrivers[sess.RuntimeType].(external.ModelCatalogProvider)
	if !ok {
		return SessionModelState{}, external.ErrModelCatalogUnavailable
	}
	remembered := strings.TrimSpace(sess.PreferredExternalModelID)
	catalog, err := provider.ModelCatalog(ctx, external.ModelCatalogRequest{
		BotID: sess.BotID, BotAgentID: sess.BotAgentID,
		ProjectPath: metadataString(runtimeSessionMeta(sess), "project_path"),
		ModelID:     remembered, ResolveDefaults: true,
	})
	if err != nil {
		return SessionModelState{}, ExternalAgentError(err)
	}
	state.Catalog = catalog
	if remembered != "" {
		state.ModelID = remembered
		state.ReasoningEffort = strings.TrimSpace(sess.PreferredReasoningEffort)
		state.Remembered = true
		return state, nil
	}
	state.ModelID = strings.TrimSpace(catalog.ConfiguredModelID)
	if state.ModelID == "" {
		for _, model := range catalog.Models {
			if model.Default {
				state.ModelID = model.ID
				break
			}
		}
	}
	// The driver applies the Agent's configured effort when a turn carries
	// none, so that is what an unremembered session runs with.
	state.ReasoningEffort = strings.TrimSpace(catalog.ConfiguredReasoningEffort)
	if state.ReasoningEffort == "" {
		if model, ok := findCatalogModel(catalog, state.ModelID); ok {
			state.ReasoningEffort = strings.TrimSpace(model.DefaultReasoningEffort)
		}
	}
	return state, nil
}

// SetSessionModelPreference remembers a model and/or effort for a direct
// runtime session, through the same reconcile and compare-and-set the Web
// picker uses. A nil modelID keeps the session's model (or its default) and
// changes only the effort.
func (s *Service) SetSessionModelPreference(ctx context.Context, botID, sessionID string, modelID, effort *string, revision string) error {
	return s.PatchSessionModelPreference(ctx, botID, sessionID, modelID, effort, revision)
}
