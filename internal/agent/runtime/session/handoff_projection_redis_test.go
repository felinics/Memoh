package sessionruntime

import (
	"os"
	"testing"
	"time"

	chatview "github.com/felinics/memoh/internal/agent/view"
)

func TestRedisValkeyHandoffTerminalRetiresOnlyAnOlderSameRunReceipt(t *testing.T) {
	url := os.Getenv("MEMOH_TEST_REDIS_URL")
	if url == "" {
		url = os.Getenv("MEMOH_TEST_VALKEY_URL")
	}
	if url == "" {
		if os.Getenv("MEMOH_TEST_DISTRIBUTED_REQUIRED") == "1" {
			t.Fatal("MEMOH_TEST_REDIS_URL or MEMOH_TEST_VALKEY_URL required")
		}
		t.Skip("set MEMOH_TEST_REDIS_URL or MEMOH_TEST_VALKEY_URL")
	}
	for _, token := range []int64{8, 9, 10} {
		backend, err := NewRedisBackend(t.Context(), RedisOptions{URL: url, KeyPrefix: uniqueRuntimeBackendPrefix("handoff-projection"), StateTTL: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		manager := NewManager(backend, Options{Ledger: newFakeLedger()})
		key := Key{BotID: testBotID, SessionID: testSessionID}
		ref := RunRef{BotID: key.BotID, SessionID: key.SessionID, RunID: "handoff", OwnerID: "old", Generation: "old-generation", FencingToken: token}
		deadline := time.Now().Add(time.Minute)
		_, _, err = backend.StartRun(t.Context(), key, ref, func(_ Snapshot, _ bool) (Snapshot, bool, error) {
			snapshot := EmptySnapshot(key.BotID, key.SessionID)
			snapshot.CurrentRunView = &CurrentRunView{RunID: ref.RunID, OwnerID: ref.OwnerID, Generation: ref.Generation, FencingToken: token, Status: RunStatusWaitingDecision, OwnerLeaseExpiresAt: &deadline, Messages: []chatview.UIMessage{{UserInput: &chatview.UIUserInput{Status: "pending", CanRespond: true}}, {Approval: &chatview.UIToolApproval{Status: "pending", CanApprove: true}}}}
			return snapshot, true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := backend.DeleteRunRef(t.Context(), ref); err != nil {
			t.Fatal(err)
		}
		if err := manager.reconcileTerminalLive(t.Context(), TerminalRun{RunID: ref.RunID, BotID: key.BotID, SessionID: key.SessionID, FencingToken: 9, State: "lost", ErrorCode: runErrorOwnerLeaseExpired}); err != nil {
			t.Fatal(err)
		}
		snapshot, _, err := backend.Load(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		run := snapshot.CurrentRunView
		if token <= 9 {
			if run.Status != RunStatusLost || run.FencingToken != 9 || run.Messages[0].UserInput.CanRespond || run.Messages[1].Approval.CanApprove {
				t.Fatalf("old terminal projection survived: %+v", run)
			}
		} else if run.Status != RunStatusWaitingDecision || !run.Messages[0].UserInput.CanRespond {
			t.Fatal("newer receipt was overwritten by a stale terminal")
		}
		_ = manager.CloseContext(t.Context())
	}
}
