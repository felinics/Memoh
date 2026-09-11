package skills

import (
	"context"
	"errors"
	"net"
	"path"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

func TestShellQuoteEscapesApostrophes(t *testing.T) {
	if got, want := shellQuote("it's 'quoted'"), `'it'"'"'s '"'"'quoted'"'"''`; got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestAppPublicationCommitCanRetryCleanup(t *testing.T) {
	client := &appPublicationTestClient{deleteErrors: []error{errors.New("temporary failure"), nil}}
	publication := &AppPublication{
		client: client, backupDir: "/backup", stagingDir: "/staging", targetExists: true,
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := publication.Commit(canceled); err == nil {
		t.Fatal("first Commit() error = nil")
	}
	if publication.closed {
		t.Fatal("failed cleanup closed the publication")
	}
	if err := publication.Commit(canceled); err != nil {
		t.Fatalf("second Commit() error = %v", err)
	}
	if !publication.closed || client.calls != 2 || !client.sawDeadline {
		t.Fatalf("publication = %+v, calls = %d, deadline = %v", publication, client.calls, client.sawDeadline)
	}
}

type appPublicationTestClient struct {
	deleteErrors []error
	calls        int
	sawDeadline  bool
}

func (c *appPublicationTestClient) DeleteFile(ctx context.Context, _ string, _ bool) error {
	c.calls++
	_, c.sawDeadline = ctx.Deadline()
	if len(c.deleteErrors) == 0 {
		return nil
	}
	err := c.deleteErrors[0]
	c.deleteErrors = c.deleteErrors[1:]
	return err
}

func (*appPublicationTestClient) Rename(context.Context, string, string) error {
	return nil
}

func TestReconcileAppRestoresRecordedRevision(t *testing.T) {
	t.Parallel()

	const registryID, appID = "openai", "docs"
	recordedRevision := strings.Repeat("a", 64)
	interruptedRevision := strings.Repeat("b", 64)
	paths, err := appOperationPaths(registryID, appID)
	if err != nil {
		t.Fatalf("appOperationPaths() error = %v", err)
	}
	client := newAppInstallTestClient(t)
	seedAppInstallTestData(t, client, paths.target, interruptedRevision, "new")
	seedAppInstallTestData(t, client, paths.backup, recordedRevision, "old")

	consistent, err := ReconcileApp(context.Background(), client, registryID, appID, recordedRevision)
	if err != nil {
		t.Fatalf("ReconcileApp() error = %v", err)
	}
	if !consistent {
		t.Fatal("ReconcileApp() did not recover the recorded revision")
	}
	if got := readAppInstallTestFile(t, client, path.Join(paths.target, "skill", "SKILL.md")); got != "old" {
		t.Fatalf("recovered Skill = %q, want old", got)
	}
	if _, err := client.Stat(context.Background(), paths.staging); !errors.Is(err, bridge.ErrNotFound) {
		t.Fatalf("staging Stat() error = %v, want not found", err)
	}
}

func TestReconcileAppRemovesUnrecordedPublication(t *testing.T) {
	t.Parallel()

	const registryID, appID = "openai", "docs"
	paths, err := appOperationPaths(registryID, appID)
	if err != nil {
		t.Fatalf("appOperationPaths() error = %v", err)
	}
	client := newAppInstallTestClient(t)
	seedAppInstallTestData(t, client, paths.target, strings.Repeat("b", 64), "unrecorded")

	consistent, err := ReconcileApp(context.Background(), client, registryID, appID, "")
	if err != nil {
		t.Fatalf("ReconcileApp() error = %v", err)
	}
	if consistent {
		t.Fatal("ReconcileApp() reported an unrecorded publication as consistent")
	}
	if _, err := client.Stat(context.Background(), paths.target); !errors.Is(err, bridge.ErrNotFound) {
		t.Fatalf("target Stat() error = %v, want not found", err)
	}
}

func TestAppPublicationRollbackRestoresPreviousRevision(t *testing.T) {
	t.Parallel()

	const registryID, appID = "openai", "docs"
	oldRevision := strings.Repeat("a", 64)
	newRevision := strings.Repeat("b", 64)
	paths, err := appOperationPaths(registryID, appID)
	if err != nil {
		t.Fatalf("appOperationPaths() error = %v", err)
	}
	client := newAppInstallTestClient(t)
	seedAppInstallTestData(t, client, paths.target, oldRevision, "old")
	publication, err := PublishApp(
		context.Background(), client, "linux", registryID, appID, newRevision,
		[]AppArchive{{SkillID: "skill", Archive: Archive{files: []archiveFile{{path: "SKILL.md", content: []byte("new")}}}}},
	)
	if err != nil {
		t.Fatalf("PublishApp() error = %v", err)
	}
	if got := readAppInstallTestFile(t, client, path.Join(paths.target, "skill", "SKILL.md")); got != "new" {
		t.Fatalf("published Skill = %q, want new", got)
	}
	if err := publication.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if got := readAppInstallTestFile(t, client, path.Join(paths.target, "skill", "SKILL.md")); got != "old" {
		t.Fatalf("rolled back Skill = %q, want old", got)
	}
	if _, err := client.Stat(context.Background(), paths.staging); !errors.Is(err, bridge.ErrNotFound) {
		t.Fatalf("staging Stat() error = %v, want not found", err)
	}
}

func newAppInstallTestClient(t *testing.T) *bridge.Client {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir: "/data",
		WorkspaceRoot:  t.TempDir(),
		DataMount:      "/data",
	}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		<-done
	})
	connection, err := grpc.NewClient(
		"passthrough://bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return bridge.NewClientFromConn(connection)
}

func seedAppInstallTestData(t *testing.T, client *bridge.Client, appDir, revision, content string) {
	t.Helper()
	ctx := context.Background()
	if err := client.Mkdir(ctx, path.Join(appDir, "skill")); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := client.WriteFile(ctx, path.Join(appDir, appRevisionMarker), []byte(revision+"\n")); err != nil {
		t.Fatalf("write revision marker: %v", err)
	}
	if err := client.WriteFile(ctx, path.Join(appDir, "skill", "SKILL.md"), []byte(content)); err != nil {
		t.Fatalf("write Skill: %v", err)
	}
}

func readAppInstallTestFile(t *testing.T, client *bridge.Client, filePath string) string {
	t.Helper()
	response, err := client.ReadFile(context.Background(), filePath, 0, 0)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return strings.TrimSuffix(response.GetContent(), "\n")
}
