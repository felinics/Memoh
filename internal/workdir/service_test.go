package workdir

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// statTestContainerService serves Stat from a fixed path -> isDir map;
// anything absent is NotFound. Paths are matched verbatim, which also
// asserts the exact path the service sends over the bridge.
type statTestContainerService struct {
	pb.UnimplementedContainerServiceServer
	dirs  map[string]bool
	seen  []string
	files map[string]bool
}

func (s *statTestContainerService) Stat(_ context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	s.seen = append(s.seen, req.GetPath())
	if s.dirs[req.GetPath()] {
		return &pb.StatResponse{Entry: &pb.FileEntry{Path: req.GetPath(), IsDir: true}}, nil
	}
	if s.files[req.GetPath()] {
		return &pb.StatResponse{Entry: &pb.FileEntry{Path: req.GetPath(), IsDir: false}}, nil
	}
	return nil, status.Error(codes.NotFound, "not found")
}

func newStatTestClient(t *testing.T, svc *statTestContainerService) *bridge.Client {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterContainerServiceServer(srv, svc)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return bridge.NewClientFromConn(conn)
}

type fakeWorkdirStore struct {
	created    []dbstore.CreateBotWorkdirInput
	create     dbstore.BotWorkdirRecord
	createFn   func(dbstore.CreateBotWorkdirInput) (dbstore.BotWorkdirRecord, error)
	get        dbstore.BotWorkdirRecord
	getErr     error
	renamed    dbstore.BotWorkdirRecord
	renameErr  error
	archiveErr error
	list       []dbstore.BotWorkdirRecord
}

func (f *fakeWorkdirStore) CreateWorkdir(_ context.Context, input dbstore.CreateBotWorkdirInput) (dbstore.BotWorkdirRecord, error) {
	f.created = append(f.created, input)
	if f.createFn != nil {
		return f.createFn(input)
	}
	return f.create, nil
}

func (f *fakeWorkdirStore) ListWorkdirs(context.Context, string, bool) ([]dbstore.BotWorkdirRecord, error) {
	return f.list, nil
}

func (f *fakeWorkdirStore) GetWorkdir(context.Context, string, string) (dbstore.BotWorkdirRecord, error) {
	return f.get, f.getErr
}

func (f *fakeWorkdirStore) RenameWorkdir(context.Context, string, string, string) (dbstore.BotWorkdirRecord, error) {
	return f.renamed, f.renameErr
}

func (f *fakeWorkdirStore) ArchiveWorkdir(context.Context, string, string) error {
	return f.archiveErr
}

type fakeTargetResolver struct {
	target    workspace.ResolvedWorkspaceTarget
	err       error
	requested string
}

func (f *fakeTargetResolver) ResolveWorkspaceTarget(_ context.Context, _, targetID string) (workspace.ResolvedWorkspaceTarget, error) {
	f.requested = targetID
	return f.target, f.err
}

