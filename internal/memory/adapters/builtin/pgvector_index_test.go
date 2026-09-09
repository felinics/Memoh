package builtin

import (
	"context"
	"errors"
	"testing"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/team"
)

func TestCheckedPGVectorInt32(t *testing.T) {
	t.Parallel()
	if got, err := checkedPgvectorInt32("limit", 42); err != nil || got != 42 {
		t.Fatalf("checkedPgvectorInt32(42) = %d, %v", got, err)
	}
	if _, err := checkedPgvectorInt32("limit", -1); err == nil {
		t.Fatal("checkedPgvectorInt32(-1) succeeded")
	}
	if _, err := checkedPgvectorInt32("limit", int(maxPgvectorInt32)+1); err == nil {
		t.Fatal("checkedPgvectorInt32(max+1) succeeded")
	}
}

func TestPGVectorTeamResolverDefaultsToSingleton(t *testing.T) {
	t.Parallel()
	index := &pgvectorIndex{resolveTeam: adapters.FixedTeamIDResolver(team.DefaultTeamID)}
	got, err := index.teamUUID(context.Background())
	if err != nil {
		t.Fatalf("teamUUID() error = %v", err)
	}
	if got.String() != team.DefaultTeamID {
		t.Fatalf("teamUUID() = %s, want %s", got.String(), team.DefaultTeamID)
	}
}

func TestPGVectorTeamResolverFailsClosed(t *testing.T) {
	t.Parallel()
	index := &pgvectorIndex{resolveTeam: func(context.Context) (string, error) {
		return "", errors.New("team missing")
	}}
	if _, err := index.teamUUID(context.Background()); err == nil {
		t.Fatal("teamUUID() without team succeeded")
	}
	index.resolveTeam = adapters.FixedTeamIDResolver("not-a-uuid")
	if _, err := index.teamUUID(context.Background()); err == nil {
		t.Fatal("teamUUID() with invalid team succeeded")
	}
}
