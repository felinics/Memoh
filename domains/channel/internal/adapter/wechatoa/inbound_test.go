package wechatoa

import "testing"

func TestBuildInboundUserMessage_Text(t *testing.T) {
	msg, ok := buildInboundMessage(wechatEnvelope{
		FromUserName: "o1",
		MsgType:      "text",
		MsgID:        "123",
		Content:      "hello",
		CreateTime:   1714112445,
	})
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.Message.Text != "hello" {
		t.Fatalf("unexpected text: %q", msg.Message.Text)
	}
	if msg.Message.ID != "123" {
		t.Fatalf("unexpected id: %q", msg.Message.ID)
	}
	if msg.ReplyTarget != "openid:o1" {
		t.Fatalf("unexpected target: %q", msg.ReplyTarget)
	}
}

func TestBuildInboundEventMessage(t *testing.T) {
	input := wechatEnvelope{
		FromUserName: "o2",
		MsgType:      "event",
		Event:        "CLICK",
		EventKey:     "MENU_A",
		CreateTime:   1714112445,
	}
	msg, ok := buildInboundMessage(input)
	if !ok {
		t.Fatal("expected inbound event")
	}
	if msg.Metadata["is_event"] != true {
		t.Fatalf("expected event metadata")
	}
	if msg.Message.Text != "click:MENU_A" {
		t.Fatalf("unexpected event text: %q", msg.Message.Text)
	}
	if msg.Message.ID == "" {
		t.Fatal("event id should not be empty")
	}
	msg2, ok := buildInboundMessage(input)
	if !ok {
		t.Fatal("expected second inbound event")
	}
	if msg2.Message.ID != msg.Message.ID {
		t.Fatalf("event id should be stable: %q vs %q", msg.Message.ID, msg2.Message.ID)
	}
}
