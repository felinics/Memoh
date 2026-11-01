package channel

import (
	"context"
	"maps"
	"strings"

	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/markdownmedia"
)

// ResolveMarkdownMessage archives the media a message references, for an IM
// destination. The agent server calls it before handing a message to the
// Channel process, which may not reach the bot workspace.
func ResolveMarkdownMessage(ctx context.Context, store OutboundAttachmentStore, botID string, channelType ChannelType, msg Message) Message {
	return resolveMarkdownMessage(ctx, store, ChannelConfig{BotID: botID, ChannelType: channelType}, msg)
}

// WithoutMarkdownMedia drops publication metadata a caller supplied itself.
// Only the agent publishes media, from snapshots it archived; other senders
// deliver their Markdown as written.
func WithoutMarkdownMedia(msg Message) Message {
	_, bound := msg.Metadata[markdownmedia.MetadataKey]
	_, earlier := msg.Metadata[markdownmedia.EarlierMetadataKey]
	if !bound && !earlier {
		return msg
	}
	msg.Metadata = maps.Clone(msg.Metadata)
	delete(msg.Metadata, markdownmedia.MetadataKey)
	delete(msg.Metadata, markdownmedia.EarlierMetadataKey)
	return msg
}

func resolveMarkdownMessage(ctx context.Context, store OutboundAttachmentStore, cfg ChannelConfig, msg Message) Message {
	// Web and Desktop render Markdown from history and open workspace links.
	if msg.Format == MessageFormatPlain || markdownmedia.IsWorkspaceSurface(string(cfg.ChannelType)) {
		return msg
	}
	if _, prepared := msg.Metadata[markdownmedia.MetadataKey]; prepared {
		return msg
	}
	mediaStore, _ := store.(markdownmedia.Store)
	bindings := markdownmedia.Resolve(ctx, mediaStore, cfg.BotID, msg.Text, markdownmedia.Options{Links: true})
	if len(bindings) == 0 {
		return msg
	}
	msg.Metadata = maps.Clone(msg.Metadata)
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	msg.Metadata[markdownmedia.MetadataKey] = bindings
	return msg
}

// expandMarkdownMessage runs before platform formatting and chunking. Each
// adapter receives ordinary text/attachment messages in the author's order.
func expandMarkdownMessage(msg OutboundMessage) ([]OutboundMessage, bool) {
	bindings := markdownmedia.Bindings(msg.Message.Metadata)
	if len(bindings) == 0 {
		return nil, false
	}
	base := msg.Message
	base.Metadata = maps.Clone(base.Metadata)
	delete(base.Metadata, markdownmedia.MetadataKey)
	delete(base.Metadata, markdownmedia.EarlierMetadataKey)
	base.Text = ""
	base.Attachments = nil
	base.Parts = nil
	base.Actions = nil
	base.ID = ""
	var result []OutboundMessage
	appendText := func(value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		part := base
		part.Text = value
		result = append(result, OutboundMessage{Target: msg.Target, Message: part})
	}
	type deliveryKey struct {
		hash  string
		image bool
	}
	seen := map[deliveryKey]bool{}
	markdownmedia.Visit(msg.Message.Text, bindings, appendText, func(binding markdownmedia.Binding) {
		if binding.ErrorCode != "" {
			locale, _ := base.Metadata["locale"].(string)
			appendText(binding.Label + " — " + i18n.New(locale).T("errors."+binding.ErrorCode))
			return
		}
		asset := binding.Asset
		key := deliveryKey{hash: asset.ContentHash, image: binding.Image}
		if seen[key] {
			return
		}
		seen[key] = true
		kind := AttachmentFile
		if binding.Image {
			kind = AttachmentImage
		}
		part := base
		part.Attachments = []Attachment{{Type: kind, ContentHash: asset.ContentHash, Mime: asset.Mime, Name: markdownmedia.Filename(binding.Target), Size: asset.SizeBytes, Metadata: map[string]any{"source_path": binding.Target, "send_as_file": !binding.Image}}}
		result = append(result, OutboundMessage{Target: msg.Target, Message: part})
	})
	if len(msg.Message.Attachments) > 0 {
		part := base
		part.Attachments = msg.Message.Attachments
		result = append(result, OutboundMessage{Target: msg.Target, Message: part})
	}
	if len(msg.Message.Parts) > 0 {
		part := base
		part.Parts = msg.Message.Parts
		result = append(result, OutboundMessage{Target: msg.Target, Message: part})
	}
	if len(result) > 0 {
		result[0].Message.ID = msg.Message.ID
		result[len(result)-1].Message.Actions = msg.Message.Actions
	}
	return result, true
}
