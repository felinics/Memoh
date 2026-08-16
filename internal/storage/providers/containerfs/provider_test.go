package containerfs

import (
	"context"
	"testing"
)

func TestParseRoutingKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key     string
		wantErr bool
	}{
		{key: "bot-1/image/ab12/ab12cd.png", wantErr: false},
		{key: "/absolute/path", wantErr: true},
		{key: "../escape", wantErr: true},
		{key: "nosubpath", wantErr: true},
		{key: "", wantErr: true},
	}
	for _, tt := range tests {
		_, _, err := parseRoutingKey(tt.key)
		if tt.wantErr && err == nil {
			t.Errorf("parseRoutingKey(%q) expected error", tt.key)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("parseRoutingKey(%q) unexpected error: %v", tt.key, err)
		}
	}
}

func TestProvider_AccessPath(t *testing.T) {
	t.Parallel()
	p := &Provider{}

	tests := []struct {
		key  string
		want string
	}{
		{key: "bot-1/image/ab12/ab12cd.png", want: "/data/.memoh/media/image/ab12/ab12cd.png"},
		{key: "bot-1/file/xx/doc.pdf", want: "/data/.memoh/media/file/xx/doc.pdf"},
	}
	for _, tt := range tests {
		got := p.AccessPath(context.Background(), tt.key)
		if got != tt.want {
			t.Errorf("AccessPath(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestParseRoutingKey_PathTraversal(t *testing.T) {
	t.Parallel()

	bad := []string{
		"../etc/passwd",
		"/absolute/key",
		"bot-1/../../escape",
	}
	for _, key := range bad {
		if _, _, err := parseRoutingKey(key); err == nil {
			t.Errorf("parseRoutingKey(%q) should reject traversal", key)
		}
	}
}

func TestSplitRoutingKey(t *testing.T) {
	t.Parallel()

	botID, sub := splitRoutingKey("bot-1/image/test.png")
	if botID != "bot-1" || sub != "image/test.png" {
		t.Errorf("splitRoutingKey: got (%q, %q)", botID, sub)
	}

	botID2, sub2 := splitRoutingKey("nosubpath")
	if botID2 != "" || sub2 != "nosubpath" {
		t.Errorf("splitRoutingKey single: got (%q, %q)", botID2, sub2)
	}
}
