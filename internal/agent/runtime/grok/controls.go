package grok

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/grok/grokcfg"
	"github.com/felinics/memoh/internal/errs"
)

var (
	_ external.ModeProvider         = (*Driver)(nil)
	_ external.CommandProvider      = (*Driver)(nil)
	_ external.Compactor            = (*Driver)(nil)
	_ external.ModelCatalogProvider = (*Driver)(nil)
	_ external.ThreadForker         = (*Driver)(nil)
)

func (d *Driver) Modes(ctx context.Context, input external.PromptInput) (external.ModeState, error) {
	cfg, err := d.agentConfig(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return external.ModeState{}, err
	}
	return modeState(permissionMode(input.RuntimeMetadata, cfg))
}

func (*Driver) SetMode(_ context.Context, _ external.PromptInput, mode string) (external.ModeState, error) {
	return modeState(mode)
}

func modeState(mode string) (external.ModeState, error) {
	if mode == "" || !grokcfg.ValidPermissionMode(mode) {
		return external.ModeState{}, external.ErrModeUnavailable
	}
	modes := []external.Mode{
		{ID: grokcfg.PermissionAsk, I18nKey: "runtime.grokBuild.modes.ask", Icon: "hand"},
		{ID: grokcfg.PermissionAuto, I18nKey: "runtime.grokBuild.modes.auto", Icon: "shield-terminal"},
		{ID: grokcfg.PermissionAlwaysApprove, I18nKey: "runtime.grokBuild.modes.alwaysApprove", Icon: "shield-alert", Warning: true},
	}
	// Grok reads the mode when a session opens, so a change lands with the
	// next turn's reopen.
	return external.ModeState{Kind: "permission", ApplyOnNextTurn: true, Supported: true, CurrentModeID: mode, AvailableModes: modes}, nil
}

const (
	compactCommand = "compact"
	statusCommand  = "status"
)

// Commands lists what Memoh drives itself. Grok advertises many more slash
// commands, some of which change the session's permissions or schedule work
// past the turn; those stay out until each is given a deliberate mapping.
func (*Driver) Commands(context.Context, external.PromptInput) ([]external.Command, error) {
	return []external.Command{
		{Name: statusCommand, I18nKey: "runtime.grokBuild.commands.status", Kind: external.CommandRead},
		{Name: compactCommand, I18nKey: "runtime.grokBuild.commands.compact", Kind: external.CommandOperation},
	}, nil
}

// ReadCommand answers status with the session's model and context use as
// Grok reports them, which it does only for a session it holds open.
func (d *Driver) ReadCommand(ctx context.Context, input external.PromptInput) (external.CommandResult, error) {
	if input.Command != statusCommand {
		return external.CommandResult{}, external.ErrCommandUnavailable
	}
	srv, th, release, err := d.openForControl(ctx, input)
	if err != nil {
		return external.CommandResult{}, err
	}
	defer release()
	info, err := srv.sessionInfo(ctx, th.session.ID())
	if err != nil {
		return external.CommandResult{}, turnFailure(err, external.FailureControlFailed)
	}
	return external.CommandResult{Data: map[string]any{
		"session_id":     th.session.ID(),
		"model":          info.Model,
		"tokens":         info.Context.Used,
		"context_window": info.Context.Total,
	}}, nil
}

// Compact runs Grok's own compaction on the conversation's session.
func (d *Driver) Compact(ctx context.Context, input external.PromptInput) (external.CompactionResult, error) {
	srv, th, release, err := d.openForControl(ctx, input)
	if err != nil {
		return external.CompactionResult{}, err
	}
	defer release()
	defer srv.login.sync(ctx)
	if err := srv.compact(ctx, th.session.ID()); err != nil {
		if ctx.Err() != nil {
			return external.CompactionResult{}, ctx.Err()
		}
		return external.CompactionResult{}, turnFailure(err, external.FailureControlFailed)
	}
	return external.CompactionResult{RuntimeMetadata: map[string]any{metadataSessionIDKey: th.session.ID()}}, nil
}

// openForControl opens the conversation's own session for a control. A
// session Grok no longer has is not replaced: a control run on a fresh
// session would report success for a conversation it never touched.
func (d *Driver) openForControl(ctx context.Context, input external.PromptInput) (*server, *thread, func(), error) {
	if metadataString(input.RuntimeMetadata, metadataSessionIDKey) == "" {
		return nil, nil, nil, external.ErrThreadUnavailable
	}
	cfg, err := d.agentConfig(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return nil, nil, nil, external.Unavailable(err)
	}
	srv, release, err := d.acquireServer(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return nil, nil, nil, serverFailure(err)
	}
	th, _, err := srv.openThread(ctx, d.toolGateway, d.approval, d.userInput, input, permissionMode(input.RuntimeMetadata, cfg), false)
	if err != nil {
		release()
		switch {
		case ctx.Err() != nil:
			return nil, nil, nil, ctx.Err()
		case errors.Is(err, external.ErrThreadUnavailable):
			return nil, nil, nil, err
		default:
			return nil, nil, nil, turnFailure(err, external.FailureUnavailable)
		}
	}
	return srv, th, release, nil
}

