//go:build integration

package db_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/team"
)

func TestTeamMembershipMigrationBackfillsAndReverses(t *testing.T) {
	ctx := context.Background()
	dsn := teamMigrationDSN(t)
	pool := freshMigratedDB(t)

	// 0001 now contains the final membership schema. Roll back 0115 to
	// materialize its legacy input shape before seeding the upgrade fixture.
	membershipSteps := countMigrationsFrom(t, "0115_team_memberships.up.sql")
	stepDown(t, dsn, membershipSteps)

	var userID, botID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (team_id, username, email, role, data_root)
		VALUES ($1, 'legacy-member', 'legacy-member@example.com', 'admin', '/legacy')
		RETURNING id`, team.DefaultTeamID).Scan(&userID); err != nil {
		t.Fatalf("seed pre-membership user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO bots (team_id, owner_user_id, name)
		VALUES ($1, $2, 'legacy-member-bot') RETURNING id`, team.DefaultTeamID, userID).Scan(&botID); err != nil {
		t.Fatalf("seed pre-membership bot: %v", err)
	}

	stepUp(t, dsn, membershipSteps)

	var hasTeamID, hasRole bool
	if err := pool.QueryRow(ctx, `
		SELECT
		  EXISTS (SELECT 1 FROM information_schema.columns
		           WHERE table_schema='public' AND table_name='users' AND column_name='team_id'),
		  EXISTS (SELECT 1 FROM information_schema.columns
		           WHERE table_schema='public' AND table_name='users' AND column_name='role')`).Scan(&hasTeamID, &hasRole); err != nil {
		t.Fatalf("inspect global users schema: %v", err)
	}
	if hasTeamID || hasRole {
		t.Fatalf("users still has membership columns: team_id=%v role=%v", hasTeamID, hasRole)
	}

	var membershipTeam, role, dataRoot string
	if err := pool.QueryRow(ctx, `
		SELECT team_id::text, role::text, data_root
		FROM team_members WHERE user_id=$1`, userID).Scan(&membershipTeam, &role, &dataRoot); err != nil {
		t.Fatalf("read backfilled membership: %v", err)
	}
	if membershipTeam != team.DefaultTeamID || role != "admin" || dataRoot != "/legacy" {
		t.Fatalf("backfilled membership = (%s, %s, %s)", membershipTeam, role, dataRoot)
	}

	var ownerParent string
	if err := pool.QueryRow(ctx, `
		SELECT parent.relname
		FROM pg_constraint con
		JOIN pg_class parent ON parent.oid=con.confrelid
		WHERE con.conrelid='public.bots'::regclass
		  AND con.conname='bots_owner_user_id_fkey'`).Scan(&ownerParent); err != nil {
		t.Fatalf("read bot owner FK: %v", err)
	}
	if ownerParent != "team_members" {
		t.Fatalf("bot owner FK parent = %q, want team_members", ownerParent)
	}

	stepDown(t, dsn, membershipSteps)

	var restoredTeam, restoredRole, restoredRoot string
	if err := pool.QueryRow(ctx, `
		SELECT team_id::text, role::text, data_root FROM users WHERE id=$1`, userID,
	).Scan(&restoredTeam, &restoredRole, &restoredRoot); err != nil {
		t.Fatalf("read restored user: %v", err)
	}
	if restoredTeam != team.DefaultTeamID || restoredRole != "admin" || restoredRoot != "/legacy" {
		t.Fatalf("restored user = (%s, %s, %s)", restoredTeam, restoredRole, restoredRoot)
	}
	var membershipTableExists bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.team_members') IS NOT NULL`,
	).Scan(&membershipTableExists); err != nil {
		t.Fatalf("inspect membership table after down: %v", err)
	}
	if membershipTableExists {
		t.Fatal("team_members still exists after rolling back 0115")
	}

	stepUp(t, dsn, membershipSteps)
	var preservedBot bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bots WHERE id=$1)`, botID).Scan(&preservedBot); err != nil {
		t.Fatalf("check bot after re-up: %v", err)
	}
	if !preservedBot {
		t.Fatal("membership migration round trip lost the owned bot")
	}
}

func TestGlobalUserCanBelongToMultipleTeams(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	const teamTwo = "00000000-0000-0000-0000-0000000000f2"

	if _, err := pool.Exec(ctx, `INSERT INTO teams (id, slug) VALUES ($1, 'team-two')`, teamTwo); err != nil {
		t.Fatalf("seed second team: %v", err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, display_name)
		VALUES ('shared-user', 'shared@example.com', 'hash', 'Shared User')
		RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("seed global user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, role, data_root)
		VALUES ($1, $3, 'admin', '/team-one'), ($2, $3, 'member', '/team-two')`,
		team.DefaultTeamID, teamTwo, userID); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}

	accountAs := func(teamID string) dbsqlc.TeamAccount {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin account read: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id', $1, true)", teamID); err != nil {
			t.Fatalf("bind account team: %v", err)
		}
		account, err := dbsqlc.New(tx).GetAccountByUserID(ctx, uuidValue(t, userID))
		if err != nil {
			t.Fatalf("get account for team %s: %v", teamID, err)
		}
		return account
	}

	teamOneAccount := accountAs(team.DefaultTeamID)
	teamTwoAccount := accountAs(teamTwo)
	if teamOneAccount.ID != teamTwoAccount.ID {
		t.Fatalf("team accounts use different principals: %s vs %s", teamOneAccount.ID, teamTwoAccount.ID)
	}
	if teamOneAccount.Role != "admin" || teamOneAccount.DataRoot.String != "/team-one" {
		t.Fatalf("team-one account role/root = %q/%q", teamOneAccount.Role, teamOneAccount.DataRoot.String)
	}
	if teamTwoAccount.Role != "member" || teamTwoAccount.DataRoot.String != "/team-two" {
		t.Fatalf("team-two account role/root = %q/%q", teamTwoAccount.Role, teamTwoAccount.DataRoot.String)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO users (username) VALUES ('shared-user')`); sqlState(err) != "23505" {
		t.Fatalf("global username uniqueness SQLSTATE = %q, want 23505", sqlState(err))
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin remove member: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id', $1, true)", teamTwo); err != nil {
		t.Fatalf("bind remove-member team: %v", err)
	}
	if _, err := dbsqlc.New(tx).RemoveMember(ctx, uuidValue(t, userID)); err != nil {
		t.Fatalf("remove team-two member: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit remove member: %v", err)
	}

	var username, passwordHash string
	if err := pool.QueryRow(ctx, `SELECT username, password_hash FROM users WHERE id=$1`, userID).Scan(&username, &passwordHash); err != nil {
		t.Fatalf("read global credentials: %v", err)
	}
	if username != "shared-user" || passwordHash != "hash" {
		t.Fatalf("remove member changed credentials: %q/%q", username, passwordHash)
	}
	var teamOneActive, teamTwoActive bool
	if err := pool.QueryRow(ctx, `
		SELECT
		  bool_or(is_active) FILTER (WHERE team_id=$1),
		  bool_or(is_active) FILTER (WHERE team_id=$2)
		FROM team_members WHERE user_id=$3`, team.DefaultTeamID, teamTwo, userID,
	).Scan(&teamOneActive, &teamTwoActive); err != nil {
		t.Fatalf("read membership states: %v", err)
	}
	if !teamOneActive || teamTwoActive {
		t.Fatalf("membership active states = %v/%v", teamOneActive, teamTwoActive)
	}
}

func TestAccountQueriesAttachExistingUserToAnotherTeam(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	const teamTwo = "00000000-0000-0000-0000-0000000000f2"
	const unusedUserID = "00000000-0000-0000-0000-0000000000a2"

	if _, err := pool.Exec(ctx, `INSERT INTO teams (id, slug) VALUES ($1, 'account-team-two')`, teamTwo); err != nil {
		t.Fatalf("seed second team: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin default-team account: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id', $1, true)", team.DefaultTeamID); err != nil {
		t.Fatalf("bind default team: %v", err)
	}
	queries := dbsqlc.New(tx)
	created, err := queries.CreateUser(ctx, dbsqlc.CreateUserParams{
		IsActive: true,
		Metadata: []byte(`{"source":"test"}`),
	})
	if err != nil {
		t.Fatalf("create global user and membership: %v", err)
	}
	account, err := queries.CreateAccount(ctx, dbsqlc.CreateAccountParams{
		Username:     pgtype.Text{String: "query-shared", Valid: true},
		Email:        pgtype.Text{String: "query-shared@example.com", Valid: true},
		PasswordHash: pgtype.Text{String: "team-one-hash", Valid: true},
		DisplayName:  pgtype.Text{String: "Query Shared", Valid: true},
		AvatarUrl:    pgtype.Text{},
		UserID:       created.ID,
		Role:         "admin",
		IsActive:     true,
		DataRoot:     pgtype.Text{String: "/query-team-one", Valid: true},
	})
	if err != nil {
		t.Fatalf("create default-team account: %v", err)
	}
	if account.Role != "admin" || account.TeamID.String() != team.DefaultTeamID {
		t.Fatalf("default-team account role/team = %q/%q", account.Role, account.TeamID.String())
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit default-team account: %v", err)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin second-team account: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id', $1, true)", teamTwo); err != nil {
		t.Fatalf("bind second team: %v", err)
	}
	attached, err := dbsqlc.New(tx).UpsertAccountByUsername(ctx, dbsqlc.UpsertAccountByUsernameParams{
		UserID:       uuidValue(t, unusedUserID),
		Username:     pgtype.Text{String: "query-shared", Valid: true},
		Email:        pgtype.Text{String: "query-shared@example.com", Valid: true},
		PasswordHash: pgtype.Text{String: "team-two-hash", Valid: true},
		DisplayName:  pgtype.Text{String: "Query Shared", Valid: true},
		AvatarUrl:    pgtype.Text{},
		IsActive:     true,
		Role:         "member",
		DataRoot:     pgtype.Text{String: "/query-team-two", Valid: true},
	})
	if err != nil {
		t.Fatalf("attach existing account to second team: %v", err)
	}
	if attached.ID != created.ID {
		t.Fatalf("attached principal = %s, want existing %s", attached.ID, created.ID)
	}
	if attached.Role != "member" || attached.TeamID.String() != teamTwo {
		t.Fatalf("second-team account role/team = %q/%q", attached.Role, attached.TeamID.String())
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit second-team account: %v", err)
	}

	var userCount, membershipCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE username='query-shared'`).Scan(&userCount); err != nil {
		t.Fatalf("count global users: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM team_members WHERE user_id=$1`, created.ID).Scan(&membershipCount); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if userCount != 1 || membershipCount != 2 {
		t.Fatalf("global users/memberships = %d/%d, want 1/2", userCount, membershipCount)
	}
	var passwordHash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, created.ID).Scan(&passwordHash); err != nil {
		t.Fatalf("read shared password hash: %v", err)
	}
	if passwordHash != "team-one-hash" {
		t.Fatalf("second-team membership overwrote global password hash: %q", passwordHash)
	}
}

