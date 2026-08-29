package tools

import (
	attachmentpkg "github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/messaging"
)

// toolAttachmentFromBundle converts a normalized bundle to a tools.Attachment.
// Callers must guarantee bundle is already normalized (produced by BundleFromXxx or Normalize()).
func toolAttachmentFromBundle(bundle attachmentpkg.Bundle) Attachment {
	return Attachment{
		Type:        bundle.Type,
		Base64:      bundle.Base64,
		Path:        bundle.Path,
		URL:         bundle.URL,
		PlatformKey: bundle.PlatformKey,
		ContentHash: bundle.ContentHash,
		Name:        bundle.Name,
		Mime:        bundle.Mime,
		Size:        bundle.Size,
		Metadata:    bundle.Metadata,
	}
}

func toolAttachmentFromChannelAttachment(att messaging.Attachment) Attachment {
	return toolAttachmentFromBundle(messaging.BundleFromAttachment(att))
}
