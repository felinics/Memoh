package workdir

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspace"
)

func TestResolveForSessionNative(t *testing.T) {
	service := &Service{store: &fakeWorkdirStore{get: dbstore.BotWorkdirRecord{
		ID: "p1", BotID: "b1", TargetKind: TargetKindNative, Path: "/data/site",
	}}}
	resolved, err := service.ResolveForSession(context.Background(), "b1", "p1")
	if err != nil {
		t.Fatalf("ResolveForSession error = %v", err)
	}
	want := Resolved{WorkdirID: "p1", TargetID: workspace.WorkspaceTargetNative, Kind: TargetKindNative, WorkDir: "/data/site"}
	if resolved != want {
		t.Fatalf("resolved = %+v, want %+v", resolved, want)
	}
}

func TestResolveForSessionRemote(t *testing.T) {
	service := &Service{store: &fakeWorkdirStore{get: dbstore.BotWorkdirRecord{
		ID: "p2", BotID: "b1", TargetKind: TargetKindRemote, RemoteBindingID: "bind-1", Path: `C:\Users\alice\code`,
	}}}
	resolved, err := service.ResolveForSession(context.Background(), "b1", "p2")
	if err != nil {
		t.Fatalf("ResolveForSession error = %v", err)
	}
	if resolved.TargetID != "bind-1" || resolved.Kind != TargetKindRemote || resolved.WorkDir != `C:\Users\alice\code` {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestResolveForSessionArchivedStillResolves(t *testing.T) {
	// A session's working directory never changes underneath it: archiving
	// the workdir must not break resolution for already-bound sessions.
	service := &Service{store: &fakeWorkdirStore{get: dbstore.BotWorkdirRecord{
		ID: "p1", TargetKind: TargetKindNative, Path: "/data/site", ArchivedAt: time.Now(),
	}}}
	resolved, err := service.ResolveForSession(context.Background(), "b1", "p1")
	if err != nil {
		t.Fatalf("ResolveForSession error = %v", err)
	}
	if resolved.WorkDir != "/data/site" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestResolveForSessionErrors(t *testing.T) {
	service := &Service{store: &fakeWorkdirStore{getErr: db.ErrNotFound}}
	if _, err := service.ResolveForSession(context.Background(), "b1", "p1"); !errors.Is(err, ErrWorkdirNotFound) {
		t.Fatalf("error = %v, want ErrWorkdirNotFound", err)
	}
	if _, err := service.ResolveForSession(context.Background(), "b1", " "); !errors.Is(err, ErrWorkdirNotFound) {
		t.Fatalf("empty id error = %v, want ErrWorkdirNotFound", err)
	}
}