func TestAdminAccountUpdateOnlyChangesCurrentMembership(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	const teamTwo = "00000000-0000-0000-0000-0000000000f2"

	if _, err := pool.Exec(ctx, `INSERT INTO teams (id, slug) VALUES ($1, 'admin-update-team-two')`, teamTwo); err != nil {
		t.Fatalf("seed second team: %v", err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, display_name, avatar_url)
		VALUES ('admin-update-shared', 'Original Name', 'https://example.com/original.png')
		RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("seed global user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, role, is_active)
		VALUES ($1, $3, 'admin', true), ($2, $3, 'member', true)`,
		team.DefaultTeamID, teamTwo, userID); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin admin update: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id', $1, true)", teamTwo); err != nil {
		t.Fatalf("bind admin-update team: %v", err)
	}
	updated, err := dbsqlc.New(tx).UpdateAccountAdmin(ctx, dbsqlc.UpdateAccountAdminParams{
		UserID:   uuidValue(t, userID),
		Role:     "admin",
		IsActive: pgtype.Bool{Bool: false, Valid: true},
	})
	if err != nil {
		t.Fatalf("update current membership: %v", err)
	}
	if updated.Role != "admin" || updated.IsActive.Bool {
		t.Fatalf("updated membership role/active = %q/%v", updated.Role, updated.IsActive.Bool)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit admin update: %v", err)
	}

	var displayName, avatarURL, teamOneRole string
	var teamOneActive, teamTwoActive bool
	if err := pool.QueryRow(ctx, `
		SELECT u.display_name, u.avatar_url,
		       one.role::text, one.is_active, two.is_active
		FROM users u
		JOIN team_members one ON one.user_id=u.id AND one.team_id=$1
		JOIN team_members two ON two.user_id=u.id AND two.team_id=$2
		WHERE u.id=$3`, team.DefaultTeamID, teamTwo, userID,
	).Scan(&displayName, &avatarURL, &teamOneRole, &teamOneActive, &teamTwoActive); err != nil {
		t.Fatalf("read post-update state: %v", err)
	}
	if displayName != "Original Name" || avatarURL != "https://example.com/original.png" {
		t.Fatalf("admin membership update changed global profile: %q/%q", displayName, avatarURL)
	}
	if teamOneRole != "admin" || !teamOneActive || teamTwoActive {
		t.Fatalf("membership states = team-one %q/%v, team-two active=%v", teamOneRole, teamOneActive, teamTwoActive)
	}
}

func TestRoleOnlyAdminUpdatePreservesMembershipState(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)

	var targetID, supportAdminID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, is_active)
		VALUES ('globally-suspended-member', false)
		RETURNING id`).Scan(&targetID); err != nil {
		t.Fatalf("seed suspended principal: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, is_active)
		VALUES ('support-admin', true)
		RETURNING id`).Scan(&supportAdminID); err != nil {
		t.Fatalf("seed support admin: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, role, is_active)
		VALUES ($1, $2, 'member', true), ($1, $3, 'admin', true)`,
		team.DefaultTeamID, targetID, supportAdminID); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin role-only update: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id', $1, true)", team.DefaultTeamID); err != nil {
		t.Fatalf("bind role-only update team: %v", err)
	}
	updated, err := dbsqlc.New(tx).UpdateAccountAdmin(ctx, dbsqlc.UpdateAccountAdminParams{
		UserID:   uuidValue(t, targetID),
		Role:     "admin",
		IsActive: pgtype.Bool{},
	})
	if err != nil {
		t.Fatalf("role-only update: %v", err)
	}
	if updated.PrincipalIsActive || !updated.MembershipIsActive || updated.IsActive.Bool {
		t.Fatalf("active states = principal %v, membership %v, effective %v",
			updated.PrincipalIsActive, updated.MembershipIsActive, updated.IsActive.Bool)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit role-only update: %v", err)
	}
}

func TestLastActiveTeamAdminGuard(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)

	var firstID, secondID, inactiveID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username) VALUES ('guard-admin-one') RETURNING id`).Scan(&firstID); err != nil {
		t.Fatalf("seed first admin principal: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, role, is_active)
		VALUES ($1, $2, 'admin', true)`, team.DefaultTeamID, firstID); err != nil {
		t.Fatalf("seed first admin membership: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, is_active)
		VALUES ('guard-inactive-admin', false)
		RETURNING id`).Scan(&inactiveID); err != nil {
		t.Fatalf("seed inactive admin principal: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, role, is_active)
		VALUES ($1, $2, 'admin', true)`, team.DefaultTeamID, inactiveID); err != nil {
		t.Fatalf("seed inactive admin membership: %v", err)
	}

	for name, statement := range map[string]string{
		"demote":     `UPDATE team_members SET role='member' WHERE user_id=$1`,
		"deactivate": `UPDATE team_members SET is_active=false WHERE user_id=$1`,
		"delete":     `DELETE FROM team_members WHERE user_id=$1`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(ctx, statement, firstID)
			if sqlState(err) != "23514" {
				t.Fatalf("SQLSTATE = %q, want 23514: %v", sqlState(err), err)
			}
		})
	}

	if err := pool.QueryRow(ctx, `INSERT INTO users (username) VALUES ('guard-admin-two') RETURNING id`).Scan(&secondID); err != nil {
		t.Fatalf("seed second admin principal: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, role, is_active)
		VALUES ($1, $2, 'admin', true)`, team.DefaultTeamID, secondID); err != nil {
		t.Fatalf("seed second admin membership: %v", err)
	}

	ids := []string{firstID, secondID}
	errs := make([]error, len(ids))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, errs[index] = pool.Exec(ctx,
				`UPDATE team_members SET role='member' WHERE team_id=$1 AND user_id=$2`,
				team.DefaultTeamID, ids[index])
		}(i)
	}
	close(start)
	wg.Wait()

	succeeded, rejected := 0, 0
	for _, err := range errs {
		switch sqlState(err) {
		case "":
			if err == nil {
				succeeded++
			}
		case "23514":
			rejected++
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent demotions succeeded/rejected = %d/%d, errors=%v", succeeded, rejected, errs)
	}

	var activeAdmins int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM team_members membership
		JOIN users principal
		  ON principal.id=membership.user_id
		 AND principal.is_active
		WHERE membership.team_id=$1
		  AND membership.role='admin'
		  AND membership.is_active`, team.DefaultTeamID).Scan(&activeAdmins); err != nil {
		t.Fatalf("count active admins: %v", err)
	}
	if activeAdmins != 1 {
		t.Fatalf("active admin count = %d, want 1", activeAdmins)
	}
}

