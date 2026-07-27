//go:build integration

package acceptance

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

func flushAcceptanceBackend(ctx context.Context, rawURL string) error {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return fmt.Errorf("parse acceptance Redis URL: %w", err)
	}
	client := redis.NewClient(options)
	defer func() { _ = client.Close() }()
	pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
	defer pingCancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		return fmt.Errorf("ping acceptance Redis: %w", err)
	}
	flushCtx, flushCancel := context.WithTimeout(ctx, 3*time.Second)
	defer flushCancel()
	if err := client.FlushDB(flushCtx).Err(); err != nil {
		return fmt.Errorf("flush acceptance Redis database: %w", err)
	}
	return nil
}
