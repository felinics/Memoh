package inbound

import (
	"context"
	"strings"

	"github.com/felinics/memoh/internal/acl"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/route"
	"github.com/felinics/memoh/internal/slash"
)

// Inspect an existing route only: command discovery must not create a thread.
// Contextual ACL and thread authorization both apply.
func (p *ChannelInboundProcessor) runtimeSlash(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage, sender channel.StreamReplySender, identity InboundIdentity, decision slash.Decision) (slash.Decision, bool, error) {
	invocation := decision.Invocation
	if invocation == nil || !decision.Directed {
		return decision, false, nil
	}
	selector := invocation.Selector
	switch strings.ToLower(selector) {
	case "help", "start", "new", "stop", "approve", "reject", "respond", "skill":
		return decision, false, nil
	}
	service, ok := p.turnSvc.(turn.RuntimeControlService)
	if !ok || p.sessionEnsurer == nil {
		return decision, false, nil
	}
	finder, ok := p.routeResolver.(interface {
		Find(context.Context, string, string, string, string) (route.Route, error)
	})
	if !ok {
		return decision, false, nil
	}
	existing, err := finder.Find(ctx, identity.BotID, msg.Channel.String(), msg.Conversation.ID, extractThreadID(msg))
	if err != nil {
		return decision, false, nil
	}
	sess, err := p.sessionEnsurer.GetActiveSession(ctx, existing.ID)
	if err != nil || !sessionUsesExternalRuntime(sess) {
		return decision, false, nil
	}
	if p.acl != nil {
		allowed, err := p.acl.Evaluate(ctx, acl.EvaluateRequest{BotID: identity.BotID, ChannelIdentityID: identity.ChannelIdentityID, ChannelType: msg.Channel.String(), SourceScope: acl.SourceScope{ConversationType: channel.NormalizeConversationType(msg.Conversation.Type), ConversationID: strings.TrimSpace(msg.Conversation.ID), ThreadID: extractThreadID(msg)}})
		if err != nil || !allowed {
			return decision, true, p.sendSlashError(ctx, sender, msg, slash.CodePermissionDenied)
		}
	}
	request := turn.RuntimeControlRequest{TeamID: cfg.TeamID, BotID: identity.BotID, ThreadID: sess.ID, ActorID: identity.UserID, Command: selector, Language: p.localizer(ctx, identity.BotID).Locale()}
	if selector == "permission" {
		if hasSlashControlAttachments(msg) {
			return decision, true, p.sendSlashError(ctx, sender, msg, slash.CodeSlashAttachmentsUnsupported)
		}
		controls, err := service.RuntimeControls(ctx, request)
		if err != nil {
			return decision, true, p.sendRuntimeControlError(ctx, sender, msg, identity, err)
		}
		modes := controls.Modes
		if !modes.Supported {
			return decision, true, p.sendSlashError(ctx, sender, msg, slash.CodePermissionModeUnsupported)
		}
		if strings.TrimSpace(invocation.Rest) != "" {
			request.ModeID = strings.TrimSpace(invocation.Rest)
			modes, err = service.SetRuntimeMode(ctx, request)
			if err != nil {
				return decision, true, p.sendRuntimeControlError(ctx, sender, msg, identity, err)
			}
		}
		var lines []string
		for _, mode := range modes.AvailableModes {
			label := mode.Name + " — /permission " + mode.ID
			if mode.ID == modes.CurrentModeID {
				label = "✓ " + label
			}
			lines = append(lines, label)
		}
		return decision, true, p.sendRuntimeControlText(ctx, sender, msg, strings.Join(lines, "\n"))
	}
	commands, err := service.RuntimeCommands(ctx, request)
	if err != nil {
		return decision, true, p.sendRuntimeControlError(ctx, sender, msg, identity, err)
	}
	var command *turn.RuntimeCommand
	for i := range commands {
		if commands[i].Name == selector {
			command = &commands[i]
			break
		}
	}
	if command == nil {
		if decision.Kind == slash.DecisionCommandAction {
			return decision, false, nil
		}
		if decision.Kind == slash.DecisionNormalChat && strings.Contains(selector, "/") {
			return decision, false, nil
		}
		return decision, true, p.sendSlashError(ctx, sender, msg, slash.CodeUnknownSlash)
	}
	if command.Kind == turn.RuntimeCommandTurn {
		decision.Kind = slash.DecisionNormalChat
		decision.AgentCommand = selector
		decision.Invocation = nil // Runtime owns it; bypass the Memoh command handlers.
		return decision, false, nil
	}
	if hasSlashControlAttachments(msg) {
		return decision, true, p.sendSlashError(ctx, sender, msg, slash.CodeSlashAttachmentsUnsupported)
	}
	if command.RunningText != "" {
		if err := p.sendRuntimeControlText(ctx, sender, msg, command.RunningText); err != nil {
			return decision, true, err
		}
	}
	result, err := service.ExecuteRuntimeCommand(ctx, request)
	if err != nil {
		return decision, true, p.sendRuntimeControlError(ctx, sender, msg, identity, err)
	}
	if result == "" {
		result = command.CompletedText
	}
	return decision, true, p.sendRuntimeControlText(ctx, sender, msg, result)
}

func (p *ChannelInboundProcessor) sendRuntimeControlText(ctx context.Context, sender channel.StreamReplySender, msg channel.InboundMessage, text string) error {
	out := applyMessageFormat(channel.Message{Text: text}, p.channelCaps(msg.Channel))
	if msg.Message.ID != "" {
		out.Reply = &channel.ReplyRef{MessageID: msg.Message.ID}
	}
	return sender.Send(ctx, channel.OutboundMessage{Target: strings.TrimSpace(msg.ReplyTarget), Message: out})
}

func (p *ChannelInboundProcessor) sendRuntimeControlError(ctx context.Context, sender channel.StreamReplySender, msg channel.InboundMessage, identity InboundIdentity, err error) error {
	if externalAgentFeedbackFromError(err) != nil {
		return p.sendExternalAgentFeedbackError(ctx, sender, msg, identity, err)
	}
	code := apperror.CodeOf(err)
	if code == "" {
		code = apperror.CodeRuntimeControlFailed
	}
	loc := p.localizer(ctx, identity.BotID)
	key := "errors." + string(code)
	text := loc.T(key)
	if text == key {
		text = loc.T("errors.runtime_control.failed")
	}
	return p.sendRuntimeControlText(ctx, sender, msg, text)
}
