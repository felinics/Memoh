package tools

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/messaging"
)

type ContactsProvider struct {
	contacts messaging.ContactReader
	logger   *slog.Logger
}

func NewContactsProvider(log *slog.Logger, contacts messaging.ContactReader) *ContactsProvider {
	if log == nil {
		log = slog.Default()
	}
	return &ContactsProvider{
		contacts: contacts,
		logger:   log.With(slog.String("tool", "contacts")),
	}
}

// Usage describes how get_contacts feeds other tools without restating those
// tools' own usage blocks.
func (*ContactsProvider) Usage(_ context.Context, session SessionContext, available AvailableTools) string {
	contactsRef, ok := available.Ref(ToolGetContacts())
	if !ok {
		return ""
	}
	var parts []string
	parts = append(parts, "Use "+contactsRef+" to list all known contacts and conversations. It returns each route's platform, conversation type, and `target`.")
	messageRefs := available.Refs(ToolSend(), ToolSpeak())
	if len(messageRefs) > 0 {
		if session.CanOmitMessagingTarget() {
			parts = append(parts, "For another channel/person, pass the returned `platform` and `target` to "+joinRefs(messageRefs, "or")+".")
		} else {
			parts = append(parts, "Pass the returned `platform` and `target` to "+joinRefs(messageRefs, "or")+" when this session needs to notify a contact.")
		}
	}
	if historyRefs := available.Refs(ToolListSessions(), ToolGetMessages()); len(historyRefs) > 0 {
		parts = append(parts, "Use the returned route and conversation metadata to choose the right session for "+joinRefs(historyRefs, "or")+".")
	}
	if searchRef, ok := available.Ref(ToolSearchMessages()); ok {
		parts = append(parts, "Use returned session/contact metadata as `session_id` or `contact_id` filters for "+searchRef+".")
	}
	return usageSection("Contacts & Messaging", parts)
}

func (p *ContactsProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	if p.contacts == nil {
		return nil, nil
	}
	sess := session
	return []sdk.Tool{
		{
			Name:        ToolGetContacts().String(),
			Description: "List all known contacts and conversations for the current bot. Returns platform, conversation type, reply target, and metadata for each route.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"platform": map[string]any{
						"type":        "string",
						"description": "Filter by channel platform (e.g. telegram, feishu). Returns all platforms when omitted.",
					},
				},
				"required": []string{},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				args := inputAsMap(input)
				botID := strings.TrimSpace(sess.BotID)
				if botID == "" {
					return nil, errors.New("bot_id is required")
				}
				routes, err := p.contacts.ListContacts(ctx.Context, botID)
				if err != nil {
					return nil, err
				}
				platformFilter := strings.ToLower(strings.TrimSpace(FirstStringArg(args, "platform")))
				contacts := make([]map[string]any, 0, len(routes))
				for _, r := range routes {
					if platformFilter != "" && !strings.EqualFold(r.Platform, platformFilter) {
						continue
					}
					entry := map[string]any{
						"route_id":          r.RouteID,
						"platform":          r.Platform,
						"conversation_type": r.ConversationType,
						"target":            r.ReplyTarget,
						"conversation_id":   r.ExternalConversationID,
						"last_active":       sess.FormatTime(r.UpdatedAt),
					}
					if len(r.Metadata) > 0 {
						if v, ok := r.Metadata["conversation_name"].(string); ok && v != "" {
							entry["display_name"] = v
						} else if v, ok := r.Metadata["sender_display_name"].(string); ok && v != "" {
							entry["display_name"] = v
						}
						if v, ok := r.Metadata["sender_username"].(string); ok && v != "" {
							entry["username"] = v
						}
						entry["metadata"] = r.Metadata
					}
					contacts = append(contacts, entry)
				}
				return map[string]any{
					"ok":       true,
					"bot_id":   botID,
					"count":    len(contacts),
					"contacts": contacts,
				}, nil
			},
		},
	}, nil
}
