package channel

import attachmentpkg "github.com/felinics/memoh/internal/attachment"

// BundleFromAttachment converts a channel attachment to the shared internal
// bundle shape.
func BundleFromAttachment(att Attachment) attachmentpkg.Bundle {
	return attachmentpkg.Bundle{
		Type:           string(att.Type),
		Base64:         att.Base64,
		Path:           att.Path,
		URL:            att.URL,
		PlatformKey:    att.PlatformKey,
		SourcePlatform: att.SourcePlatform,
		ContentHash:    att.ContentHash,
		Name:           att.Name,
		Mime:           att.Mime,
		Size:           att.Size,
		DurationMs:     att.DurationMs,
		Width:          att.Width,
		Height:         att.Height,
		ThumbnailURL:   att.ThumbnailURL,
		Caption:        att.Caption,
		Metadata:       att.Metadata,
	}.Normalize()
}

// AttachmentFromBundle converts the shared internal bundle shape to a channel
// attachment. URL and Path are kept in their respective fields.
// Callers must guarantee bundle is already normalized (produced by BundleFromXxx or Normalize()).
func AttachmentFromBundle(bundle attachmentpkg.Bundle) Attachment {
	attType := AttachmentType(bundle.Type)
	if attType == "" {
		attType = AttachmentFile
	}
	return Attachment{
		Type:           attType,
		URL:            bundle.URL,
		Path:           bundle.Path,
		PlatformKey:    bundle.PlatformKey,
		SourcePlatform: bundle.SourcePlatform,
		ContentHash:    bundle.ContentHash,
		Base64:         bundle.Base64,
		Name:           bundle.Name,
		Size:           bundle.Size,
		Mime:           bundle.Mime,
		DurationMs:     bundle.DurationMs,
		Width:          bundle.Width,
		Height:         bundle.Height,
		ThumbnailURL:   bundle.ThumbnailURL,
		Caption:        bundle.Caption,
		Metadata:       bundle.Metadata,
	}
}
