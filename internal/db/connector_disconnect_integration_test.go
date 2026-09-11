//go:build integration

package db_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/team"
)

func TestConnectorDisconnectFinalizationIsAtomicAndScoped(t *testing.T) {
	ctx := t.Context()
	pool := freshMigratedDB(t)
	teamA, teamB := team.DefaultTeamID, uuid.NewString()
	user, botA, siblingBot, botB := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO teams(id) VALUES ($1)", teamB)
	exec("INSERT INTO users(id,username) VALUES ($1,'disconnect-test')", user)
	for _, id := range []string{teamA, teamB} {
		exec("INSERT INTO team_members(team_id,user_id,role) VALUES ($1,$2,'admin')", id, user)
	}
	for _, pair := range [][2]string{{teamA, botA}, {teamA, siblingBot}, {teamB, botB}} {
		exec("INSERT INTO bots(id,team_id,owner_user_id,name) VALUES ($1,$2,$3,$4)", pair[1], pair[0], user, pair[1])
	}
	for _, pair := range [][2]string{{teamA, botA}, {teamB, botB}} {
		exec("INSERT INTO connectors(team_id,bot_id,connection_id,alias) VALUES ($1,$2,'shared-id','github')", pair[0], pair[1])
	}
	var apps []string
	for index, pair := range [][2]string{{teamA, botA}, {teamA, botA}, {teamA, siblingBot}, {teamB, botB}} {
		id := uuid.NewString()
		apps = append(apps, id)
		exec(`INSERT INTO bot_app_installations(id,team_id,bot_id,workspace_target_id,registry_id,app_id,revision,status)
		 VALUES ($1,$2,$3,'native','memoh',$4,repeat('a',64),'installed')`, id, pair[0], pair[1], id)
		exec(`INSERT INTO bot_app_connector_refs(team_id,installation_id,connector_type,connection_id,required)
		 VALUES ($1,$2,'github','shared-id',$3)`, pair[0], id, index != 1)
	}
	// Inject an error after the statement has attempted to clear references.
	exec(`CREATE FUNCTION reject_disconnect_status() RETURNS trigger LANGUAGE plpgsql AS $$
	 BEGIN IF NEW.status = 'partial' THEN RAISE EXCEPTION 'injected status failure'; END IF; RETURN NEW; END $$`)
	exec("CREATE TRIGGER reject_disconnect BEFORE UPDATE ON bot_app_installations FOR EACH ROW EXECUTE FUNCTION reject_disconnect_status()")
	rc := rlsConn(t, pool, teamMigrationDSN(t))
	if _, err := rc.Exec(ctx, "SELECT set_config('memoh.team_id',$1,false)", teamA); err != nil {
		t.Fatal(err)
	}
	q := dbsqlc.New(rc)
	var botID pgtype.UUID
	if err := botID.Scan(botA); err != nil {
		t.Fatal(err)
	}
	params := dbsqlc.DeleteConnectorParams{BotID: botID, ConnectionID: "shared-id"}
	if err := q.DeleteConnector(ctx, params); err == nil {
		t.Fatal("expected status-write failure")
	}
	check := func(id, wantConnection, wantStatus string) {
		t.Helper()
		var connection, status string
		if err := pool.QueryRow(ctx, `SELECT r.connection_id,i.status FROM bot_app_connector_refs r
		 JOIN bot_app_installations i ON i.team_id=r.team_id AND i.id=r.installation_id WHERE i.id=$1`, id).Scan(&connection, &status); err != nil {
			t.Fatal(err)
		}
		if connection != wantConnection || status != wantStatus {
			t.Fatalf("app %s: connection=%q status=%q", id, connection, status)
		}
	}
	for _, id := range apps {
		check(id, "shared-id", "installed")
	}
	if _, err := q.GetConnectorByConnectionID(ctx, dbsqlc.GetConnectorByConnectionIDParams(params)); err != nil {
		t.Fatalf("failed finalization lost retry binding: %v", err)
	}
	exec("DROP TRIGGER reject_disconnect ON bot_app_installations")
	if err := q.DeleteConnector(ctx, params); err != nil {
		t.Fatal(err)
	}
	check(apps[0], "", "partial")
	check(apps[1], "", "installed")
	check(apps[2], "shared-id", "installed")
	check(apps[3], "shared-id", "installed")
	if err := q.DeleteConnector(ctx, params); err != nil {
		t.Fatalf("retry must be idempotent: %v", err)
	}
}
