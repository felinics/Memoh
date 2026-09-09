package channel

import "github.com/felinics/memoh/internal/slash"

// RejectReservedSkillMetadata rejects client-supplied reserved skill metadata
// anywhere it can ride on a message: top-level metadata, content parts,
// attachments, and reply attachments. This is a security boundary — reserved
// keys (requested_skills, skill_activation, user_message_kind, …) are trusted
// downstream as server-authored state. Single walker shared by every public
// inbound surface (web REST, web WS, channel inbound) so a future
// metadata-bearing location on Message only needs guarding once.
func RejectReservedSkillMetadata(msg Message) error {
	if err := slash.RejectReservedSkillMetadataValue(msg.Metadata); err != nil {
		return err
	}
	for _, part := range msg.Parts {
		if err := slash.RejectReservedSkillMetadataValue(part.Metadata); err != nil {
			return err
		}
	}
	for _, att := range msg.Attachments {
		if err := slash.RejectReservedSkillMetadataValue(att.Metadata); err != nil {
			return err
		}
	}
	if msg.Reply != nil {
		for _, att := range msg.Reply.Attachments {
			if err := slash.RejectReservedSkillMetadataValue(att.Metadata); err != nil {
				return err
			}
		}
	}
	return nil
}
