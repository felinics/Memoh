package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func TestRuntimeRoundPublishesOnlyCompletedRequestedHead(t *testing.T) {
	t.Parallel()

	failure := errors.New("codex turn failed")
	for _, tc := range []struct {
		name        string
		publishHead bool
		promptErr   error
		completed   bool
		published   bool
	}{
		{"requested_completed", true, nil, true, true},
		{"requested_aborted", true, nil, false, false},
		{"requested_failed", true, failure, false, false},
		{"not_requested", false, nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			messages := &recordingMessageService{}
			service := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}
			err := service.persistRuntimeRound(
				context.Background(),
				ChatRequest{BotID: "bot-1", ThreadID: "session-1", RunID: "run-1", Query: "inspect"},
				"codex", "/data/app",
				external.PromptResult{Text: "partial", PublishHead: tc.publishHead},
				tc.promptErr, tc.completed, nil, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			publication := messages.roundOptions[len(messages.roundOptions)-1].AgentPublication
			if (publication != nil) != tc.published {
				t.Fatalf("publication = %#v", publication)
			}
			if publication != nil && (publication.RunID != "run-1") {
				t.Fatalf("publication = %#v", publication)
			}
		})
	}
}
