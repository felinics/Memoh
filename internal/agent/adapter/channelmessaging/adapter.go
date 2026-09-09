// Package channelmessaging adapts Agent-owned messaging ports to the Channel
// delivery runtime.
package channelmessaging

import (
	"context"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/messaging"
)

type runtime interface {
	Send(ctx context.Context, botID string, channelType channel.ChannelType, req channel.SendRequest) error
	React(ctx context.Context, botID string, channelType channel.ChannelType, req channel.ReactRequest) error
}

type resolver interface {
	ParseChannelType(raw string) (channel.ChannelType, error)
}

type Adapter struct {
	runtime  runtime
	resolver resolver
	assets   channel.OutboundAttachmentStore
}

func New(runtime runtime, resolver resolver, assets channel.OutboundAttachmentStore) *Adapter {
	return &Adapter{runtime: runtime, resolver: resolver, assets: assets}
}

func (a *Adapter) Send(ctx context.Context, botID string, platform messaging.Platform, req messaging.SendRequest) error {
	return a.runtime.Send(ctx, botID, channel.ChannelType(platform), channel.SendRequest{
		Target:            req.Target,
		ChannelIdentityID: req.ChannelIdentityID,
		Message:           toChannelMessage(req.Message),
	})
}

func (a *Adapter) React(ctx context.Context, botID string, platform messaging.Platform, req messaging.ReactRequest) error {
	return a.runtime.React(ctx, botID, channel.ChannelType(platform), channel.ReactRequest{
		Target:    req.Target,
		MessageID: req.MessageID,
		Emoji:     req.Emoji,
		Remove:    req.Remove,
	})
}

func (a *Adapter) ParseChannelType(raw string) (messaging.Platform, error) {
	kind, err := a.resolver.ParseChannelType(raw)
	return messaging.Platform(kind), err
}

func (a *Adapter) PromoteAttachments(ctx context.Context, botID string, platform messaging.Platform, msg messaging.Message) (messaging.Message, error) {
	if a == nil || a.assets == nil {
		return msg, nil
	}
	prepared, err := channel.PrepareOutboundMessage(ctx, a.assets, channel.ChannelConfig{
		BotID:       botID,
		ChannelType: channel.ChannelType(platform),
	}, channel.OutboundMessage{Message: toChannelMessage(msg)})
	if err != nil {
		return messaging.Message{}, err
	}
	return fromChannelMessage(prepared.LogicalMessage().Message), nil
}

func toChannelMessage(msg messaging.Message) channel.Message {
	result := channel.Message{
		ID:          msg.ID,
		Format:      channel.MessageFormat(msg.Format),
		Text:        msg.Text,
		Attachments: toChannelAttachments(msg.Attachments),
		Actions:     make([]channel.Action, len(msg.Actions)),
		Metadata:    msg.Metadata,
	}
	result.Parts = make([]channel.MessagePart, len(msg.Parts))
	for i, part := range msg.Parts {
		styles := make([]channel.MessageTextStyle, len(part.Styles))
		for j, style := range part.Styles {
			styles[j] = channel.MessageTextStyle(style)
		}
		result.Parts[i] = channel.MessagePart{
			Type:              channel.MessagePartType(part.Type),
			Text:              part.Text,
			URL:               part.URL,
			Styles:            styles,
			Language:          part.Language,
			ChannelIdentityID: part.ChannelIdentityID,
			Emoji:             part.Emoji,
			Metadata:          part.Metadata,
		}
	}
	for i, action := range msg.Actions {
		result.Actions[i] = channel.Action{
			Type: action.Type, Label: action.Label, Value: action.Value, URL: action.URL, Row: action.Row,
		}
	}
	if msg.Thread != nil {
		result.Thread = &channel.ThreadRef{ID: msg.Thread.ID}
	}
	if msg.Reply != nil {
		result.Reply = &channel.ReplyRef{
			Target:           msg.Reply.Target,
			MessageID:        msg.Reply.MessageID,
			Sender:           msg.Reply.Sender,
			Preview:          msg.Reply.Preview,
			Attachments:      toChannelAttachments(msg.Reply.Attachments),
			AttachmentsKnown: msg.Reply.AttachmentsKnown,
		}
	}
	if msg.Forward != nil {
		result.Forward = &channel.ForwardRef{
			MessageID:          msg.Forward.MessageID,
			FromUserID:         msg.Forward.FromUserID,
			FromConversationID: msg.Forward.FromConversationID,
			Sender:             msg.Forward.Sender,
			Date:               msg.Forward.Date,
			AttachmentsKnown:   msg.Forward.AttachmentsKnown,
		}
	}
	return result
}

