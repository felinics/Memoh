package application

import (
	"context"
	"testing"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	sessionqueue "github.com/felinics/memoh/internal/agent/runtime/session/queue"
)

type recordingQueueRuntimeStore struct {
	continuation          sessionqueue.ContinuationRun
	acquireLiveGeneration string
}

func (*recordingQueueRuntimeStore) NextQueueStepIndex(context.Context, string) (int, error) {
	return 0, nil
}

func (*recordingQueueRuntimeStore) ListOwnerlessContinuations(context.Context, int32) ([]sessionqueue.OwnerlessContinuation, error) {
	return nil, nil
}

func (*recordingQueueRuntimeStore) FollowUpByID(context.Context, sessionqueue.FollowUpItemID) (sessionqueue.FollowUpItem, error) {
	return sessionqueue.FollowUpItem{}, nil
}

func (s *recordingQueueRuntimeStore) GetContinuationRun(context.Context, string) (sessionqueue.ContinuationRun, error) {
	return s.continuation, nil
}

func (s *recordingQueueRuntimeStore) AcquireContinuationRun(_ context.Context, _, _, liveGeneration string) (sessionruntime.RunHandle, bool, error) {
	s.acquireLiveGeneration = liveGeneration
	return sessionruntime.RunHandle{}, false, nil
}

func (*recordingQueueRuntimeStore) ClaimAssignedFollowUp(context.Context, string, sessionruntime.RunHandle) (sessionqueue.FollowUpItem, error) {
	return sessionqueue.FollowUpItem{}, nil
}

func (*recordingQueueRuntimeStore) GetClaimedSteerForRun(context.Context, string) (sessionqueue.SteerItem, error) {
	return sessionqueue.SteerItem{}, nil
}

func (*recordingQueueRuntimeStore) GetRecoverableRun(context.Context, string) (sessionqueue.RecoverableRun, error) {
	return sessionqueue.RecoverableRun{}, nil
}

func (*recordingQueueRuntimeStore) AcquireQueuedRun(context.Context, string, int64, string, string) (sessionruntime.RunHandle, bool, error) {
	return sessionruntime.RunHandle{}, false, nil
}

func (*recordingQueueRuntimeStore) ReclaimSteer(context.Context, string, sessionruntime.RunHandle) (sessionqueue.SteerItem, sessionqueue.SteerClaimRef, error) {
	return sessionqueue.SteerItem{}, sessionqueue.SteerClaimRef{}, nil
}

func TestQueueContinuationUsesRuntimeLivenessGenerationForDurableAcquire(t *testing.T) {
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{OwnerID: "continuation-owner"})
	t.Cleanup(func() { _ = manager.Close() })
	store := &recordingQueueRuntimeStore{continuation: sessionqueue.ContinuationRun{
		RunID: "continuation-run", BotID: "bot", SessionID: "session", TurnID: "turn",
	}}
	service := &Service{sessionManager: manager, queueStore: store}

	want, err := manager.LivenessGeneration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	service.runQueueContinuation(context.Background(), "continuation-run", sessionqueue.FollowUpItem{})
	if store.acquireLiveGeneration != want {
		t.Fatalf("acquire live generation = %q, want %q", store.acquireLiveGeneration, want)
	}
}

var _ queueRuntimeStore = (*recordingQueueRuntimeStore)(nil)
