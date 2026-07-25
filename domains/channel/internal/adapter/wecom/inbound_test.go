package wecom

import "testing"

func TestBuildInboundMessage_Text(t *testing.T) {
	msg, ok := buildInboundMessage(MessageCallbackBody{
		MsgID:    "m1",
		ChatID:   "chat-1",
		ChatType: "group",
		From: CallbackFrom{
			UserID: "u1",
		},
		MsgType: "text",
		Text: &MessageText{
			Content: "hello",
		},
	}, "req-1")
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.Message.Text != "hello" {
		t.Fatalf("unexpected text: %q", msg.Message.Text)
	}
	if msg.ReplyTarget != "chat_id:chat-1" {
		t.Fatalf("unexpected target: %q", msg.ReplyTarget)
	}
	if msg.Metadata["req_id"] != "req-1" {
		t.Fatalf("unexpected req_id: %v", msg.Metadata["req_id"])
	}
}

func TestBuildInboundMessage_Quote(t *testing.T) {
	msg, ok := buildInboundMessage(MessageCallbackBody{
		MsgID:    "m1",
		ChatID:   "chat-1",
		ChatType: "group",
		From:     CallbackFrom{UserID: "u1"},
		MsgType:  "text",
		Text:     &MessageText{Content: "reply body"},
		Quote:    &MessageQuote{MsgID: "source-msg"},
	}, "req-1")
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.Message.Reply == nil {
		t.Fatal("expected reply ref")
	}
	if msg.Message.Reply.MessageID != "source-msg" || msg.Message.Reply.Target != "chat_id:chat-1" {
		t.Fatalf("unexpected reply ref: %#v", msg.Message.Reply)
	}
}

func TestBuildInboundMessage_Markdown(t *testing.T) {
	msg, ok := buildInboundMessage(MessageCallbackBody{
		MsgID:    "m2",
		ChatID:   "chat-2",
		ChatType: "private",
		From:     CallbackFrom{UserID: "u2"},
		MsgType:  "markdown",
		Markdown: &MessageText{
			Content: "**hello**",
		},
	}, "req-2")
	if !ok {
		t.Fatal("expected inbound markdown message")
	}
	if msg.Message.Text != "**hello**" {
		t.Fatalf("unexpected markdown text: %q", msg.Message.Text)
	}
}

func TestBuildInboundMessage_Video(t *testing.T) {
	msg, ok := buildInboundMessage(MessageCallbackBody{
		MsgID:    "m3",
		ChatID:   "chat-3",
		ChatType: "group",
		From:     CallbackFrom{UserID: "u3"},
		MsgType:  "video",
		Video: &MessageVideo{
			URL:    "https://example.com/v.mp4",
			AESKey: "k",
		},
	}, "req-3")
	if !ok {
		t.Fatal("expected inbound video message")
	}
	if len(msg.Message.Attachments) != 1 {
		t.Fatalf("expected one attachment, got %d", len(msg.Message.Attachments))
	}
	if msg.Message.Attachments[0].Type != "video" {
		t.Fatalf("unexpected attachment type: %s", msg.Message.Attachments[0].Type)
	}
}

func TestBuildInboundEventMessage_NormalizeEventType(t *testing.T) {
	msg, ok := buildInboundEventMessage(EventCallbackBody{
		MsgID:      "e1",
		ChatID:     "chat-1",
		ChatType:   "group",
		From:       CallbackFrom{UserID: "u1"},
		CreateTime: 1,
		MsgType:    "event",
		Event: EventPayload{
			EventType2: "template-card-event",
			EventKey:   "btn_1",
			TaskID:     "task_1",
		},
		Task: EventTask{
			TaskStatus: "done",
		},
	}, "req-e1")
	if !ok {
		t.Fatal("expected event message")
	}
	if msg.Message.Text != "template_card_event" {
		t.Fatalf("unexpected normalized event text: %q", msg.Message.Text)
	}
	if msg.Message.Metadata["event_key"] != "btn_1" {
		t.Fatalf("unexpected event key: %v", msg.Message.Metadata["event_key"])
	}
	if msg.Message.Metadata["task_status"] != "done" {
		t.Fatalf("unexpected task status: %v", msg.Message.Metadata["task_status"])
	}
}
