package tools

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/sessionmode"
	"github.com/memohai/memoh/internal/messaging"
)

type MessageProvider struct {
	exec *messaging.Executor
}

func NewMessageProvider(log *slog.Logger, sender messaging.Sender, reactor messaging.Reactor, resolver messaging.ChannelTypeResolver, assetResolver messaging.AssetResolver) *MessageProvider {
	if log == nil {
		log = slog.Default()
	}
	exec := &messaging.Executor{
		Sender:        sender,
		Reactor:       reactor,
		Resolver:      resolver,
		AssetResolver: assetResolver,
		Logger:        log.With(slog.String("tool", "message")),
	}
	if promoter, ok := sender.(messaging.AttachmentPromoter); ok {
		exec.Promoter = promoter
	}
	return &MessageProvider{exec: exec}
}

func (*MessageProvider) Usage(_ context.Context, session SessionContext, available AvailableTools) string {
	var parts []string
	if sendRef, ok := available.Ref(ToolSend()); ok {
		switch session.SessionType {
		case sessionmode.Discuss:
			parts = append(parts, "In Discuss, every addressed or forced turn must finish with one successful current-conversation "+sendRef+" call. Put all final audience-facing content for the turn in that single call because later sequential sends do not run after current delivery succeeds. `reply_to` is optional and controls only the platform's visible quote. Omit it whenever the audience can understand which message you mean without a quote; use it only when the quote is necessary to disambiguate a specific message, and only with a message ID visible in this turn; never emit delivery markers such as `[Sent ...]`. Ordinary Assistant text is private, never delivered, and cannot replace "+sendRef+".")
		case sessionmode.Schedule, sessionmode.Heartbeat:
			parts = append(parts, "Use "+sendRef+" only when the background task needs to notify a person or channel; specify `platform` and `target`.")
		default:
			if session.CanOmitMessagingTarget() {
				parts = append(parts, sendRef+": Send a file or attachment into the current conversation, or send a message/file/attachment to another channel/person. Use ordinary assistant text for normal replies in the current conversation.")
			} else {
				parts = append(parts, sendRef+": Send a message, file, or attachment. Specify `platform` and `target` in this session.")
			}
		}
		parts = append(parts, "Use `message.parts` only when a messaging tool needs precise structured output such as link/code_block/mention/heading/blockquote/list_item parts or inline styles; keep ordinary prose and Markdown in `text`.")
		if messagingSessionSupportsMarkdownMath(session) {
			parts = append(parts, "For Telegram targets, math formulas in Markdown text can use `$...$` for inline LaTeX and `$$...$$` for display LaTeX; do not wrap formulas in code blocks unless you want to show source code.")
		}
	}
	if reactRef, ok := available.Ref(ToolReact()); ok {
		if session.CanOmitMessagingTarget() {
			parts = append(parts, reactRef+": Add or remove an emoji reaction on a message. Omit `target` to react in the current conversation.")
		} else {
			parts = append(parts, reactRef+": Add or remove an emoji reaction on a message. Specify `platform` and `target` in this session.")
		}
	}
	return usageSection("Messaging", parts)
}

func (p *MessageProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	if session.IsSubagent {
		return nil, nil
	}
	var tools []sdk.Tool
	sess := session
	if p.exec.CanSend() {
		sendDescription, sendPlatformDescription, sendTargetDescription, sendRequired := sendToolPromptMetadata(session)
		tools = append(tools, sdk.Tool{
			Name:        ToolSend().String(),
			Description: sendDescription,
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"bot_id":   map[string]any{"type": "string", "description": "Bot ID, optional and defaults to current bot"},
					"platform": map[string]any{"type": "string", "description": sendPlatformDescription},
					"target":   map[string]any{"type": "string", "description": sendTargetDescription},
					"text":     map[string]any{"type": "string", "description": "Message text shortcut when message object is omitted"},
					"reply_to": sendReplyToSchema(),
					"attachments": map[string]any{
						"type":        "array",
						"description": "File paths, URLs, data URLs, or attachment objects to attach.",
						"items":       sendAttachmentItemSchema(),
					},
					"message": sendMessageObjectSchema(),
				},
				"required": sendRequired,
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execSend(ctx.Context, sess, ctx.ToolCallID, inputAsMap(input))
			},
		})
	}
	if p.exec.CanReact() {
		reactDescription, reactPlatformDescription, reactTargetDescription, reactRequired := reactToolPromptMetadata(session)
		tools = append(tools, sdk.Tool{
			Name:        ToolReact().String(),
			Description: reactDescription,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bot_id":     map[string]any{"type": "string", "description": "Bot ID, optional and defaults to current bot"},
					"platform":   map[string]any{"type": "string", "description": reactPlatformDescription},
					"target":     map[string]any{"type": "string", "description": reactTargetDescription},
					"message_id": map[string]any{"type": "string", "description": "The message ID to react to"},
					"emoji":      map[string]any{"type": "string", "description": "Emoji to react with (e.g. 👍, ❤️). Required when adding a reaction."},
					"remove":     map[string]any{"type": "boolean", "description": "If true, remove the reaction instead of adding it. Default false."},
				},
				"required": reactRequired,
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execReact(ctx.Context, sess, inputAsMap(input))
			},
		})
	}
	return tools, nil
}

func sendMessageObjectSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          "Structured message payload. Use text for ordinary messages; use parts only when you need explicit links, code blocks, mentions, emoji, headings, quotes, list items, or inline styles.",
		"additionalProperties": false,
		"properties": map[string]any{
			"format": map[string]any{
				"type":        "string",
				"description": "Rendering hint for text. Use markdown for ordinary Markdown text; use rich only with parts.",
				"enum":        []any{"plain", "markdown", "rich"},
			},
			"text": map[string]any{
				"type":        "string",
				"description": "Message body. Prefer this for ordinary prose and Markdown replies.",
			},
			"parts": map[string]any{
				"type":        "array",
				"description": "Structured rich body. Use only for explicit link/code_block/mention/emoji/heading/blockquote/list_item parts or styled spans.",
				"items":       sendMessagePartSchema(),
			},
			"attachments": map[string]any{
				"type":        "array",
				"description": "File paths, URLs, data URLs, or attachment objects to attach.",
				"items":       sendAttachmentItemSchema(),
			},
			"actions": map[string]any{
				"type":        "array",
				"description": "Optional action buttons. URL actions render only on channels with button support.",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"label", "url"},
					"properties": map[string]any{
						"type":  map[string]any{"type": "string"},
						"label": map[string]any{"type": "string"},
						"url":   map[string]any{"type": "string"},
						"row":   map[string]any{"type": "integer"},
					},
				},
			},
		},
	}
}

func sendReplyToSchema() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": "Optional message ID for a visible platform quote. Omit this field whenever the audience can understand the reference without a quote; it is never inferred from the triggering message. Only IDs visible in the current turn are accepted.",
	}
}

func sendAttachmentItemSchema() map[string]any {
	return map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string"},
			sendAttachmentObjectSchema(),
		},
	}
}

func sendAttachmentObjectSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"anyOf": []any{
			map[string]any{"required": []string{"path"}},
			map[string]any{"required": []string{"url"}},
			map[string]any{"required": []string{"base64"}},
			map[string]any{"required": []string{"content_hash"}},
			map[string]any{"required": []string{"platform_key"}},
		},
		"properties": map[string]any{
			"type": map[string]any{
				"type": "string",
				"enum": []any{
					string(messaging.AttachmentImage),
					string(messaging.AttachmentAudio),
					string(messaging.AttachmentVideo),
					string(messaging.AttachmentVoice),
					string(messaging.AttachmentFile),
					string(messaging.AttachmentGIF),
				},
			},
			"base64":          map[string]any{"type": "string"},
			"path":            map[string]any{"type": "string"},
			"url":             map[string]any{"type": "string"},
			"platform_key":    map[string]any{"type": "string"},
			"source_platform": map[string]any{"type": "string"},
			"content_hash":    map[string]any{"type": "string"},
			"name":            map[string]any{"type": "string"},
			"mime":            map[string]any{"type": "string"},
			"size":            map[string]any{"type": "integer"},
			"duration_ms":     map[string]any{"type": "integer"},
			"width":           map[string]any{"type": "integer"},
			"height":          map[string]any{"type": "integer"},
			"thumbnail_url":   map[string]any{"type": "string"},
			"caption":         map[string]any{"type": "string"},
			"metadata":        map[string]any{"type": "object"},
		},
	}
}

func sendMessagePartSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"type"},
		"properties": map[string]any{
			"type": map[string]any{
				"type": "string",
				"enum": []any{"text", "link", "code_block", "mention", "emoji", "heading", "blockquote", "list_item"},
			},
			"text": map[string]any{
				"type":        "string",
				"description": "Visible text for text/code_block/mention/emoji/heading/blockquote/list_item parts. Optional label for link parts; required for mention parts.",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "URL for link parts.",
			},
			"styles": map[string]any{
				"type":        "array",
				"description": "Inline styles for text-like parts.",
				"items": map[string]any{
					"type": "string",
					"enum": []any{"bold", "italic", "strikethrough", "code", "underline", "spoiler"},
				},
			},
			"language": map[string]any{
				"type":        "string",
				"description": "Language hint for code_block parts.",
			},
			"channel_identity_id": map[string]any{
				"type":        "string",
				"description": "Platform identity ID for mention parts when known.",
			},
			"emoji": map[string]any{
				"type":        "string",
				"description": "Emoji fallback for emoji parts.",
			},
		},
	}
}

func messagingSessionSupportsMarkdownMath(session SessionContext) bool {
	return strings.EqualFold(strings.TrimSpace(session.CurrentPlatform), "telegram")
}

func sendToolPromptMetadata(session SessionContext) (description string, platformDescription string, targetDescription string, required []string) {
	if session.SessionType == sessionmode.Discuss {
		if session.CanOmitMessagingTarget() {
			return "Publish an audience-visible message. Every addressed or forced Discuss turn must call this tool successfully before ending; ordinary Assistant text is private and never delivered. Omit target for the observed conversation; specify target only for another channel/person. The optional reply_to field adds a visible quote: omit it whenever the audience can identify the intended message without that quote.",
				"Channel platform name. Defaults to current session platform.",
				"Channel target (chat/group/thread ID). Optional — omit to send in the observed conversation.",
				[]string{}
		}
		return "Publish an audience-visible message. Every addressed or forced Discuss turn must call this tool successfully before ending; ordinary Assistant text is private and never delivered. Specify platform and target in this session. The optional reply_to field adds a visible quote: omit it whenever the audience can identify the intended message without that quote.",
			"Channel platform name. Required in this session.",
			"Channel target (chat/group/thread ID). Required in this session.",
			[]string{"platform", "target"}
	}
	if session.CanOmitMessagingTarget() {
		return "Send a file or attachment into the current conversation, or send a message, file, or attachment to another channel/person. Use ordinary assistant text for normal replies in the current conversation.",
			"Channel platform name. Defaults to current session platform.",
			"Channel target (chat/group/thread ID). Optional only for current-conversation attachments; specify it for another channel/person.",
			[]string{}
	}
	return "Send a message, file, or attachment. Specify platform and target when notifying a person or channel from this session.",
		"Channel platform name. Required in this session.",
		"Channel target (chat/group/thread ID). Required in this session.",
		[]string{"platform", "target"}
}

func reactToolPromptMetadata(session SessionContext) (description string, platformDescription string, targetDescription string, required []string) {
	if session.CanOmitMessagingTarget() {
		return "Add or remove an emoji reaction on a message. When target/platform are omitted, reacts in the current conversation.",
			"Channel platform name. Defaults to current session platform.",
			"Channel target (chat/group ID). Defaults to current session reply target.",
			[]string{"message_id"}
	}
	return "Add or remove an emoji reaction on a message. Specify platform and target in this session.",
		"Channel platform name. Required in this session.",
		"Channel target (chat/group ID). Required in this session.",
		[]string{"message_id", "platform", "target"}
}

