package channel

import (
	"context"
	"maps"
	"strings"

	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/markdownmedia"
)

func resolveMarkdownMessage(ctx context.Context, store OutboundAttachmentStore, cfg ChannelConfig, msg Message) Message {
	if msg.Format == MessageFormatPlain {
		return msg
	}
	if _, prepared := msg.Metadata[markdownmedia.MetadataKey]; prepared {
		return msg
	}
	mediaStore, _ := store.(markdownmedia.Store)
	bindings := markdownmedia.Resolve(ctx, mediaStore, cfg.BotID, msg.Text)
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
	seen := map[string]bool{}
	markdownmedia.Visit(msg.Message.Text, bindings, appendText, func(binding markdownmedia.Binding) {
		if binding.ErrorCode != "" {
			locale, _ := base.Metadata["locale"].(string)
			appendText(binding.Label + " — " + i18n.New(locale).T(binding.ErrorCode))
			return
		}
		asset := binding.Asset
		if seen[asset.ContentHash] {
			return
		}
		seen[asset.ContentHash] = true
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