// ForkThread implements external.ThreadForker. Grok copies a session whole,
// so a fork is possible only from the conversation's latest turn: cut any
// earlier, the copy would carry turns the forked conversation does not show.
func (d *Driver) ForkThread(ctx context.Context, botID, botAgentID string, runtimeMetadata map[string]any, lastTurnID string) (map[string]any, error) {
	sessionID := strings.TrimSpace(metadataString(runtimeMetadata, metadataSessionIDKey))
	if sessionID == "" {
		return nil, external.Unavailable(errs.New("conversation has no grok session to fork"))
	}
	lastPromptID := metadataString(runtimeMetadata, metadataLastPromptIDKey)
	if anchor := strings.TrimSpace(lastTurnID); anchor != "" && anchor != lastPromptID {
		return nil, external.Fail(external.FailureControlFailed, errs.New("grok forks a conversation only from its latest turn"))
	}
	srv, release, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil {
		return nil, serverFailure(err)
	}
	defer release()
	resolved, err := acpclient.ResolveSessionContext(acpclient.SessionContextInput{
		Backend:     srv.workspaceInfo.Backend,
		ProjectPath: metadataString(runtimeMetadata, "project_path"),
	})
	if err != nil {
		return nil, err
	}
	forked, err := srv.fork(ctx, sessionID, resolved.ProjectPath)
	if err != nil {
		return nil, turnFailure(err, external.FailureControlFailed)
	}
	return map[string]any{metadataSessionIDKey: forked, metadataLastPromptIDKey: lastPromptID}, nil
}

// ModelCatalog returns the models this Agent's account can use and the
// reasoning efforts each accepts.
func (d *Driver) ModelCatalog(ctx context.Context, request external.ModelCatalogRequest) (external.ModelCatalog, error) {
	cfg, err := d.agentConfig(ctx, request.BotID, request.BotAgentID)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	srv, release, err := d.acquireServer(ctx, request.BotID, request.BotAgentID)
	if err != nil {
		return external.ModelCatalog{}, serverFailure(err)
	}
	defer release()
	// Starting Grok may already have refreshed its login.
	defer srv.login.sync(ctx)
	models, err := catalogFromState(srv.modelState(ctx))
	if err != nil {
		return external.ModelCatalog{}, external.Unavailable(err)
	}
	configured := cfg.Model == ""
	for i := range models {
		// Default marks what a turn without a pick runs on; a configured
		// model takes that role instead.
		models[i].Default = models[i].Default && cfg.Model == ""
		configured = configured || models[i].ID == cfg.Model
	}
	if !configured {
		models = append([]external.ModelOption{{ID: cfg.Model, Name: cfg.Model}}, models...)
	}
	return external.ModelCatalog{Models: models, ConfiguredModelID: cfg.Model, ConfiguredReasoningEffort: cfg.ReasoningEffort}, nil
}

// grokModelState is the catalog Grok reports as modelState: in the _meta of
// its initialize response, in models/update notifications, and from
// models/list, with the efforts each model accepts. Standard ACP describes
// models only to a session, and a model's efforts only once it is selected.
type grokModelState struct {
	CurrentModelID  string `json:"currentModelId"`
	AvailableModels []struct {
		ModelID     string `json:"modelId"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Meta        struct {
			ReasoningEffort  string `json:"reasoningEffort"`
			ReasoningEfforts []struct {
				Value       string `json:"value"`
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"reasoningEfforts"`
		} `json:"_meta"`
	} `json:"availableModels"`
}

func catalogFromState(state map[string]any) ([]external.ModelOption, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var parsed grokModelState
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.AvailableModels) == 0 {
		return nil, errs.New("grok reported no model catalog")
	}
	models := make([]external.ModelOption, 0, len(parsed.AvailableModels))
	for _, model := range parsed.AvailableModels {
		efforts := make([]external.ReasoningEffortOption, 0, len(model.Meta.ReasoningEfforts))
		for _, effort := range model.Meta.ReasoningEfforts {
			efforts = append(efforts, external.ReasoningEffortOption{ID: effort.Value, Name: effort.Label, Description: effort.Description})
		}
		models = append(models, external.ModelOption{
			ID:                     model.ModelID,
			Name:                   firstNonEmpty(model.Name, model.ModelID),
			Description:            model.Description,
			Default:                model.ModelID == parsed.CurrentModelID,
			DefaultReasoningEffort: model.Meta.ReasoningEffort,
			ReasoningEfforts:       efforts,
		})
	}
	return models, nil
}
