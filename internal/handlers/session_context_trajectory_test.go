package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/context/trajectory"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type contextTrajectoryQueryStub struct {
	*contextLifecycleAccessStub
	events       []sqlc.ListContextTrajectoryEventsRow
	event        []byte
	contents     []sqlc.GetContextTrajectoryEventContentsRow
	detailReads  int
	listParams   []sqlc.ListContextTrajectoryEventsParams
	detailParams []sqlc.GetContextTrajectoryEventParams
}

func (s *contextTrajectoryQueryStub) ListContextTrajectoryEvents(_ context.Context, params sqlc.ListContextTrajectoryEventsParams) ([]sqlc.ListContextTrajectoryEventsRow, error) {
	s.listParams = append(s.listParams, params)
	return s.events, nil
}

func (s *contextTrajectoryQueryStub) GetContextTrajectoryEvent(_ context.Context, params sqlc.GetContextTrajectoryEventParams) ([]byte, error) {
	s.detailReads++
	s.detailParams = append(s.detailParams, params)
	return s.event, nil
}

func (s *contextTrajectoryQueryStub) GetContextTrajectoryEventContents(_ context.Context, params sqlc.GetContextTrajectoryEventContentsParams) ([]sqlc.GetContextTrajectoryEventContentsRow, error) {
	return s.contents, nil
}

func TestContextTrajectoryDetailsRequireWorkspaceRead(t *testing.T) {
	queries := &contextTrajectoryQueryStub{contextLifecycleAccessStub: newContextLifecycleAccessStub(t, "chat")}
	handler := newContextLifecycleGranteeHandler(queries)
	ctx := newContextLifecycleGranteeContext(t, "", false)
	ctx.SetParamNames("bot_id", "session_id", "event_id")
	ctx.SetParamValues(lifecycleTestBotID, lifecycleTestSessionID, "1")
	err := handler.GetSessionContextTrajectoryEvent(ctx)
	problem, ok := apperror.ProblemFrom(err, "test")
	if !ok || problem.Status != http.StatusForbidden || queries.detailReads != 0 {
		t.Fatalf("trajectory access = %v, reads = %d", err, queries.detailReads)
	}
}

func TestContextTrajectoryRestoresFullContentAndReportsMissingChunks(t *testing.T) {
	text := strings.Repeat("完整上下文🙂", 50000) + "FINAL_TAIL"
	data := []byte(text)
	middle := len(data) / 2
	first, second := trajectory.Hash(data[:middle]), trajectory.Hash(data[middle:])
	event := trajectory.Event{RunID: "run", CaptureID: "capture", Sequence: 1, Stage: "wire_request", Blocks: []trajectory.BlockRef{
		{Kind: "request_body", Label: "request", Hash: trajectory.Hash(data), Bytes: len(data), Chunks: []string{first, second}},
		{Kind: "context", Label: "missing", Hash: "absent", Chunks: []string{"absent"}},
	}}
	raw, _ := json.Marshal(event)
	queries := &contextTrajectoryQueryStub{
		contextLifecycleAccessStub: newContextLifecycleAccessStub(t, "chat", "workspace_read"), event: raw,
		contents: []sqlc.GetContextTrajectoryEventContentsRow{{ContentHash: second, Content: data[middle:]}, {ContentHash: first, Content: data[:middle]}},
	}
	ctx := newContextLifecycleGranteeContext(t, "", false)
	ctx.SetParamNames("bot_id", "session_id", "event_id")
	ctx.SetParamValues(lifecycleTestBotID, lifecycleTestSessionID, "1")
	if err := newContextLifecycleGranteeHandler(queries).GetSessionContextTrajectoryEvent(ctx); err != nil {
		t.Fatal(err)
	}
	var got ContextTrajectoryEventResponse
	if err := json.Unmarshal(ctx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 2 || !got.Blocks[0].Available || got.Blocks[0].Content != text || got.Blocks[1].Available || got.Complete {
		t.Fatal("detail truncated full content or concealed a missing chunk")
	}
	if len(queries.detailParams) != 1 || queries.detailParams[0].BotID != testUUID(lifecycleTestBotID) || queries.detailParams[0].SessionID != testUUID(lifecycleTestSessionID) {
		t.Fatal("detail query did not use authorized bot and session scope")
	}
}

func TestContextTrajectoryListDoesNotReadBodies(t *testing.T) {
	queries := &contextTrajectoryQueryStub{contextLifecycleAccessStub: newContextLifecycleAccessStub(t, "chat"), events: []sqlc.ListContextTrajectoryEventsRow{
		{ID: 7, RunID: testUUID("66666666-6666-6666-6666-666666666666"), Sequence: 3, Summary: []byte(`{"stage":"wire_request","block_count":3}`)},
	}}
	ctx := newContextLifecycleGranteeContext(t, "", false)
	if err := newContextLifecycleGranteeHandler(queries).GetSessionContextTrajectory(ctx); err != nil {
		t.Fatal(err)
	}
	var got ContextTrajectoryResponse
	if err := json.Unmarshal(ctx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || got.Events[0].ID != "7" || got.Events[0].BlockCount != 3 || queries.detailReads != 0 {
		t.Fatalf("summary page = %#v", got)
	}
}

func TestContextTrajectoryListUsesBoundedCursorPage(t *testing.T) {
	queries := &contextTrajectoryQueryStub{contextLifecycleAccessStub: newContextLifecycleAccessStub(t, "chat"), events: []sqlc.ListContextTrajectoryEventsRow{
		{ID: 7, Summary: []byte(`{"stage":"wire_request"}`)}, {ID: 6, Summary: []byte(`{"stage":"context_selected"}`)},
	}}
	ctx := newContextLifecycleGranteeContext(t, "?before=9&limit=1", false)
	if err := newContextLifecycleGranteeHandler(queries).GetSessionContextTrajectory(ctx); err != nil {
		t.Fatal(err)
	}
	var got ContextTrajectoryResponse
	if err := json.Unmarshal(ctx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || !got.HasMore || got.NextCursor != "7" || len(queries.listParams) != 1 || queries.listParams[0].BeforeID != 9 || queries.listParams[0].RowLimit != 2 {
		t.Fatalf("cursor page = %#v, query = %#v", got, queries.listParams)
	}
}
