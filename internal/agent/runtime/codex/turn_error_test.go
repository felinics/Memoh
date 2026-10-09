package codex

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/errs"
)

// A failed turn is the Failure its codexErrorInfo stands for, whether the
// error arrived on the completed turn or in an error notification, and a
// category the user cannot act on keeps the plain failure. Either way the
// category and the upstream status stay on the error for the result record.
func TestFailedTurnClassification(t *testing.T) {
	for _, tc := range []struct {
		name         string
		info         string
		notification bool
		kind         external.FailureKind
		category     string
		status       string
	}{
		{name: "usage limit on the turn", info: `"usageLimitExceeded"`, kind: external.FailureUsageLimited, category: "usageLimitExceeded"},
		{name: "usage limit in a notification", info: `"usageLimitExceeded"`, notification: true, kind: external.FailureUsageLimited, category: "usageLimitExceeded"},
		{name: "rate limit", info: `"rateLimitExceeded"`, kind: external.FailureRateLimited, category: "rateLimitExceeded"},
		{name: "context window", info: `"contextWindowExceeded"`, kind: external.FailureContextWindowExceeded, category: "contextWindowExceeded"},
		{name: "sign-in expired", info: `"unauthorized"`, kind: external.FailureAuthRequired, category: "unauthorized"},
		{name: "model at capacity", info: `"serverOverloaded"`, kind: external.FailureOverloaded, category: "serverOverloaded"},
		{name: "policy", info: `"cyberPolicy"`, kind: external.FailureRequestBlocked, category: "cyberPolicy"},
		{name: "connection failed", info: `{"httpConnectionFailed":{"httpStatusCode":502}}`, kind: external.FailureUpstreamUnreachable, category: "httpConnectionFailed", status: "502"},
		{name: "retries exhausted on a throttle", info: `{"responseTooManyFailedAttempts":{"httpStatusCode":429}}`, kind: external.FailureRateLimited, category: "responseTooManyFailedAttempts", status: "429"},
		{name: "retries exhausted on a server error", info: `{"responseTooManyFailedAttempts":{"httpStatusCode":500}}`, kind: external.FailureOverloaded, category: "responseTooManyFailedAttempts", status: "500"},
		{name: "nothing the user can do", info: `"sandboxError"`, category: "sandboxError"},
		{name: "no error info"},
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
			var kind external.FailureKind
			var failure *external.Failure
			if errors.As(err, &failure) {
				kind = failure.Kind
			}
			if kind != tc.kind {
				t.Fatalf("failure kind = %d, want %d (err %v)", kind, tc.kind, err)
			}
			attrs := map[string]string{}
			for _, attr := range errs.Analyze(t.Context(), err).Attrs {
				attrs[attr.Key] = attr.Value.String()
			}
			if attrs["runtime_error_kind"] != tc.category || attrs["upstream_http_status"] != tc.status {
				t.Fatalf("record attrs = %v, want category %q and status %q", attrs, tc.category, tc.status)
			}
		})
	}
}

type exitedProcess struct {
	procIO
	exit   error
	stderr string
}

func (p exitedProcess) Err() error         { return p.exit }
func (p exitedProcess) StderrTail() string { return p.stderr }

// A call the closed connection fails says how the process ended and what it
// last wrote: the caller has no other account of a crashed app-server.
func TestClosedConnectionErrorNamesTheExit(t *testing.T) {
	c := &conn{proc: exitedProcess{exit: errors.New("agent process exited with code 137"), stderr: "out of memory"}}
	err := c.closeErr()
	if !errors.Is(err, ErrConnClosed) || !strings.Contains(err.Error(), "agent process exited with code 137; stderr: out of memory") {
		t.Fatalf("closed error = %q, want ErrConnClosed naming the exit and stderr", err)
	}
}
