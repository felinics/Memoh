package inbound

import (
	"context"
	"slices"
	"strings"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/commandsyntax"
	"github.com/felinics/memoh/internal/i18n"
)

// runtimeModelResource maps the /model and /reasoning spellings the command
// layer accepts onto the runtime control they drive on an Agent session.
func runtimeModelResource(selector string) string {
	switch strings.ToLower(selector) {
	case "model", "models":
		return "model"
	case "reasoning", "reason", "effort", "think":
		return "reasoning"
	}
	return ""
}

// runtimeModelSlash answers /model and /reasoning on an external Agent
// session. Codex and Claude Code choose their model per session from their
// own catalog, remembered through the same preference the Web picker writes;
// the bot's chat model would not reach them. ACP changes its model inside the
// Agent, so it is pointed at the Web picker instead.
func (p *ChannelInboundProcessor) runtimeModelSlash(ctx context.Context, msg channel.InboundMessage, sender channel.StreamReplySender, identity InboundIdentity, service turn.RuntimeControlService, request turn.RuntimeControlRequest, resource string, parsed commandsyntax.ParsedCommand) error {
	loc := p.localizer(ctx, identity.BotID)
	state, err := service.RuntimeModel(ctx, request)
	if err != nil {
		return p.sendRuntimeControlError(ctx, sender, msg, identity, err)
	}
	if !state.Supported {
		return p.sendRuntimeControlText(ctx, sender, msg, loc.T("cmd."+resource+".agentUnsupported"))
	}
	arg := strings.TrimSpace(strings.Join(parsed.Args, " "))
	if parsed.Action != "set" || arg == "" {
		return p.sendRuntimeModelResult(ctx, msg, sender, loc, runtimeModelChoices(loc, resource, state))
	}
	if resource == "model" {
		model, ok := findRuntimeModel(state, arg)
		if !ok {
			return p.sendRuntimeControlText(ctx, sender, msg, loc.T("cmd.model.notFound", map[string]any{
				"name": `"` + arg + `"`, "command": command.CmdRef("model"),
			}))
		}
		request.ModelID = model.ID
	} else {
		current, _ := state.Model()
		level := strings.ToLower(arg)
		if !slices.Contains(current.ReasoningEfforts, level) {
			return p.sendRuntimeControlText(ctx, sender, msg, loc.T("cmd.reasoning.unknownLevel", map[string]any{
				"level": `"` + arg + `"`, "levels": strings.Join(current.ReasoningEfforts, ", "),
			}))
		}
		request.ReasoningEffort = level
	}
	state, err = service.SetRuntimeModel(ctx, request)
	if err != nil {
		return p.sendRuntimeControlError(ctx, sender, msg, identity, err)
	}
	return p.sendRuntimeModelResult(ctx, msg, sender, loc, runtimeModelChoices(loc, resource, state))
}

// runtimeModelChoices renders the session's model or effort as a one-level
// pick: an Agent catalog has no providers to drill through. Each button
// re-runs "/<resource> set <value>"; the list callback stashes values too
// long for Telegram's callback limit.
func runtimeModelChoices(loc *i18n.Localizer, resource string, state turn.RuntimeModelState) *command.Result {
	current, _ := state.Model()
	effort := strings.TrimSpace(state.ReasoningEffort)
	effortLabel := effort
	if effortLabel == "" {
		effortLabel = loc.T("cmd.common.default")
	}
	var header string
	var choices []command.ListItem
	if resource == "model" {
		name := current.Name
		if name == "" {
			name = state.ModelID
		}
		header = command.MdBold(loc.T("chrome.modelConfiguration")) + "\n\n" +
			loc.T("cmd.model.sessionCurrent", map[string]any{"model": name}) + "\n" +
			loc.T("chrome.reasoningLine", map[string]any{"effort": effortLabel}) + "\n\n" +
			loc.T("chrome.selectModel")
		// The default model leads, as in the Web picker.
		models := append([]turn.RuntimeModelOption(nil), state.Models...)
		for i := range models {
			if models[i].Default && i > 0 {
				models[0], models[i] = models[i], models[0]
				break
			}
		}
		for _, model := range models {
			choices = append(choices, command.ListItem{
				Label: model.Name, Selected: model.ID == current.ID && current.ID != "",
				Action: &command.ItemAction{Resource: "model", Action: "set", Args: []string{model.ID}},
			})
		}
	} else {
		header = command.MdBold(loc.T("cmd.reasoning.header")) + "\n" +
			loc.T("cmd.reasoning.current", map[string]any{"level": effortLabel})
		if len(current.ReasoningEfforts) == 0 {
			return &command.Result{Text: header + "\n\n" + loc.T("cmd.reasoning.uncontrollable")}
		}
		header += "\n\n" + loc.T("cmd.reasoning.choosePrompt")
		for _, level := range current.ReasoningEfforts {
			choices = append(choices, command.ListItem{
				Label: level, Selected: level == effort,
				Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{level}},
			})
		}
	}
	if len(choices) == 0 {
		return &command.Result{Text: header}
	}
	return &command.Result{
		Text:        header,
		Interactive: &command.Interactive{Kind: command.InteractiveChoices, Choices: &command.ChoicesView{Title: header, Choices: choices}},
	}
}

// sendRuntimeModelResult renders a pick like the command layer does: in place
// when it answers a tap on the previous card, as a quoted reply otherwise.
func (p *ChannelInboundProcessor) sendRuntimeModelResult(ctx context.Context, msg channel.InboundMessage, sender channel.StreamReplySender, loc *i18n.Localizer, result *command.Result) error {
	caps := p.channelCaps(msg.Channel)
	out := renderResult(result, RenderContext{Caps: caps, T: loc})
	if editID, ok := msg.Metadata["edit_message_id"].(string); ok && strings.TrimSpace(editID) != "" && caps.Edit {
		out.ID = strings.TrimSpace(editID)
	} else if mid := strings.TrimSpace(msg.Message.ID); mid != "" {
		out.Reply = &channel.ReplyRef{MessageID: mid}
	}
	return sender.Send(ctx, channel.OutboundMessage{Target: strings.TrimSpace(msg.ReplyTarget), Message: out})
}

// findRuntimeModel matches a typed or tapped model by id, then by name.
func findRuntimeModel(state turn.RuntimeModelState, target string) (turn.RuntimeModelOption, bool) {
	for _, model := range state.Models {
		if model.ID == target {
			return model, true
		}
	}
	for _, model := range state.Models {
		if strings.EqualFold(model.Name, target) || strings.EqualFold(model.ID, target) {
			return model, true
		}
	}
	return turn.RuntimeModelOption{}, false
}
