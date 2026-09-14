package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

type failureCheckpointPersister struct {
	*recordingStepPersister
	failureErr  error
	attempts    int
	checkpoints []messagepkg.AgentStep
}

func (s *failureCheckpointPersister) PersistAgentFailure(_ context.Context, step messagepkg.AgentStep) ([]messagepkg.Message, error) {
	s.attempts++
	if s.failureErr != nil {
		return nil, s.failureErr
	}
	// The database resolves the request and deduplicates by this identity.
	if _, err := uuid.Parse(step.Messages[0].TurnID); err != nil {
		return nil, err
	}
	if step.Messages[0].TurnPosition == nil {
		return nil, errors.New("turn position is required with turn id")
	}
	s.checkpoints = append(s.checkpoints, step)
	return []messagepkg.Message{{ID: "failure", Role: "assistant", TurnID: step.Messages[0].TurnID}}, nil
}

func TestFailureCheckpointPreservesPersistedRequestTurn(t *testing.T) {
	for _, fromEarlierStep := range []bool{false, true} {
		name := "pre-persisted skill activation"
		if fromEarlierStep {
			name = "earlier committed step"
		}
		t.Run(name, func(t *testing.T) {
			turnID := uuid.NewString()
			position := int64(7)
			store := &failureCheckpointPersister{recordingStepPersister: &recordingStepPersister{}}
			req := ChatRequest{
				BotID: "bot", ThreadID: "session", RunID: "run", TurnID: turnID, TurnPosition: &position,
				UserMessagePersisted: true, PersistedUserMessageID: "request", UserMessageKind: UserMessageKindSkillActivation,
			}
			committer := &agentStepCommitter{
				service: &Service{logger: slog.New(slog.DiscardHandler)}, persister: store, req: req,
				turnRequestMessageID: "request", commitErr: apperror.New(apperror.CodeAgentPersistenceFailed, nil),
			}
			if fromEarlierStep {
				committer.req.UserMessagePersisted = false
				committer.req.TurnID = uuid.NewString()
				committer.persisted = []messagepkg.Message{{ID: "request", Role: "user", TurnID: turnID, TurnPosition: &position}}
			}
			if err := committer.finish(context.Background(), 0); err != nil {
				t.Fatal(err)
			}
			if len(store.checkpoints) != 1 || len(store.checkpoints[0].Messages) != 1 {
				t.Fatalf("expected an assistant-only checkpoint: %#v", store.checkpoints)
			}
			checkpoint := store.checkpoints[0].Messages[0]
			if checkpoint.TurnID != turnID || *checkpoint.TurnPosition != position || checkpoint.TurnRequestMessageID != "request" || checkpoint.Role != "assistant" {
				t.Fatalf("checkpoint lost its persisted request identity: %#v", checkpoint)
			}
		})
	}
}

func TestFailureCheckpointCanRetryAfterPersistenceFailure(t *testing.T) {
	failure := errors.New("database unavailable")
	position := int64(1)
	store := &failureCheckpointPersister{recordingStepPersister: &recordingStepPersister{}, failureErr: failure}
	committer := &agentStepCommitter{
		service: &Service{logger: slog.New(slog.DiscardHandler)}, persister: store,
		req:       ChatRequest{BotID: "bot", ThreadID: "session", RunID: "run", TurnID: uuid.NewString(), TurnPosition: &position, Query: "request"},
		commitErr: apperror.New(apperror.CodeAgentPersistenceFailed, nil),
	}
	for range 2 {
		if err := committer.finish(context.Background(), 0); !errors.Is(err, failure) {
			t.Fatalf("finish hid the failed checkpoint: %v", err)
		}
		if committer.finalized || committer.failureRecorded || len(committer.persistedMessages()) != 0 {
			t.Fatal("failed checkpoint was treated as durable")
		}
	}
	store.failureErr = nil
	for range 2 {
		if err := committer.finish(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
	}
	if store.attempts != 3 || len(store.checkpoints) != 1 || len(committer.persistedMessages()) != 1 {
		t.Fatalf("cleanup did not retry exactly until success: attempts=%d checkpoints=%d", store.attempts, len(store.checkpoints))
	}
}
