package qq

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/media"
)

const (
	qqMediaTypeImage = 1
	qqMediaTypeVideo = 2
	qqMediaTypeVoice = 3
	qqMediaTypeFile  = 4
)

type qqTargetKind string

const (
	qqTargetC2C     qqTargetKind = "c2c"
	qqTargetGroup   qqTargetKind = "group"
	qqTargetChannel qqTargetKind = "channel"
)

type qqTarget struct {
	Kind qqTargetKind
	ID   string
}

var qqUUIDTargetPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type attachmentUpload struct {
	Base64   string
	FileName string
	Mime     string
}

func (a *QQAdapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	resolvedTarget, err := a.resolveTarget(ctx, msg.Target)
	if err != nil {
		return err
	}
	target, err := parseTarget(resolvedTarget)
	if err != nil {
		return err
	}
	client := a.getOrCreateClient(cfg, parsed)
	replyTo := ""
	if msg.Message.Message.Reply != nil {
		replyTo = strings.TrimSpace(msg.Message.Message.Reply.MessageID)
	}

	text := strings.TrimSpace(msg.Message.Message.PlainText())
	if text != "" {
		useMarkdown := parsed.MarkdownSupport && msg.Message.Message.Format == channel.MessageFormatMarkdown && target.Kind != qqTargetChannel
		if msg.Message.Message.Format == channel.MessageFormatMarkdown && !useMarkdown {
			text = channel.StripInlineMarkup(text)
		}
		if err := client.sendText(ctx, target, text, replyTo, useMarkdown); err != nil {
			return err
		}
	}

	for _, att := range msg.Message.Attachments {
		if err := sendAttachment(ctx, client, target, replyTo, att); err != nil {
			return err
		}
	}
	return nil
}

func parseTarget(raw string) (qqTarget, error) {
	normalized := normalizeTarget(raw)
	switch {
	case strings.HasPrefix(normalized, "c2c:"):
		id := strings.TrimSpace(strings.TrimPrefix(normalized, "c2c:"))
		if id == "" {
			return qqTarget{}, errors.New("qq target c2c id is required")
		}
		if err := validateQQC2CTarget(id); err != nil {
			return qqTarget{}, err
		}
		return qqTarget{Kind: qqTargetC2C, ID: id}, nil
	case strings.HasPrefix(normalized, "group:"):
		id := strings.TrimSpace(strings.TrimPrefix(normalized, "group:"))
		if id == "" {
			return qqTarget{}, errors.New("qq target group id is required")
		}
		return qqTarget{Kind: qqTargetGroup, ID: id}, nil
	case strings.HasPrefix(normalized, "channel:"):
		id := strings.TrimSpace(strings.TrimPrefix(normalized, "channel:"))
		if id == "" {
			return qqTarget{}, errors.New("qq target channel id is required")
		}
		return qqTarget{Kind: qqTargetChannel, ID: id}, nil
	default:
		return qqTarget{}, errors.New("unsupported qq target")
	}
}

func validateQQC2CTarget(id string) error {
	if qqUUIDTargetPattern.MatchString(strings.TrimSpace(id)) {
		return errors.New("qq c2c target must be user_openid, not an internal UUID; use c2c:<user_openid>")
	}
	return nil
}

