// Package channelmessaging adapts Agent-owned messaging ports to the Channel
// delivery runtime.
package messaging

import (
	"context"

	"github.com/memohai/memoh/domains/channel/delivery"
	"github.com/memohai/memoh/domains/channel/gateway"
)

type runtime interface {
	Send(ctx context.Context, botID string, channelType gateway.ChannelType, req gateway.SendRequest) error
	React(ctx context.Context, botID string, channelType gateway.ChannelType, req gateway.ReactRequest) error
}

type resolver interface {
	ParseChannelType(raw string) (gateway.ChannelType, error)
}

type Adapter struct {
	runtime  runtime
	resolver resolver
	assets   gateway.OutboundAttachmentStore
}

func New(runtime runtime, resolver resolver, assets gateway.OutboundAttachmentStore) *Adapter {
	return &Adapter{runtime: runtime, resolver: resolver, assets: assets}
}

func (a *Adapter) Send(ctx context.Context, botID string, platform delivery.Platform, req delivery.SendRequest) error {
	return a.runtime.Send(ctx, botID, gateway.ChannelType(platform), gateway.SendRequest{
		Target:            req.Target,
		ChannelIdentityID: req.ChannelIdentityID,
		Message:           toChannelMessage(req.Message),
	})
}

func (a *Adapter) React(ctx context.Context, botID string, platform delivery.Platform, req delivery.ReactRequest) error {
	return a.runtime.React(ctx, botID, gateway.ChannelType(platform), gateway.ReactRequest{
		Target:    req.Target,
		MessageID: req.MessageID,
		Emoji:     req.Emoji,
		Remove:    req.Remove,
	})
}

func (a *Adapter) ParseChannelType(raw string) (delivery.Platform, error) {
	kind, err := a.resolver.ParseChannelType(raw)
	return delivery.Platform(kind), err
}

func (a *Adapter) PromoteAttachments(ctx context.Context, botID string, platform delivery.Platform, msg delivery.Message) (delivery.Message, error) {
	if a == nil || a.assets == nil {
		return msg, nil
	}
	prepared, err := gateway.PrepareOutboundMessage(ctx, a.assets, gateway.ChannelConfig{
		BotID:       botID,
		ChannelType: gateway.ChannelType(platform),
	}, gateway.OutboundMessage{Message: toChannelMessage(msg)})
	if err != nil {
		return delivery.Message{}, err
	}
	return fromChannelMessage(prepared.LogicalMessage().Message), nil
}

func toChannelMessage(msg delivery.Message) gateway.Message {
	result := gateway.Message{
		ID:          msg.ID,
		Format:      gateway.MessageFormat(msg.Format),
		Text:        msg.Text,
		Attachments: toChannelAttachments(msg.Attachments),
		Actions:     make([]gateway.Action, len(msg.Actions)),
		Metadata:    msg.Metadata,
	}
	result.Parts = make([]gateway.MessagePart, len(msg.Parts))
	for i, part := range msg.Parts {
		styles := make([]gateway.MessageTextStyle, len(part.Styles))
		for j, style := range part.Styles {
			styles[j] = gateway.MessageTextStyle(style)
		}
		result.Parts[i] = gateway.MessagePart{
			Type:              gateway.MessagePartType(part.Type),
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
		result.Actions[i] = gateway.Action{
			Type: action.Type, Label: action.Label, Value: action.Value, URL: action.URL, Row: action.Row,
		}
	}
	if msg.Thread != nil {
		result.Thread = &gateway.ThreadRef{ID: msg.Thread.ID}
	}
	if msg.Reply != nil {
		result.Reply = &gateway.ReplyRef{
			Target:           msg.Reply.Target,
			MessageID:        msg.Reply.MessageID,
			Sender:           msg.Reply.Sender,
			Preview:          msg.Reply.Preview,
			Attachments:      toChannelAttachments(msg.Reply.Attachments),
			AttachmentsKnown: msg.Reply.AttachmentsKnown,
		}
	}
	if msg.Forward != nil {
		result.Forward = &gateway.ForwardRef{
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

func fromChannelMessage(msg gateway.Message) delivery.Message {
	result := delivery.Message{
		ID:          msg.ID,
		Format:      delivery.MessageFormat(msg.Format),
		Text:        msg.Text,
		Attachments: fromChannelAttachments(msg.Attachments),
		Actions:     make([]delivery.Action, len(msg.Actions)),
		Metadata:    msg.Metadata,
	}
	result.Parts = make([]delivery.MessagePart, len(msg.Parts))
	for i, part := range msg.Parts {
		styles := make([]delivery.MessageTextStyle, len(part.Styles))
		for j, style := range part.Styles {
			styles[j] = delivery.MessageTextStyle(style)
		}
		result.Parts[i] = delivery.MessagePart{
			Type:              delivery.MessagePartType(part.Type),
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
		result.Actions[i] = delivery.Action{
			Type: action.Type, Label: action.Label, Value: action.Value, URL: action.URL, Row: action.Row,
		}
	}
	if msg.Thread != nil {
		result.Thread = &delivery.ThreadRef{ID: msg.Thread.ID}
	}
	if msg.Reply != nil {
		result.Reply = &delivery.ReplyRef{
			Target:           msg.Reply.Target,
			MessageID:        msg.Reply.MessageID,
			Sender:           msg.Reply.Sender,
			Preview:          msg.Reply.Preview,
			Attachments:      fromChannelAttachments(msg.Reply.Attachments),
			AttachmentsKnown: msg.Reply.AttachmentsKnown,
		}
	}
	if msg.Forward != nil {
		result.Forward = &delivery.ForwardRef{
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

func toChannelAttachments(items []delivery.Attachment) []gateway.Attachment {
	result := make([]gateway.Attachment, len(items))
	for i, item := range items {
		result[i] = gateway.Attachment{
			Type: gateway.AttachmentType(item.Type), URL: item.URL, Path: item.Path,
			PlatformKey: item.PlatformKey, SourcePlatform: item.SourcePlatform,
			ContentHash: item.ContentHash, Base64: item.Base64, Name: item.Name,
			Size: item.Size, Mime: item.Mime, DurationMs: item.DurationMs,
			Width: item.Width, Height: item.Height, ThumbnailURL: item.ThumbnailURL,
			Caption: item.Caption, Metadata: item.Metadata,
		}
	}
	return result
}

func fromChannelAttachments(items []gateway.Attachment) []delivery.Attachment {
	result := make([]delivery.Attachment, len(items))
	for i, item := range items {
		result[i] = delivery.Attachment{
			Type: delivery.AttachmentType(item.Type), URL: item.URL, Path: item.Path,
			PlatformKey: item.PlatformKey, SourcePlatform: item.SourcePlatform,
			ContentHash: item.ContentHash, Base64: item.Base64, Name: item.Name,
			Size: item.Size, Mime: item.Mime, DurationMs: item.DurationMs,
			Width: item.Width, Height: item.Height, ThumbnailURL: item.ThumbnailURL,
			Caption: item.Caption, Metadata: item.Metadata,
		}
	}
	return result
}
