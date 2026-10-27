package application

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type pairingQueries struct {
	dbstore.Queries
	uncompacted []sqlc.ListUncompactedMessagesBySessionRow
	logID       pgtype.UUID
	markedIDs   []pgtype.UUID
}

func (f *pairingQueries) ListUncompactedMessagesBySession(context.Context, pgtype.UUID) ([]sqlc.ListUncompactedMessagesBySessionRow, error) {
	return f.uncompacted, nil
}

func (f *pairingQueries) MeasureUncompactedMessagesBySession(context.Context, sqlc.MeasureUncompactedMessagesBySessionParams) (sqlc.MeasureUncompactedMessagesBySessionRow, error) {
	return sqlc.MeasureUncompactedMessagesBySessionRow{CandidateCount: int64(len(f.uncompacted)), CandidateBytes: 1}, nil
}

func (f *pairingQueries) ListUncompactedMessagesBySessionWithinBytes(context.Context, sqlc.ListUncompactedMessagesBySessionWithinBytesParams) ([]sqlc.ListUncompactedMessagesBySessionWithinBytesRow, error) {
	return compactionRowsForPairing(f.uncompacted), nil
}

func compactionRowsForPairing(rows []sqlc.ListUncompactedMessagesBySessionRow) []sqlc.ListUncompactedMessagesBySessionWithinBytesRow {
	converted := make([]sqlc.ListUncompactedMessagesBySessionWithinBytesRow, len(rows))
	for i, row := range rows {
		converted[i] = sqlc.ListUncompactedMessagesBySessionWithinBytesRow{
			ID: row.ID, BotID: row.BotID, SessionID: row.SessionID,
			SenderChannelIdentityID: row.SenderChannelIdentityID, SenderUserID: row.SenderUserID,
			ExternalMessageID: row.ExternalMessageID, SourceReplyToMessageID: row.SourceReplyToMessageID,
			Role: row.Role, Content: row.Content, Metadata: row.Metadata, Usage: row.Usage,
			EventID: row.EventID, DisplayText: row.DisplayText, CompactID: row.CompactID, CreatedAt: row.CreatedAt,
			SenderDisplayName: row.SenderDisplayName, SenderAvatarUrl: row.SenderAvatarUrl,
			Platform: row.Platform, CompactionEpoch: row.CompactionEpoch,
			ConversationType: row.ConversationType, ConversationName: row.ConversationName, ReplyTarget: row.ReplyTarget,
			CandidateCount: int64(len(rows)), CandidateBytes: 1, CumulativeBytes: 1,
		}
	}
	return converted
}

func (*pairingQueries) ListCompactionLogsBySession(context.Context, pgtype.UUID) ([]sqlc.BotHistoryMessageCompact, error) {
	return nil, nil
}

func (*pairingQueries) ListCompactionArtifactLineageBySession(context.Context, pgtype.UUID) ([]sqlc.BotHistoryMessageCompact, error) {
	return nil, nil
}

func (*pairingQueries) ListMessageAssetsBatch(context.Context, []pgtype.UUID) ([]sqlc.ListMessageAssetsBatchRow, error) {
	return nil, nil
}

func (f *pairingQueries) CreateCompactionLog(context.Context, sqlc.CreateCompactionLogParams) (sqlc.BotHistoryMessageCompact, error) {
	return sqlc.BotHistoryMessageCompact{ID: f.logID}, nil
}

func (f *pairingQueries) ListMessagesByCompactIDWithinBytes(ctx context.Context, arg sqlc.ListMessagesByCompactIDWithinBytesParams) ([]sqlc.ListMessagesByCompactIDWithinBytesRow, error) {
	rows, err := f.ListMessagesByCompactID(ctx, arg.CompactID)
	bounded := make([]sqlc.ListMessagesByCompactIDWithinBytesRow, len(rows))
	for i, row := range rows {
		bounded[i] = sqlc.ListMessagesByCompactIDWithinBytesRow(row)
	}
	return bounded, err
}

func (f *pairingQueries) ListMessagesByCompactID(_ context.Context, compactID pgtype.UUID) ([]sqlc.ListMessagesByCompactIDRow, error) {
	var rows []sqlc.ListMessagesByCompactIDRow
	for _, row := range f.uncompacted {
		if compactID == f.logID && slices.Contains(f.markedIDs, row.ID) {
			row.CompactID = compactID
			rows = append(rows, sqlc.ListMessagesByCompactIDRow(row))
		}
	}
	return rows, nil
}

func (f *pairingQueries) MarkMessagesCompacted(_ context.Context, arg sqlc.MarkMessagesCompactedParams) (int64, error) {
	f.markedIDs = append([]pgtype.UUID(nil), arg.MessageIds...)
	return int64(len(arg.MessageIds)), nil
}

