package acpsession

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	acp "github.com/felinics/memoh/internal/agent/runtime/acp"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestPostgresACPStateStreamsBatchesAndKeysetPagesInOrder(t *testing.T) {
	ctx := context.Background()
	pool := openACPStatePostgresPool(t, ctx)
	botID, sessionID := createACPStatePostgresFixture(t, ctx, pool)
	queries := dbsqlc.New(pool)
	store := NewStateStore(postgresstore.NewQueriesWithPool(pool, queries))
	runID, turnID, fence := createACPStateRun(t, ctx, queries, pool, botID, sessionID)

	transcriptPath := "state/sessions/a-" + runID + ".jsonl"
	const linesPerFile = 300
	const totalLines = linesPerFile * 2
	secondPath := "state/sessions/z-" + runID + ".jsonl"
	padding := strings.Repeat("x", 20*1024)
	firstContents := make([]string, 0, linesPerFile)
	secondContents := make([]string, 0, linesPerFile)
	for seq := 1; seq <= totalLines; seq++ {
		content := fmt.Sprintf(`{"seq":%d,"pad":%q}`, seq, padding)
		if seq == 1 {
			// TEXT storage keeps this short exponent verbatim: with JSONB it
			// would have expanded to roughly 128 KiB on the round trip and
			// broken every content digest.
			content = `{"seq":1,"value":1e131071}`
		}
		if seq <= linesPerFile {
			firstContents = append(firstContents, content)
		} else {
			secondContents = append(secondContents, content)
		}
	}
	state := acp.PersistedSessionState{
		AgentID: "codex", ACPSessionID: "native-" + runID, ThroughRunID: runID,
		Cwd: "/workspace", TranscriptPath: transcriptPath, RuntimeFencingToken: fence.Token,
		FileCount: 2, RecordCount: totalLines,
		Files: []acp.PersistedSessionStateFile{
			{SessionStateFileShape: acp.SessionStateFileShape{
				Path: transcriptPath, Records: linesPerFile, Digest: acpTestFileDigest(t, firstContents...),
			}},
			{SessionStateFileShape: acp.SessionStateFileShape{
				Path: secondPath, Records: linesPerFile, Digest: acpTestFileDigest(t, secondContents...),
			}},
		},
	}
	nextIndex := 0
	records := func(context.Context) (acp.SessionStateRecord, error) {
		if nextIndex == totalLines {
			return acp.SessionStateRecord{}, io.EOF
		}
		filePath := transcriptPath
		lineNumber := nextIndex + 1
		content := ""
		if nextIndex < linesPerFile {
			content = firstContents[nextIndex]
		} else {
			filePath = secondPath
			lineNumber = nextIndex - linesPerFile + 1
			content = secondContents[nextIndex-linesPerFile]
		}
		nextIndex++
		return acp.SessionStateRecord{
			FilePath: filePath, LineNumber: int64(lineNumber), Content: json.RawMessage(content),
		}, nil
	}
	if err := store.Replace(runtimefence.WithContext(ctx, fence), botID, sessionID, state, records); err != nil {
		t.Fatalf("stage ACP state: %v", err)
	}

	var headers, lines, storedFiles int
	var storedRecords int64
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM acp_session_states WHERE session_id = $1),
			(SELECT count(*) FROM acp_session_state_lines WHERE session_id = $1),
			(SELECT file_count FROM acp_session_states WHERE session_id = $1),
			(SELECT record_count FROM acp_session_states WHERE session_id = $1)
	`, sessionID).Scan(&headers, &lines, &storedFiles, &storedRecords); err != nil {
		t.Fatalf("count staged ACP state: %v", err)
	}
	if headers != 1 || lines != totalLines || storedFiles != 2 || storedRecords != totalLines {
		t.Fatalf(
			"staged ACP rows = headers:%d lines:%d files:%d records:%d",
			headers,
			lines,
			storedFiles,
			storedRecords,
		)
	}
	var storedExponent string
	var storedExponentBytes int64
	if err := pool.QueryRow(ctx, `
		SELECT content, content_bytes::bigint
		FROM acp_session_state_lines
		WHERE session_id = $1 AND file_path = $2 AND line_number = 1
	`, sessionID, transcriptPath).Scan(&storedExponent, &storedExponentBytes); err != nil {
		t.Fatalf("load stored exponent record: %v", err)
	}
	if storedExponent != `{"seq":1,"value":1e131071}` || storedExponentBytes != int64(len(storedExponent)) {
		t.Fatalf("stored content is not byte-exact: %q (%d bytes)", storedExponent, storedExponentBytes)
	}
	guard := stateLineBatchWriter{
		batchCount: 1,
		jsonlBytes: maxStateJSONLBytes,
	}
	if err := guard.account(dbsqlc.InsertACPSessionStateLinesRow{
		RowsWritten: 1,
		JsonlBytes:  1,
	}); err == nil || !strings.Contains(err.Error(), "restored JSONL bytes") {
		t.Fatalf("stored byte overflow error = %v", err)
	}
	called := false
	if found, err := store.Load(ctx, botID, sessionID, func(context.Context, acp.PersistedSessionState, acp.SessionStateRecordReader) error {
		called = true
		return nil
	}); err != nil || found || called {
		t.Fatalf("load before canonical publication = (found=%t, err=%v), want false, nil", found, err)
	}

	persistACPStateWatermark(t, ctx, pool, botID, sessionID, runID, turnID)
	var leaked acp.SessionStateRecordReader
	loadedCount := 0
	found, err := store.Load(ctx, botID, sessionID, func(
		ctx context.Context,
		header acp.PersistedSessionState,
		reader acp.SessionStateRecordReader,
	) error {
		if header.ThroughRunID != runID || header.TranscriptPath != transcriptPath ||
			header.FileCount != 2 || header.RecordCount != totalLines {
			return fmt.Errorf("unexpected streamed header: %#v", header)
		}
		leaked = reader
		for {
			record, readErr := reader(ctx)
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			wantPath := transcriptPath
			wantLine := loadedCount + 1
			if loadedCount >= linesPerFile {
				wantPath = secondPath
				wantLine = loadedCount - linesPerFile + 1
			}
			if record.FilePath != wantPath || record.LineNumber != int64(wantLine) {
				return fmt.Errorf("record %d = %s:%d", loadedCount, record.FilePath, record.LineNumber)
			}
			var value struct {
				Seq int `json:"seq"`
			}
			if err := json.Unmarshal(record.Content, &value); err != nil {
				return fmt.Errorf("decode record %d content: %w", loadedCount, err)
			}
			if value.Seq != loadedCount+1 {
				return fmt.Errorf("record %d content has sequence %d", loadedCount, value.Seq)
			}
			loadedCount++
		}
	})
	if err != nil {
		t.Fatalf("load published ACP state: %v", err)
	}
	if !found || loadedCount != totalLines {
		t.Fatalf("streamed ACP state found=%t records=%d", found, loadedCount)
	}
	if _, readErr := leaked(ctx); readErr == nil || !strings.Contains(readErr.Error(), "no longer valid") {
		t.Fatalf("reader used after Load returned: %v", readErr)
	}
}

// TestPostgresACPStateAppendsTailIncrementally pins the incremental staging
// contract: a second version with a valid prefix proof appends only its tail
// (the canonical prefix rows are physically untouched), a dangling candidate
// tail stays invisible to Load, a broken proof declines without mutating any
// canonical row, and a full rewrite becomes legal once a reset head is
// canonical.
func TestPostgresACPStateAppendsTailIncrementally(t *testing.T) {
	ctx := context.Background()
	pool := openACPStatePostgresPool(t, ctx)
	botID, sessionID := createACPStatePostgresFixture(t, ctx, pool)
	queries := dbsqlc.New(pool)
	store := NewStateStore(postgresstore.NewQueriesWithPool(pool, queries))

	transcriptPath := "state/sessions/incremental.jsonl"
	contentsV1 := []string{`{"turn":1,"role":"user"}`, `{"turn":1,"role":"assistant"}`}

	// Turn 1: full stage + publish head.
	run1, turn1, fence1 := createACPStateRun(t, ctx, queries, pool, botID, sessionID)
	state1, records1 := multiRecordACPState(t, run1, fence1.Token, transcriptPath, 0, contentsV1...)
	if err := store.Replace(runtimefence.WithContext(ctx, fence1), botID, sessionID, state1, records1); err != nil {
		t.Fatalf("stage v1: %v", err)
	}
	persistACPStateWatermark(t, ctx, pool, botID, sessionID, run1, turn1)

	finishRun := func(runID string) {
		if _, err := pool.Exec(ctx, `
			UPDATE session_runs SET state = 'completed' WHERE run_id = $1
		`, runID); err != nil {
			t.Fatalf("finish run %s: %v", runID, err)
		}
	}
	finishRun(run1)

	rowVersion := func(line int64) string {
		var xmin string
		if err := pool.QueryRow(ctx, `
			SELECT xmin::text FROM acp_session_state_lines
			WHERE session_id = $1 AND file_path = $2 AND line_number = $3
		`, sessionID, transcriptPath, line).Scan(&xmin); err != nil {
			t.Fatalf("load line %d row version: %v", line, err)
		}
		return xmin
	}
	prefixVersionBefore := rowVersion(1)

	// Turn 2: append-only stage with a valid prefix proof.
	contentsV2 := append(append([]string{}, contentsV1...), `{"turn":2,"role":"user"}`, `{"turn":2,"role":"assistant"}`)
	run2, turn2, fence2 := createACPStateRun(t, ctx, queries, pool, botID, sessionID)
	state2, records2 := multiRecordACPState(t, run2, fence2.Token, transcriptPath, int64(len(contentsV1)), contentsV2...)
	if err := store.Replace(runtimefence.WithContext(ctx, fence2), botID, sessionID, state2, records2); err != nil {
		t.Fatalf("stage v2: %v", err)
	}

	var lineCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acp_session_state_lines WHERE session_id = $1
	`, sessionID).Scan(&lineCount); err != nil {
		t.Fatalf("count lines after v2: %v", err)
	}
	if lineCount != len(contentsV2) {
		t.Fatalf("lines after append = %d, want %d", lineCount, len(contentsV2))
	}
	if got := rowVersion(1); got != prefixVersionBefore {
		t.Fatalf("append rewrote the canonical prefix: xmin %s -> %s", prefixVersionBefore, got)
	}

	// The v2 candidate is staged but unpublished: Load must still serve
	// exactly v1, bounded by the head version's shape.
	loaded := make([]string, 0, len(contentsV2))
	found, err := store.Load(ctx, botID, sessionID, func(
		ctx context.Context,
		header acp.PersistedSessionState,
		reader acp.SessionStateRecordReader,
	) error {
		if header.ThroughRunID != run1 || header.RecordCount != int64(len(contentsV1)) {
			return fmt.Errorf("unexpected v1 header: %#v", header)
		}
		for {
			record, readErr := reader(ctx)
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			loaded = append(loaded, string(record.Content))
		}
	})
	if err != nil || !found {
		t.Fatalf("load canonical v1 = (found=%t, err=%v)", found, err)
	}
	if len(loaded) != len(contentsV1) {
		t.Fatalf("dangling v2 tail leaked into v1 load: %v", loaded)
	}

	// Publish v2 and confirm Load now serves the full transcript.
	persistACPStateWatermark(t, ctx, pool, botID, sessionID, run2, turn2)
	finishRun(run2)
	loaded = loaded[:0]
	if _, err := store.Load(ctx, botID, sessionID, func(
		ctx context.Context,
		header acp.PersistedSessionState,
		reader acp.SessionStateRecordReader,
	) error {
		if header.ThroughRunID != run2 {
			return fmt.Errorf("unexpected v2 header: %#v", header)
		}
		for {
			record, readErr := reader(ctx)
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			loaded = append(loaded, string(record.Content))
		}
	}); err != nil {
		t.Fatalf("load published v2: %v", err)
	}
	if len(loaded) != len(contentsV2) {
		t.Fatalf("published v2 records = %d, want %d", len(loaded), len(contentsV2))
	}

	// Turn 3: a broken prefix proof must DECLINE staging without touching any
	// row the canonical version references - staging and publication commit in
	// different transactions, so a destructive rewrite here would corrupt the
	// still-canonical v2 if this round never commits.
	contentsV3 := append(append([]string{}, contentsV2...), `{"turn":3,"role":"user"}`)
	run3, turn3, fence3 := createACPStateRun(t, ctx, queries, pool, botID, sessionID)
	state3, records3 := multiRecordACPState(t, run3, fence3.Token, transcriptPath, int64(len(contentsV2)), contentsV3...)
	state3.Files[0].PrefixDigest = strings.Repeat("0", 64)
	prefixVersionBeforeDecline := rowVersion(1)
	if err := store.Replace(runtimefence.WithContext(ctx, fence3), botID, sessionID, state3, records3); !errors.Is(err, acp.ErrSessionStateDivergent) {
		t.Fatalf("stage v3 with broken proof error = %v, want ErrSessionStateDivergent", err)
	}
	if got := rowVersion(1); got != prefixVersionBeforeDecline {
		t.Fatalf("declined staging mutated canonical rows: xmin %s -> %s", prefixVersionBeforeDecline, got)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acp_session_state_lines WHERE session_id = $1
	`, sessionID).Scan(&lineCount); err != nil {
		t.Fatalf("count lines after declined v3: %v", err)
	}
	if lineCount != len(contentsV2) {
		t.Fatalf("lines after declined staging = %d, want untouched %d", lineCount, len(contentsV2))
	}

	// Once the divergent turn's reset head is canonical, the rows are
	// unreferenced and the next staging performs a safe full rewrite.
	if _, err := pool.Exec(ctx, `
		INSERT INTO bot_history_messages (
			bot_id, session_id, role, content, metadata, session_mode, runtime_type,
			turn_id, turn_position, turn_message_seq, turn_visible, run_id
		) VALUES (
			$1, $2, 'assistant', '{"role":"assistant","content":"ok"}'::jsonb,
			'{"acp_turn_outcome":"succeeded"}'::jsonb,
			'chat', 'acp_agent', $3, 1, 1, true, $4
		)
	`, botID, sessionID, turn3, run3); err != nil {
		t.Fatalf("persist reset round: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acp_session_publications (session_id, run_id, checkpoint_reset)
		VALUES ($1, $2, true)
		ON CONFLICT (team_id, session_id) DO UPDATE SET
			run_id = EXCLUDED.run_id,
			checkpoint_reset = EXCLUDED.checkpoint_reset,
			updated_at = now()
	`, sessionID, run3); err != nil {
		t.Fatalf("publish reset head: %v", err)
	}
	finishRun(run3)
	run4, _, fence4 := createACPStateRun(t, ctx, queries, pool, botID, sessionID)
	state4, records4 := multiRecordACPState(t, run4, fence4.Token, transcriptPath, 0, contentsV3...)
	if err := store.Replace(runtimefence.WithContext(ctx, fence4), botID, sessionID, state4, records4); err != nil {
		t.Fatalf("stage full rewrite after reset head: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acp_session_state_lines WHERE session_id = $1
	`, sessionID).Scan(&lineCount); err != nil {
		t.Fatalf("count lines after post-reset rewrite: %v", err)
	}
	if lineCount != len(contentsV3) {
		t.Fatalf("lines after post-reset rewrite = %d, want %d", lineCount, len(contentsV3))
	}
}

func TestPostgresHistoryClearDeletesACPStateAndFencesOldOwner(t *testing.T) {
	ctx := context.Background()
	pool := openACPStatePostgresPool(t, ctx)
	botID, sessionID := createACPStatePostgresFixture(t, ctx, pool)
	queries := dbsqlc.New(pool)
	storeQueries := postgresstore.NewQueriesWithPool(pool, queries)
	store := NewStateStore(storeQueries)
	runID, turnID, fence := createACPStateRun(t, ctx, queries, pool, botID, sessionID)
	state, records := singleRecordACPState(t, runID, fence.Token, `{"before_clear":true}`)

	if err := store.Replace(runtimefence.WithContext(ctx, fence), botID, sessionID, state, records); err != nil {
		t.Fatalf("stage ACP state: %v", err)
	}
	persistACPStateWatermark(t, ctx, pool, botID, sessionID, runID, turnID)
	if err := storeQueries.ClearHistoryBySession(ctx, mustACPStateUUID(t, sessionID)); err != nil {
		t.Fatalf("clear session history: %v", err)
	}

	var states, lines, messages int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM acp_session_states WHERE session_id = $1),
			(SELECT count(*) FROM acp_session_state_lines WHERE session_id = $1),
			(SELECT count(*) FROM bot_history_messages WHERE session_id = $1)
	`, sessionID).Scan(&states, &lines, &messages); err != nil {
		t.Fatalf("count cleared ACP history: %v", err)
	}
	if states != 0 || lines != 0 || messages != 0 {
		t.Fatalf("history clear left states=%d lines=%d messages=%d", states, lines, messages)
	}
	_, staleRecords := singleRecordACPState(t, runID, fence.Token, `{"after_clear":true}`)
	if err := store.Replace(runtimefence.WithContext(ctx, fence), botID, sessionID, state, staleRecords); !errors.Is(err, runtimefence.ErrStale) {
		t.Fatalf("stage with pre-clear fence error = %v, want runtimefence.ErrStale", err)
	}
}

func openACPStatePostgresPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("ACP state PostgreSQL test is required, but TEST_POSTGRES_DSN is not set")
		}
		t.Skip("set TEST_POSTGRES_DSN to run ACP state PostgreSQL integration")
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open ACP state PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		if err := dbtest.MigratePostgresUp(dsn); err != nil {
			t.Fatalf("migrate ACP state PostgreSQL database: %v", err)
		}
	}
	return pool
}

func createACPStatePostgresFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	userID := uuid.New()
	botID := uuid.New()
	sessionID := uuid.New()
	name := "acp-state-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, userID, name); err != nil {
		t.Fatalf("create ACP state user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, $3)
	`, botID, userID, name); err != nil {
		t.Fatalf("create ACP state bot: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, runtime_type)
		VALUES ($1, $2, 'local', 'acp_agent')
	`, sessionID, botID); err != nil {
		t.Fatalf("create ACP state session: %v", err)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM bots WHERE id = $1", botID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM users WHERE id = $1", userID)
	})
	return botID.String(), sessionID.String()
}

func createACPStateRun(
	t *testing.T,
	ctx context.Context,
	queries *dbsqlc.Queries,
	pool *pgxpool.Pool,
	botID, sessionID string,
) (runID string, turnID string, fence runtimefence.Fence) {
	t.Helper()
	token, err := queries.NextSessionRuntimeFenceToken(ctx)
	if err != nil {
		t.Fatalf("allocate ACP state fence: %v", err)
	}
	runID = uuid.NewString()
	turnID = uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_runs (
			run_id, bot_id, session_id, invocation_id, turn_id, turn_position,
			state, input_json, input_fingerprint, owner_id, fencing_token,
			owner_since, live_generation
		) VALUES ($1, $2, $3, $4, $5, 1, 'running', '{}'::jsonb, $6, $7, $8, now(), $9)
	`, runID, botID, sessionID, uuid.NewString(), turnID, "acp-state-input", "owner", token, "generation"); err != nil {
		t.Fatalf("create ACP state run: %v", err)
	}
	fence = runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: token}
	if err := runtimefence.Activate(ctx, postgresstore.NewQueriesWithPool(pool, queries), fence); err != nil {
		t.Fatalf("activate ACP state run: %v", err)
	}
	return runID, turnID, fence
}

func persistACPStateWatermark(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	botID, sessionID, runID, turnID string,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO bot_history_messages (
			bot_id, session_id, role, content, metadata, session_mode, runtime_type,
			turn_id, turn_position, turn_message_seq, turn_visible, run_id
		) VALUES (
			$1, $2, 'assistant', '{"role":"assistant","content":"ok"}'::jsonb,
			'{"acp_turn_outcome":"succeeded"}'::jsonb,
			'chat', 'acp_agent', $3, 1, 1, true, $4
		)
	`, botID, sessionID, turnID, runID); err != nil {
		t.Fatalf("persist ACP canonical round: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acp_session_publications (session_id, run_id, checkpoint_reset)
		VALUES ($1, $2, false)
		ON CONFLICT (team_id, session_id) DO UPDATE SET
			run_id = EXCLUDED.run_id,
			checkpoint_reset = EXCLUDED.checkpoint_reset,
			updated_at = now()
	`, sessionID, runID); err != nil {
		t.Fatalf("persist ACP canonical publication head: %v", err)
	}
}

// acpTestFileDigest computes the capture-side content digest with the spool
// framing: each compacted record followed by LF.
func acpTestFileDigest(t *testing.T, contents ...string) string {
	t.Helper()
	hasher := sha256.New()
	for _, content := range contents {
		var compacted bytes.Buffer
		if err := json.Compact(&compacted, []byte(content)); err != nil {
			t.Fatalf("compact test record %q: %v", content, err)
		}
		hasher.Write(compacted.Bytes())
		hasher.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func multiRecordACPState(
	t *testing.T,
	runID string,
	fencingToken int64,
	transcriptPath string,
	prefixRecords int64,
	contents ...string,
) (acp.PersistedSessionState, acp.SessionStateRecordReader) {
	t.Helper()
	file := acp.PersistedSessionStateFile{
		SessionStateFileShape: acp.SessionStateFileShape{
			Path:    transcriptPath,
			Records: int64(len(contents)),
			Digest:  acpTestFileDigest(t, contents...),
		},
	}
	if prefixRecords > 0 {
		file.PrefixRecords = prefixRecords
		file.PrefixDigest = acpTestFileDigest(t, contents[:prefixRecords]...)
	}
	state := acp.PersistedSessionState{
		AgentID: "codex", ACPSessionID: "native-" + runID, ThroughRunID: runID,
		Cwd: "/workspace", TranscriptPath: transcriptPath, RuntimeFencingToken: fencingToken,
		FileCount: 1, RecordCount: int64(len(contents)),
		Files: []acp.PersistedSessionStateFile{file},
	}
	index := 0
	return state, func(context.Context) (acp.SessionStateRecord, error) {
		if index == len(contents) {
			return acp.SessionStateRecord{}, io.EOF
		}
		index++
		return acp.SessionStateRecord{
			FilePath: transcriptPath, LineNumber: int64(index), Content: json.RawMessage(contents[index-1]),
		}, nil
	}
}

func singleRecordACPState(
	t *testing.T,
	runID string,
	fencingToken int64,
	content string,
) (acp.PersistedSessionState, acp.SessionStateRecordReader) {
	t.Helper()
	return multiRecordACPState(t, runID, fencingToken, "state/sessions/"+runID+".jsonl", 0, content)
}

func mustACPStateUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	parsed, err := dbpkg.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse ACP state UUID %q: %v", value, err)
	}
	return parsed
}