func sendAttachment(ctx context.Context, client *qqClient, target qqTarget, replyTo string, att channel.PreparedAttachment) error {
	if target.Kind == qqTargetChannel {
		switch att.Logical.Type {
		case channel.AttachmentImage, channel.AttachmentGIF:
			return errors.New("qq channel does not support image attachments")
		case channel.AttachmentVideo:
			return errors.New("qq channel does not support video attachments")
		case channel.AttachmentVoice, channel.AttachmentAudio:
			return errors.New("qq channel does not support voice attachments")
		case channel.AttachmentFile, "":
			return errors.New("qq channel does not support file attachments")
		default:
			return fmt.Errorf("unsupported qq attachment type: %s", att.Logical.Type)
		}
	}

	upload, err := prepareAttachmentUpload(ctx, att)
	if err != nil {
		return err
	}

	switch att.Logical.Type {
	case channel.AttachmentImage, channel.AttachmentGIF:
		fileInfo, err := client.uploadMedia(ctx, target, qqMediaTypeImage, upload.Base64, "")
		if err != nil {
			return err
		}
		return client.sendMedia(ctx, target, fileInfo, replyTo, att.Logical.Caption)
	case channel.AttachmentVideo:
		fileInfo, err := client.uploadMedia(ctx, target, qqMediaTypeVideo, upload.Base64, "")
		if err != nil {
			return err
		}
		return client.sendMedia(ctx, target, fileInfo, replyTo, att.Logical.Caption)
	case channel.AttachmentVoice, channel.AttachmentAudio:
		if !supportsQQVoiceUpload(att, upload.FileName, upload.Mime) {
			return errors.New("qq voice attachments require SILK/WAV/MP3/AMR input")
		}
		fileInfo, err := client.uploadMedia(ctx, target, qqMediaTypeVoice, upload.Base64, "")
		if err != nil {
			return err
		}
		return client.sendMedia(ctx, target, fileInfo, replyTo, att.Logical.Caption)
	case channel.AttachmentFile, "":
		fileInfo, err := client.uploadMedia(ctx, target, qqMediaTypeFile, upload.Base64, upload.FileName)
		if err != nil {
			return err
		}
		return client.sendMedia(ctx, target, fileInfo, replyTo, att.Logical.Caption)
	default:
		return fmt.Errorf("unsupported qq attachment type: %s", att.Logical.Type)
	}
}

func prepareAttachmentUpload(ctx context.Context, att channel.PreparedAttachment) (attachmentUpload, error) {
	if att.Kind != channel.PreparedAttachmentUpload {
		return attachmentUpload{}, fmt.Errorf("qq attachment requires upload source, got %s", att.Kind)
	}
	if att.Open == nil {
		return attachmentUpload{}, errors.New("qq attachment upload is not openable")
	}
	reader, err := att.Open(ctx)
	if err != nil {
		return attachmentUpload{}, err
	}
	defer func() { _ = reader.Close() }()
	data, err := media.ReadAllWithLimit(reader, media.MaxAssetBytes)
	if err != nil {
		return attachmentUpload{}, err
	}
	fileName := deriveAttachmentName(att)
	return attachmentUpload{
		Base64:   base64.StdEncoding.EncodeToString(data),
		FileName: fileName,
		Mime:     strings.TrimSpace(att.Mime),
	}, nil
}

func deriveAttachmentName(att channel.PreparedAttachment) string {
	if name := strings.TrimSpace(att.Name); name != "" {
		return name
	}
	return deriveFileNameFromMime(att.Mime, att.Logical.Type)
}

func deriveFileNameFromMime(mimeType string, attType channel.AttachmentType) string {
	ext := mimeExtension(mimeType)
	base := "attachment"
	switch attType {
	case channel.AttachmentImage, channel.AttachmentGIF:
		base = "image"
	case channel.AttachmentVideo:
		base = "video"
	case channel.AttachmentVoice, channel.AttachmentAudio:
		base = "audio"
	case channel.AttachmentFile:
		base = "file"
	}
	return base + ext
}

func mimeExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/amr":
		return ".amr"
	case "application/pdf":
		return ".pdf"
	default:
		return ""
	}
}

func supportsQQVoiceUpload(att channel.PreparedAttachment, fileName string, resolvedMime string) bool {
	check := strings.ToLower(strings.TrimSpace(fileName))
	if check == "" {
		check = strings.ToLower(strings.TrimSpace(att.Name))
	}
	for _, ext := range []string{".silk", ".slk", ".amr", ".wav", ".mp3"} {
		if strings.HasSuffix(check, ext) {
			return true
		}
	}
	mimeType := strings.ToLower(strings.TrimSpace(resolvedMime))
	if mimeType == "" {
		mimeType = strings.ToLower(strings.TrimSpace(att.Mime))
	}
	switch mimeType {
	case "audio/silk", "audio/amr", "audio/wav", "audio/x-wav", "audio/mpeg", "audio/mp3":
		return true
	default:
		return false
	}
}
