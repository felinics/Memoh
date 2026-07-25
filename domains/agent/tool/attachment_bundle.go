package tool

import (
	"github.com/memohai/memoh/domains/channel/delivery"
	attachmentpkg "github.com/memohai/memoh/domains/media/attachment"
)

// toolAttachmentFromBundle converts a normalized bundle to a tool.Attachment.
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

func toolAttachmentFromChannelAttachment(att delivery.Attachment) Attachment {
	return toolAttachmentFromBundle(delivery.BundleFromAttachment(att))
}
