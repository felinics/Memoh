package engine

import (
	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/tool"
	attachmentpkg "github.com/memohai/memoh/domains/media/attachment"
)

func bundleFromToolAttachment(att tool.Attachment) attachmentpkg.Bundle {
	return attachmentpkg.Bundle{
		Type:        att.Type,
		Base64:      att.Base64,
		Path:        att.Path,
		URL:         att.URL,
		PlatformKey: att.PlatformKey,
		ContentHash: att.ContentHash,
		Name:        att.Name,
		Mime:        att.Mime,
		Size:        att.Size,
		Metadata:    att.Metadata,
	}.Normalize()
}

// fileAttachmentFromBundle converts a normalized bundle to an agent FileAttachment.
// Callers must guarantee bundle is already normalized (produced by BundleFromXxx or Normalize()).
func fileAttachmentFromBundle(bundle attachmentpkg.Bundle) agentdomain.FileAttachment {
	return agentdomain.FileAttachment{
		Type:        bundle.Type,
		Base64:      bundle.Base64,
		Path:        bundle.Path,
		URL:         bundle.URL,
		PlatformKey: bundle.PlatformKey,
		Mime:        bundle.Mime,
		Name:        bundle.Name,
		ContentHash: bundle.ContentHash,
		Size:        bundle.Size,
		Metadata:    bundle.Metadata,
	}
}

func fileAttachmentFromToolAttachment(att tool.Attachment) agentdomain.FileAttachment {
	return fileAttachmentFromBundle(bundleFromToolAttachment(att))
}
