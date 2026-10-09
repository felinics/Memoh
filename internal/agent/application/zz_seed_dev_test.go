package application

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// Scratch seeding for the MEMOH-100 UI check; not committed.
func TestZZSeedDevSession(t *testing.T) {
	dsn, bot, session, mode := os.Getenv("SEED_DSN"), os.Getenv("SEED_BOT"), os.Getenv("SEED_SESSION"), os.Getenv("SEED_MODE")
	if dsn == "" {
		t.Skip()
	}
	ctx := context.Background()
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	queries := dbsqlc.New(pool)
	f := progressFixture{t: t, ctx: ctx, pool: pool, queries: queries, messages: messagepkg.NewService(nil, postgresstore.NewQueries(queries)), botID: bot, sessionID: session}
	if mode == "refused" {
		f.text("user", strings.Repeat("REFUSED question about the incident report. ", 30))
		f.text("assistant", strings.Repeat("REFUSED answer with the incident details. ", 30))
		f.reasoning()
		f.text("user", strings.Repeat("LATER question about the release plan. ", 30))
		f.text("assistant", strings.Repeat("LATER answer with the release plan. ", 30))
		f.text("user", "current question")
		return
	}
	if mode == "barrier" {
		f.text("user", strings.Repeat("WARMUP question about the repository layout. ", 20))
		f.text("assistant", strings.Repeat("WARMUP answer describing the repository layout. ", 20))
		f.text("user", "TASK-1: migrate the build scripts and report the failing targets")
		f.reasoning()
		for n := 1; n <= 6; n++ {
			f.exec(n)
		}
		f.text("assistant", strings.Repeat("ANSWER-1 the build scripts are migrated. ", 20))
		f.text("user", "TASK-2: now port the deploy scripts the same way")
		f.reasoning()
		for n := 7; n <= 10; n++ {
			f.exec(n)
		}
		return
	}
	if mode == "longturn" {
		f.text("user", "ok")
		f.text("user", "LONG-TASK: migrate every module and report the failing ones")
		for n := 1; n <= 20; n++ {
			f.exec(100 + n)
		}
		return
	}
	for n := 1; n <= 14; n++ {
		f.askUser(n)
		if n == 5 || n == 10 {
			f.reasoning()
		}
	}
	f.text("user", "请帮我继续处理剩下的这些任务")
	f.reasoning()
	if mode == "production" {
		for n := 1; n <= 6; n++ {
			f.text("user", fmt.Sprintf("LATER request %d: migrate the build scripts and report every failing target", n))
			f.exec(n)
			f.text("assistant", strings.Repeat(fmt.Sprintf("LATER answer %d explains the migration result in detail. ", n), 40))
		}
	}
	f.text("user", "current question")
}
