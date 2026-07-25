package inbound

import (
	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/media/attachment"
)

func turnAttachmentFromBundle(bundle attachment.Bundle) agentdomain.Attachment {
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
