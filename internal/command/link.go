package command

import (
	"context"
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/channelaccess"
)

// LinkConsumer binds the calling channel identity to the web user that owns a
// one-time link code. It is satisfied by channelaccess.Service.
type LinkConsumer interface {
	ConsumeLinkCode(ctx context.Context, token, channelIdentityID string) (channelaccess.Binding, error)
}

// SetLinkConsumer wires the account-link consumer used by the /link command.
func (h *Handler) SetLinkConsumer(c LinkConsumer) {
	h.linkConsumer = c
}

func (h *Handler) buildLinkGroup() *CommandGroup {
	g := newCommandGroup("link", "Link this account to your Memoh user")
	g.DefaultAction = "consume"
	g.Register(SubCommand{
		Name:  "consume",
		Usage: "link <code> - connect this account to your Memoh user",
		Handler: func(cc CommandContext) (string, error) {
			if h.linkConsumer == nil {
				return cc.T("cmd.link.unavailable"), nil
			}
			if strings.TrimSpace(cc.ChannelIdentityID) == "" {
				return cc.T("cmd.link.noIdentity"), nil
			}
			token := ""
			if len(cc.Args) > 0 {
				token = strings.TrimSpace(cc.Args[0])
			}
			if token == "" {
				return cc.T("cmd.link.missingCode"), nil
			}
			if _, err := h.linkConsumer.ConsumeLinkCode(cc.Ctx, token, cc.ChannelIdentityID); err != nil {
				switch {
				case errors.Is(err, channelaccess.ErrCodeNotFound):
					return cc.T("cmd.link.notFound"), nil
				case errors.Is(err, channelaccess.ErrCodeExpired):
					return cc.T("cmd.link.expired"), nil
				case errors.Is(err, channelaccess.ErrCodeConsumed):
					return cc.T("cmd.link.consumed"), nil
				default:
					return cc.T("cmd.link.failed"), nil
				}
			}
			return cc.T("cmd.link.success"), nil
		},
	})
	return g
}

// normalizeLinkShorthand rewrites "/link <code>" into "/link consume <code>" so the
// bare code reaches the consume handler's argument slice. Must run BEFORE Args is
// frozen onto CommandContext.
func normalizeLinkShorthand(resource string, parsed *ParsedCommand) {
	if resource == "link" && parsed.Action != "" && parsed.Action != "consume" {
		parsed.Args = append([]string{parsed.Action}, parsed.Args...)
		parsed.Action = "consume"
	}
}
