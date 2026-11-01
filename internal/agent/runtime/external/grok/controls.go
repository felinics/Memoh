package grok

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external/grok/grokcfg"
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

// permissionMenu is Grok's permission menu, in grokcfg.PermissionModes order.
// Grok reads the mode when a session opens, so a change lands with the next
// turn's reopen.
var permissionMenu = []external.Mode{
	{ID: grokcfg.PermissionAsk, I18nKey: "runtime.grokBuild.modes.ask", Icon: "hand"},
	{ID: grokcfg.PermissionAuto, I18nKey: "runtime.grokBuild.modes.auto", Icon: "shield-terminal"},
	{ID: grokcfg.PermissionAlwaysApprove, I18nKey: "runtime.grokBuild.modes.alwaysApprove", Icon: "shield-alert", Warning: true},
}

func modeState(mode string) (external.ModeState, error) {
	return external.PermissionModeState(permissionMenu, mode)
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
	b, release, err := d.openForControl(ctx, input)
	if err != nil {
		return external.CommandResult{}, err
	}
	defer release()
	info, err := grokState(b.Process()).sessionInfo(ctx, b.Session().ID())
	if err != nil {
		return external.CommandResult{}, turnFailure(err)
	}
	return external.CommandResult{Data: map[string]any{
		"session_id":     b.Session().ID(),
		"model":          info.Model,
		"tokens":         info.Context.Used,
		"context_window": info.Context.Total,
	}}, nil
}

// Compact runs Grok's own compaction on the conversation's session.
func (d *Driver) Compact(ctx context.Context, input external.PromptInput) (external.CompactionResult, error) {
	b, release, err := d.openForControl(ctx, input)
	if err != nil {
		return external.CompactionResult{}, err
	}
	defer release()
	if err := grokState(b.Process()).compact(ctx, b.Session().ID()); err != nil {
		if ctx.Err() != nil {
			return external.CompactionResult{}, ctx.Err()
		}
		return external.CompactionResult{}, turnFailure(err)
	}
	return external.CompactionResult{RuntimeMetadata: map[string]any{metadataSessionIDKey: b.Session().ID()}}, nil
}

// openForControl opens the conversation's own session for a control and
// holds it until the returned release. A session Grok no longer has is not
// replaced: a control run on a fresh session would report success for a
// conversation it never touched.
func (d *Driver) openForControl(ctx context.Context, input external.PromptInput) (*acp.Binding, func(), error) {
	turn, err := d.turnInput(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	b, release, err := d.host.AcquireExisting(ctx, turn)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return nil, nil, ctx.Err()
		case errors.Is(err, external.ErrThreadUnavailable):
			return nil, nil, err
		default:
			return nil, nil, resumeFailure(ctx, err)
		}
	}
	unhold := b.Hold()
	return b, func() { unhold(); release() }, nil
}

// ForkThread implements external.ThreadForker. Grok copies a session whole,
// so a fork is possible only from the conversation's latest turn: cut any
// earlier, the copy would carry turns the forked conversation does not show.
func (d *Driver) ForkThread(ctx context.Context, botID, botAgentID string, runtimeMetadata map[string]any, lastTurnID string) (map[string]any, error) {
	sessionID := strings.TrimSpace(external.MetadataString(runtimeMetadata, metadataSessionIDKey))
	if sessionID == "" {
		return nil, external.ErrThreadUnavailable
	}
	lastPromptID := external.MetadataString(runtimeMetadata, metadataLastPromptIDKey)
	if anchor := strings.TrimSpace(lastTurnID); anchor != "" && anchor != lastPromptID {
		return nil, external.Fail(external.FailureForkLatestOnly, errs.New("grok forks a conversation only from its latest turn"))
	}
	proc, release, err := d.host.Process(ctx, acp.PromptInput{BotID: botID, BotAgentID: botAgentID})
	if err != nil {
		return nil, err
	}
	defer release()
	resolved, err := acpclient.ResolveSessionContext(acpclient.SessionContextInput{
		Backend:     proc.Info().Backend,
		ProjectPath: external.MetadataString(runtimeMetadata, "project_path"),
	})
	if err != nil {
		return nil, err
	}
	forked, err := grokState(proc).fork(ctx, sessionID, resolved.ProjectPath)
	if err != nil {
		return nil, turnFailure(err)
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
	proc, release, err := d.host.Process(ctx, acp.PromptInput{BotID: request.BotID, BotAgentID: request.BotAgentID})
	if err != nil {
		return external.ModelCatalog{}, err
	}
	defer release()
	models, err := catalogFromState(grokState(proc).modelState(ctx))
	if err != nil {
		return external.ModelCatalog{}, external.Upstream(err)
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
			Name:                   external.FirstNonEmpty(model.Name, model.ModelID),
			Description:            model.Description,
			Default:                model.ModelID == parsed.CurrentModelID,
			DefaultReasoningEffort: model.Meta.ReasoningEffort,
			ReasoningEfforts:       efforts,
		})
	}
	return models, nil
}
