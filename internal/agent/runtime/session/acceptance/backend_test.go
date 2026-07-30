//go:build integration

package acceptance

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const acceptanceRuntimeKeyPrefix = "memoh:session_runtime:acceptance:"

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

func deleteAcceptanceDecisionResult(ctx context.Context, rawURL, commandType, botID, decisionID, controlID string) error {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return fmt.Errorf("parse acceptance Redis URL: %w", err)
	}
	client := redis.NewClient(options)
	defer func() { _ = client.Close() }()
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(commandType),
		strings.TrimSpace(botID),
		strings.TrimSpace(decisionID),
		strings.TrimSpace(controlID),
	}, "\x00")))
	key := fmt.Sprintf("%scommand_result:decision-control-%x", acceptanceRuntimeKeyPrefix, sum[:])
	deleteCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := client.Del(deleteCtx, key).Err(); err != nil {
		return fmt.Errorf("delete acceptance decision command result: %w", err)
	}
	return nil
}