func fromChannelMessage(msg channel.Message) messaging.Message {
	result := messaging.Message{
		ID:          msg.ID,
		Format:      messaging.MessageFormat(msg.Format),
		Text:        msg.Text,
		Attachments: fromChannelAttachments(msg.Attachments),
		Actions:     make([]messaging.Action, len(msg.Actions)),
		Metadata:    msg.Metadata,
	}
	result.Parts = make([]messaging.MessagePart, len(msg.Parts))
	for i, part := range msg.Parts {
		styles := make([]messaging.MessageTextStyle, len(part.Styles))
		for j, style := range part.Styles {
			styles[j] = messaging.MessageTextStyle(style)
		}
		result.Parts[i] = messaging.MessagePart{
			Type:              messaging.MessagePartType(part.Type),
			Text:              part.Text,
			URL:               part.URL,
			Styles:            styles,
			Language:          part.Language,
			ChannelIdentityID: part.ChannelIdentityID,
			Emoji:             part.Emoji,
			Metadata:          part.Metadata,
		}
	}
	for i, action := range msg.Actions {
		result.Actions[i] = messaging.Action{
			Type: action.Type, Label: action.Label, Value: action.Value, URL: action.URL, Row: action.Row,
		}
	}
	if msg.Thread != nil {
		result.Thread = &messaging.ThreadRef{ID: msg.Thread.ID}
	}
	if msg.Reply != nil {
		result.Reply = &messaging.ReplyRef{
			Target:           msg.Reply.Target,
			MessageID:        msg.Reply.MessageID,
			Sender:           msg.Reply.Sender,
			Preview:          msg.Reply.Preview,
			Attachments:      fromChannelAttachments(msg.Reply.Attachments),
			AttachmentsKnown: msg.Reply.AttachmentsKnown,
		}
	}
	if msg.Forward != nil {
		result.Forward = &messaging.ForwardRef{
			MessageID:          msg.Forward.MessageID,
			FromUserID:         msg.Forward.FromUserID,
			FromConversationID: msg.Forward.FromConversationID,
			Sender:             msg.Forward.Sender,
			Date:               msg.Forward.Date,
			AttachmentsKnown:   msg.Forward.AttachmentsKnown,
		}
	}
	return result
}

func toChannelAttachments(items []messaging.Attachment) []channel.Attachment {
	result := make([]channel.Attachment, len(items))
	for i, item := range items {
		result[i] = channel.Attachment{
			Type: channel.AttachmentType(item.Type), URL: item.URL, Path: item.Path,
			PlatformKey: item.PlatformKey, SourcePlatform: item.SourcePlatform,
			ContentHash: item.ContentHash, Base64: item.Base64, Name: item.Name,
			Size: item.Size, Mime: item.Mime, DurationMs: item.DurationMs,
			Width: item.Width, Height: item.Height, ThumbnailURL: item.ThumbnailURL,
			Caption: item.Caption, Metadata: item.Metadata,
		}
	}
	return result
}

func fromChannelAttachments(items []channel.Attachment) []messaging.Attachment {
	result := make([]messaging.Attachment, len(items))
	for i, item := range items {
		result[i] = messaging.Attachment{
			Type: messaging.AttachmentType(item.Type), URL: item.URL, Path: item.Path,
			PlatformKey: item.PlatformKey, SourcePlatform: item.SourcePlatform,
			ContentHash: item.ContentHash, Base64: item.Base64, Name: item.Name,
			Size: item.Size, Mime: item.Mime, DurationMs: item.DurationMs,
			Width: item.Width, Height: item.Height, ThumbnailURL: item.ThumbnailURL,
			Caption: item.Caption, Metadata: item.Metadata,
		}
	}
	return result
}
