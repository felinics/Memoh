package codex

import (
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

// A failed turn whose codexErrorInfo says the account's usage limit was hit is
// a FailureUsageLimited, whether the error arrived on the completed turn or in
// an error notification. Every other codexErrorInfo keeps the plain failure.
func TestFailedTurnUsageLimit(t *testing.T) {
	for _, tc := range []struct {
		name         string
		info         string
		notification bool
		limited      bool
	}{
		{"usage limit on the turn", `"usageLimitExceeded"`, false, true},
		{"usage limit in a notification", `"usageLimitExceeded"`, true, true},
		{"rate limit", `"rateLimitExceeded"`, false, false},
		{"unauthorized", `"unauthorized"`, false, false},
		{"connection failed", `{"httpConnectionFailed":{"httpStatusCode":502}}`, true, false},
		{"no error info", ``, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"message":"You've hit your usage limit. Try again at 5:00 PM."`
			if tc.info != "" {
				raw += `,"codexErrorInfo":` + tc.info
			}
			raw += `}`
			var turnErr protocol.TurnError
			if err := json.Unmarshal([]byte(raw), &turnErr); err != nil {
				t.Fatal(err)
			}
			turn := newTurnState(t.Context(), external.PromptInput{}, "thread", nil, nil, nil, nil, slog.Default())
			defer turn.close()
			completed := protocol.Turn{ID: "turn", Status: protocol.TurnStatusFailed}
			if tc.notification {
				turn.handleNotification(&protocol.ErrorNotification{Error: turnErr, ThreadID: "thread", TurnID: "turn"})
			} else {
				completed.Error = &turnErr
			}
			turn.handleNotification(&protocol.TurnCompletedNotification{ThreadID: "thread", Turn: completed})

			_, err := turn.result()
			if err == nil {
				t.Fatal("failed turn returned no error")
			}
			var failure *external.Failure
			limited := errors.As(err, &failure) && failure.Kind == external.FailureUsageLimited
			if limited != tc.limited {
				t.Fatalf("usage limited = %v, want %v (err %v)", limited, tc.limited, err)
			}
		})
	}
}
