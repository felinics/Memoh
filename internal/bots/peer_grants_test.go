package bots

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/memohai/memoh/internal/db/postgres/store"
)

// peerGrantRow is one row of a ListBotPeerGrantsForCaller result.
type peerGrantRow struct {
	subjectType string
	permissions []byte
}

// fakePeerRows implements pgx.Rows over a fixed set of peer grant rows.
type fakePeerRows struct {
	rows []peerGrantRow
	idx  int
}

func (*fakePeerRows) Close()                                       {}
func (*fakePeerRows) Err() error                                   { return nil }
func (*fakePeerRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*fakePeerRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (*fakePeerRows) Values() ([]any, error)                       { return nil, nil }
func (*fakePeerRows) RawValues() [][]byte                          { return nil }
func (*fakePeerRows) Conn() *pgx.Conn                              { return nil }

func (r *fakePeerRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

// Scan fills the ListBotPeerGrantsForCaller column order:
// id, bot_id, subject_type, subject_bot_id, permissions.
func (r *fakePeerRows) Scan(dest ...any) error {
	if len(dest) < 5 {
		return errors.New("unexpected column count")
	}
	row := r.rows[r.idx-1]
	*dest[0].(*pgtype.UUID) = pgtype.UUID{}
	*dest[1].(*pgtype.UUID) = pgtype.UUID{}
	*dest[2].(*string) = row.subjectType
	*dest[3].(*pgtype.UUID) = pgtype.UUID{}
	*dest[4].(*[]byte) = row.permissions
	return nil
}

func peerService(t *testing.T, rows []peerGrantRow, queried *int) *Service {
	t.Helper()
	dbtx := &fakeDBTX{
		queryFunc: func(context.Context, string, ...any) (pgx.Rows, error) {
			if queried != nil {
				*queried++
			}
			return &fakePeerRows{rows: rows}, nil
		},
	}
	return NewService(nil, postgresstore.NewQueries(sqlc.New(dbtx)))
}

func TestPeerVocabularyExpandsDelegateTransitively(t *testing.T) {
	got, err := peerVocabulary.normalize([]string{PeerPermissionDelegate})
	if err != nil {
		t.Fatalf("normalize() error = %v", err)
	}
	want := []string{PeerPermissionDiscover, PeerPermissionContact, PeerPermissionDelegate}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalize() = %#v, want %#v", got, want)
	}
}

func TestPeerVocabularyRejectsUserScopes(t *testing.T) {
	// The whole point of a second vocabulary: a peer grant must not be able to
	// carry manage or any workspace scope, whatever the request says.
	for _, scope := range []string{
		PermissionManage,
		PermissionChat,
		PermissionWorkspaceRead,
		PermissionWorkspaceWrite,
		PermissionWorkspaceExec,
	} {
		if _, err := peerVocabulary.normalize([]string{scope}); !errors.Is(err, ErrInvalidPermission) {
			t.Fatalf("peer normalize(%q) error = %v, want ErrInvalidPermission", scope, err)
		}
	}
}

func TestUserVocabularyRejectsPeerScopes(t *testing.T) {
	for _, scope := range AllPeerPermissions() {
		if _, err := userVocabulary.normalize([]string{scope}); !errors.Is(err, ErrInvalidPermission) {
			t.Fatalf("user normalize(%q) error = %v, want ErrInvalidPermission", scope, err)
		}
	}
}

func TestVocabulariesAreDisjoint(t *testing.T) {
	// A scope appearing in both vocabularies would make the two grant tables
	// silently interchangeable at the encode/decode boundary.
	for _, scope := range AllPeerPermissions() {
		if userVocabulary.known(scope) {
			t.Fatalf("scope %q appears in both vocabularies", scope)
		}
	}
	for _, scope := range allPermissions() {
		if peerVocabulary.known(scope) {
			t.Fatalf("scope %q appears in both vocabularies", scope)
		}
	}
}

func TestHasPeerPermissionHasNoImplicitDefault(t *testing.T) {
	// HasPermission treats an empty scope as manage; the peer check must not
	// mirror that, because an unchecked call site would be authorized instead
	// of rejected.
	if HasPeerPermission(AllPeerPermissions(), "") {
		t.Fatal("HasPeerPermission(all, \"\") = true, want false")
	}
	if HasPeerPermission([]string{PeerPermissionContact}, PeerPermissionDelegate) {
		t.Fatal("contact must not satisfy delegate")
	}
	if !HasPeerPermission([]string{PeerPermissionDelegate}, PeerPermissionContact) {
		t.Fatal("delegate must satisfy contact")
	}
}

func TestDecodePermissionsDropsPeerScopes(t *testing.T) {
	// Cross-contamination guard: even if a peer scope reached the user grant
	// column, the user resolver must never surface it.
	got := decodePermissions([]byte(`["chat","contact","delegate"]`))
	want := []string{PermissionChat}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decodePermissions() = %#v, want %#v", got, want)
	}
}

