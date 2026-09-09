package line

import (
	"context"
	"errors"
	"log/slog"
	neturl "net/url"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"

	"github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/publicmedia"
)

const (
	lineImageOriginalMaxBytes = publicmedia.OriginalMaxBytes
	lineImagePreviewMaxBytes  = publicmedia.PreviewMaxBytes
)

func (a *Adapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	_, err := a.sendPrepared(ctx, cfg, msg)
	return err
}

func (a *Adapter) sendPrepared(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) (int, error) {
	creds, err := parseConfigForUse(cfg.Credentials)
	if err != nil {
		return 0, err
	}
	target := normalizeTarget(msg.Target)
	if target == "" {
		return 0, errors.New("line target is required")
	}
	callCtx, cancel := context.WithTimeout(ctx, lineAPITimeout)
	defer cancel()
	client, err := a.client.NewMessagingClient(callCtx, creds.ChannelAccessToken)
	if err != nil {
		return 0, sanitizeLineError("line create messaging client failed", err)
	}

	text := strings.TrimSpace(msg.Message.Message.PlainText())
	imageMessages := make([]messaging_api.MessageInterface, 0)
	skippedAttachments := 0
	for _, att := range msg.Message.Attachments {
		image, ok := a.lineImageMessage(cfg, att)
		if !ok {
			skippedAttachments++
			continue
		}
		imageMessages = append(imageMessages, image)
	}
	if skippedAttachments > 0 {
		return 0, errors.New("line message has unsupported attachments")
	}

	var sent int
	if text != "" {
		for _, batch := range batchLineMessages(lineTextMessages(text), 5) {
			if err := a.pushMessages(client, target, batch); err != nil {
				return sent, err
			}
			sent++
		}
	}
	for _, batch := range batchLineMessages(imageMessages, 5) {
		if err := a.pushMessages(client, target, batch); err != nil {
			return sent, err
		}
		sent++
	}
	if sent == 0 {
		if text == "" && len(msg.Message.Attachments) == 0 && msg.Message.Message.IsEmpty() {
			return 0, errors.New("line message is required")
		}
		return 0, errors.New("line message has no supported content")
	}
	return sent, nil
}

func (a *Adapter) pushMessages(client messagingClient, target string, messages []messaging_api.MessageInterface) error {
	if len(messages) == 0 {
		return nil
	}
	_, err := client.PushMessage(&messaging_api.PushMessageRequest{
		To:       target,
		Messages: messages,
	}, "")
	if err != nil {
		a.logWarn("line push message failed",
			slog.String("target_hash", hashValue(target)),
			slog.String("reason", "push_api_failed"),
		)
		return sanitizeLineError("line push failed", err)
	}
	return nil
}

func lineTextMessages(text string) []messaging_api.MessageInterface {
	chunks := splitLINETextByUTF16CodeUnits(text, lineMaxMessageUTF16Units)
	messages := make([]messaging_api.MessageInterface, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk = strings.TrimSpace(chunk); chunk != "" {
			messages = append(messages, messaging_api.TextMessage{Text: chunk})
		}
	}
	return messages
}

func batchLineMessages(messages []messaging_api.MessageInterface, size int) [][]messaging_api.MessageInterface {
	if len(messages) == 0 {
		return nil
	}
	if size <= 0 {
		size = 5
	}
	batches := make([][]messaging_api.MessageInterface, 0, (len(messages)+size-1)/size)
	for start := 0; start < len(messages); start += size {
		end := start + size
		if end > len(messages) {
			end = len(messages)
		}
		batches = append(batches, messages[start:end])
	}
	return batches
}

func (a *Adapter) lineImageMessage(cfg channel.ChannelConfig, att channel.PreparedAttachment) (messaging_api.MessageInterface, bool) {
	originalURL, previewURL, ok, reason := a.lineImageURLs(cfg, att)
	if !ok {
		total := a.incrementCounter("line_outbound_image_skipped_no_public_url")
		a.logWarn("line outbound attachment skipped",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.String("attachment_type", string(att.Logical.Type)),
			slog.String("reason", reason),
			slog.Int64("total", total),
		)
		return nil, false
	}
	return messaging_api.ImageMessage{
		OriginalContentUrl: originalURL,
		PreviewImageUrl:    previewURL,
	}, true
}

func (a *Adapter) lineImageURLs(cfg channel.ChannelConfig, att channel.PreparedAttachment) (string, string, bool, string) {
	if att.Logical.Type != "" && att.Logical.Type != channel.AttachmentImage {
		return "", "", false, "unsupported_attachment_type"
	}
	if att.Kind == channel.PreparedAttachmentPublicURL {
		originalURL, ok, reason := allowedLineImageURL(att, lineImageOriginalMaxBytes)
		if !ok {
			return "", "", false, reason
		}
		previewURL := strings.TrimSpace(att.Logical.ThumbnailURL)
		if previewURL != "" {
			previewAtt := att
			previewAtt.PublicURL = previewURL
			previewAtt.Logical.URL = previewURL
			previewAtt.Size = 0
			previewAtt.Logical.Size = 0
			if _, ok, _ := allowedLineImageURL(previewAtt, lineImagePreviewMaxBytes); ok {
				return originalURL, previewURL, true, ""
			}
		}
		if knownLineImageSize(att) > lineImagePreviewMaxBytes {
			return "", "", false, "missing_preview_url_for_large_image"
		}
		return originalURL, originalURL, true, ""
	}
	originalURL, previewURL, ok, reason := a.publicMediaLineImageURLs(cfg, att)
	if !ok {
		return "", "", false, reason
	}
	return originalURL, previewURL, true, ""
}

