package claudecode

import (
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func TestFailedResultReportsWhatClaudeSaid(t *testing.T) {
	apiError := func(category string) string {
		return `{"type":"assistant","error":"` + category + `","message":{"content":[{"type":"text","text":"diagnostic"}]}}`
	}
	recovered := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`
	failed := `{"type":"result","subtype":"success","is_error":true,"result":"Invalid API key · Fix external API key"}`
	for _, tc := range []struct {
		name  string
		lines []string
		want  external.FailureKind
	}{
		{"signed out", []string{apiError("authentication_failed")}, external.FailureAuthRequired},
		{"rate limit", []string{apiError("rate_limit")}, external.FailureReported},
		{"recovered from a sign-in error", []string{apiError("authentication_failed"), recovered}, external.FailureReported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRunner(&recordingSink{})
			defer r.close()
			for _, line := range append(tc.lines, failed) {
				feed(t, r, line)
			}
			_, err := r.buildResult("")
			var failure *external.Failure
			if !errors.As(err, &failure) || failure.Kind != tc.want || failure.Message != "Invalid API key · Fix external API key" {
				t.Fatalf("err = %v (%+v), want kind %d with Claude's result", err, failure, tc.want)
			}
		})
	}
}
