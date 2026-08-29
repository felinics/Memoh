package command

import (
	"fmt"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

const (
	modelPickerPageSize    = 8
	providerPickerPageSize = 10
)

// modelCandidate is one chat model in the canonical, selection-independent order
// used by the picker. Its position in the canonical slice is the flat index that
// selection callbacks round-trip.
type modelCandidate struct {
	dbID       string
	modelID    string
	name       string
	providerID string
	provider   string
}

type providerGroup struct {
	name     string
	modelIdx []int // indices into the canonical candidate slice
}

// buildModelCandidates returns all chat models in a stable order (provider name,
// then model name) so flat indices are reproducible across renders regardless of
// the current selection. Provider names are resolved once per provider.
func (h *Handler) buildModelCandidates(cc CommandContext, items []models.GetResponse) []modelCandidate {
	provCache := make(map[string]string)
	resolve := func(id string) string {
		if v, ok := provCache[id]; ok {
			return v
		}
		v := h.resolveProviderName(cc, id)
		provCache[id] = v
		return v
	}
	cands := make([]modelCandidate, 0, len(items))
	for _, it := range items {
		cands = append(cands, modelCandidate{
			dbID:       it.ID,
			modelID:    it.ModelID,
			name:       modelDisplayName(it),
			providerID: it.ProviderID,
			provider:   resolve(it.ProviderID),
		})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		pi, pj := strings.ToLower(cands[i].provider), strings.ToLower(cands[j].provider)
		if pi != pj {
			return pi < pj
		}
		return strings.ToLower(cands[i].name) < strings.ToLower(cands[j].name)
	})
	return cands
}

// groupCandidatesByProvider buckets the (provider-sorted) candidates into
// contiguous, alphabetical provider groups.
func groupCandidatesByProvider(cands []modelCandidate) []providerGroup {
	groups := make([]providerGroup, 0)
	idxByName := make(map[string]int)
	for i, c := range cands {
		gi, ok := idxByName[c.provider]
		if !ok {
			gi = len(groups)
			groups = append(groups, providerGroup{name: c.provider})
			idxByName[c.provider] = gi
		}
		groups[gi].modelIdx = append(groups[gi].modelIdx, i)
	}
	return groups
}

// modelCandidateByDBID re-derives the canonical candidate list and returns the
// candidate with the given stable DB id. ok is false when no selectable model
// has that id (e.g. it was deleted between render and tap).
func (h *Handler) modelCandidateByDBID(cc CommandContext, dbID string) (modelCandidate, bool, error) {
	items, err := h.selectableChatModels(cc)
	if err != nil {
		return modelCandidate{}, false, err
	}
	dbID = strings.TrimSpace(dbID)
	for _, c := range h.buildModelCandidates(cc, items) {
		if c.dbID == dbID {
			return c, true, nil
		}
	}
	return modelCandidate{}, false, nil
}

// buildModelPickerResult produces the model list as a Result: complete flat text
// (for channels without buttons, preserving prior behavior) plus a two-level
// ModelPickerView for interactive channels. Provider grid is shown unless a
// provider is selected (via --prov or a positional provider arg).
func (h *Handler) buildModelPickerResult(cc CommandContext) (*Result, error) {
	items, err := h.selectableChatModels(cc)
	if err != nil {
		return nil, err
	}

	filterProvider := ""
	if len(cc.Args) > 0 {
		filterProvider = strings.TrimSpace(strings.Join(cc.Args, " "))
	}

	settingsResp, _ := h.getBotSettings(cc)
	cands := h.buildModelCandidates(cc, items)
	groups := groupCandidatesByProvider(cands)
	currentDisplay := h.resolveModelName(cc, settingsResp.ChatModelID)
	reasoning := formatReasoningLabel(cc, settingsResp)

	provIdx := cc.Prov
	if provIdx < 0 && filterProvider != "" {
		for i, g := range groups {
			if strings.EqualFold(g.name, filterProvider) {
				provIdx = i
				break
			}
		}
	}

	// Selecting a model is owner-only (/model set is IsWrite), but the picker
	// buttons render for everyone: permission is enforced at execution time, so a
	// non-owner tap returns a clear "owner only" message. Hiding the buttons for
	// non-owners also hid them from owners whose Telegram identity isn't resolved
	// as owner (WriteAccess=false) — which silently killed the picker on Telegram.
	selectable := true

	// No-button-channel parity: when the user opens /model without drilling
	// into a provider, mirror Telegram's LevelProviders picker structure as
	// the Text body — provider summary with counts and the active-provider
	// marker. A flat first-N-of-many model list would dump unrelated models
	// (image/voice/etc.) and leave the user with no way to discover provider
	// names to type. Skipped when only one provider exists (nothing to pick).
	if filterProvider == "" && provIdx < 0 && len(groups) > 1 {
		r := &Result{Text: formatProvidersSummary(cc, groups, cands, settingsResp.ChatModelID, currentDisplay, reasoning)}
		if selectable {
			r.Interactive = &Interactive{Kind: InteractiveModelPicker, Picker: buildProvidersPickerView(groups, cands, settingsResp.ChatModelID, currentDisplay, reasoning, cc.Page)}
		}
		return r, nil
	}

	// Text fallback: flat list, selected-first, preserving prior /model list output.
	textModels := h.filterModelsByProvider(cc, items, filterProvider)
	if len(textModels) == 0 {
		if filterProvider != "" {
			return &Result{Text: cc.T("cmd.model.noneUnderProvider", map[string]any{"provider": fmt.Sprintf("%q", filterProvider), "command": CmdRef("model list")})}, nil
		}
		return &Result{Text: cc.T("cmd.model.empty")}, nil
	}
	sort.SliceStable(textModels, func(i, j int) bool {
		return modelSortRank(textModels[i], settingsResp) < modelSortRank(textModels[j], settingsResp)
	})
	records := make([]listRecord, 0, len(textModels))
	for _, item := range textModels {
		fields := []kv{
			{cc.T("cmd.status.fieldModel"), modelDisplayName(item)},
			{cc.T("cmd.model.fieldProvider"), h.resolveProviderName(cc, item.ProviderID)},
		}
		// The active chat marker is a chip, not bracketed into the
		// name — brackets would force the whole name into a monospace code span.
		if markers := modelMarkers(item.ID, settingsResp); len(markers) > 0 {
			for i, marker := range markers {
				markers[i] = cc.T("cmd.model.marker." + marker)
			}
			fields = append(fields, kv{"", strings.Join(markers, ", ")})
		}
		records = append(records, listRecord{fields: fields})
	}
	res := buildListResult(cc.T("cmd.model.title"), "model", "list", nil, records, cc.Page, defaultListLimit, cc.L)

	if !selectable {
		return res, nil
	}
	var picker *ModelPickerView
	if eff := pickerProviderIndex(provIdx, len(groups)); eff >= 0 {
		picker = buildModelsPickerView(groups, cands, eff, settingsResp.ChatModelID, currentDisplay, reasoning, cc.Page)
	} else {
		picker = buildProvidersPickerView(groups, cands, settingsResp.ChatModelID, currentDisplay, reasoning, cc.Page)
	}
	res.Interactive = &Interactive{Kind: InteractiveModelPicker, Picker: picker}
	return res, nil
}

