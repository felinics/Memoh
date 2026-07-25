package vector

import (
	"context"
	"fmt"

	"github.com/memohai/memoh/internal/config"
	pgvectordb "github.com/memohai/memoh/internal/db/pgvector"
)

// Config carries connection settings for the optional semantic seed index.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
	SSLMode  string
}

// Open connects to the pgvector database and returns a Memory embedding store.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	legacy, err := pgvectordb.Open(ctx, config.PGVectorConfig{
		Enabled:  true,
		Host:     cfg.Host,
		Port:     cfg.Port,
		User:     cfg.User,
		Password: cfg.Password,
		Database: cfg.Database,
		SSLMode:  cfg.SSLMode,
	})
	if err != nil {
		return nil, fmt.Errorf("pgvector open: %w", err)
	}
	return NewStore(legacy), nil
}

// Close releases the underlying pgvector connection pool.
func (s *Store) Close() {
	if s != nil && s.legacy != nil {
		s.legacy.Close()
	}
}
