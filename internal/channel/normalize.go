package channel

import (
	"path/filepath"
	"strings"

	"github.com/felinics/memoh/internal/attachment"
)

// InferAttachmentType infers a canonical attachment type from type/mime/name.
func InferAttachmentType(currentType AttachmentType, mime, name string) AttachmentType {
	switch strings.ToLower(strings.TrimSpace(string(currentType))) {
	case string(AttachmentImage):
		return AttachmentImage
	case string(AttachmentGIF):
		return AttachmentGIF
	case string(AttachmentAudio):
		return AttachmentAudio
	case string(AttachmentVoice):
		return AttachmentVoice
	case string(AttachmentVideo):
		return AttachmentVideo
	case string(AttachmentFile):
		// keep inferring below for better classification
	default:
		// unknown type: infer from mime/name
	}

	normalizedMime := attachment.NormalizeMime(mime)
	switch {
	case strings.HasPrefix(normalizedMime, "image/gif"):
		return AttachmentGIF
	case strings.HasPrefix(normalizedMime, "image/"):
		return AttachmentImage
	case strings.HasPrefix(normalizedMime, "audio/"):
		return AttachmentAudio
	case strings.HasPrefix(normalizedMime, "video/"):
		return AttachmentVideo
	}

	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(strings.TrimSpace(name))))
	switch ext {
	case ".gif":
		return AttachmentGIF
	case ".jpg", ".jpeg", ".png", ".webp", ".bmp", ".heic", ".heif":
		return AttachmentImage
	case ".mp3", ".wav", ".ogg", ".m4a", ".aac", ".flac":
		return AttachmentAudio
	case ".mp4", ".mov", ".mkv", ".webm":
		return AttachmentVideo
	default:
		return AttachmentFile
	}
}

// NormalizeInboundChannelAttachment normalizes a channel attachment at adapter boundary.
func NormalizeInboundChannelAttachment(att Attachment) Attachment {
	normalized := AttachmentFromBundle(BundleFromAttachment(att))
	normalized.Type = InferAttachmentType(att.Type, normalized.Mime, normalized.Name)
	return normalized
}
