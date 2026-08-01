package channel

import "testing"

func TestIsSilentReplyText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		text string
		want bool
	}{
		{text: "NO_REPLY", want: true},
		{text: "no_reply.", want: true},
		{text: "NO_REPLY\nreason", want: true},
		{text: "reason: NO_REPLY", want: true},
		{text: "XNO_REPLY", want: false},
		{text: "NO_REPLYING", want: false},
		{text: "ordinary reply", want: false},
		{text: "", want: false},
	}
	for _, tt := range tests {
		if got := IsSilentReplyText(tt.text); got != tt.want {
			t.Errorf("IsSilentReplyText(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}
