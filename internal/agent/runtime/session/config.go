package sessionruntime

import (
	"log/slog"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/config"
)

// NewManagerFromConfig is the single construction site where the two configured
// values turn into the full set of derived timings. runs is the durable ledger
// and fence is the persistence-ownership cutover that claims apply; they go
// together, and both may be nil only in tests that never admit a run.
func NewManagerFromConfig(log *slog.Logger, cfg config.SessionRuntimeConfig, runs ledger.Store, fence FenceActivator) (*Manager, error) {
	stateTTL, err := time.ParseDuration(cfg.StateTTLOrDefault())
	if err != nil {
		return nil, err
	}
	// owner_lease_ttl is read on both backends: even without a cross-process
	// lease it paces the reaper tick and defines the orphan grace.
	leaseTTL, err := cfg.OwnerLeaseTTLDuration()
	if err != nil {
		return nil, err
	}
	backendLossGrace, err := cfg.BackendLossGraceDuration()
	if err != nil {
		return nil, err
	}
	var backend Backend
	switch cfg.BackendOrDefault() {
	case config.SessionRuntimeBackendRedis:
		redisBackend, err := newRedisBackend(RedisOptions{
			URL:       cfg.Redis.URLOrDefault(),
			KeyPrefix: cfg.Redis.KeyPrefixOrDefault(),
			StateTTL:  stateTTL,
		})
		if err != nil {
			return nil, err
		}
		backend = redisBackend
	default:
		backend = NewMemoryBackendWithTTL(stateTTL)
	}

	return NewManager(backend, Options{
		StateTTL:         stateTTL,
		OwnerLeaseTTL:    leaseTTL,
		BackendLossGrace: backendLossGrace,
		Cluster:          cfg.Cluster,
		Ledger:           runs,
		Fence:            fence,
		Logger:           log,
	}), nil
}
