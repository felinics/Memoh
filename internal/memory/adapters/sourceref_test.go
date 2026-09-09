package adapters

import (
	"fmt"
	"slices"
	"testing"
)

func TestEncodeSourceRef(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		sessionID string
		messageID string
		want      string
	}{
		{"session and message", "sess-1", "msg-1", "sess-1/msg-1"},
		{"bare message", "", "msg-1", "msg-1"},
		{"trims whitespace", " sess-1 ", " msg-1 ", "sess-1/msg-1"},
		{"empty message", "sess-1", "", ""},
		{"empty both", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := EncodeSourceRef(tc.sessionID, tc.messageID); got != tc.want {
				t.Fatalf("EncodeSourceRef(%q, %q) = %q, want %q", tc.sessionID, tc.messageID, got, tc.want)
			}
		})
	}
}

func TestParseSourceRef(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		ref         string
		wantSession string
		wantMessage string
	}{
		{"composite", "sess-1/msg-1", "sess-1", "msg-1"},
		{"bare message id", "msg-1", "", "msg-1"},
		{"trims whitespace", " sess-1/msg-1 ", "sess-1", "msg-1"},
		{"empty", "", "", ""},
		{"only separator", "/", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotSession, gotMessage := ParseSourceRef(tc.ref)
			if gotSession != tc.wantSession || gotMessage != tc.wantMessage {
				t.Fatalf("ParseSourceRef(%q) = (%q, %q), want (%q, %q)", tc.ref, gotSession, gotMessage, tc.wantSession, tc.wantMessage)
			}
		})
	}
}

func TestEncodeParseSourceRefRoundTrip(t *testing.T) {
	t.Parallel()
	ref := EncodeSourceRef("0d9c1a2b-3e4f-4a5b-8c6d-7e8f9a0b1c2d", "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d")
	session, message := ParseSourceRef(ref)
	if session != "0d9c1a2b-3e4f-4a5b-8c6d-7e8f9a0b1c2d" || message != "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d" {
		t.Fatalf("round trip failed: got (%q, %q)", session, message)
	}
}

func TestParseScopedSourceRefRejectsUnscopedAndMalformedRefs(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{"", "msg-1", "/msg-1", "sess-1/", "sess-1/msg-1/extra"} {
		if sessionID, messageID, ok := ParseScopedSourceRef(ref); ok {
			t.Fatalf("ParseScopedSourceRef(%q) = (%q, %q, true), want rejected", ref, sessionID, messageID)
		}
	}
	if sessionID, messageID, ok := ParseScopedSourceRef(" sess-1/msg-1 "); !ok || sessionID != "sess-1" || messageID != "msg-1" {
		t.Fatalf("ParseScopedSourceRef(valid) = (%q, %q, %v)", sessionID, messageID, ok)
	}
}

func TestRetainSourceRefsValidatesBeforeCapping(t *testing.T) {
	t.Parallel()
	refs := []string{"sess-1/msg-1", "sess-1/msg-2", "bare", "broken/", "also/broken/tail"}
	want := []string{"sess-1/msg-1", "sess-1/msg-2"}
	if got := RetainSourceRefs(refs, 2); !slices.Equal(got, want) {
		t.Fatalf("RetainSourceRefs() = %v, want %v", got, want)
	}
}

func TestMergeSourceRefsBoundsDurableProvenance(t *testing.T) {
	t.Parallel()
	existing := make([]string, 0, MaxSourceRefsPerMemory)
	for i := 0; i < MaxSourceRefsPerMemory; i++ {
		existing = append(existing, fmt.Sprintf("sess-1/msg-%03d", i))
	}
	got := MergeSourceRefs(existing, []string{"sess-1/msg-010", "unscoped", "sess-1/msg-new"})
	if len(got) != MaxSourceRefsPerMemory {
		t.Fatalf("MergeSourceRefs() length = %d, want %d", len(got), MaxSourceRefsPerMemory)
	}
	if got[0] != "sess-1/msg-001" || got[len(got)-1] != "sess-1/msg-new" {
		t.Fatalf("MergeSourceRefs() retained range = %q...%q", got[0], got[len(got)-1])
	}
}
