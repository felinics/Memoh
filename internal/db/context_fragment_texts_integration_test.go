//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/team"
)

func TestContextFragmentTextsBelongToTheirBot(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire database connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		t.Fatalf("bind default team: %v", err)
	}

	const (
		botA = "00000000-0000-0000-0000-00000000b601"
		botB = "00000000-0000-0000-0000-00000000b602"
	)
	if _, err := conn.Exec(ctx, `
WITH principal AS (
  INSERT INTO users (username, is_active, metadata)
  VALUES ('context-fragment-texts-owner', true, '{}')
  RETURNING id
), membership AS (
  INSERT INTO team_members (team_id, user_id)
  SELECT $1, principal.id FROM principal
  RETURNING user_id
)
INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
SELECT bot.id, $1, membership.user_id, bot.name, 'ready', '{}'
FROM membership, (VALUES ($2::uuid, 'fragment-texts-a'), ($3::uuid, 'fragment-texts-b')) AS bot(id, name)
`, team.DefaultTeamID, botA, botB); err != nil {
		t.Fatalf("seed bots: %v", err)
	}

	queries := sqlc.New(conn)
	pgBotA := mustParseLifecycleUUID(t, botA)
	pgBotB := mustParseLifecycleUUID(t, botB)
	hashes := []string{"h-shared", "h-only-a"}
	if err := queries.UpsertContextFragmentTexts(ctx, sqlc.UpsertContextFragmentTextsParams{
		BotID:         pgBotA,
		ContentHashes: hashes,
		Kinds:         []string{"system_prompt", "workspace_instruction"},
		Labels:        []string{"system.prompt.body", "system.workspace_file.AGENTS.md"},
		Texts:         []string{"You are Memoh.", "Follow AGENTS.md"},
		TextBytes:     []int32{14, 16},
		Truncated:     []bool{false, false},
	}); err != nil {
		t.Fatalf("upsert bot A texts: %v", err)
	}
	if err := queries.UpsertContextFragmentTexts(ctx, sqlc.UpsertContextFragmentTextsParams{
		BotID:         pgBotB,
		ContentHashes: hashes[:1],
		Kinds:         []string{"system_prompt"},
		Labels:        []string{"system.prompt.body"},
		Texts:         []string{"You are Memoh."},
		TextBytes:     []int32{14},
		Truncated:     []bool{false},
	}); err != nil {
		t.Fatalf("upsert bot B texts: %v", err)
	}

	previewsA, err := queries.ListContextFragmentPreviews(ctx, sqlc.ListContextFragmentPreviewsParams{PreviewChars: 8, BotID: pgBotA, ContentHashes: hashes})
	if err != nil {
		t.Fatalf("list bot A previews: %v", err)
	}
	if len(previewsA) != 2 || len(previewsA[0].Preview) > 8 {
		t.Fatalf("bot A previews = %#v, want both hashes with bounded heads", previewsA)
	}
	textsB, err := queries.ListContextFragmentTexts(ctx, sqlc.ListContextFragmentTextsParams{BotID: pgBotB, ContentHashes: hashes})
	if err != nil {
		t.Fatalf("list bot B texts: %v", err)
	}
	if len(textsB) != 1 || textsB[0].ContentHash != "h-shared" {
		t.Fatalf("bot B texts = %#v, want only its own row", textsB)
	}

	if _, err := conn.Exec(ctx, "DELETE FROM bots WHERE team_id = $1 AND id = $2", team.DefaultTeamID, botA); err != nil {
		t.Fatalf("delete bot A: %v", err)
	}
	var remaining int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM context_fragment_texts WHERE team_id = $1", team.DefaultTeamID).Scan(&remaining); err != nil {
		t.Fatalf("count texts: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("texts after deleting bot A = %d, want only bot B's row", remaining)
	}
}
