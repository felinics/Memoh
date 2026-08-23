package core

import (
	"context"
	"errors"
	"log/slog"

	"go.uber.org/fx"

	sessionruntime "github.com/memohai/memoh/internal/agent/runtime/session"
	"github.com/memohai/memoh/internal/agent/runtime/session/ledger"
	"github.com/memohai/memoh/internal/config"
	postgresstore "github.com/memohai/memoh/internal/db/postgres/store"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/runtimefence"
)

// provideSessionRunLedger returns the durable half of the session runtime. It
// is the seam the persistence contract requires: the composition root names the
// PostgreSQL constructor, and everything above it holds only the port.
func provideSessionRunLedger(postgresStore *postgresstore.Store) (ledger.Store, error) {
	if postgresStore == nil {
		return nil, errors.New("session run ledger requires postgres")
	}
	return ledger.NewPostgres(postgresStore.SQLC(), postgresStore.Pool()), nil
}

// provideRuntimeFenceActivator supplies the second half of a claim. The ledger
// decides who owns a run; this decides whose writes PostgreSQL still accepts,
// and both order off the same monotonic sequence.
func provideRuntimeFenceActivator(queries dbstore.Queries) sessionruntime.FenceActivator {
	return runtimefence.NewActivator(queries)
}

// provideSessionRuntimeManager constructs the manager and ties its reaper to the
// process lifecycle.
//
// Start is not optional bookkeeping. It health-probes the live backend (which is
// what refuses a cluster deployment whose backend cannot arbitrate ownership),
// elects the reaper leader, and begins the fail-closed generation sweep — for a
// single OSS instance that sweep is what turns the previous process's runs into
// `lost` instead of leaving them active forever.
func provideSessionRuntimeManager(lc fx.Lifecycle, log *slog.Logger, cfg config.Config, runs ledger.Store, fence sessionruntime.FenceActivator) (*sessionruntime.Manager, error) {
	manager, err := sessionruntime.NewManagerFromConfig(log, cfg.SessionRuntime, runs, fence)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{
		OnStart: manager.Start,
		OnStop: func(ctx context.Context) error {
			return manager.CloseContext(ctx)
		},
	})
	return manager, nil
}
