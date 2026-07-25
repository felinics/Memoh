package gateway

import (
	"context"
	"io"
)

// PreparedAttachmentKind identifies how an attachment should be delivered.
type PreparedAttachmentKind string

const (
	PreparedAttachmentNativeRef PreparedAttachmentKind = "native_ref"
	PreparedAttachmentPublicURL PreparedAttachmentKind = "public_url"
	PreparedAttachmentUpload    PreparedAttachmentKind = "upload"
)

// PreparedAttachment is the adapter-facing attachment model after preparation.
type PreparedAttachment struct {
	Logical   Attachment
	Kind      PreparedAttachmentKind
	NativeRef string
	PublicURL string
	Name      string
	Mime      string
	Size      int64
	// Open must return a fresh reader each time so retries are safe.
	Open func(ctx context.Context) (io.ReadCloser, error)
}

// PreparedMessage is the adapter-facing form of a Message.
type PreparedMessage struct {
	Message     Message
	Attachments []PreparedAttachment
}

// LogicalMessage converts the prepared message back to the logical channel message.
func (m PreparedMessage) LogicalMessage() Message {
	msg := m.Message
	if len(m.Attachments) == 0 {
		return msg
	}
	attachments := make([]Attachment, 0, len(m.Attachments))
	for _, att := range m.Attachments {
		attachments = append(attachments, att.Logical)
	}
	msg.Attachments = attachments
	return msg
}

// PreparedOutboundMessage is the adapter-facing form of OutboundMessage.
type PreparedOutboundMessage struct {
	Target  string
	Message PreparedMessage
}

// LogicalMessage converts the prepared outbound message back to the logical model.
func (m PreparedOutboundMessage) LogicalMessage() OutboundMessage {
	return OutboundMessage{
		Target:  m.Target,
		Message: m.Message.LogicalMessage(),
	}
}

// PreparedStreamFinalizePayload is the adapter-facing stream final payload.
type PreparedStreamFinalizePayload struct {
	Message PreparedMessage
}

// PreparedStreamEvent is the adapter-facing form of StreamEvent.
type PreparedStreamEvent struct {
	Type        StreamEventType
	Status      StreamStatus
	Delta       string
	Final       *PreparedStreamFinalizePayload
	Error       string
	ToolCall    *StreamToolCall
	Phase       StreamPhase
	Attachments []PreparedAttachment
	Reactions   []ReactRequest
	Speeches    []SpeechRequest
	Metadata    map[string]any
}

// LogicalEvent converts the prepared stream event back to the logical model.
func (e PreparedStreamEvent) LogicalEvent() StreamEvent {
	result := StreamEvent{
		Type:      e.Type,
		Status:    e.Status,
		Delta:     e.Delta,
		Error:     e.Error,
		ToolCall:  e.ToolCall,
		Phase:     e.Phase,
		Reactions: e.Reactions,
		Speeches:  e.Speeches,
		Metadata:  e.Metadata,
	}
	if len(e.Attachments) > 0 {
		result.Attachments = make([]Attachment, 0, len(e.Attachments))
		for _, att := range e.Attachments {
			result.Attachments = append(result.Attachments, att.Logical)
		}
	}
	if e.Final != nil {
		result.Final = &StreamFinalizePayload{
			Message: e.Final.Message.LogicalMessage(),
		}
	}
	return result
}
