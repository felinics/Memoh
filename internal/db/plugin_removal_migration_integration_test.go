//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/team"
)

func TestPluginRemovalMigrationDeletesManagedMCPConnections(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)
	steps := countMigrationsFrom(t, "0136_remove_plugin_system.up.sql")

	// Recreate the legacy Plugin schema, then exercise the real 0136 upgrade.
	stepDown(t, dsn, steps)

	const (
		userID       = "10000000-0000-4000-8000-000000000131"
		botID        = "20000000-0000-4000-8000-000000000131"
		userMCPID    = "30000000-0000-4000-8000-000000000131"
		managedMCPID = "40000000-0000-4000-8000-000000000131"
	)

	if _, err := pool.Exec(ctx, `
		INSERT INTO public.users (id, username)
		VALUES ($1, 'plugin-removal-owner')`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.team_members (team_id, user_id)
		VALUES ($1, $2)`, team.DefaultTeamID, userID); err != nil {
		t.Fatalf("seed team membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.bots (id, team_id, owner_user_id, name)
		VALUES ($1, $2, $3, 'plugin-removal-bot')`, botID, team.DefaultTeamID, userID); err != nil {
		t.Fatalf("seed bot: %v", err)
	}

	var installationID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO public.bot_plugin_installations (team_id, bot_id, plugin_id, plugin_name)
		VALUES ($1, $2, 'legacy-plugin', 'Legacy Plugin')
		RETURNING id`, team.DefaultTeamID, botID).Scan(&installationID); err != nil {
		t.Fatalf("seed Plugin installation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.mcp_connections (id, team_id, bot_id, name, type)
		VALUES ($1, $2, $3, 'user-mcp', 'http')`, userMCPID, team.DefaultTeamID, botID); err != nil {
		t.Fatalf("seed user MCP connection: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.mcp_connections (
			id, team_id, bot_id, name, type,
			managed_by_plugin_installation_id, managed_resource_key, visible
		)
		VALUES ($1, $2, $3, 'plugin-mcp', 'http', $4, 'mcp:legacy', false)`,
		managedMCPID, team.DefaultTeamID, botID, installationID); err != nil {
		t.Fatalf("seed managed MCP connection: %v", err)
	}

	stepUp(t, dsn, steps)

	var userMCPCount, managedMCPCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE id = $1),
		       count(*) FILTER (WHERE id = $2)
		  FROM public.mcp_connections`, userMCPID, managedMCPID).
		Scan(&userMCPCount, &managedMCPCount); err != nil {
		t.Fatalf("inspect migrated MCP connections: %v", err)
	}
	if userMCPCount != 1 || managedMCPCount != 0 {
		t.Fatalf("MCP counts after migration = user:%d managed:%d, want user:1 managed:0", userMCPCount, managedMCPCount)
	}

	var rlsEnabled, rlsForced bool
	if err := pool.QueryRow(ctx, `
		SELECT relrowsecurity, relforcerowsecurity
		  FROM pg_catalog.pg_class
		 WHERE oid = 'public.mcp_connections'::regclass`).Scan(&rlsEnabled, &rlsForced); err != nil {
		t.Fatalf("inspect MCP row-level security: %v", err)
	}
	if !rlsEnabled || !rlsForced {
		t.Fatalf("mcp_connections RLS after migration = enabled:%t forced:%t, want true/true", rlsEnabled, rlsForced)
	}
}