func TestResolvePeerPermissionsSelfEdgeSkipsQuery(t *testing.T) {
	queried := 0
	svc := peerService(t, nil, &queried)
	const botID = "00000000-0000-0000-0000-000000000001"
	got, err := svc.ResolvePeerPermissions(context.Background(), botID, botID)
	if err != nil {
		t.Fatalf("ResolvePeerPermissions() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ResolvePeerPermissions() = %#v, want empty", got)
	}
	if queried != 0 {
		t.Fatalf("self edge issued %d queries, want 0", queried)
	}
}

func TestResolvePeerPermissionsUnionsAndExpands(t *testing.T) {
	svc := peerService(t, []peerGrantRow{
		{subjectType: PeerGrantSubjectAnyBot, permissions: []byte(`["discover"]`)},
		{subjectType: PeerGrantSubjectBot, permissions: []byte(`["delegate"]`)},
	}, nil)
	got, err := svc.ResolvePeerPermissions(
		context.Background(),
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000002",
	)
	if err != nil {
		t.Fatalf("ResolvePeerPermissions() error = %v", err)
	}
	want := []string{PeerPermissionDiscover, PeerPermissionContact, PeerPermissionDelegate}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolvePeerPermissions() = %#v, want %#v", got, want)
	}
}

// reachablePeerRow is one row of a ListBotPeerGrantsForSubject result.
type reachablePeerRow struct {
	botID       pgtype.UUID
	subjectType string
	permissions []byte
	botName     string
}

type fakeReachableRows struct {
	rows []reachablePeerRow
	idx  int
}

func (*fakeReachableRows) Close()                                       {}
func (*fakeReachableRows) Err() error                                   { return nil }
func (*fakeReachableRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*fakeReachableRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (*fakeReachableRows) Values() ([]any, error)                       { return nil, nil }
func (*fakeReachableRows) RawValues() [][]byte                          { return nil }
func (*fakeReachableRows) Conn() *pgx.Conn                              { return nil }

func (r *fakeReachableRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

// Scan fills the ListBotPeerGrantsForSubject column order:
// id, bot_id, subject_type, subject_bot_id, permissions,
// bot_name, bot_display_name, bot_avatar_url.
func (r *fakeReachableRows) Scan(dest ...any) error {
	if len(dest) < 8 {
		return errors.New("unexpected column count")
	}
	row := r.rows[r.idx-1]
	*dest[0].(*pgtype.UUID) = pgtype.UUID{}
	*dest[1].(*pgtype.UUID) = row.botID
	*dest[2].(*string) = row.subjectType
	*dest[3].(*pgtype.UUID) = pgtype.UUID{}
	*dest[4].(*[]byte) = row.permissions
	*dest[5].(*string) = row.botName
	*dest[6].(*pgtype.Text) = pgtype.Text{}
	*dest[7].(*pgtype.Text) = pgtype.Text{}
	return nil
}

func TestListReachablePeersMergesBlanketAndDirectedRows(t *testing.T) {
	calleeOne := mustParseUUID("00000000-0000-0000-0000-0000000000a1")
	calleeTwo := mustParseUUID("00000000-0000-0000-0000-0000000000a2")
	dbtx := &fakeDBTX{
		queryFunc: func(context.Context, string, ...any) (pgx.Rows, error) {
			return &fakeReachableRows{rows: []reachablePeerRow{
				{botID: calleeOne, subjectType: PeerGrantSubjectAnyBot, permissions: []byte(`["discover"]`), botName: "one"},
				{botID: calleeOne, subjectType: PeerGrantSubjectBot, permissions: []byte(`["contact"]`), botName: "one"},
				{botID: calleeTwo, subjectType: PeerGrantSubjectAnyBot, permissions: []byte(`["contact"]`), botName: "two"},
			}}, nil
		},
	}
	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(dbtx)))

	items, err := svc.ListReachablePeers(context.Background(), "00000000-0000-0000-0000-000000000002")
	if err != nil {
		t.Fatalf("ListReachablePeers() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("ListReachablePeers() returned %d peers, want 2", len(items))
	}
	// A callee matched by both a blanket row and a directed row collapses to one
	// entry whose scopes are the union, and the directed edge clears ViaAnyBot.
	if got, want := items[0].Permissions, []string{PeerPermissionDiscover, PeerPermissionContact}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged permissions = %#v, want %#v", got, want)
	}
	if items[0].ViaAnyBot {
		t.Fatal("a directed edge must clear ViaAnyBot")
	}
	if !items[1].ViaAnyBot {
		t.Fatal("a blanket-only edge must keep ViaAnyBot")
	}
	if items[1].BotName != "two" {
		t.Fatalf("second peer name = %q, want %q", items[1].BotName, "two")
	}
}

func TestResolvePeerPermissionsDefaultsToEmpty(t *testing.T) {
	// No grant means no access: there is no owner or admin shortcut on the peer
	// path, because a bot is not a seat.
	svc := peerService(t, nil, nil)
	got, err := svc.ResolvePeerPermissions(
		context.Background(),
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000002",
	)
	if err != nil {
		t.Fatalf("ResolvePeerPermissions() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ResolvePeerPermissions() = %#v, want empty", got)
	}
}
