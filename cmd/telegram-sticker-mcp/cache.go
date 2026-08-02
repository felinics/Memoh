package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	descriptionStatusReady  = "ready"
	descriptionStatusFailed = "failed"
)

type cachedStickerDescription struct {
	Description string
	Status      string
	Attempts    int
}

type stickerDescriptionProfile struct {
	Model         string
	PromptVersion string
}

type stickerDescriptionCache struct {
	db *sql.DB
}

func openStickerDescriptionCache(path string) (*stickerDescriptionCache, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("TELEGRAM_STICKER_MCP_CACHE_PATH is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create sticker cache directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sticker description cache: %w", err)
	}
	db.SetMaxOpenConns(1)
	cache := &stickerDescriptionCache{db: db}
	if err := cache.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure sticker description cache: %w", err)
	}
	return cache, nil
}

func (c *stickerDescriptionCache) initialize(ctx context.Context) error {
	for _, statement := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS sticker_descriptions (
			file_unique_id TEXT NOT NULL,
			model TEXT NOT NULL,
			prompt_version TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK (status IN ('ready', 'failed')),
			attempts INTEGER NOT NULL DEFAULT 1,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (file_unique_id, model, prompt_version)
		)`,
		`CREATE TABLE IF NOT EXISTS sticker_sets (
			cache_key TEXT PRIMARY KEY,
			set_name TEXT NOT NULL,
			payload BLOB NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sticker_description_profiles (
			cache_key TEXT PRIMARY KEY,
			model TEXT NOT NULL,
			prompt_version TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	} {
		if _, err := c.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize sticker description cache: %w", err)
		}
	}
	if _, err := c.db.ExecContext(
		ctx,
		`ALTER TABLE sticker_descriptions ADD COLUMN attempts INTEGER NOT NULL DEFAULT 1`,
	); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("migrate sticker description cache attempts: %w", err)
	}
	return nil
}

func (c *stickerDescriptionCache) GetProfile(
	ctx context.Context,
	cacheKey string,
) (stickerDescriptionProfile, bool, error) {
	var profile stickerDescriptionProfile
	err := c.db.QueryRowContext(
		ctx,
		`SELECT model, prompt_version
		 FROM sticker_description_profiles
		 WHERE cache_key = ?`,
		cacheKey,
	).Scan(&profile.Model, &profile.PromptVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return stickerDescriptionProfile{}, false, nil
	}
	if err != nil {
		return stickerDescriptionProfile{}, false, fmt.Errorf("read sticker description profile: %w", err)
	}
	return profile, true, nil
}

func (c *stickerDescriptionCache) PutProfile(
	ctx context.Context,
	cacheKey string,
	profile stickerDescriptionProfile,
) error {
	profile.Model = strings.TrimSpace(profile.Model)
	profile.PromptVersion = strings.TrimSpace(profile.PromptVersion)
	if profile.Model == "" || profile.PromptVersion == "" {
		return errors.New("sticker description profile requires model and prompt version")
	}
	_, err := c.db.ExecContext(
		ctx,
		`INSERT INTO sticker_description_profiles (cache_key, model, prompt_version, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (cache_key) DO UPDATE SET
			model = excluded.model,
			prompt_version = excluded.prompt_version,
			updated_at = excluded.updated_at`,
		cacheKey,
		profile.Model,
		profile.PromptVersion,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("write sticker description profile: %w", err)
	}
	return nil
}

func (c *stickerDescriptionCache) GetStickerSet(
	ctx context.Context,
	cacheKey string,
) (telegramStickerSet, bool, error) {
	var payload []byte
	err := c.db.QueryRowContext(
		ctx,
		`SELECT payload FROM sticker_sets WHERE cache_key = ?`,
		cacheKey,
	).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return telegramStickerSet{}, false, nil
	}
	if err != nil {
		return telegramStickerSet{}, false, fmt.Errorf("read Sticker Set metadata cache: %w", err)
	}
	var set telegramStickerSet
	if err := json.Unmarshal(payload, &set); err != nil {
		return telegramStickerSet{}, false, fmt.Errorf("decode Sticker Set metadata cache: %w", err)
	}
	if strings.TrimSpace(set.Name) == "" || len(set.Stickers) == 0 {
		return telegramStickerSet{}, false, errors.New("Sticker Set metadata cache is empty")
	}
	return set, true, nil
}

func (c *stickerDescriptionCache) PutStickerSet(
	ctx context.Context,
	cacheKey string,
	set telegramStickerSet,
) error {
	payload, err := json.Marshal(set)
	if err != nil {
		return fmt.Errorf("encode Sticker Set metadata cache: %w", err)
	}
	_, err = c.db.ExecContext(
		ctx,
		`INSERT INTO sticker_sets (cache_key, set_name, payload, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (cache_key) DO UPDATE SET
			set_name = excluded.set_name,
			payload = excluded.payload,
			updated_at = excluded.updated_at`,
		cacheKey,
		set.Name,
		payload,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("write Sticker Set metadata cache: %w", err)
	}
	return nil
}

func (c *stickerDescriptionCache) Get(
	ctx context.Context,
	fileUniqueID, model, promptVersion string,
) (cachedStickerDescription, bool, error) {
	var entry cachedStickerDescription
	err := c.db.QueryRowContext(
		ctx,
		`SELECT description, status, attempts
		 FROM sticker_descriptions
		 WHERE file_unique_id = ? AND model = ? AND prompt_version = ?`,
		fileUniqueID,
		model,
		promptVersion,
	).Scan(&entry.Description, &entry.Status, &entry.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return cachedStickerDescription{}, false, nil
	}
	if err != nil {
		return cachedStickerDescription{}, false, fmt.Errorf("read sticker description cache: %w", err)
	}
	return entry, true, nil
}

func (c *stickerDescriptionCache) Put(
	ctx context.Context,
	fileUniqueID, model, promptVersion string,
	entry cachedStickerDescription,
) error {
	_, err := c.db.ExecContext(
		ctx,
		`INSERT INTO sticker_descriptions (
			file_unique_id, model, prompt_version, description, status, attempts, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (file_unique_id, model, prompt_version) DO UPDATE SET
			description = excluded.description,
			status = excluded.status,
			attempts = excluded.attempts,
			updated_at = excluded.updated_at`,
		fileUniqueID,
		model,
		promptVersion,
		entry.Description,
		entry.Status,
		max(entry.Attempts, 1),
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("write sticker description cache: %w", err)
	}
	return nil
}

func (c *stickerDescriptionCache) Delete(
	ctx context.Context,
	fileUniqueID, model, promptVersion string,
) error {
	_, err := c.db.ExecContext(
		ctx,
		`DELETE FROM sticker_descriptions
		 WHERE file_unique_id = ? AND model = ? AND prompt_version = ?`,
		fileUniqueID,
		model,
		promptVersion,
	)
	if err != nil {
		return fmt.Errorf("delete sticker description cache: %w", err)
	}
	return nil
}

func (c *stickerDescriptionCache) Close() error {
	return c.db.Close()
}
