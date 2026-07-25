package application

import (
	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/media/attachment"
)

func attachmentFromBundle(bundle attachment.Bundle) agentdomain.Attachment {
	return agentdomain.Attachment{
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

func bundleFromAttachment(att agentdomain.Attachment) attachment.Bundle {
	return attachment.Bundle{
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