// pickerProviderIndex decides which model-picker level to render: a provider
// group index to drill into (model level), or -1 to show the provider-selection
// level. A single provider is always drilled into — there is no meaningful
// provider choice, and a one-button provider grid would leave no-button users
// with a /model list trailer instead of a /model set one.
func pickerProviderIndex(provIdx, numGroups int) int {
	if provIdx >= 0 && provIdx < numGroups {
		return provIdx
	}
	if provIdx < 0 && numGroups == 1 {
		return 0
	}
	return -1
}

// formatProvidersSummary builds the text body shown to no-button channels when
// the user opens bare /model. Mirrors Telegram's LevelProviders picker: title +
// current-model header + per-provider count list with an ● marking the provider
// that owns the active chat model.
func formatProvidersSummary(cc CommandContext, groups []providerGroup, cands []modelCandidate, currentDBID, currentDisplay, reasoning string) string {
	var b strings.Builder
	totalModels := 0
	for _, g := range groups {
		totalModels += len(g.modelIdx)
	}
	b.WriteString(MdBold(fmt.Sprintf("%s (%d)", cc.T("cmd.model.title"), totalModels)))
	b.WriteString("\n\n")
	if current := strings.TrimSpace(currentDisplay); current != "" {
		fmt.Fprintf(&b, "%s\n", cc.T("chrome.currentModel", map[string]any{"model": current}))
	}
	if r := strings.TrimSpace(reasoning); r != "" {
		fmt.Fprintf(&b, "%s\n", cc.T("chrome.reasoningLine", map[string]any{"effort": r}))
	}
	b.WriteString("\n")
	b.WriteString(MdBold(cc.T("cmd.model.byProvider")) + "\n")
	for _, g := range groups {
		hasCurrent := false
		for _, mi := range g.modelIdx {
			if cands[mi].dbID == currentDBID {
				hasCurrent = true
				break
			}
		}
		marker := ""
		if hasCurrent {
			marker = " ●"
		}
		fmt.Fprintf(&b, "- %s (%d)%s\n", g.name, len(g.modelIdx), marker)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatReasoningLabel renders the current reasoning state for picker headers.
func formatReasoningLabel(cc CommandContext, s settings.Settings) string {
	e := strings.TrimSpace(s.ReasoningEffort)
	if models.IsReasoningDisabled(e) {
		return cc.T("cmd.common.off")
	}
	if e != "" {
		return e
	}
	return cc.T("cmd.common.on")
}

func buildProvidersPickerView(groups []providerGroup, cands []modelCandidate, currentDBID, currentDisplay, reasoning string, page int) *ModelPickerView {
	providers := make([]PickerProvider, 0, len(groups))
	for i, g := range groups {
		hasCurrent := false
		for _, mi := range g.modelIdx {
			if cands[mi].dbID == currentDBID {
				hasCurrent = true
				break
			}
		}
		providers = append(providers, PickerProvider{Index: i, Name: g.name, Count: len(g.modelIdx), HasCurrent: hasCurrent})
	}
	return &ModelPickerView{
		Level:            LevelProviders,
		Providers:        providers,
		CurrentModelDBID: currentDBID,
		CurrentDisplay:   currentDisplay,
		Reasoning:        reasoning,
		Page:             page,
		PageSize:         providerPickerPageSize,
		Total:            len(providers),
	}
}

func buildModelsPickerView(groups []providerGroup, cands []modelCandidate, provIdx int, currentDBID, currentDisplay, reasoning string, page int) *ModelPickerView {
	g := groups[provIdx]
	picks := make([]PickerModel, 0, len(g.modelIdx))
	for _, mi := range g.modelIdx {
		c := cands[mi]
		picks = append(picks, PickerModel{
			DBID:     c.dbID,
			Name:     c.name,
			Provider: c.provider,
			Selected: c.dbID == currentDBID,
		})
	}
	return &ModelPickerView{
		Level:            LevelModels,
		Models:           picks,
		ProviderIndex:    provIdx,
		ProviderName:     g.name,
		CurrentModelDBID: currentDBID,
		CurrentDisplay:   currentDisplay,
		Reasoning:        reasoning,
		Page:             page,
		PageSize:         modelPickerPageSize,
		Total:            len(picks),
	}
}
