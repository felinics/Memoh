package session

import (
	"log/slog"
	"time"

	"github.com/memohai/memoh/internal/config"
)

func NewManagerFromConfig(log *slog.Logger, cfg config.SessionRuntimeConfig) (*Manager, error) {
	stateTTL, err := time.ParseDuration(cfg.StateTTLOrDefault())
	if err != nil {
		return nil, err
	}
	var backend Backend
	var leaseTTL time.Duration
	switch cfg.BackendOrDefault() {
	case config.SessionRuntimeBackendRedis:
		leaseTTL, err = time.ParseDuration(cfg.OwnerLeaseTTLOrDefault())
		if err != nil {
			return nil, err
		}
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
		StateTTL:      stateTTL,
		OwnerLeaseTTL: leaseTTL,
		Logger:        log,
	}), nil
}
