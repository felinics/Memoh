package view

import (
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
	attachmentpkg "github.com/felinics/memoh/internal/attachment"
)

// UIAttachmentsFromTurnAttachments converts turn attachments into the
// normalized UI shape for live runtime projections. Runtime state carries
// media references, not the uploaded bytes: Base64 is deliberately not
// copied, which would duplicate every attachment into the live backend.
func UIAttachmentsFromTurnAttachments(botID string, attachments []turn.Attachment) []UIAttachment {
	uiAttachments := make([]UIAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		contentHash := strings.TrimSpace(attachment.ContentHash)
		uiAttachments = append(uiAttachments, UIAttachment{
			ID:          contentHash,
			Type:        normalizeUIAttachmentType(attachment.Type, attachment.Mime),
			Path:        strings.TrimSpace(attachment.Path),
			URL:         strings.TrimSpace(attachment.URL),
			Name:        strings.TrimSpace(attachment.Name),
			ContentHash: contentHash,
			BotID:       strings.TrimSpace(botID),
			Mime:        strings.TrimSpace(attachment.Mime),
			Size:        attachment.Size,
			StorageKey:  attachmentpkg.MetadataString(attachment.Metadata, attachmentpkg.MetadataKeyStorageKey),
		})
	}
	return uiAttachments
}

// NewRequestUserTurn builds the subscriber-facing projection of the user
// message that fired a run, so subscribers watching a thread see the
// triggering message while the run is still executing — not only after the
// step committer lands it in history.
//
// Two invariants keep the live bubble seamless with the settled one:
//
//  1. The result is nil exactly when the turn will not persist a user
//     message (mirrors prependTurnUserMessage in internal/agent/application):
//     an attachment-only or discuss-shaped command produces no history turn,
//     so projecting one would make a bubble vanish at the database handover.
//  2. The text is the same display text the persistence layer stores
//     (mirrors the displayText switch in service_store.go): UserVisibleText
//     when present, else the envelope-unwrapped Query. Anything else would
//     visibly change the bubble's content when the runtime projection hands
//     over to the database at run end.
//
// Reply, forward, sender, and platform fields come straight from the command,
// which is also the source buildInteractionMetadata persists from.
func NewRequestUserTurn(cmd turn.StartTurnCommand, turnID string) *UITurn {
	if strings.TrimSpace(cmd.Query) == "" && strings.TrimSpace(cmd.UserMessageKind) != turn.UserMessageKindSkillActivation {
		return nil
	}
	platform := strings.ToLower(strings.TrimSpace(cmd.CurrentChannel))
	if platform == "local" {
		// Mirrors resolveUIPersistencePlatform: local turns render without a
		// platform badge, and sender fields only ride along with a platform.
		platform = ""
	}
	request := &UITurn{
		TurnID:            turnID,
		Role:              "user",
		Text:              requestUserTurnText(cmd),
		UserMessageKind:   strings.TrimSpace(cmd.UserMessageKind),
		SkillActivation:   cmd.SkillActivation,
		Attachments:       UIAttachmentsFromTurnAttachments(cmd.BotID, cmd.Attachments),
		Reply:             uiReplyFromCommand(cmd),
		Forward:           uiForwardFromCommand(cmd),
		Timestamp:         time.Now().UTC(),
		Platform:          platform,
		ExternalMessageID: strings.TrimSpace(cmd.ExternalMessageID),
	}
	if platform != "" {
		request.SenderDisplayName = strings.TrimSpace(cmd.DisplayName)
		request.SenderUserID = strings.TrimSpace(cmd.SourceChannelIdentityID)
	}
	return request
}

// requestUserTurnText mirrors the displayText selection the persistence layer
// applies to the turn-leading user message (service_store.go): the visible
// text wins when present, otherwise the query with any transport envelope
// stripped. cmd.Query is not yet envelope-wrapped at admission time, but the
// unwrap keeps the two equal even if a caller ever admits a wrapped query.
func requestUserTurnText(cmd turn.StartTurnCommand) string {
	if trimmed := strings.TrimSpace(cmd.UserVisibleText); trimmed != "" ||
		strings.TrimSpace(cmd.UserMessageKind) == turn.UserMessageKindSkillActivation {
		return trimmed
	}
	return strings.TrimSpace(turn.UnwrapUserMessageEnvelope(cmd.Query))
}

// uiReplyFromCommand mirrors uiReplyFromMessage: the same fields the
// persistence layer writes into the reply metadata, in the same UI shape.
func uiReplyFromCommand(cmd turn.StartTurnCommand) *UIReplyRef {
	reply := UIReplyRef{
		MessageID:   strings.TrimSpace(cmd.SourceReplyToMessageID),
		Sender:      strings.TrimSpace(cmd.ReplySender),
		Preview:     truncateUIReplyPreview(cmd.ReplyPreview),
		Attachments: UIAttachmentsFromTurnAttachments(cmd.BotID, cmd.ReplyAttachments),
	}
	if reply.MessageID == "" && reply.Sender == "" && reply.Preview == "" && len(reply.Attachments) == 0 {
		return nil
	}
	return &reply
}

// uiForwardFromCommand mirrors uiForwardFromMessage for the live projection.
func uiForwardFromCommand(cmd turn.StartTurnCommand) *UIForwardRef {
	forward := UIForwardRef{
		MessageID:          strings.TrimSpace(cmd.ForwardMessageID),
		FromUserID:         strings.TrimSpace(cmd.ForwardFromUserID),
		FromConversationID: strings.TrimSpace(cmd.ForwardFromConversationID),
		Sender:             strings.TrimSpace(cmd.ForwardSender),
		Date:               cmd.ForwardDate,
	}
	if forward.MessageID == "" && forward.FromUserID == "" && forward.FromConversationID == "" && forward.Sender == "" && forward.Date == 0 {
		return nil
	}
	return &forward
}
