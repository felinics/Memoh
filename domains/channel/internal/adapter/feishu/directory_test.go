package feishu

import (
	"testing"

	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func Test_directoryLimit(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want int
	}{
		{"zero", 0, defaultDirectoryPageSize},
		{"negative", -1, defaultDirectoryPageSize},
		{"one", 1, 1},
		{"default", defaultDirectoryPageSize, defaultDirectoryPageSize},
		{"over max", maxDirectoryPageSize + 100, maxDirectoryPageSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := directoryLimit(tt.n); got != tt.want {
				t.Errorf("directoryLimit() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_parseFeishuUserInput(t *testing.T) {
	tests := []struct {
		raw        string
		wantID     string
		wantIDType string
	}{
		{"open_id:ou_xxx", "ou_xxx", larkcontact.UserIdTypeOpenId},
		{"user_id:u_yyy", "u_yyy", larkcontact.UserIdTypeUserId},
		{"ou_abc", "ou_abc", larkcontact.UserIdTypeOpenId},
		{"u_123", "u_123", larkcontact.UserIdTypeUserId},
		{"b3f195f9", "b3f195f9", larkcontact.UserIdTypeUserId},
		{"  open_id: ou_zzz  ", "ou_zzz", larkcontact.UserIdTypeOpenId},
		{"", "", ""},
	}
	for _, tt := range tests {
		id, idType := parseFeishuUserInput(tt.raw)
		if id != tt.wantID || idType != tt.wantIDType {
			t.Errorf("parseFeishuUserInput(%q) = %q, %q; want %q, %q", tt.raw, id, idType, tt.wantID, tt.wantIDType)
		}
	}
}

func Test_ptrStr(t *testing.T) {
	s := "x"
	if got := ptrStr(nil); got != "" {
		t.Errorf("ptrStr(nil) = %q", got)
	}
	if got := ptrStr(&s); got != "x" {
		t.Errorf("ptrStr(&s) = %q", got)
	}
	space := "  a  "
	if got := ptrStr(&space); got != "a" {
		t.Errorf("ptrStr(space) = %q", got)
	}
}

func Test_feishuUserToEntry(t *testing.T) {
	openID := "ou_1"
	name := "Alice"
	u := &larkcontact.User{OpenId: &openID, Name: &name}
	e := feishuUserToEntry(u)
	if e.Kind != "user" || e.ID != "open_id:ou_1" || e.Name != "Alice" {
		t.Errorf("feishuUserToEntry = %+v", e)
	}
	userID := "u_2"
	u2 := &larkcontact.User{UserId: &userID, Name: &name}
	e2 := feishuUserToEntry(u2)
	if e2.ID != "user_id:u_2" {
		t.Errorf("feishuUserToEntry user_id only = %+v", e2)
	}
}

func Test_feishuChatToEntry(t *testing.T) {
	chatID := "oc_abc"
	name := "Test Group"
	c := &larkim.ListChat{ChatId: &chatID, Name: &name}
	e := feishuChatToEntry(c)
	if e.Kind != "group" || e.ID != "chat_id:oc_abc" || e.Name != "Test Group" {
		t.Errorf("feishuChatToEntry = %+v", e)
	}
}

func Test_feishuMemberToEntry(t *testing.T) {
	memberID := "ou_m1"
	memberType := "open_id"
	name := "Bob"
	m := &larkim.ListMember{MemberId: &memberID, MemberIdType: &memberType, Name: &name}
	e := feishuMemberToEntry(m)
	if e.Kind != "user" || e.ID != "open_id:ou_m1" || e.Name != "Bob" {
		t.Errorf("feishuMemberToEntry = %+v", e)
	}
	memberTypeUser := "user_id"
	m2 := &larkim.ListMember{MemberId: &memberID, MemberIdType: &memberTypeUser, Name: &name}
	e2 := feishuMemberToEntry(m2)
	if e2.ID != "user_id:ou_m1" {
		t.Errorf("feishuMemberToEntry user_id type = %+v", e2)
	}
}

func Test_feishuAvatarURL(t *testing.T) {
	if got := feishuAvatarURL(nil); got != "" {
		t.Errorf("feishuAvatarURL(nil) = %q", got)
	}
	url72 := "https://avatar.example/72.png"
	a := &larkcontact.AvatarInfo{Avatar72: &url72}
	if got := feishuAvatarURL(a); got != url72 {
		t.Errorf("feishuAvatarURL = %q", got)
	}
}