func (p *MessageProvider) execSend(ctx context.Context, session SessionContext, toolCallID string, args map[string]any) (any, error) {
	if session.SessionType == sessionmode.Discuss {
		sendResult, err := p.exec.SendDirectForTool(ctx, toMessagingSession(session), "", toolCallID, args)
		if err != nil {
			return messageSendErrorResult(err)
		}
		resp := map[string]any{"ok": true}
		if !session.IsSameConversation(sendResult.Platform, sendResult.Target) {
			resp["platform"] = sendResult.Platform
			resp["target"] = sendResult.Target
			resp["delivered"] = "target"
		}
		return resp, nil
	}
	result, err := p.exec.Send(ctx, toMessagingSession(session), args)
	if err != nil {
		return messageSendErrorResult(err)
	}
	if result.Local && session.Emitter != nil {
		atts := channelAttachmentsToToolAttachments(result.LocalAttachments)
		if len(atts) > 0 {
			session.Emitter(ToolStreamEvent{
				Type:        StreamEventAttachment,
				ToolCallID:  toolCallID,
				Attachments: atts,
			})
		}
		resp := map[string]any{
			"ok":          true,
			"delivered":   "current_conversation",
			"attachments": len(atts),
		}
		if result.LocalTextOmitted {
			resp["text_delivered"] = false
			resp["note"] = "attachments were delivered to the current conversation, but message text is never sent this way; include it in your assistant reply text instead"
		}
		if result.MessageID != "" {
			resp["message_id"] = result.MessageID
		}
		return resp, nil
	}
	if result.Local {
		sendResult, err := p.exec.SendDirect(ctx, toMessagingSession(session), result.Target, args)
		if err != nil {
			return messageSendErrorResult(err)
		}
		resp := map[string]any{
			"ok":        true,
			"bot_id":    sendResult.BotID,
			"platform":  sendResult.Platform,
			"target":    sendResult.Target,
			"delivered": messageDeliveryLabel(session, sendResult.Platform, sendResult.Target),
		}
		if sendResult.MessageID != "" {
			resp["message_id"] = sendResult.MessageID
		}
		return resp, nil
	}
	resp := map[string]any{
		"ok":        true,
		"bot_id":    result.BotID,
		"platform":  result.Platform,
		"target":    result.Target,
		"delivered": messageDeliveryLabel(session, result.Platform, result.Target),
	}
	if result.MessageID != "" {
		resp["message_id"] = result.MessageID
	}
	return resp, nil
}

const messageReplyNotVisibleCode = "messaging.reply_not_visible"

func messageSendErrorResult(err error) (any, error) {
	if !errors.Is(err, messaging.ErrReplyMessageNotVisible) {
		return nil, err
	}
	return map[string]any{
		"ok":         false,
		"error_code": messageReplyNotVisibleCode,
		"retryable":  true,
		"guidance":   "Omit reply_to for an unquoted message, or retry with a message ID listed as visible in this turn.",
	}, nil
}

func messageDeliveryLabel(session SessionContext, platform, target string) string {
	if session.IsSameConversation(platform, target) {
		return "current_conversation"
	}
	return "target"
}

func channelAttachmentsToToolAttachments(atts []messaging.Attachment) []Attachment {
	if len(atts) == 0 {
		return nil
	}
	result := make([]Attachment, 0, len(atts))
	for _, a := range atts {
		result = append(result, toolAttachmentFromChannelAttachment(a))
	}
	return result
}

func (p *MessageProvider) execReact(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	result, err := p.exec.React(ctx, toMessagingSession(session), args)
	if err != nil {
		return nil, err
	}
	if result.Local && session.Emitter != nil {
		session.Emitter(ToolStreamEvent{
			Type: StreamEventReaction,
			Reactions: []Reaction{{
				Emoji:     result.Emoji,
				MessageID: result.MessageID,
				Remove:    result.Remove,
			}},
		})
	}
	return map[string]any{
		"ok": true, "bot_id": result.BotID, "platform": result.Platform,
		"target": result.Target, "message_id": result.MessageID, "emoji": result.Emoji, "action": result.Action,
	}, nil
}

func toMessagingSession(s SessionContext) messaging.SessionContext {
	session := messaging.SessionContext{
		BotID:              s.BotID,
		ChatID:             s.ChatID,
		CanOmitTarget:      s.CanOmitMessagingTarget() || s.SessionType == sessionmode.Discuss,
		AllowLocalShortcut: s.CanUseLocalMessagingShortcut(),
		CurrentPlatform:    s.CurrentPlatform,
		ReplyTarget:        s.ReplyTarget,
	}
	if s.SessionType == sessionmode.Discuss && strings.EqualFold(strings.TrimSpace(s.CurrentPlatform), "telegram") {
		session.AllowedReplyMessageIDs = make(map[string]struct{}, len(s.ReplyableMessageIDs))
		for _, raw := range s.ReplyableMessageIDs {
			if messageID := strings.TrimSpace(raw); messageID != "" {
				session.AllowedReplyMessageIDs[messageID] = struct{}{}
			}
		}
	}
	return session
}
