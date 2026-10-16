package command

import (
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/settings"
)

func (h *Handler) buildSearchGroup() *CommandGroup {
	g := newCommandGroup("search", "Manage search provider")
	g.DefaultAction = "list" // bare /search lands on the provider list (current marked)
	g.Register(SubCommand{
		Name:  "list",
		Usage: "list - List all search providers",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if h.searchProvService == nil {
				return &Result{Text: cc.T("cmd.search.unavailable")}, nil
			}
			items, err := h.searchProvService.List(cc.Ctx, "")
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return &Result{Text: cc.T("cmd.search.empty")}, nil
			}
			settingsResp, _ := h.getBotSettings(cc)
			currentRecords := make([]listRecord, 0, 1)
			otherRecords := make([]listRecord, 0, len(items))
			for _, item := range items {
				rec := providerListRecord(cc, item.Name, item.Provider, false, item.ID == settingsResp.SearchProviderID)
				// Tap a provider to switch to it — no typing of /search set.
				rec.action = &ItemAction{Resource: "search", Action: "set", Args: []string{item.Name}}
				if item.ID == settingsResp.SearchProviderID {
					currentRecords = append(currentRecords, rec)
					continue
				}
				otherRecords = append(otherRecords, rec)
			}
			currentRecords = append(currentRecords, otherRecords...)
			return buildListResult(cc.T("cmd.search.title"), "search", "list", nil, currentRecords, cc.Page, defaultListLimit, cc.L), nil
		},
	})
	g.Register(SubCommand{
		Name:  "current",
		Usage: "current - Show the current search provider",
		Handler: func(cc CommandContext) (string, error) {
			if h.settingsService == nil {
				return cc.T("cmd.search.unavailable"), nil
			}
			settingsResp, err := h.getBotSettings(cc)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(settingsResp.SearchProviderID) == "" {
				return cc.T("cmd.search.noneSet", map[string]any{"list": CmdRef("search list"), "set": CmdRef("search set <name>")}), nil
			}
			return cc.T("cmd.search.active", map[string]any{"name": h.resolveSearchProviderName(cc, settingsResp.SearchProviderID)}), nil
		},
	})
	g.Register(SubCommand{
		Name:    "set",
		Usage:   "set <name> - Set the search provider for this bot",
		IsWrite: true,
		Handler: func(cc CommandContext) (string, error) {
			if len(cc.Args) < 1 {
				return cc.T("cmd.search.setUsage"), nil
			}
			if h.settingsService == nil {
				return cc.T("cmd.search.unavailable"), nil
			}
			name := cc.Args[0]
			before, _ := h.getBotSettings(cc)
			items, err := h.searchProvService.List(cc.Ctx, "")
			if err != nil {
				return "", err
			}
			for _, item := range items {
				if strings.EqualFold(item.Name, name) {
					_, err := h.settingsService.UpsertBot(cc.Ctx, cc.BotID, settings.UpsertRequest{
						SearchProviderID: &item.ID,
					})
					if err != nil {
						return "", err
					}
					return formatChangedValueT(cc, cc.T("cmd.search.label"), h.resolveSearchProviderName(cc, before.SearchProviderID), item.Name), nil
				}
			}
			return cc.T("cmd.search.notFound", map[string]any{"name": fmt.Sprintf("%q", name), "command": CmdRef("search list")}), nil
		},
	})
	return g
}

// providerListRecord builds a compact provider row: the name as the label, then
// chips for current/default and the engine slug — but the engine chip is shown
// only when it adds information the name doesn't already convey, so e.g.
// "Built-in Memory" does not get a redundant "builtin" chip.
func providerListRecord(cc CommandContext, name, provider string, isDefault, isCurrent bool) listRecord {
	fields := []kv{{cc.T("cmd.common.fieldName"), name}}
	if isCurrent {
		fields = append(fields, kv{"", cc.T("cmd.common.current")})
	}
	if isDefault {
		fields = append(fields, kv{"", cc.T("cmd.common.default")})
	}
	if engine := distinctProviderEngine(name, provider); engine != "" {
		fields = append(fields, kv{"", engine})
	}
	return listRecord{selected: isCurrent, fields: fields}
}

// distinctProviderEngine returns the provider engine slug only when it is not
// already implied by the name (comparing alphanumerics only); otherwise "".
func distinctProviderEngine(name, provider string) string {
	p := strings.TrimSpace(provider)
	if p == "" {
		return ""
	}
	if n, pn := alnumLower(name), alnumLower(p); pn == "" || strings.Contains(n, pn) {
		return ""
	}
	return p
}

func alnumLower(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
