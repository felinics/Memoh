package messaging

import "testing"

func TestReplyMessageIDFromArgsMatchesSendCompatibilityForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    map[string]any
		want    string
		wantErr string
	}{
		{name: "omitted", args: map[string]any{}},
		{name: "top level", args: map[string]any{"reply_to": " message-42 "}, want: "message-42"},
		{
			name: "empty top level falls through to nested",
			args: map[string]any{
				"reply_to": "   ",
				"message":  map[string]any{"reply": map[string]any{"message_id": " message-84 "}},
			},
			want: "message-84",
		},
		{name: "plain string message", args: map[string]any{"message": "hello"}},
		{name: "invalid top level", args: map[string]any{"reply_to": 42}, wantErr: "reply_to must be string"},
		{
			name:    "invalid nested object",
			args:    map[string]any{"message": map[string]any{"reply": "message-42"}},
			wantErr: "message reply must be object",
		},
		{
			name:    "missing nested id",
			args:    map[string]any{"message": map[string]any{"reply": map[string]any{}}},
			wantErr: "message reply message_id is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReplyMessageIDFromArgs(test.args)
			if test.wantErr != "" {
				if err == nil || err.Error() != test.wantErr {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("reply ID = %q, error = %v, want %q", got, err, test.want)
			}
		})
	}
}
