package application

import (
	"context"
	"errors"
	"testing"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/chat/attention"
)

type markCall struct {
	sessionID string
	kind      attention.Kind
}

type recordingAttentionMarker struct {
	calls []markCall
	err   error
}

func (r *recordingAttentionMarker) Mark(_ context.Context, sessionID string, kind attention.Kind) (bool, error) {
	r.calls = append(r.calls, markCall{sessionID, kind})
	return r.err == nil, r.err
}

func TestTerminalObserverMarksAttention(t *testing.T) {
	cases := []struct {
		name     string
		terminal sessionruntime.TerminalRun
		want     []markCall
	}{
		{"completed", sessionruntime.TerminalRun{RunID: "r1", SessionID: "s1", State: "completed", Applied: true}, []markCall{{"s1", attention.KindReply}}},
		{"failed", sessionruntime.TerminalRun{RunID: "r2", SessionID: "s2", State: "failed", Applied: true}, []markCall{{"s2", attention.KindFailed}}},
		{"lost", sessionruntime.TerminalRun{RunID: "r3", SessionID: "s3", State: "lost", Applied: true}, []markCall{{"s3", attention.KindFailed}}},
		{"aborted by the user", sessionruntime.TerminalRun{RunID: "r4", SessionID: "s4", State: "aborted", Applied: true}, nil},
		{"replayed terminal", sessionruntime.TerminalRun{RunID: "r5", SessionID: "s5", State: "completed", Applied: false}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marker := &recordingAttentionMarker{}
			logger, _ := captureLogs()
			service := &Service{logger: logger}
			service.SetAttentionMarker(marker)

			service.markTerminalAttention(context.Background(), tc.terminal)

			if len(marker.calls) != len(tc.want) {
				t.Fatalf("Mark calls = %+v, want %+v", marker.calls, tc.want)
			}
			for i := range tc.want {
				if marker.calls[i] != tc.want[i] {
					t.Fatalf("Mark call %d = %+v, want %+v", i, marker.calls[i], tc.want[i])
				}
			}
		})
	}
}

func TestWaitingDecisionMarksNeedsInput(t *testing.T) {
	marker := &recordingAttentionMarker{}
	service := &Service{}
	service.SetAttentionMarker(marker)

	service.markWaitingAttention(context.Background(), "bot", "s1")

	if len(marker.calls) != 1 || marker.calls[0] != (markCall{"s1", attention.KindNeedsInput}) {
		t.Fatalf("Mark calls = %+v, want one needs_input for s1", marker.calls)
	}
}

func TestAttentionMarkerErrorsAreLoggedNotPropagated(t *testing.T) {
	marker := &recordingAttentionMarker{err: errors.New("db down")}
	logger, logs := captureLogs()
	service := &Service{logger: logger}
	service.SetAttentionMarker(marker)

	service.markTerminalAttention(context.Background(), sessionruntime.TerminalRun{RunID: "r1", SessionID: "s1", State: "completed", Applied: true})

	logs.mu.Lock()
	logged := len(logs.records)
	logs.mu.Unlock()
	if logged == 0 {
		t.Fatalf("marker failure was not logged")
	}
}

func TestAttentionWithoutMarkerIsNoop(_ *testing.T) {
	service := &Service{}
	service.markTerminalAttention(context.Background(), sessionruntime.TerminalRun{RunID: "r1", SessionID: "s1", State: "completed", Applied: true})
	service.markWaitingAttention(context.Background(), "bot", "s1")
}
