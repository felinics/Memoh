//go:build integration

package db_test

import (
	"io/fs"
	"testing"
)

func TestFishAudioProviderMigration(t *testing.T) {
	ctx := t.Context()
	pool := freshMigratedDB(t)
	for _, clientType := range []string{"fishaudio-speech", "fishaudio-transcription"} {
		if _, err := pool.Exec(ctx, `INSERT INTO providers (name, client_type, team_id) VALUES ($1, $1, '00000000-0000-0000-0000-000000000001')`, clientType); err != nil {
			t.Fatalf("create %s provider after upgrade: %v", clientType, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO providers (name, client_type, team_id) VALUES ('unknown', 'unknown-audio', '00000000-0000-0000-0000-000000000001')`); sqlState(err) != "23514" {
		t.Fatalf("unknown provider type: got %v, want check violation", err)
	}

	down, err := fs.ReadFile(postgresMigrationsFS(t), "0169_fishaudio_providers.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, err := fs.ReadFile(postgresMigrationsFS(t), "0169_fishaudio_providers.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); sqlState(err) != "23514" {
		t.Fatalf("rollback must refuse configured Fish providers: got %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM providers WHERE client_type IN ('fishaudio-speech', 'fishaudio-transcription')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rollback lost provider configuration: count=%d err=%v", count, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM providers WHERE client_type IN ('fishaudio-speech', 'fishaudio-transcription')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := pool.Exec(ctx, string(down)); err != nil {
			t.Fatalf("rollback without Fish providers: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO providers (name, client_type, team_id) VALUES ('fish-after-down', 'fishaudio-speech', '00000000-0000-0000-0000-000000000001')`); sqlState(err) != "23514" {
		t.Fatalf("rollback still permits Fish: got %v", err)
	}
	for range 2 {
		if _, err := pool.Exec(ctx, string(up)); err != nil {
			t.Fatalf("reapply Fish upgrade: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO providers (name, client_type, team_id) VALUES ('fish-after-up', 'fishaudio-speech', '00000000-0000-0000-0000-000000000001')`); err != nil {
		t.Fatalf("reapplied upgrade rejects Fish: %v", err)
	}

	t.Run("canonical init supports both providers", func(t *testing.T) {
		canonical := resetToEmpty(t)
		applyCanonicalInitOnly(t, teamMigrationDSN(t))
		for _, clientType := range []string{"fishaudio-speech", "fishaudio-transcription"} {
			if _, err := canonical.Exec(ctx, `INSERT INTO providers (name, client_type, team_id) VALUES ($1, $1, '00000000-0000-0000-0000-000000000001')`, clientType); err != nil {
				t.Fatalf("canonical schema rejects %s: %v", clientType, err)
			}
		}
	})
}
