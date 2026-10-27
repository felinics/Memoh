package compaction

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestCompactRejectsUnrenderedPipelineVersion(t *testing.T) {
	for _, stream := range []string{"turn response", "rendered input"} {
		t.Run(stream, func(t *testing.T) {
			row := mkRow(t, "assistant", `{"role":"assistant","content":"`+strings.Repeat("visible text ", 200)+`"}`, 100)
			trs := timeline.DecodeTurnResponseEntries([]messagepkg.Message{{ID: formatUUID(row.ID), Role: "assistant", Content: row.Content, CreatedAt: time.UnixMilli(100)}})
			trs[0].ContentHash = historyfrag.MessageContentHash(historyfrag.DecodeStoredModelMessage(nil, formatUUID(row.ID), "assistant", row.Content))
			rc := timeline.RenderedContext{{MessageID: "current-input", ReceivedAtMs: 200, Content: []timeline.RenderedContentPiece{{Type: "text", Text: "current input"}}}}
			if stream == "rendered input" {
				row.ExternalMessageID = pgtype.Text{String: "old-input", Valid: true}
				trs = nil
				rc = append(timeline.RenderedContext{{MessageID: "old-input", ReceivedAtMs: 100, LastEventCursor: 20, Content: []timeline.RenderedContentPiece{{Type: "text", Text: "edited input"}}}}, rc...)
			} else {
				row.Content = []byte(`{"role":"assistant","content":"` + strings.Repeat("unrendered edit ", 200) + `"}`)
			}
			stub := &stubModel{summary: "SUMMARY"}
			cfg := machineryConfig(stub, 450)
			for _, message := range timeline.ComposeContext(rc, trs).Messages {
				cfg.View = append(cfg.View, *message.Source)
			}
			q := &fakeQueries{uncompacted: []sqlc.ListUncompactedMessagesBySessionRow{row}}
			passes, err := newMachineryService(q).Compact(t.Context(), cfg)
			if ReasonOf(err) != ReasonStaleSource || len(passes) != 0 || stub.calls != 0 || q.completed.Status == "ok" {
				t.Fatalf("unseen source version committed: passes=%d calls=%d status=%s err=%v", len(passes), stub.calls, q.completed.Status, err)
			}
		})
	}
}

func TestCompactRejectsSourceEditedAfterRendering(t *testing.T) {
	rows := machineryCorpus(t)
	items, _ := candidatesFromRows(rows)
	stub := &stubModel{summary: "SUMMARY"}
	cfg := machineryConfig(stub, 450)
	for _, item := range items {
		cfg.View = append(cfg.View, turn.ContextMessageSource{Kind: "history", ID: formatUUID(item.ID), ContentHash: historyfrag.MessageContentHash(item.Record.ModelMessage)})
	}
	rows[0].Content = []byte(`{"role":"user","content":[{"type":"text","text":"edited after request assembly"}]}`)
	q := &fakeQueries{uncompacted: rows}
	passes, err := newMachineryService(q).Compact(t.Context(), cfg)
	if ReasonOf(err) != ReasonStaleSource || stub.calls != 0 || len(passes) != 0 {
		t.Fatalf("passes=%d calls=%d err=%v, want stale_source without summarizing a different version", len(passes), stub.calls, err)
	}
}

// statusSequence answers each summarizer call with the next status, and with
// the stub once the sequence is used up.
type statusSequence struct {
	stub     *stubModel
	statuses []int
	calls    int
}

func (s *statusSequence) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	if s.calls <= len(s.statuses) {
		return &http.Response{
			StatusCode: s.statuses[s.calls-1],
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"summarizer down"}}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}
	return s.stub.RoundTrip(req)
}

// A summarizer call the provider rejects is not made again; one it failed
// to serve is made once more, and the event fails when that fails too.
func TestCompactRetriesTheSummarizerOnceAndOnlyWhenTransient(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		statuses []int
		calls    int
		folded   bool
	}{
		{"rejected", []int{http.StatusBadRequest}, 1, false},
		{"down twice", []int{http.StatusInternalServerError, http.StatusInternalServerError}, 2, false},
		{"down once", []int{http.StatusInternalServerError}, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := &fakeQueries{uncompacted: machineryCorpus(t)}
			transport := &statusSequence{stub: &stubModel{summary: "SUMMARY"}, statuses: tc.statuses}
			cfg := machineryConfig(transport.stub, 450)
			cfg.HTTPClient = &http.Client{Transport: transport}

			passes, err := newMachineryService(q).Compact(context.Background(), cfg)
			if transport.calls != tc.calls || (len(passes) == 1) != tc.folded {
				t.Fatalf("calls=%d passes=%d err=%v, want %d calls folded=%v", transport.calls, len(passes), err, tc.calls, tc.folded)
			}
			if !tc.folded && ReasonOf(err) != ReasonSummarizerFailed {
				t.Fatalf("err=%v, want %s", err, ReasonSummarizerFailed)
			}
		})
	}
}

