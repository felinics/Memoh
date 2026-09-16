//go:build integration

package workspacedeps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func TestTrustedReceiptRecoveryDoesNotNeedCurrentPrerequisiteGraph(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	f := isolatedFixture(t)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool)))
	f.svc.store = store
	botID := createDependencyBot(t, ctx, pool)
	base := repairNamedPublication(t, "bar", nil, "")
	dep := repairNamedPublication(t, "foo", []string{"bar"}, "")
	cat, err := catalog.New([]catalog.Definition{base, dep})
	if err != nil {
		t.Fatal(err)
	}
	provider := &repairTestProvider{cached: cat, definitions: map[string]catalog.Definition{base.Dependency().Revision: base, dep.Dependency().Revision: dep}}
	f.svc.provider = provider
	run := f.svc.run
	f.svc.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
		result, err := run(ctx, client, spec, sink)
		if err == nil && spec.DepID == "foo" && spec.Receipt != nil {
			return result, ErrOperationUncertain
		}
		return result, err
	}
	if _, err := f.svc.Install(WithRepairActor(ctx, "manager:alice"), botID, "foo", "1.0.0", nil); !errors.Is(err, ErrOperationUncertain) {
		t.Fatal(err)
	}
	receipt, err := ReadOperationReceipt(ctx, f.client, f.home("foo"), "foo")
	if err != nil {
		t.Fatal(err)
	}
	f.svc.run = run
	f.svc.catalog = catalog.Empty()
	provider.cached, provider.refuseLive = catalog.Empty(), true
	provider.liveCalls = 0
	rec, err := f.svc.recoverReceipt(ctx, InstallationKey{BotID: botID, DependencyID: "foo"}, catalog.Dependency{}, f.platform, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.Status != StatusInstalled || rec.InstalledVersion != "1.0.0" {
		t.Fatalf("frozen receipt not recovered: %+v", rec)
	}
	desired, err := store.(DesiredStore).GetDesired(ctx, InstallationKey{BotID: botID, DependencyID: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	if desired.AuthorizedByActor != "manager:alice" || desired.DefinitionRevision != dep.Dependency().Revision {
		t.Fatalf("recovery changed trusted authorization: %+v", desired)
	}
	if provider.liveCalls != 0 {
		t.Fatal("recovery consulted current registry")
	}
	if _, err := os.Stat(filepath.Join(desired.PayloadPath, "bin", "foo")); err != nil {
		t.Fatal(err)
	}
}
