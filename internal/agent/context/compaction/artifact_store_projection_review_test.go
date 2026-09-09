package compaction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestLoadActiveByIDUsesReverseEdgesToRejectOneSidedParent(t *testing.T) {
	t.Parallel()

	a1 := projectionRow(t, "00000000-0000-0000-0000-00000000cb01")
	a2 := projectionRow(t, "00000000-0000-0000-0000-00000000cb02")
	b := projectionRow(t, "00000000-0000-0000-0000-00000000cb03")
	a1.SupersededBy, a1.SupersededAt = b.ID, validTimestamp(1)
	a2.SupersededBy, a2.SupersededAt = b.ID, validTimestamp(2)
	a1.Coverage = testCoverageJSON(t, "row-1")
	a2.Coverage = testCoverageJSON(t, "row-2")
	b.ParentIds = []pgtype.UUID{a1.ID}
	b.Coverage = testCoverageJSON(t, "row-1", "row-2")
	queries := &projectionQueries{rows: map[pgtype.UUID]sqlc.BotHistoryMessageCompact{
		a1.ID: a1,
		a2.ID: a2,
		b.ID:  b,
	}}

	_, err := NewArtifactProjection(queries).LoadActiveByID(context.Background(), a1.ID.String(), ArtifactOwner{})
	assertLineageKind(t, err, LineageIssueParentMismatch)
}

func TestLoadActiveByIDRecursivelyRejectsCorruptSiblingAncestry(t *testing.T) {
	t.Parallel()

	a1 := projectionRow(t, "00000000-0000-0000-0000-00000000cc01")
	a2 := projectionRow(t, "00000000-0000-0000-0000-00000000cc02")
	b := projectionRow(t, "00000000-0000-0000-0000-00000000cc03")
	missingParent := mustProjectionUUID(t, "00000000-0000-0000-0000-00000000cc04")
	a1.SupersededBy, a1.SupersededAt = b.ID, validTimestamp(1)
	a2.SupersededBy, a2.SupersededAt = b.ID, validTimestamp(2)
	a1.Coverage = testCoverageJSON(t, "row-1")
	a2.ParentIds = []pgtype.UUID{missingParent}
	a2.Coverage = testCoverageJSON(t, "row-2")
	b.ParentIds = []pgtype.UUID{a1.ID, a2.ID}
	b.Coverage = testCoverageJSON(t, "row-1", "row-2")
	queries := &projectionQueries{rows: map[pgtype.UUID]sqlc.BotHistoryMessageCompact{
		a1.ID: a1,
		a2.ID: a2,
		b.ID:  b,
	}}

	_, err := NewArtifactProjection(queries).LoadActiveByID(context.Background(), a1.ID.String(), ArtifactOwner{})
	assertLineageKind(t, err, LineageIssueParentMismatch)
}

func TestLoadActiveByIDPropagatesStorageErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("database unavailable")
	leaf := projectionRow(t, "00000000-0000-0000-0000-00000000cd01")
	_, err := NewArtifactProjection(&projectionQueries{
		rows:       map[pgtype.UUID]sqlc.BotHistoryMessageCompact{leaf.ID: leaf},
		parentsErr: sentinel,
	}).LoadActiveByID(context.Background(), leaf.ID.String(), ArtifactOwner{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("reverse-edge storage error = %v, want %v", err, sentinel)
	}

	parent := projectionRow(t, "00000000-0000-0000-0000-00000000cd02")
	active := projectionRow(t, "00000000-0000-0000-0000-00000000cd03")
	parent.SupersededBy, parent.SupersededAt = active.ID, validTimestamp(1)
	parent.Coverage = testCoverageJSON(t, "row-parent")
	active.ParentIds = []pgtype.UUID{parent.ID}
	active.Coverage = testCoverageJSON(t, "row-parent")
	_, err = NewArtifactProjection(&projectionQueries{
		rows: map[pgtype.UUID]sqlc.BotHistoryMessageCompact{
			parent.ID: parent,
			active.ID: active,
		},
		getErrors: map[pgtype.UUID]error{parent.ID: sentinel},
	}).LoadActiveByID(context.Background(), active.ID.String(), ArtifactOwner{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("parent storage error = %v, want %v", err, sentinel)
	}
}

func TestLoadActiveByIDRejectsOwnerAndCrossSessionEdges(t *testing.T) {
	t.Parallel()

	bot1 := "00000000-0000-0000-0000-00000000be01"
	bot2 := "00000000-0000-0000-0000-00000000be02"
	session1 := "00000000-0000-0000-0000-00000000ce01"
	session2 := "00000000-0000-0000-0000-00000000ce02"
	foreign := projectionRow(t, "00000000-0000-0000-0000-00000000ce03")
	foreign.BotID = mustProjectionUUID(t, bot2)
	foreign.SessionID = mustProjectionUUID(t, session2)
	queries := &projectionQueries{rows: map[pgtype.UUID]sqlc.BotHistoryMessageCompact{foreign.ID: foreign}}
	_, err := NewArtifactProjection(queries).LoadActiveByID(context.Background(), foreign.ID.String(), ArtifactOwner{BotID: bot1, SessionID: session1})
	assertLineageKind(t, err, LineageIssueScopeMismatch)

	parent := projectionRow(t, "00000000-0000-0000-0000-00000000ce04")
	active := projectionRow(t, "00000000-0000-0000-0000-00000000ce05")
	parent.BotID, active.BotID = mustProjectionUUID(t, bot1), mustProjectionUUID(t, bot1)
	active.SessionID = mustProjectionUUID(t, session2)
	parent.SupersededBy, parent.SupersededAt = active.ID, validTimestamp(1)
	parent.Coverage = testCoverageJSON(t, "row-cross-session")
	active.ParentIds = []pgtype.UUID{parent.ID}
	active.Coverage = testCoverageJSON(t, "row-cross-session")
	queries = &projectionQueries{rows: map[pgtype.UUID]sqlc.BotHistoryMessageCompact{
		parent.ID: parent,
		active.ID: active,
	}}
	_, err = NewArtifactProjection(queries).LoadActiveByID(context.Background(), parent.ID.String(), ArtifactOwner{BotID: bot1})
	assertLineageKind(t, err, LineageIssueScopeMismatch)
}

func assertLineageKind(t *testing.T, err error, kind LineageIssueKind) {
	t.Helper()
	var lineageErr *LineageError
	if !errors.As(err, &lineageErr) || lineageErr.Issue.Kind != kind {
		t.Fatalf("error = %v, want lineage issue %q", err, kind)
	}
}

func validTimestamp(seconds int64) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Unix(seconds, 0), Valid: true}
}

func mustProjectionUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", value, err)
	}
	return id
}
