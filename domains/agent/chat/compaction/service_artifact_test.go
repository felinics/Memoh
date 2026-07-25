package compaction

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/memohai/memoh/domains/agent/chat/context/history"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
)

type artifactQueries struct {
	*fakeQueries
	assets      []AssetRecord
	assetsErr   error
	completeErr error
	assetCalls  int
}

func (q *artifactQueries) ListAssets(_ context.Context, _ []string) ([]AssetRecord, error) {
	q.assetCalls++
	q.queryCalls = append(q.queryCalls, "assets")
	return q.assets, q.assetsErr
}

func (q *artifactQueries) CompleteLog(_ context.Context, arg CompleteLogInput) error {
	q.completed = arg
	if q.completeErr != nil {
		return q.completeErr
	}
	return nil
}

func TestDoCompactionPersistsDurableCoverageAndAnchor(t *testing.T) {
	t.Parallel()

	rows := []CandidateRecord{
		mkRow(t, "user", `"old question"`, 100),
		mkRow(t, "assistant", `"old answer"`, 100),
		mkRow(t, "user", `"current question"`, 100),
		mkRow(t, "assistant", `"current answer"`, 100),
	}
	for i := range rows {
		rows[i].CreatedAt = time.UnixMilli(int64(i+1) * 1000)
	}
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "SUMMARY"}

	if _, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 200)); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}

	coverage, err := DecodeArtifactCoverage(q.completed.Coverage)
	if err != nil {
		t.Fatalf("decode coverage: %v", err)
	}
	if len(coverage) != 2 || coverage[0].Ref.ID != rows[0].ID || coverage[1].Ref.ID != rows[1].ID {
		t.Fatalf("coverage = %#v, want durable refs for the two compacted rows", coverage)
	}
	if coverage[0].Ref.ContentHash == "" || coverage[1].Ref.ContentHash == "" {
		t.Fatalf("coverage must preserve source hashes: %#v", coverage)
	}
	if q.completed.AnchorStartMs != 1000 || q.completed.AnchorEndMs != 2000 {
		t.Fatalf("anchor = %d..%d, want 1000..2000", q.completed.AnchorStartMs, q.completed.AnchorEndMs)
	}
}

func TestDoCompactionOrdersDurableCoverageBySourceTime(t *testing.T) {
	t.Parallel()

	rows := []CandidateRecord{
		mkRow(t, "user", `"old question"`, 100),
		mkRow(t, "assistant", `"old answer"`, 100),
		mkRow(t, "user", `"current question"`, 100),
		mkRow(t, "assistant", `"current answer"`, 100),
	}
	rows[0].CreatedAt = time.UnixMilli(2000)
	rows[1].CreatedAt = time.UnixMilli(1000)
	rows[2].CreatedAt = time.UnixMilli(3000)
	rows[3].CreatedAt = time.UnixMilli(4000)
	q := &fakeQueries{uncompacted: rows}

	if _, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(&stubModel{summary: "SUMMARY"}, 200)); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}

	coverage, err := DecodeArtifactCoverage(q.completed.Coverage)
	if err != nil {
		t.Fatalf("decode coverage: %v", err)
	}
	if len(coverage) != 2 || coverage[0].Ref.ID != rows[1].ID || coverage[1].Ref.ID != rows[0].ID {
		t.Fatalf("coverage = %#v, want source-time order", coverage)
	}
	if q.completed.AnchorStartMs != 1000 || q.completed.AnchorEndMs != 2000 {
		t.Fatalf("anchor = %d..%d, want 1000..2000", q.completed.AnchorStartMs, q.completed.AnchorEndMs)
	}
}

