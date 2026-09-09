package command

import (
	"strings"

	"github.com/felinics/memoh/internal/acl"
)

func (h *Handler) buildAccessGroup() *CommandGroup {
	g := newCommandGroup("access", "Inspect identity and permission context")
	g.DefaultAction = "show"
	g.Register(SubCommand{
		Name:  "show",
		Usage: "show - Show current identity, write access, and chat ACL context",
		Handler: func(cc CommandContext) (string, error) {
			writeAccess := cc.T("cmd.common.no")
			if cc.WriteAccess {
				writeAccess = cc.T("cmd.common.yes")
			}

			pairs := []kv{
				{cc.T("cmd.access.fieldBotRole"), fallbackValueT(cc, cc.Role)},
				{cc.T("cmd.access.fieldWriteCommands"), writeAccess},
				{cc.T("cmd.access.fieldChannel"), fallbackValueT(cc, cc.ChannelType)},
				{cc.T("cmd.access.fieldConversationType"), fallbackValueT(cc, cc.ConversationType)},
				// Identifier rows vanish when empty rather than printing "(none)";
				// they are support/debug detail, not the facts the user came for.
				{cc.T("cmd.access.fieldChannelIdentity"), strings.TrimSpace(cc.ChannelIdentityID)},
				{cc.T("cmd.access.fieldLinkedUser"), strings.TrimSpace(cc.UserID)},
				{cc.T("cmd.access.fieldConversationID"), strings.TrimSpace(cc.ConversationID)},
				{cc.T("cmd.access.fieldThreadID"), strings.TrimSpace(cc.ThreadID)},
			}
			if strings.TrimSpace(cc.RouteID) != "" {
				pairs = append(pairs, kv{cc.T("cmd.access.fieldRouteID"), cc.RouteID})
			}
			if strings.TrimSpace(cc.SessionID) != "" {
				pairs = append(pairs, kv{cc.T("cmd.access.fieldSessionID"), cc.SessionID})
			}

			aclStatus := cc.T("cmd.common.unavailable")
			if h.aclEvaluator != nil && strings.TrimSpace(cc.ChannelType) != "" {
				allowed, err := h.aclEvaluator.Evaluate(cc.Ctx, acl.EvaluateRequest{
					BotID:             cc.BotID,
					ChannelIdentityID: cc.ChannelIdentityID,
					ChannelType:       cc.ChannelType,
					SourceScope: acl.SourceScope{
						ConversationType: cc.ConversationType,
						ConversationID:   cc.ConversationID,
						ThreadID:         cc.ThreadID,
					},
				})
				switch {
				case err != nil:
					// Don't leak raw DB/driver error text into user output.
					aclStatus = cc.T("cmd.common.error")
				case allowed:
					aclStatus = cc.T("cmd.common.allowed")
				default:
					aclStatus = cc.T("cmd.common.denied")
				}
			}
			pairs = append(pairs, kv{cc.T("cmd.access.fieldChatACL"), aclStatus})

			return formatKVTitled(cc.T("cmd.access.title"), pairs), nil
		},
	})
	return g
}

func fallbackValueT(cc CommandContext, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return cc.T("cmd.common.none")
	}
	return value
}

func formatChangedValueT(cc CommandContext, label, before, after string) string {
	a := fallbackValueT(cc, after)
	if strings.EqualFold(strings.TrimSpace(before), strings.TrimSpace(after)) {
		return cc.T("cmd.common.alreadySet", map[string]any{"label": label, "value": renderValue(a)})
	}
	return cc.T("cmd.common.changedTo", map[string]any{"label": label, "value": renderValue(a)})
}
