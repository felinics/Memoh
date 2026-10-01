package workdir

import (
	"context"
	"errors"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func TestDirectoriesNativeDefaultsToDataMount(t *testing.T) {
	svc := &statTestContainerService{listings: map[string][]*pb.FileEntry{
		"/data": {
			{Path: "site", IsDir: true},
			{Path: "notes.txt"},
			{Path: ".cache", IsDir: true},
		},
	}}
	resolver := &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
		TargetID: workspace.WorkspaceTargetNative, Kind: TargetKindNative, Client: newStatTestClient(t, svc),
	}}
	service := &Service{store: &fakeWorkdirStore{}, targets: resolver}

	got, err := service.Directories(context.Background(), "b1", "", "")
	if err != nil {
		t.Fatalf("Directories error = %v", err)
	}
	if resolver.requested != workspace.WorkspaceTargetNative {
		t.Fatalf("resolved target = %q, want native default", resolver.requested)
	}
	if got.Path != "/data" || got.WorkspaceTargetID != workspace.WorkspaceTargetNative {
		t.Fatalf("response = %+v", got)
	}
	want := []Directory{{Name: "site", Path: "/data/site"}, {Name: ".cache", Path: "/data/.cache"}}
	if !slices.Equal(got.Directories, want) {
		t.Fatalf("directories = %+v, want %+v", got.Directories, want)
	}
}

func TestDirectoriesNativeStaysUnderDataMount(t *testing.T) {
	svc := &statTestContainerService{listings: map[string][]*pb.FileEntry{"/data/site": {}}}
	service := &Service{store: &fakeWorkdirStore{}, targets: &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
		TargetID: workspace.WorkspaceTargetNative, Kind: TargetKindNative, Client: newStatTestClient(t, svc),
	}}}
	if _, err := service.Directories(context.Background(), "b1", "native", "/../etc"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Directories(/../etc) error = %v, want ErrInvalidPath", err)
	}
	if _, err := service.Directories(context.Background(), "b1", "native", "site"); err != nil {
		t.Fatalf("Directories(site) error = %v", err)
	}
	if !slices.Equal(svc.listed, []string{"/data/site"}) {
		t.Fatalf("listed %v, want only the in-root path", svc.listed)
	}
}

func TestDirectoriesRemoteStartsAtWorkspaceBase(t *testing.T) {
	svc := &statTestContainerService{listings: map[string][]*pb.FileEntry{
		"/Users/alice": {{Path: "code", IsDir: true}, {Path: "/Users/alice/Documents/", IsDir: true}},
	}}
	resolver := &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
		TargetID: "bind-1",
		Kind:     TargetKindRemote,
		Client:   newStatTestClient(t, svc),
		Info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, OS: "darwin", DefaultWorkDir: "/Users/alice"},
	}}
	service := &Service{store: &fakeWorkdirStore{}, targets: resolver}

	got, err := service.Directories(context.Background(), "b1", "bind-1", " ")
	if err != nil {
		t.Fatalf("Directories error = %v", err)
	}
	if resolver.requested != "bind-1" || got.Path != "/Users/alice" || got.WorkspaceTargetID != "bind-1" {
		t.Fatalf("requested %q, response %+v", resolver.requested, got)
	}
	want := []Directory{{Name: "code", Path: "/Users/alice/code"}, {Name: "Documents", Path: "/Users/alice/Documents"}}
	if !slices.Equal(got.Directories, want) {
		t.Fatalf("directories = %+v, want %+v", got.Directories, want)
	}
}

func TestDirectoriesRemoteWindowsJoinsWithBackslash(t *testing.T) {
	svc := &statTestContainerService{listings: map[string][]*pb.FileEntry{
		`C:\`:            {{Path: "Users", IsDir: true}},
		`C:\Users\alice`: {{Path: "code", IsDir: true}},
	}}
	service := &Service{store: &fakeWorkdirStore{}, targets: &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
		TargetID: "bind-1",
		Kind:     TargetKindRemote,
		Client:   newStatTestClient(t, svc),
		Info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, OS: "win32", DefaultWorkDir: `C:\Users\alice`},
	}}}

	root, err := service.Directories(context.Background(), "b1", "bind-1", "C:/")
	if err != nil {
		t.Fatalf("Directories(C:/) error = %v", err)
	}
	if root.Path != `C:\` || !slices.Equal(root.Directories, []Directory{{Name: "Users", Path: `C:\Users`}}) {
		t.Fatalf("drive root response = %+v", root)
	}
	home, err := service.Directories(context.Background(), "b1", "bind-1", "")
	if err != nil {
		t.Fatalf("Directories(default) error = %v", err)
	}
	if home.Path != `C:\Users\alice` || !slices.Equal(home.Directories, []Directory{{Name: "code", Path: `C:\Users\alice\code`}}) {
		t.Fatalf("default response = %+v", home)
	}
}

func TestDirectoriesMapsBridgeErrors(t *testing.T) {
	svc := &statTestContainerService{listErrs: map[string]codes.Code{
		"/home/alice/private": codes.PermissionDenied,
		"/home/alice/file":    codes.InvalidArgument,
		"/home/alice/down":    codes.Unavailable,
	}}
	service := &Service{store: &fakeWorkdirStore{}, targets: &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
		TargetID: "bind-1",
		Kind:     TargetKindRemote,
		Client:   newStatTestClient(t, svc),
		Info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, OS: "linux"},
	}}}
	for raw, want := range map[string]error{
		"/home/alice/private": ErrPathForbidden,
		"/home/alice/file":    ErrPathNotDirectory,
		"/home/alice/missing": ErrPathNotFound,
		"/home/alice/down":    bridge.ErrUnavailable,
		"relative/path":       ErrInvalidPath,
	} {
		if _, err := service.Directories(context.Background(), "b1", "bind-1", raw); !errors.Is(err, want) {
			t.Fatalf("Directories(%q) error = %v, want %v", raw, err, want)
		}
	}
}

func TestDirectoriesPropagatesTargetErrors(t *testing.T) {
	service := &Service{store: &fakeWorkdirStore{}, targets: &fakeTargetResolver{err: workspace.ErrRemoteRuntimeOffline}}
	if _, err := service.Directories(context.Background(), "b1", "bind-1", ""); !errors.Is(err, workspace.ErrRemoteRuntimeOffline) {
		t.Fatalf("error = %v, want ErrRemoteRuntimeOffline", err)
	}
}