func TestDoCompactionCoverageHashIncludesMessageAssets(t *testing.T) {
	t.Parallel()

	rows := []CandidateRecord{
		mkRow(t, "user", `"old question"`, 100),
		mkRow(t, "assistant", `"old answer"`, 100),
		mkRow(t, "user", `"current question"`, 100),
		mkRow(t, "assistant", `"current answer"`, 100),
	}
	rows[0].CompactID = rows[2].ID
	rows[1].CompactID = rows[3].ID
	asset := AssetRecord{
		MessageID:   rows[0].ID,
		Role:        "attachment",
		Ordinal:     0,
		ContentHash: "sha256:asset-1",
		Name:        "diagram.png",
		Metadata:    []byte(`{"alt":"architecture"}`),
	}
	q := &artifactQueries{
		fakeQueries: &fakeQueries{uncompacted: rows},
		assets:      []AssetRecord{asset},
	}
	stub := &stubModel{summary: "SUMMARY"}

	if _, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 200)); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}

	coverage, err := DecodeArtifactCoverage(q.completed.Coverage)
	if err != nil {
		t.Fatalf("decode coverage: %v", err)
	}
	if len(coverage) != 2 {
		t.Fatalf("coverage = %#v, want two covered rows", coverage)
	}
	expectedMessage := rowToMessage(rows[0])
	expectedMessage.Assets = []messagepkg.MessageAsset{{
		ContentHash: asset.ContentHash,
		Role:        asset.Role,
		Ordinal:     asset.Ordinal,
		Name:        asset.Name,
		Metadata:    map[string]any{"alt": "architecture"},
	}}
	wantHash := history.DBMessageSourceHash(expectedMessage).Value
	if got := coverage[0].Ref.ContentHash; got != wantHash {
		t.Fatalf("coverage hash = %q, want attachment-aware source hash %q", got, wantHash)
	}
	plainHash := history.DBMessageSourceHash(rowToMessage(rows[0])).Value
	if coverage[0].Ref.ContentHash == plainHash {
		t.Fatal("coverage hash ignored the attached asset")
	}
	if q.assetCalls != 1 {
		t.Fatalf("asset batch calls = %d, want 1", q.assetCalls)
	}
	wantMessageIDs := []string{rows[0].ID, rows[1].ID}
	if !slices.Equal(q.markArg.MessageIDs, wantMessageIDs) {
		t.Fatalf("claimed message IDs = %v, want %v", q.markArg.MessageIDs, wantMessageIDs)
	}
	wantClaims := []string{rows[0].CompactID, rows[1].CompactID}
	if !slices.Equal(q.markArg.ExpectedCompactIDs, wantClaims) {
		t.Fatalf("expected compact IDs = %v, want %v", q.markArg.ExpectedCompactIDs, wantClaims)
	}
	if !slices.Equal(q.queryCalls, []string{"mark", "assets"}) {
		t.Fatalf("claim protocol calls = %v, want mark before assets", q.queryCalls)
	}
}

func TestDoCompactionStopsWhenAssetsCannotBeLoaded(t *testing.T) {
	t.Parallel()

	rows := []CandidateRecord{
		mkRow(t, "user", `"old question"`, 100),
		mkRow(t, "assistant", `"old answer"`, 100),
		mkRow(t, "user", `"current question"`, 100),
		mkRow(t, "assistant", `"current answer"`, 100),
	}
	assetErr := errors.New("asset read failed")
	q := &artifactQueries{
		fakeQueries: &fakeQueries{uncompacted: rows},
		assetsErr:   assetErr,
	}

	_, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(&stubModel{summary: "SUMMARY"}, 200))
	if !errors.Is(err, assetErr) {
		t.Fatalf("RunCompactionSync error = %v, want %v", err, assetErr)
	}
	if !q.created || len(q.markedIDs) == 0 || q.completed.Status != "error" {
		t.Fatalf("asset failure must leave reclaimable error claims: created=%v marked=%v status=%q", q.created, q.markedIDs, q.completed.Status)
	}
}

func TestDoCompactionReturnsSuccessfulArtifactFinalizationError(t *testing.T) {
	t.Parallel()

	rows := []CandidateRecord{
		mkRow(t, "user", `"old question"`, 100),
		mkRow(t, "assistant", `"old answer"`, 100),
		mkRow(t, "user", `"current question"`, 100),
		mkRow(t, "assistant", `"current answer"`, 100),
	}
	completeErr := errors.New("artifact finalization failed")
	q := &artifactQueries{
		fakeQueries: &fakeQueries{uncompacted: rows},
		completeErr: completeErr,
	}

	_, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(&stubModel{summary: "SUMMARY"}, 200))
	if !errors.Is(err, completeErr) {
		t.Fatalf("RunCompactionSync error = %v, want %v", err, completeErr)
	}
	if len(q.markedIDs) != 2 {
		t.Fatalf("marked ids = %v, want generated artifact rows marked before finalization", q.markedIDs)
	}
}

func TestDoCompactionRejectsPartialSourceMarking(t *testing.T) {
	t.Parallel()

	rows := []CandidateRecord{
		mkRow(t, "user", `"old question"`, 100),
		mkRow(t, "assistant", `"old answer"`, 100),
		mkRow(t, "user", `"current question"`, 100),
		mkRow(t, "assistant", `"current answer"`, 100),
	}
	markedRows := int64(1)
	q := &fakeQueries{uncompacted: rows, markedRowCount: &markedRows}

	_, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(&stubModel{summary: "SUMMARY"}, 200))
	if err == nil || !strings.Contains(err.Error(), "marked 1 of 2") {
		t.Fatalf("RunCompactionSync error = %v, want partial-mark failure", err)
	}
	if q.completed.Status != "error" || q.completed.Summary != "" {
		t.Fatalf("partial mark published an artifact: %#v", q.completed)
	}
}
