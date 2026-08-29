package command

import (
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

func (h *Handler) buildModelGroup() *CommandGroup {
	g := newCommandGroup("model", "Manage bot models")
	g.DefaultAction = "list"
	g.Register(SubCommand{
		Name:  "list",
		Usage: "list [provider_name] - List available chat models",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if h.modelsService == nil {
				return &Result{Text: cc.T("cmd.model.unavailable")}, nil
			}
			return h.buildModelPickerResult(cc)
		},
	})
	g.Register(SubCommand{
		Name:  "current",
		Usage: "current - Show current chat model",
		Handler: func(cc CommandContext) (string, error) {
			if h.settingsService == nil {
				return cc.T("cmd.model.unavailable"), nil
			}
			settingsResp, err := h.getBotSettings(cc)
			if err != nil {
				return "", err
			}
			return formatKVTitled(cc.T("cmd.model.currentTitle"), []kv{
				{cc.T("cmd.settings.fieldChatModel"), h.resolveModelName(cc, settingsResp.ChatModelID)},
			}), nil
		},
	})
	g.Register(SubCommand{
		Name:    "set",
		Usage:   "set <model_id> | <provider_name> <model_name> - Set the chat model",
		IsWrite: true,
		Handler: func(cc CommandContext) (string, error) {
			var selectedID string
			if cc.SelectID != "" {
				// Selection from a picker button: resolve the stable model id.
				// A miss means the model was removed between render and tap.
				cand, ok, err := h.modelCandidateByDBID(cc, cc.SelectID)
				if err != nil {
					return "", err
				}
				if !ok {
					return cc.T("cmd.model.listChanged"), nil
				}
				selectedID = cand.dbID
			} else {
				if len(cc.Args) < 1 {
					return cc.T("cmd.model.setUsage"), nil
				}
				modelResp, err := h.findModelForSelection(cc, cc.Args)
				if err != nil {
					return "", err
				}
				selectedID = modelResp.ID
			}
			if h.settingsService == nil {
				return cc.T("cmd.model.unavailable"), nil
			}
			before, _ := h.getBotSettings(cc)
			if _, err := h.settingsService.UpsertBot(cc.Ctx, cc.BotID, settings.UpsertRequest{
				ChatModelID: &selectedID,
			}); err != nil {
				return "", err
			}
			return formatChangedValueT(cc, cc.T("cmd.settings.fieldChatModel"), h.resolveModelName(cc, before.ChatModelID), h.resolveModelName(cc, selectedID)), nil
		},
	})
	return g
}

func (h *Handler) resolveProviderName(cc CommandContext, providerID string) string {
	if h.providersService == nil || providerID == "" {
		return providerID
	}
	p, err := h.providersService.Get(cc.Ctx, providerID)
	if err != nil {
		return providerID
	}
	// A blank provider name would drop the provider button from Telegram
	// keyboards and render blank in summaries; fall back to the id.
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	return providerID
}

// modelDisplayName returns a non-empty label for a model: its display name, or
// its model_id when the (nullable) name column is blank. model_id is required
// at write time, so this never yields "" — an empty label would be dropped from
// Telegram inline keyboards and render blank for text-only users, making an
// otherwise selectable model impossible to choose or discover.
func modelDisplayName(m models.GetResponse) string {
	if n := strings.TrimSpace(m.Name); n != "" {
		return n
	}
	return m.ModelID
}

func (h *Handler) findModelByProviderAndName(cc CommandContext, providerName, modelName string) (models.GetResponse, error) {
	chatModels, err := h.selectableChatModels(cc)
	if err != nil {
		return models.GetResponse{}, err
	}
	for _, m := range chatModels {
		if strings.EqualFold(h.resolveProviderName(cc, m.ProviderID), providerName) &&
			(strings.EqualFold(m.Name, modelName) || strings.EqualFold(m.ModelID, modelName)) {
			return m, nil
		}
	}
	return models.GetResponse{}, fmt.Errorf("%s", cc.T("cmd.model.notFoundUnderProvider", map[string]any{"name": fmt.Sprintf("%q", modelName), "provider": fmt.Sprintf("%q", providerName), "command": CmdRef("model list")}))
}

func (h *Handler) findModelForSelection(cc CommandContext, args []string) (models.GetResponse, error) {
	if h.modelsService == nil {
		return models.GetResponse{}, fmt.Errorf("%s", cc.T("cmd.model.serviceUnavailable"))
	}
	if len(args) == 0 {
		return models.GetResponse{}, fmt.Errorf("%s", cc.T("cmd.model.idRequired"))
	}
	if len(args) == 1 {
		return h.findModelByIDOrName(cc, args[0])
	}
	return h.findModelByProviderAndName(cc, args[0], strings.Join(args[1:], " "))
}

func (h *Handler) findModelByIDOrName(cc CommandContext, target string) (models.GetResponse, error) {
	items, err := h.selectableChatModels(cc)
	if err != nil {
		return models.GetResponse{}, err
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return models.GetResponse{}, fmt.Errorf("%s", cc.T("cmd.model.idRequired"))
	}
	for _, item := range items {
		if strings.EqualFold(item.ModelID, target) {
			return item, nil
		}
	}
	matches := make([]models.GetResponse, 0, 4)
	for _, item := range items {
		if strings.EqualFold(item.Name, target) {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 0:
		return models.GetResponse{}, fmt.Errorf("%s", cc.T("cmd.model.notFound", map[string]any{"name": fmt.Sprintf("%q", target), "command": CmdRef("model list")}))
	case 1:
		return matches[0], nil
	default:
		choices := make([]string, 0, len(matches))
		for _, item := range matches {
			choices = append(choices, fmt.Sprintf("%s/%s", h.resolveProviderName(cc, item.ProviderID), item.ModelID))
		}
		return models.GetResponse{}, fmt.Errorf("%s", cc.T("cmd.model.ambiguous", map[string]any{
			"name":       fmt.Sprintf("%q", target),
			"candidates": strings.Join(choices, ", "),
		}))
	}
}

func (h *Handler) selectableChatModels(cc CommandContext) ([]models.GetResponse, error) {
	if h.modelsService == nil {
		return nil, fmt.Errorf("%s", cc.T("cmd.model.serviceUnavailable"))
	}
	return h.modelsService.ListEnabledByType(cc.Ctx, models.ModelTypeChat)
}

func (h *Handler) filterModelsByProvider(cc CommandContext, items []models.GetResponse, providerName string) []models.GetResponse {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return items
	}
	filtered := make([]models.GetResponse, 0, len(items))
	for _, item := range items {
		if strings.EqualFold(h.resolveProviderName(cc, item.ProviderID), providerName) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func modelMarkers(modelID string, settingsResp settings.Settings) []string {
	var markers []string
	if modelID == settingsResp.ChatModelID {
		markers = append(markers, "chat")
	}
	return markers
}

func modelSortRank(model models.GetResponse, settingsResp settings.Settings) int {
	if len(modelMarkers(model.ID, settingsResp)) > 0 {
		return 1
	}
	return 2
}