func TestTeamMembershipRejectsCrossTeamOwnership(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	const teamTwo = "00000000-0000-0000-0000-0000000000f2"

	if _, err := pool.Exec(ctx, `INSERT INTO teams (id, slug) VALUES ($1, 'owner-team-two')`, teamTwo); err != nil {
		t.Fatalf("seed second team: %v", err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username) VALUES ('owner-one') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, team.DefaultTeamID, userID); err != nil {
		t.Fatalf("seed team-one membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bots (team_id, owner_user_id, name) VALUES ($1, $2, 'cross-owner')`, teamTwo, userID); sqlState(err) != "23503" {
		t.Fatalf("cross-team owner SQLSTATE = %q, want 23503", sqlState(err))
	}
	if _, err := pool.Exec(ctx, `INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, teamTwo, userID); err != nil {
		t.Fatalf("add team-two membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bots (team_id, owner_user_id, name) VALUES ($1, $2, 'same-owner')`, teamTwo, userID); err != nil {
		t.Fatalf("same-team owner insert: %v", err)
	}
}

func TestTeamMembershipDownFailsWithMultipleMemberships(t *testing.T) {
	ctx := context.Background()
	dsn := teamMigrationDSN(t)
	pool := freshMigratedDB(t)
	const teamTwo = "00000000-0000-0000-0000-0000000000f2"

	if _, err := pool.Exec(ctx, `INSERT INTO teams (id, slug) VALUES ($1, 'down-team-two')`, teamTwo); err != nil {
		t.Fatalf("seed second team: %v", err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username) VALUES ('multi-down') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("seed global user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id)
		VALUES ($1, $3), ($2, $3)`, team.DefaultTeamID, teamTwo, userID); err != nil {
		t.Fatalf("seed multiple memberships: %v", err)
	}
	if err := tryStepDown(t, dsn, countMigrationsFrom(t, "0115_team_memberships.up.sql")); err == nil {
		t.Fatal("0115 down must fail closed when a user has multiple memberships")
	}
}

func uuidValue(t *testing.T, value string) (out pgtype.UUID) {
	t.Helper()
	if err := out.Scan(value); err != nil {
		t.Fatalf("parse uuid %q: %v", value, err)
	}
	return out
}
