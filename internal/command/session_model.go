package command

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/runtimekind"
)

// SessionModelState is the model a session's runtime will use, as the
// application layer resolves it. Only direct external runtimes (Codex, Claude
// Code) choose their model per session; the native runtime keeps the bot-level
// settings path.
type SessionModelState struct {
	Runtime         string
	BotAgentID      string
	Direct          bool
	ModelID         string
	ReasoningEffort string
	Revision        string
	Catalog         external.ModelCatalog
}

// SessionModels reads and writes the model of the session a channel command
// runs in. *application.Service satisfies it through an adapter wired at
// startup; nil leaves every session on the bot-level path.
type SessionModels interface {
	// SessionRuntime returns the session's runtime type without reading a
	// model catalog, for commands that only branch on it.
	SessionRuntime(ctx context.Context, botID, sessionID string) (string, error)
	SessionModelState(ctx context.Context, botID, sessionID string) (SessionModelState, error)
	SetSessionModelPreference(ctx context.Context, botID, sessionID string, modelID, effort *string, revision string) error
}

// ErrSessionModelConflict reports a session preference another writer changed
// between this command's read and its write. ErrSessionModelUnavailable reports
// a model the runtime no longer offers. Adapters map the application errors to
// these so command copy does not depend on the application package.
var (
	ErrSessionModelConflict    = errors.New("session model preference changed")
	ErrSessionModelUnavailable = errors.New("session model unavailable")
)

// SetSessionModels wires the session-scoped model port used by /model,
// /reasoning, /status and /context.
func (h *Handler) SetSessionModels(models SessionModels) {
	h.sessionModels = models
}

// sessionRuntime reports the runtime of the command's session. ok is false when
// the command has no session or no port, which means the bot-level path.
func (h *Handler) sessionRuntime(cc CommandContext) (SessionModelState, bool, error) {
	if h.sessionModels == nil || strings.TrimSpace(cc.SessionID) == "" {
		return SessionModelState{}, false, nil
	}
	state, err := h.sessionModels.SessionModelState(cc.Ctx, cc.BotID, cc.SessionID)
	if err != nil {
		return SessionModelState{}, false, err
	}
	return state, true, nil
}

// externalSession reports a session that runs on an external Agent (direct or
// ACP). ok is false for native sessions and for commands without a session.
func (h *Handler) externalSession(cc CommandContext) (SessionModelState, bool, error) {
	state, ok, err := h.sessionRuntime(cc)
	if err != nil || !ok {
		return SessionModelState{}, false, err
	}
	if state.Direct || runtimekind.IsExternal(state.Runtime) {
		return state, true, nil
	}
	return SessionModelState{}, false, nil
}

// sessionRuntimeKind returns the runtime of the command's session, "" when the
// command has no session or no port (the bot-level path).
func (h *Handler) sessionRuntimeKind(cc CommandContext) (string, error) {
	if h.sessionModels == nil || strings.TrimSpace(cc.SessionID) == "" {
		return "", nil
	}
	return h.sessionModels.SessionRuntime(cc.Ctx, cc.BotID, cc.SessionID)
}

func catalogModel(catalog external.ModelCatalog, modelID string) (external.ModelOption, bool) {
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

func catalogModelName(catalog external.ModelCatalog, modelID string) string {
	if model, ok := catalogModel(catalog, modelID); ok {
		if name := strings.TrimSpace(model.Name); name != "" {
			return name
		}
		return model.ID
	}
	return strings.TrimSpace(modelID)
}

// directModelListResult renders a direct runtime's catalog as the one-level
// model picker: the runtime has no providers to drill through.
func directModelListResult(cc CommandContext, state SessionModelState) *Result {
	models := append([]external.ModelOption(nil), state.Catalog.Models...)
	if len(models) == 0 {
		return &Result{Text: cc.T("cmd.model.empty")}
	}
	sort.SliceStable(models, func(i, j int) bool {
		return models[i].Default && !models[j].Default
	})
	current, _ := catalogModel(state.Catalog, state.ModelID)
	currentDisplay := catalogModelName(state.Catalog, state.ModelID)
	reasoning := directReasoningLabel(cc, state.ReasoningEffort)

	picks := make([]PickerModel, 0, len(models))
	records := make([]listRecord, 0, len(models))
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = model.ID
		}
		selected := current.ID != "" && model.ID == current.ID
		picks = append(picks, PickerModel{DBID: model.ID, Name: name, Selected: selected})
		fields := []kv{{cc.T("cmd.status.fieldModel"), name}}
		if selected {
			fields = append(fields, kv{"", cc.T("cmd.common.current")})
		}
		records = append(records, listRecord{selected: selected, fields: fields})
	}
	res := buildListResult(cc.T("cmd.model.title"), "model", "list", nil, records, cc.Page, defaultListLimit, cc.L)
	res.Interactive = &Interactive{Kind: InteractiveModelPicker, Picker: &ModelPickerView{
		Level:            LevelModels,
		Models:           picks,
		ProviderIndex:    0,
		CurrentModelDBID: current.ID,
		CurrentDisplay:   currentDisplay,
		Reasoning:        reasoning,
		Page:             cc.Page,
		PageSize:         modelPickerPageSize,
		Total:            len(picks),
		Direct:           true,
	}}
	return res
}

func directReasoningLabel(cc CommandContext, effort string) string {
	if e := strings.TrimSpace(effort); e != "" {
		return e
	}
	return cc.T("cmd.common.default")
}

// setDirectModel remembers the picked model for the session. The new model's
// own default effort replaces the old one, as in the Web picker.
func (h *Handler) setDirectModel(cc CommandContext, state SessionModelState) (string, error) {
	target := strings.TrimSpace(cc.SelectID)
	if target == "" {
		target = strings.TrimSpace(strings.Join(cc.Args, " "))
	}
	if target == "" {
		return cc.T("cmd.model.directSetUsage"), nil
	}
	model, ok := catalogModel(state.Catalog, target)
	if !ok {
		for _, candidate := range state.Catalog.Models {
			if strings.EqualFold(strings.TrimSpace(candidate.Name), target) || strings.EqualFold(candidate.ID, target) {
				model, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		if cc.SelectID != "" {
			return cc.T("cmd.model.listChanged"), nil
		}
		return "", newReplyError("cmd.model.notFound", map[string]any{"name": fmt.Sprintf("%q", target), "command": CmdRef("model list")})
	}
	before := catalogModelName(state.Catalog, state.ModelID)
	modelID := model.ID
	if err := h.sessionModels.SetSessionModelPreference(cc.Ctx, cc.BotID, cc.SessionID, &modelID, nil, state.Revision); err != nil {
		return "", err
	}
	after := catalogModelName(state.Catalog, modelID)
	return formatChangedValueT(cc, cc.T("cmd.model.sessionModel"), before, after), nil
}
