package command

import (
	"github.com/felinics/memoh/internal/settings"
)

// buildMemoryGroup exposes the bot's single Built-in Memory switch.
func (h *Handler) buildMemoryGroup() *CommandGroup {
	g := newCommandGroup("memory", "Turn memory on or off")
	g.DefaultAction = "status" // bare /memory shows the switch
	g.Register(SubCommand{
		Name:  "status",
		Usage: "status - Show whether memory is on",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if h.settingsService == nil {
				return &Result{Text: cc.T("cmd.memory.unavailable")}, nil
			}
			settingsResp, err := h.getBotSettings(cc)
			if err != nil {
				return nil, err
			}
			return memoryStatusResult(cc, settingsResp.MemoryEnabled), nil
		},
	})
	g.Register(h.memorySwitchCommand("on", true))
	g.Register(h.memorySwitchCommand("off", false))
	return g
}

func (h *Handler) memorySwitchCommand(name string, enabled bool) SubCommand {
	return SubCommand{
		Name:    name,
		Usage:   name + " - Turn memory " + name + " for this bot",
		IsWrite: true,
		Handler: func(cc CommandContext) (string, error) {
			if h.settingsService == nil {
				return cc.T("cmd.memory.unavailable"), nil
			}
			before, err := h.getBotSettings(cc)
			if err != nil {
				return "", err
			}
			if _, err := h.settingsService.UpsertBot(cc.Ctx, cc.BotID, settings.UpsertRequest{
				MemoryEnabled: &enabled,
			}); err != nil {
				return "", err
			}
			return formatChangedValueT(cc, cc.T("cmd.memory.label"), memoryStateLabel(cc, before.MemoryEnabled), memoryStateLabel(cc, enabled)), nil
		},
	}
}

func memoryStateLabel(cc CommandContext, enabled bool) string {
	if enabled {
		return cc.T("cmd.common.on")
	}
	return cc.T("cmd.common.off")
}

// memoryStatusResult shows the current state with a one-tap switch; text-only
// channels get the equivalent command in the body.
func memoryStatusResult(cc CommandContext, enabled bool) *Result {
	title := cc.T("cmd.memory.status", map[string]any{"state": memoryStateLabel(cc, enabled)})
	next, label, hintKey := "on", cc.T("cmd.memory.action.on"), "cmd.memory.hintOn"
	if enabled {
		next, label, hintKey = "off", cc.T("cmd.memory.action.off"), "cmd.memory.hintOff"
	}
	body := title + "\n" + cc.T(hintKey, map[string]any{"command": CmdRef("memory " + next)})
	choices := []ListItem{{Label: label, Action: &ItemAction{Resource: "memory", Action: next}}}
	return &Result{
		Text:        body,
		Interactive: &Interactive{Kind: InteractiveChoices, Choices: &ChoicesView{Title: title, Choices: choices}},
	}
}
