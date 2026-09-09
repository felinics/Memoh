package command

import (
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/settings"
)

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

func (h *Handler) buildMemoryGroup() *CommandGroup {
	g := newCommandGroup("memory", "Manage memory provider")
	g.DefaultAction = "list" // bare /memory lands on the provider list (current marked)
	g.Register(SubCommand{
		Name:  "list",
		Usage: "list - List all memory providers",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if h.memProvService == nil {
				return &Result{Text: cc.T("cmd.memory.unavailable")}, nil
			}
			items, err := h.memProvService.List(cc.Ctx)
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return &Result{Text: cc.T("cmd.memory.empty")}, nil
			}
			settingsResp, _ := h.getBotSettings(cc)
			currentRecords := make([]listRecord, 0, 1)
			otherRecords := make([]listRecord, 0, len(items))
			for _, item := range items {
				rec := providerListRecord(cc, item.Name, item.Provider, item.IsDefault, item.ID == settingsResp.MemoryProviderID)
				// Tap a provider to switch to it — no typing of /memory set.
				rec.action = &ItemAction{Resource: "memory", Action: "set", Args: []string{item.Name}}
				if item.ID == settingsResp.MemoryProviderID {
					currentRecords = append(currentRecords, rec)
					continue
				}
				otherRecords = append(otherRecords, rec)
			}
			currentRecords = append(currentRecords, otherRecords...)
			return buildListResult(cc.T("cmd.memory.title"), "memory", "list", nil, currentRecords, cc.Page, defaultListLimit, cc.L), nil
		},
	})
	g.Register(SubCommand{
		Name:  "current",
		Usage: "current - Show the current memory provider",
		Handler: func(cc CommandContext) (string, error) {
			if h.settingsService == nil {
				return cc.T("cmd.memory.unavailable"), nil
			}
			settingsResp, err := h.getBotSettings(cc)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(settingsResp.MemoryProviderID) == "" {
				return cc.T("cmd.memory.noneSet", map[string]any{"list": CmdRef("memory list"), "set": CmdRef("memory set <name>")}), nil
			}
			return cc.T("cmd.memory.active", map[string]any{"name": h.resolveMemoryProviderName(cc, settingsResp.MemoryProviderID)}), nil
		},
	})
	g.Register(SubCommand{
		Name:    "set",
		Usage:   "set <name> - Set the memory provider for this bot",
		IsWrite: true,
		Handler: func(cc CommandContext) (string, error) {
			if len(cc.Args) < 1 {
				return cc.T("cmd.memory.setUsage"), nil
			}
			if h.settingsService == nil {
				return cc.T("cmd.memory.unavailable"), nil
			}
			name := cc.Args[0]
			before, _ := h.getBotSettings(cc)
			items, err := h.memProvService.List(cc.Ctx)
			if err != nil {
				return "", err
			}
			for _, item := range items {
				if strings.EqualFold(item.Name, name) {
					_, err := h.settingsService.UpsertBot(cc.Ctx, cc.BotID, settings.UpsertRequest{
						MemoryProviderID: &item.ID,
					})
					if err != nil {
						return "", err
					}
					return formatChangedValueT(cc, cc.T("cmd.memory.label"), h.resolveMemoryProviderName(cc, before.MemoryProviderID), item.Name), nil
				}
			}
			return cc.T("cmd.memory.notFound", map[string]any{"name": fmt.Sprintf("%q", name), "command": CmdRef("memory list")}), nil
		},
	})
	return g
}