func TestNormalizeWorkdirPathNative(t *testing.T) {
	native := workspace.ResolvedWorkspaceTarget{Kind: TargetKindNative}
	for name, tc := range map[string]struct {
		raw  string
		want string
	}{
		"data root":         {"/data", "/data"},
		"under data":        {"/data/site", "/data/site"},
		"relative":          {"site", "/data/site"},
		"trailing slash":    {"/data/site/", "/data/site"},
		"dot segments kept": {"/data/site/../site", "/data/site"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeWorkdirPath(tc.raw, native)
			if err != nil {
				t.Fatalf("normalizeWorkdirPath(%q) error = %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeWorkdirPath(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeWorkdirPathNativeRejectsEscape(t *testing.T) {
	native := workspace.ResolvedWorkspaceTarget{Kind: TargetKindNative}
	for _, raw := range []string{"/etc/passwd", "/data/../etc", "../x", "/tmp/x"} {
		if _, err := normalizeWorkdirPath(raw, native); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("normalizeWorkdirPath(%q) error = %v, want ErrInvalidPath", raw, err)
		}
	}
}

func TestNormalizeWorkdirPathEmpty(t *testing.T) {
	if _, err := normalizeWorkdirPath("  ", workspace.ResolvedWorkspaceTarget{Kind: TargetKindNative}); !errors.Is(err, ErrPathRequired) {
		t.Fatalf("error = %v, want ErrPathRequired", err)
	}
}

func TestNormalizeWorkdirPathRemotePosix(t *testing.T) {
	remote := workspace.ResolvedWorkspaceTarget{Kind: TargetKindRemote, Info: bridge.WorkspaceInfo{OS: "darwin"}}
	got, err := normalizeWorkdirPath("/Users/alice/code/site/", remote)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if got != "/Users/alice/code/site" {
		t.Fatalf("got %q", got)
	}
	if _, err := normalizeWorkdirPath("code/site", remote); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("relative remote path error = %v, want ErrInvalidPath", err)
	}
}

func TestNormalizeWorkdirPathRemoteWindows(t *testing.T) {
	remote := workspace.ResolvedWorkspaceTarget{Kind: TargetKindRemote, Info: bridge.WorkspaceInfo{OS: "win32"}}
	for name, tc := range map[string]struct {
		raw  string
		want string
	}{
		"backslash":      {`C:\Users\alice\code`, `C:\Users\alice\code`},
		"forward slash":  {`C:/Users/alice/code`, `C:\Users\alice\code`},
		"trailing slash": {`C:\Users\alice\code\`, `C:\Users\alice\code`},
		"drive root":     {`C:\`, `C:\`},
		"unc":            {`\\server\share\dir`, `\\server\share\dir`},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeWorkdirPath(tc.raw, remote)
			if err != nil {
				t.Fatalf("normalizeWorkdirPath(%q) error = %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeWorkdirPath(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
	for _, raw := range []string{`code\site`, `/Users/alice`, `C:site`} {
		if _, err := normalizeWorkdirPath(raw, remote); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("normalizeWorkdirPath(%q) error = %v, want ErrInvalidPath", raw, err)
		}
	}
}

func TestCreateValidatesDirectoryOnTarget(t *testing.T) {
	svc := &statTestContainerService{dirs: map[string]bool{"/data/site": true}}
	client := newStatTestClient(t, svc)
	store := &fakeWorkdirStore{create: dbstore.BotWorkdirRecord{
		ID: "p1", BotID: "b1", Name: "Site", TargetKind: TargetKindNative, Path: "/data/site",
	}}
	resolver := &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
		TargetID: workspace.WorkspaceTargetNative,
		Kind:     TargetKindNative,
		Client:   client,
	}}
	service := &Service{store: store, targets: resolver}

	created, err := service.Create(context.Background(), "b1", "u1", CreateRequest{Name: " Site ", Path: "site"})
	if err != nil {
		t.Fatalf("Create error = %v", err)
	}
	if resolver.requested != workspace.WorkspaceTargetNative {
		t.Fatalf("resolved target = %q, want native default", resolver.requested)
	}
	if len(store.created) != 1 {
		t.Fatalf("store.created = %d, want 1", len(store.created))
	}
	input := store.created[0]
	if input.Path != "/data/site" || input.Name != "Site" || input.TargetKind != TargetKindNative || input.RemoteBindingID != "" {
		t.Fatalf("store input = %+v", input)
	}
	if len(svc.seen) != 1 || svc.seen[0] != "/data/site" {
		t.Fatalf("stat saw %v, want [/data/site]", svc.seen)
	}
	if created.WorkspaceTargetID != workspace.WorkspaceTargetNative {
		t.Fatalf("created.WorkspaceTargetID = %q", created.WorkspaceTargetID)
	}
}

func TestCreateRejectsMissingDirectory(t *testing.T) {
	client := newStatTestClient(t, &statTestContainerService{})
	service := &Service{
		store: &fakeWorkdirStore{},
		targets: &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
			TargetID: workspace.WorkspaceTargetNative, Kind: TargetKindNative, Client: client,
		}},
	}
	_, err := service.Create(context.Background(), "b1", "u1", CreateRequest{Name: "Site", Path: "/data/missing"})
	if !errors.Is(err, ErrPathNotFound) {
		t.Fatalf("error = %v, want ErrPathNotFound", err)
	}
	if !strings.Contains(err.Error(), "/data/missing") {
		t.Fatalf("error %q should name the checked path", err)
	}
}

func TestCreateRejectsFilePath(t *testing.T) {
	client := newStatTestClient(t, &statTestContainerService{files: map[string]bool{"/data/notes.txt": true}})
	service := &Service{
		store: &fakeWorkdirStore{},
		targets: &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
			TargetID: workspace.WorkspaceTargetNative, Kind: TargetKindNative, Client: client,
		}},
	}
	_, err := service.Create(context.Background(), "b1", "u1", CreateRequest{Name: "Site", Path: "/data/notes.txt"})
	if !errors.Is(err, ErrPathNotDirectory) {
		t.Fatalf("error = %v, want ErrPathNotDirectory", err)
	}
}

func TestCreateRemoteStoresBindingID(t *testing.T) {
	svc := &statTestContainerService{dirs: map[string]bool{"/Users/alice/code": true}}
	client := newStatTestClient(t, svc)
	store := &fakeWorkdirStore{create: dbstore.BotWorkdirRecord{
		ID: "p2", BotID: "b1", TargetKind: TargetKindRemote, RemoteBindingID: "bind-1", Path: "/Users/alice/code",
	}}
	service := &Service{
		store: store,
		targets: &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
			TargetID: "bind-1",
			Kind:     TargetKindRemote,
			Client:   client,
			Info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, OS: "darwin"},
		}},
	}
	created, err := service.Create(context.Background(), "b1", "u1", CreateRequest{
		Name: "Code", WorkspaceTargetID: "bind-1", Path: "/Users/alice/code",
	})
	if err != nil {
		t.Fatalf("Create error = %v", err)
	}
	if store.created[0].RemoteBindingID != "bind-1" || store.created[0].TargetKind != TargetKindRemote {
		t.Fatalf("store input = %+v", store.created[0])
	}
	if created.WorkspaceTargetID != "bind-1" {
		t.Fatalf("created.WorkspaceTargetID = %q", created.WorkspaceTargetID)
	}
}

func TestCreateMapsUniqueViolationToDuplicatePath(t *testing.T) {
	client := newStatTestClient(t, &statTestContainerService{dirs: map[string]bool{"/data/site": true}})
	store := &fakeWorkdirStore{createFn: func(dbstore.CreateBotWorkdirInput) (dbstore.BotWorkdirRecord, error) {
		return dbstore.BotWorkdirRecord{}, &pgconn.PgError{Code: "23505"}
	}}
	service := &Service{
		store: store,
		targets: &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
			TargetID: workspace.WorkspaceTargetNative, Kind: TargetKindNative, Client: client,
		}},
	}
	_, err := service.Create(context.Background(), "b1", "u1", CreateRequest{Name: "Site", Path: "/data/site"})
	if !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("error = %v, want ErrDuplicatePath", err)
	}
}

func TestCreateRequiresName(t *testing.T) {
	service := &Service{store: &fakeWorkdirStore{}, targets: &fakeTargetResolver{}}
	if _, err := service.Create(context.Background(), "b1", "u1", CreateRequest{Name: "  ", Path: "/data/x"}); !errors.Is(err, ErrNameRequired) {
		t.Fatalf("error = %v, want ErrNameRequired", err)
	}
}

func TestRequireActiveRefusesArchived(t *testing.T) {
	service := &Service{store: &fakeWorkdirStore{get: dbstore.BotWorkdirRecord{
		ID: "p1", TargetKind: TargetKindNative, ArchivedAt: time.Now(),
	}}}
	if _, err := service.RequireActive(context.Background(), "b1", "p1"); !errors.Is(err, ErrWorkdirArchived) {
		t.Fatalf("error = %v, want ErrWorkdirArchived", err)
	}
}

func TestGetMapsNotFound(t *testing.T) {
	service := &Service{store: &fakeWorkdirStore{getErr: db.ErrNotFound}}
	if _, err := service.Get(context.Background(), "b1", "p1"); !errors.Is(err, ErrWorkdirNotFound) {
		t.Fatalf("error = %v, want ErrWorkdirNotFound", err)
	}
}