// When what the request keeps leaves less room than a useful summary, the
// event fails before it claims a row or calls the summarizer.
func TestCompactFailsWithoutRoomForAUsefulSummary(t *testing.T) {
	t.Parallel()

	rows := machineryCorpus(t)
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "SUMMARY"}
	cfg := machineryConfig(stub, 0)
	cfg.ProtectedSources = historySources(rows[8])
	cfg.ProtectedSources[0].Current = false
	items, _ := candidatesFromRows(rows)
	protectCurrentSources(items, cfg.ProtectedSources)
	cfg.HistoryBudgetTokens = keptReplayTokens(items, cfg.ProtectedSources) + minSummaryTokens/2

	passes, err := newMachineryService(q).Compact(context.Background(), cfg)
	if len(passes) != 0 || ReasonOf(err) != ReasonProtectedOverflow || stub.calls != 0 || q.created {
		t.Fatalf("passes=%d err=%v summarizer calls=%d claimed=%v, want %s before any claim", len(passes), err, stub.calls, q.created, ReasonProtectedOverflow)
	}
}

// A summary the summarizer writes up to the bound the prompt states fits the
// room it was bounded by once the request carries it, wrapper and message
// envelope included.
func TestSummaryAtItsStatedLimitFitsItsRoom(t *testing.T) {
	t.Parallel()

	for room := minSummaryTokens; room <= 6200; room += 7 {
		limit := summaryOutputLimit(room, 1<<20)
		if replay := summaryReplayTokens(strings.Repeat("x", limit*contextfrag.EstimateBytesPerToken)); replay > room {
			t.Fatalf("room %d: a summary of the stated %d tokens replays as %d", room, limit, replay)
		}
	}
}

// statedLimit answers with a summary exactly as long as the bound the prompt
// states.
type statedLimit struct{ stub *stubModel }

var statedLimitPattern = regexp.MustCompile(`must stay under (\d+) tokens`)

func (s statedLimit) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	limit := 0
	if stated := statedLimitPattern.FindSubmatch(body); stated != nil {
		limit, _ = strconv.Atoi(string(stated[1]))
	}
	s.stub.summary = strings.Repeat("x", limit*contextfrag.EstimateBytesPerToken)
	req.Body = io.NopCloser(bytes.NewReader(body))
	return s.stub.RoundTrip(req)
}

// A pass whose summary fills its stated bound commits: the bound already
// prices the summary as the request renders it.
func TestCompactCommitsASummaryAtItsStatedLimit(t *testing.T) {
	t.Parallel()

	for _, budget := range []int{300, 450, 900} {
		q := &fakeQueries{uncompacted: machineryCorpus(t)}
		stub := &stubModel{}
		cfg := machineryConfig(stub, budget)
		cfg.HTTPClient = &http.Client{Transport: statedLimit{stub: stub}}

		passes, err := newMachineryService(q).Compact(context.Background(), cfg)
		if err != nil || len(passes) != 1 {
			t.Fatalf("budget %d: passes=%d err=%v, want the summary at its limit committed", budget, len(passes), err)
		}
	}
}

// Every way a pass fails names the reason the event reports.
func TestCompactNamesWhyAPassFailed(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		reason Reason
		stub   stubModel
		marked int64
	}{
		{ReasonEmpty, stubModel{summary: "  "}, -1},
		{ReasonTruncated, stubModel{summary: "SUMMARY", finishReason: "length"}, -1},
		{ReasonIneffective, stubModel{summary: strings.Repeat("long summary ", 400)}, -1},
		{ReasonStaleSource, stubModel{summary: "SUMMARY"}, 1},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			t.Parallel()
			q := &fakeQueries{uncompacted: machineryCorpus(t)}
			if tc.marked >= 0 {
				q.markedRowCount = &tc.marked
			}
			passes, err := newMachineryService(q).Compact(context.Background(), machineryConfig(&tc.stub, 100000))
			if len(passes) != 0 || ReasonOf(err) != tc.reason {
				t.Fatalf("passes=%d err=%v, want %s", len(passes), err, tc.reason)
			}
		})
	}
}

type cancelOnSummary struct {
	stub   *stubModel
	cancel context.CancelFunc
}

func (r cancelOnSummary) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := r.stub.RoundTrip(req)
	r.cancel()
	return response, err
}

func TestCompactCancellationBeforeCommitPublishesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := &stubModel{summary: "short-summary"}
	cfg := machineryConfig(stub, 450)
	cfg.HTTPClient = &http.Client{Transport: cancelOnSummary{stub: stub, cancel: cancel}}
	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	passes, err := newMachineryService(q).Compact(ctx, cfg)
	if !errors.Is(err, context.Canceled) || len(passes) > 0 || q.completed.Status == "ok" || len(q.rollupCalls) > 0 {
		t.Fatalf("canceled pass published a summary: ctx=%v err=%v passes=%d completed=%q rollups=%d", ctx.Err(), err, len(passes), q.completed.Status, len(q.rollupCalls))
	}
}
