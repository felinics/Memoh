//go:build integration

package workspacedeps

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func TestLegacyOperationEpochEnrollmentIsAtomicAndDoesNotRefreshIntent(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	for _, operationID := range []string{"", strings.Repeat("5", 32)} {
		t.Run(fmt.Sprint(operationID != ""), func(t *testing.T) {
			key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), DependencyID: "codex"}
			in := UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: StatusInstalling}
			var before Installation
			var err error
			if operationID == "" {
				before, err = store.Upsert(ctx, in)
			} else {
				in.OperationIntent = &OperationReceipt{ID: operationID, DependencyID: key.DependencyID, RequestedVersion: "0.154.0", AuthorizedByActor: "original"}
				before, err = store.ClaimOperation(ctx, in, operationID)
			}
			if err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				epoch string
				err   error
			}
			start := make(chan struct{})
			results := make(chan outcome, 12)
			for index := range 12 {
				go func() {
					<-start
					epoch, err := store.EnrollLegacyOperationEpoch(ctx, key, operationID, fmt.Sprintf("boot:%d:namespace", index))
					results <- outcome{epoch, err}
				}()
			}
			close(start)
			winner := ""
			for range 12 {
				result := <-results
				if result.err != nil || result.epoch == "" {
					t.Fatalf("enroll epoch: %+v", result)
				}
				if winner == "" {
					winner = result.epoch
				} else if result.epoch != winner {
					t.Fatalf("concurrent observations overwrote first lifetime: %q != %q", result.epoch, winner)
				}
			}
			peer := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
			if epoch, err := peer.EnrollLegacyOperationEpoch(ctx, key, operationID, "next-boot"); err != nil || epoch != winner {
				t.Fatalf("server restart replaced initial lifetime: %q %v", epoch, err)
			}
			after, err := peer.Get(ctx, key)
			if err != nil || !after.UpdatedAt.Equal(before.UpdatedAt) || after.OperationID != before.OperationID || after.Status != before.Status || after.OperationIntent == nil {
				t.Fatalf("epoch changed operation state or stale timestamp: %+v %v", after, err)
			}
			if operationID != "" && (after.OperationIntent.RequestedVersion != "0.154.0" || after.OperationIntent.AuthorizedByActor != "original") {
				t.Fatalf("epoch replaced approved metadata: %+v", after.OperationIntent)
			}
			if _, err := peer.EnrollLegacyOperationEpoch(ctx, key, "not-this-operation", "other"); !errors.Is(err, ErrBusy) {
				t.Fatalf("stale operation enrolled lifetime: %v", err)
			}
			if operationID == "" {
				_, err = peer.SetStatus(ctx, key, StatusFailed, "")
			} else {
				after.Status = StatusFailed
				_, err = peer.FinishOperation(ctx, key, operationID, &after)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := peer.EnrollLegacyOperationEpoch(ctx, key, operationID, "terminal-boot"); !errors.Is(err, ErrBusy) {
				t.Fatalf("terminal installation enrolled lifetime: %v", err)
			}
		})
	}
}
