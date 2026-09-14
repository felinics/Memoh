//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
)

func TestPersistenceFailureIsExplicitAndDoesNotReplayTools(t *testing.T) {
	if !envBool("MEMOH_SESSION_RUNTIME_PERSISTENCE_FAULT") {
		t.Skip("enable MEMOH_SESSION_RUNTIME_PERSISTENCE_FAULT only against an isolated development database")
	}
	fixture := requireFixture(t, false)
	prepareFakeModel(t)
	sessionID := mustCreateSession(t, fixture, "persistence-failure")
	marker := uniqueMarker("persist-failure")
	// Only this test session is affected; the rest of the isolated database,
	// including the durable run ledger, remains writable.
	pool, err := pgxpool.New(context.Background(), envOr(postgresURLEnv, ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	name := "acceptance_fail_" + strings.ReplaceAll(sessionID, "-", "")
	sql := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.session_id = '%s'::uuid AND NEW.role = 'tool' THEN
 RAISE EXCEPTION 'private injected persistence failure' USING ERRCODE = '22P05';
 END IF; RETURN NEW; END $$;
 CREATE TRIGGER %s BEFORE INSERT ON bot_history_messages FOR EACH ROW EXECUTE FUNCTION %s();`, name, sessionID, name, name)
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON bot_history_messages; DROP FUNCTION IF EXISTS %s();", name, name)); err != nil {
			t.Error(err)
		}
	})
	conn := mustDial(t, loadEnvironment().primaryURL, fixture)
	defer closeWebSocket(conn)
	mustSubscribeAndReadSnapshot(t, conn, sessionID)
	invocationID := "invocation-" + marker
	_, admitted := mustSendAndAccept(t, fixture, conn, sessionID, invocationID, fmt.Sprintf("[acceptance:%s mode=binary_output chunks=2] Read binary output", marker))
	events, _ := mustReadRunTerminal(t, conn, admitted.RunID)
	terminal := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool { return run.terminal() })
	if terminal.State != "failed" || terminal.ErrorCode != string(apperror.CodeAgentPersistenceFailed) {
		t.Fatalf("durable failure=%#v", terminal)
	}
	raw, _ := json.Marshal(events)
	if !strings.Contains(string(raw), string(apperror.CodeAgentPersistenceFailed)) || strings.Contains(string(raw), "private injected") {
		t.Fatalf("failure was absent or leaked infrastructure details: %s", raw)
	}
	if count := globalFakeModel.RequestCount(marker); count != 1 {
		t.Fatalf("model requests=%d, tool step must not replay", count)
	}
	history, err := fixture.api.history(fixture.botID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !valueContainsString(history, string(apperror.CodeAgentPersistenceFailed)) || len(objectList(history)) != 2 {
		t.Fatalf("failure checkpoint missing or duplicated in fresh HTTP history: %#v", history)
	}
	t.Logf("verified explicit durable failure without replay: session=%s run=%s", sessionID, admitted.RunID)
}
