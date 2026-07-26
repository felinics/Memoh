package assembly

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	runtimeclient "github.com/memohai/memoh/domains/runtime/client"
	clientpostgres "github.com/memohai/memoh/domains/runtime/internal/postgres/client"
)

// ClientDeps are the explicit public inputs required to assemble the reverse
// user-runtime Service, Hub, and Pipe.
type ClientDeps struct {
	Log        *slog.Logger
	Pool       *pgxpool.Pool
	Membership runtimeclient.MembershipReader
}

// Client is the assembled reverse user-runtime surface returned to composition.
type Client struct {
	Service *runtimeclient.Service
	Hub     *runtimeclient.Hub
	Pipe    runtimeclient.Pipe
}

// NewClient constructs the public reverse user-runtime Service backed by
// postgres credentials and an in-process Hub/DirectPipe. The returned cleanup
// shuts down the Hub and must be called on process shutdown.
func NewClient(deps ClientDeps) (*Client, func(), error) {
	if deps.Pool == nil {
		return nil, nil, errors.New("postgres pool is required")
	}
	if deps.Membership == nil {
		return nil, nil, errors.New("membership reader is required")
	}
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	store := clientpostgres.NewCredentialStore(deps.Pool)
	if store == nil {
		return nil, nil, errors.New("postgres user runtime store not configured")
	}
	hub := runtimeclient.NewHub(log)
	svc := runtimeclient.NewService(store, deps.Membership, hub)
	pipe := runtimeclient.NewDirectPipe()
	cleanup := func() {
		_ = hub.Shutdown(context.Background())
	}
	return &Client{Service: svc, Hub: hub, Pipe: pipe}, cleanup, nil
}