func (a *Adapter) publicMediaLineImageURLs(cfg channel.ChannelConfig, att channel.PreparedAttachment) (string, string, bool, string) {
	if att.Kind != channel.PreparedAttachmentUpload {
		return "", "", false, "attachment_not_public_url"
	}
	if !isSupportedLineImageMime(att) {
		return "", "", false, "unsupported_image_mime"
	}
	if knownLineImageSize(att) > lineImageOriginalMaxBytes {
		return "", "", false, "image_too_large"
	}
	base := ""
	if a != nil && a.publicBaseURLProvider != nil {
		base = strings.TrimRight(strings.TrimSpace(a.publicBaseURLProvider.PublicBaseURL()), "/")
	}
	if base == "" {
		return "", "", false, "public_base_url_unavailable"
	}
	botID := strings.TrimSpace(cfg.BotID)
	if botID == "" {
		botID = attachment.MetadataString(att.Logical.Metadata, attachment.MetadataKeyBotID)
	}
	if botID == "" {
		return "", "", false, "bot_id_required_for_public_media"
	}
	contentHash := strings.ToLower(strings.TrimSpace(att.Logical.ContentHash))
	if !publicmedia.IsContentHash(contentHash) {
		return "", "", false, "content_hash_required_for_public_media"
	}
	name := lineImageFilename(att)
	signer, ok := a.publicBaseURLProvider.(publicMediaPathSigner)
	if !ok {
		return "", "", false, "public_media_signer_unavailable"
	}
	channelType := strings.TrimSpace(cfg.ChannelType.String())
	if channelType == "" {
		channelType = channel.ChannelTypeLine.String()
	}
	originalPath, ok := signer.SignPublicMediaPath(publicmedia.OriginalPath(channelType, botID, contentHash, name))
	if !ok {
		return "", "", false, "public_media_signature_failed"
	}
	previewPath, ok := signer.SignPublicMediaPath(publicmedia.PreviewPath(channelType, botID, contentHash))
	if !ok {
		return "", "", false, "public_media_signature_failed"
	}
	originalURL := base + originalPath
	previewURL := base + previewPath
	if len(originalURL) > 2000 || len(previewURL) > 2000 {
		return "", "", false, "public_media_url_too_long"
	}
	return originalURL, previewURL, true, ""
}

func allowedLineImageURL(att channel.PreparedAttachment, maxBytes int64) (string, bool, string) {
	if att.Kind != channel.PreparedAttachmentPublicURL {
		return "", false, "attachment_not_public_url"
	}
	raw := strings.TrimSpace(att.PublicURL)
	if raw == "" {
		raw = strings.TrimSpace(att.Logical.URL)
	}
	if raw == "" {
		return "", false, "missing_url"
	}
	if len(raw) > 2000 || !utf8.ValidString(raw) {
		return "", false, "invalid_url_length_or_encoding"
	}
	parsed, err := neturl.Parse(raw)
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", false, "url_not_https"
	}
	if parsed.User != nil {
		return "", false, "url_has_userinfo"
	}
	if !channel.IsPublicHost(parsed.Hostname()) {
		return "", false, "url_host_not_public"
	}
	mimeType := attachment.NormalizeMime(att.Mime)
	if mimeType == "" {
		mimeType = attachment.NormalizeMime(att.Logical.Mime)
	}
	ext := strings.ToLower(filepath.Ext(parsed.Path))
	if mimeType != "" && mimeType != "image/jpeg" && mimeType != "image/png" {
		return "", false, "unsupported_image_mime"
	}
	if mimeType == "" && ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		return "", false, "unsupported_image_extension"
	}
	if maxBytes > 0 && knownLineImageSize(att) > maxBytes {
		return "", false, "image_too_large"
	}
	return raw, true, ""
}

func knownLineImageSize(att channel.PreparedAttachment) int64 {
	if att.Size > 0 {
		return att.Size
	}
	return att.Logical.Size
}

func isSupportedLineImageMime(att channel.PreparedAttachment) bool {
	mimeType := attachment.NormalizeMime(att.Mime)
	if mimeType == "" {
		mimeType = attachment.NormalizeMime(att.Logical.Mime)
	}
	if mimeType != "" {
		return mimeType == "image/jpeg" || mimeType == "image/png"
	}
	name := lineImageFilename(att)
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png"
}

func lineImageFilename(att channel.PreparedAttachment) string {
	name := strings.TrimSpace(att.Name)
	if name == "" {
		name = strings.TrimSpace(att.Logical.Name)
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
		return filepath.Base(name)
	}
	mimeType := attachment.NormalizeMime(att.Mime)
	if mimeType == "" {
		mimeType = attachment.NormalizeMime(att.Logical.Mime)
	}
	if mimeType == "image/jpeg" {
		return "image.jpg"
	}
	return "image.png"
}

func splitLINETextByUTF16CodeUnits(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if limit <= 0 {
		return []string{text}
	}
	chunks := make([]string, 0, 1)
	var builder strings.Builder
	units := 0
	for _, r := range text {
		runeUnits := 1
		if r1, r2 := utf16.EncodeRune(r); r1 != 0xfffd || r2 != 0xfffd {
			runeUnits = 2
		}
		if units > 0 && units+runeUnits > limit {
			chunk := strings.TrimSpace(builder.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			builder.Reset()
			units = 0
		}
		builder.WriteRune(r)
		units += runeUnits
	}
	if chunk := strings.TrimSpace(builder.String()); chunk != "" {
		chunks = append(chunks, chunk)
	}
	return chunks
}

func lineLogicalAttachments(attachments []channel.PreparedAttachment) []channel.Attachment {
	if len(attachments) == 0 {
		return nil
	}
	items := make([]channel.Attachment, 0, len(attachments))
	for _, att := range attachments {
		items = append(items, att.Logical)
	}
	return items
}
