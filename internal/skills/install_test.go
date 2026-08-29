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

func TestPackagePublicationCommitCanRetryCleanup(t *testing.T) {
	client := &packagePublicationTestClient{deleteErrors: []error{errors.New("temporary failure"), nil}}
	publication := &PackagePublication{
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

type packagePublicationTestClient struct {
	deleteErrors []error
	calls        int
	sawDeadline  bool
}

func (c *packagePublicationTestClient) DeleteFile(ctx context.Context, _ string, _ bool) error {
	c.calls++
	_, c.sawDeadline = ctx.Deadline()
	if len(c.deleteErrors) == 0 {
		return nil
	}
	err := c.deleteErrors[0]
	c.deleteErrors = c.deleteErrors[1:]
	return err
}

func (*packagePublicationTestClient) Rename(context.Context, string, string) error {
	return nil
}

func TestReconcilePackageRestoresRecordedRevision(t *testing.T) {
	t.Parallel()

	const registryID, packageID = "openai", "docs"
	recordedRevision := strings.Repeat("a", 64)
	interruptedRevision := strings.Repeat("b", 64)
	paths, err := packageOperationPaths(registryID, packageID)
	if err != nil {
		t.Fatalf("packageOperationPaths() error = %v", err)
	}
	client := newPackageInstallTestClient(t)
	seedPackageInstallTestData(t, client, paths.target, interruptedRevision, "new")
	seedPackageInstallTestData(t, client, paths.backup, recordedRevision, "old")

	consistent, err := ReconcilePackage(context.Background(), client, registryID, packageID, recordedRevision)
	if err != nil {
		t.Fatalf("ReconcilePackage() error = %v", err)
	}
	if !consistent {
		t.Fatal("ReconcilePackage() did not recover the recorded revision")
	}
	if got := readPackageInstallTestFile(t, client, path.Join(paths.target, "skill", "SKILL.md")); got != "old" {
		t.Fatalf("recovered Skill = %q, want old", got)
	}
	if _, err := client.Stat(context.Background(), paths.staging); !errors.Is(err, bridge.ErrNotFound) {
		t.Fatalf("staging Stat() error = %v, want not found", err)
	}
}

func TestReconcilePackageRemovesUnrecordedPublication(t *testing.T) {
	t.Parallel()

	const registryID, packageID = "openai", "docs"
	paths, err := packageOperationPaths(registryID, packageID)
	if err != nil {
		t.Fatalf("packageOperationPaths() error = %v", err)
	}
	client := newPackageInstallTestClient(t)
	seedPackageInstallTestData(t, client, paths.target, strings.Repeat("b", 64), "unrecorded")

	consistent, err := ReconcilePackage(context.Background(), client, registryID, packageID, "")
	if err != nil {
		t.Fatalf("ReconcilePackage() error = %v", err)
	}
	if consistent {
		t.Fatal("ReconcilePackage() reported an unrecorded publication as consistent")
	}
	if _, err := client.Stat(context.Background(), paths.target); !errors.Is(err, bridge.ErrNotFound) {
		t.Fatalf("target Stat() error = %v, want not found", err)
	}
}

func TestPackagePublicationRollbackRestoresPreviousRevision(t *testing.T) {
	t.Parallel()

	const registryID, packageID = "openai", "docs"
	oldRevision := strings.Repeat("a", 64)
	newRevision := strings.Repeat("b", 64)
	paths, err := packageOperationPaths(registryID, packageID)
	if err != nil {
		t.Fatalf("packageOperationPaths() error = %v", err)
	}
	client := newPackageInstallTestClient(t)
	seedPackageInstallTestData(t, client, paths.target, oldRevision, "old")
	publication, err := PublishPackage(
		context.Background(), client, "linux", registryID, packageID, newRevision,
		[]PackageArchive{{SkillID: "skill", Archive: Archive{files: []archiveFile{{path: "SKILL.md", content: []byte("new")}}}}},
	)
	if err != nil {
		t.Fatalf("PublishPackage() error = %v", err)
	}
	if got := readPackageInstallTestFile(t, client, path.Join(paths.target, "skill", "SKILL.md")); got != "new" {
		t.Fatalf("published Skill = %q, want new", got)
	}
	if err := publication.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if got := readPackageInstallTestFile(t, client, path.Join(paths.target, "skill", "SKILL.md")); got != "old" {
		t.Fatalf("rolled back Skill = %q, want old", got)
	}
	if _, err := client.Stat(context.Background(), paths.staging); !errors.Is(err, bridge.ErrNotFound) {
		t.Fatalf("staging Stat() error = %v, want not found", err)
	}
}

func newPackageInstallTestClient(t *testing.T) *bridge.Client {
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

func seedPackageInstallTestData(t *testing.T, client *bridge.Client, packageDir, revision, content string) {
	t.Helper()
	ctx := context.Background()
	if err := client.Mkdir(ctx, path.Join(packageDir, "skill")); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := client.WriteFile(ctx, path.Join(packageDir, packageRevisionMarker), []byte(revision+"\n")); err != nil {
		t.Fatalf("write revision marker: %v", err)
	}
	if err := client.WriteFile(ctx, path.Join(packageDir, "skill", "SKILL.md"), []byte(content)); err != nil {
		t.Fatalf("write Skill: %v", err)
	}
}

func readPackageInstallTestFile(t *testing.T, client *bridge.Client, filePath string) string {
	t.Helper()
	response, err := client.ReadFile(context.Background(), filePath, 0, 0)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return strings.TrimSuffix(response.GetContent(), "\n")
}
