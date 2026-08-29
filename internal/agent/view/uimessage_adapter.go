package view

import (
	"strings"

	"github.com/felinics/memoh/internal/agent/event"
)

// UIStreamEventFromAgentEvent adapts a runtime stream event to the UI
// converter's input shape. This is the ONLY adaptation point between the
// streaming vocabulary (internal/agent/event) and UI rendering - every
// delivery path (WS handler, trigger/background delivery, parity tests) must
// use it. It previously existed as two hand-maintained copies (handlers and
// flow) that had already drifted: one extracted bot_id/storage_key from
// attachment metadata, the other silently didn't.
func UIStreamEventFromAgentEvent(ev event.StreamEvent) UIMessageStreamEvent {
	attachments := make([]UIAttachment, 0, len(ev.Attachments))
	for _, attachment := range ev.Attachments {
		attachments = append(attachments, UIAttachmentFromAgentAttachment(attachment))
	}

	return UIMessageStreamEvent{
		Type:        string(ev.Type),
		Delta:       ev.Delta,
		ToolName:    ev.ToolName,
		ToolCallID:  ev.ToolCallID,
		Input:       ev.Input,
		Output:      ev.Result,
		Progress:    ev.Progress,
		Attachments: attachments,
		Error:       ev.Error,
		ApprovalID:  ev.ApprovalID,
		UserInputID: ev.UserInputID,
		ShortID:     ev.ShortID,
		Status:      ev.Status,
		Metadata:    ev.Metadata,
	}
}

// UIAttachmentFromAgentAttachment adapts a runtime file attachment to the UI
// shape, including the bot_id/storage_key metadata extraction the UI needs to
// resolve asset URLs.
func UIAttachmentFromAgentAttachment(attachment event.FileAttachment) UIAttachment {
	result := UIAttachment{
		ID:          strings.TrimSpace(attachment.ContentHash),
		Type:        normalizeUIAttachmentType(attachment.Type, attachment.Mime),
		Path:        strings.TrimSpace(attachment.Path),
		URL:         strings.TrimSpace(attachment.URL),
		Name:        strings.TrimSpace(attachment.Name),
		ContentHash: strings.TrimSpace(attachment.ContentHash),
		Mime:        strings.TrimSpace(attachment.Mime),
		Size:        attachment.Size,
		Metadata:    attachment.Metadata,
	}
	if attachment.Metadata != nil {
		if botID, ok := attachment.Metadata["bot_id"].(string); ok {
			result.BotID = strings.TrimSpace(botID)
		}
		if storageKey, ok := attachment.Metadata["storage_key"].(string); ok {
			result.StorageKey = strings.TrimSpace(storageKey)
		}
	}
	return result
}
