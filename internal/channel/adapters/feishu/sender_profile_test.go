package feishu

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestApplySenderProfileFillDisplayAndUsername(t *testing.T) {
	msg := &channel.InboundMessage{
		Sender: channel.Identity{
			SubjectID:  "ou_test",
			Attributes: map[string]string{"open_id": "ou_test"},
		},
	}

	applySenderProfile(msg, feishuSenderProfile{
		displayName: "张三",
		username:    "zhangsan",
	})

	if got := msg.Sender.DisplayName; got != "张三" {
		t.Fatalf("expected display name 张三, got %q", got)
	}
	if got := msg.Sender.Attribute("display_name"); got != "张三" {
		t.Fatalf("expected attribute display_name 张三, got %q", got)
	}
	if got := msg.Sender.Attribute("name"); got != "张三" {
		t.Fatalf("expected attribute name 张三, got %q", got)
	}
	if got := msg.Sender.Attribute("username"); got != "zhangsan" {
		t.Fatalf("expected attribute username zhangsan, got %q", got)
	}
}

func TestApplySenderProfileKeepExistingIdentityFields(t *testing.T) {
	msg := &channel.InboundMessage{
		Sender: channel.Identity{
			SubjectID:   "ou_test",
			DisplayName: "原名",
			Attributes: map[string]string{
				"open_id":      "ou_test",
				"display_name": "原显示名",
				"name":         "原姓名",
				"username":     "old_user",
			},
		},
	}

	applySenderProfile(msg, feishuSenderProfile{
		displayName: "新名",
		username:    "new_user",
	})

	if got := msg.Sender.DisplayName; got != "原名" {
		t.Fatalf("expected original display name preserved, got %q", got)
	}
	if got := msg.Sender.Attribute("display_name"); got != "原显示名" {
		t.Fatalf("expected original attribute display_name preserved, got %q", got)
	}
	if got := msg.Sender.Attribute("name"); got != "原姓名" {
		t.Fatalf("expected original attribute name preserved, got %q", got)
	}
	if got := msg.Sender.Attribute("username"); got != "old_user" {
		t.Fatalf("expected original attribute username preserved, got %q", got)
	}
}
