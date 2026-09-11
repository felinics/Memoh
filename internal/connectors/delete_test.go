package connectors

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	connectsdk "github.com/felinics/connect-it/sdk/go"
	"github.com/jackc/pgx/v5"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type disconnectQueries struct {
	dbstore.Queries
	exists  bool
	failure error
}

func (q *disconnectQueries) GetConnectorByConnectionID(_ context.Context, args dbsqlc.GetConnectorByConnectionIDParams) (dbsqlc.Connector, error) {
	if !q.exists {
		return dbsqlc.Connector{}, pgx.ErrNoRows
	}
	return dbsqlc.Connector{BotID: args.BotID, ConnectionID: args.ConnectionID}, nil
}

func (q *disconnectQueries) DeleteConnector(context.Context, dbsqlc.DeleteConnectorParams) error {
	if q.failure != nil {
		return q.failure
	}
	q.exists = false
	return nil
}

func TestDisconnectRetriesLocalFinalizationAfterRemoteRevocation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := connectsdk.New(server.URL, "test-only")
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected local finalization failure")
	q := &disconnectQueries{exists: true, failure: failure}
	s := &Service{queries: q, client: client}
	const bot = "00000000-0000-0000-0000-000000000001"
	if err := s.Delete(t.Context(), bot, "connection"); !errors.Is(err, failure) || !q.exists {
		t.Fatalf("local failure must preserve binding: %v", err)
	}
	q.failure = nil
	if err := s.Delete(t.Context(), bot, "connection"); err != nil || q.exists {
		t.Fatalf("retry did not finish finalization: %v", err)
	}
	if err := s.Delete(t.Context(), bot, "connection"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("repeated completed disconnect contacted upstream: %d", requests.Load())
	}
}