func (*pairingQueries) CompleteCompactionLog(_ context.Context, arg sqlc.CompleteCompactionLogParams) (sqlc.BotHistoryMessageCompact, error) {
	return sqlc.BotHistoryMessageCompact{ID: arg.ID, Status: arg.Status, Summary: arg.Summary}, nil
}

type pairingSummarizer struct{ summary string }

func (s pairingSummarizer) RoundTrip(*http.Request) (*http.Response, error) {
	body := `{"id":"stub","object":"chat.completion","created":0,"model":"stub",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"` + s.summary + `"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func pairingRow(t *testing.T, role, content string) sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	return sqlc.ListUncompactedMessagesBySessionRow{
		ID:      pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Role:    role,
		Content: []byte(content),
		Usage:   []byte(`{"outputTokens":100}`),
	}
}

// TestSelectorToReadPathFoldsAClosedAskUserCall drives the real compaction
// selector over a history whose ask_user call the conversation moved on from
// without an answer, and feeds its actual marked rows into
// replaceCompactedHistoryRecords. It pins the pair of invariants that live in
// two packages: the selector folds the closed call with the history around
// it as one contiguous run, and the read path substitutes the summary in
// place, ahead of the current input it never covers.
func TestSelectorToReadPathFoldsAClosedAskUserCall(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		pairingRow(t, "user", `"old q with plenty of extra descriptive words so the raw exchange clearly outweighs its summary"`),
		pairingRow(t, "assistant", `"old a walking through the answer in enough detail that compaction genuinely shrinks it"`),
		pairingRow(t, "assistant", `[{"type":"tool-call","toolCallId":"ask-1","toolName":"ask_user","input":{"questions":[]}}]`),
		pairingRow(t, "user", `"mid q"`),
		pairingRow(t, "user", `"current q"`),
	}
	q := &pairingQueries{
		uncompacted: rows,
		logID:       pgtype.UUID{Bytes: uuid.New(), Valid: true},
	}
	svc := compaction.NewService(slog.New(slog.DiscardHandler), q)

	passes, err := svc.Compact(context.Background(), compaction.TriggerConfig{
		BotID:               uuid.NewString(),
		SessionID:           uuid.NewString(),
		ModelID:             "stub-model",
		ClientType:          "openai-completions",
		APIKey:              "test",
		BaseURL:             "http://stub.invalid",
		HTTPClient:          &http.Client{Transport: pairingSummarizer{summary: "condensed old exchange"}},
		ProtectedSources:    []turn.ContextMessageSource{{Kind: "history", ID: uuid.UUID(rows[4].ID.Bytes).String(), Current: true}},
		SummaryWindowTokens: 128000,
		HistoryBudgetTokens: 1000,
	})
	if err != nil || len(passes) != 1 || passes[0].More {
		t.Fatalf("passes = %+v err = %v, want one pass that folds the history before the current input", passes, err)
	}

	marked := make(map[pgtype.UUID]bool, len(q.markedIDs))
	for _, id := range q.markedIDs {
		marked[id] = true
	}
	if len(marked) != 4 || !marked[rows[0].ID] || !marked[rows[1].ID] || !marked[rows[2].ID] || !marked[rows[3].ID] {
		t.Fatalf("marked = %v, want the contiguous run [old q, old a, ask_user call, mid q]", q.markedIDs)
	}

	compactID := uuid.UUID(q.logID.Bytes).String()
	texts := []string{`old q`, `old a`, `ask you something`, `mid q`, `current q`}
	roles := []string{"user", "assistant", "assistant", "user", "user"}
	records := make([]historyfrag.HistoryRecord, 0, len(rows))
	for i, row := range rows {
		id := uuid.UUID(row.ID.Bytes).String()
		record := historyRecord(id, ModelMessage{
			Role:    roles[i],
			Content: newTextContent(texts[i]),
		}, nil)
		if marked[row.ID] {
			record.CompactID = compactID
		}
		records = append(records, record)
	}

	got := replaceCompactedHistoryRecords(records, map[string]string{compactID: passes[0].Summary}, contextfrag.Scope{})
	want := []ModelMessage{
		{Role: "user", Content: newTextContent("<summary>\n" + passes[0].Summary + "\n</summary>")},
		{Role: "user", Content: newTextContent("current q")},
	}
	if gotMessages := historyfrag.ToModelMessages(got); !reflect.DeepEqual(gotMessages, want) {
		t.Fatalf("selector output broke read-path ordering:\ngot  %#v\nwant %#v", gotMessages, want)
	}
}
