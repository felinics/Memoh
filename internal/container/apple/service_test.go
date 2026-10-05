package apple

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felinics/acgo"

	containerapi "github.com/felinics/memoh/internal/container"
)

// newTestService serves the Docker-compatible API on a unix socket: /_ping
// answers 200, a request for container "broken" answers 500 and any other
// container answers 404 the way the engine reports a missing container.
func newTestService(t *testing.T) *Service {
	t.Helper()

	// A unix socket path is limited to about 100 bytes, which t.TempDir can exceed.
	dir, err := os.MkdirTemp("", "apple")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "api.sock")
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/containers/broken/"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"engine failure"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such container: workspace-1"}`))
		}
	}))
	_ = srv.Listener.Close()
	srv.Listener = lis
	srv.Start()
	t.Cleanup(srv.Close)

	client, err := acgo.New(acgo.WithSocketPath(socket))
	if err != nil {
		t.Fatalf("acgo.New: %v", err)
	}
	return &Service{client: client}
}

func TestServiceReportsMissingContainerAsNotFound(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx := context.Background()
	calls := map[string]func(id string) error{
		"GetContainer": func(id string) error {
			_, err := svc.GetContainer(ctx, id)
			return err
		},
		"DeleteContainer": func(id string) error { return svc.DeleteContainer(ctx, id, nil) },
		"StartContainer":  func(id string) error { return svc.StartContainer(ctx, id, nil) },
		"StopContainer":   func(id string) error { return svc.StopContainer(ctx, id, nil) },
		"GetTaskInfo": func(id string) error {
			_, err := svc.GetTaskInfo(ctx, id)
			return err
		},
	}
	for name, call := range calls {
		err := call("workspace-1")
		if !containerapi.IsNotFound(err) {
			t.Errorf("%s(missing) error = %v, want container.ErrNotFound", name, err)
		}
		var apiErr *acgo.APIError
		if !errors.As(err, &apiErr) {
			t.Errorf("%s(missing) error = %v, want the acgo error kept in the chain", name, err)
		}

		if err := call("broken"); err == nil || containerapi.IsNotFound(err) {
			t.Errorf("%s(broken) error = %v, want an error that is not container.ErrNotFound", name, err)
		}
	}
}
