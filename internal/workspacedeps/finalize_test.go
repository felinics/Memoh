package workspacedeps

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// An orderly transport EOF is not proof of process exit. The bridge may have
// forwarded finalization output while losing the terminal EXIT frame.
func TestFinalizationEOFWithoutExitRetainsRecoverableOperation(t *testing.T) {
	f := newServiceFixture(t)
	var dropExit atomic.Bool
	f.client = newFinalizationClient(t, &dropExit)
	f.ws.client = f.client
	cat, err := catalog.LoadFS(fstest.MapFS{
		"foo/dependency.yaml": &fstest.MapFile{Data: []byte(e2eFooYAML)},
		"foo/install.sh":      &fstest.MapFile{Data: []byte(e2eFooInstall)},
		"foo/remove.sh":       &fstest.MapFile{Data: []byte(e2eFooRemove)},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.cat, f.svc.catalog = cat, cat
	f.platform, err = ProbePlatform(f.ctx(), f.client)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.discover = Discover
	f.svc.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
		result, err := Run(ctx, client, spec, sink)
		if err == nil {
			dropExit.Store(true)
		}
		return result, err
	}
	if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "foo", "1.0.0", nil); !errors.Is(err, ErrOperationUncertain) {
		t.Fatalf("finalization without EXIT = %v, want uncertain", err)
	}
	rec, exists := f.store.get(f.key("foo"))
	if !exists || !rec.Status.InProgress() || rec.OperationID == "" {
		t.Fatalf("unconfirmed finalization released operation: %+v", rec)
	}
	receipt, err := ReadOperationReceipt(f.ctx(), f.client, f.home("foo"), "foo")
	if err != nil || receipt == nil || receipt.ID != rec.OperationID || !receipt.Completed {
		t.Fatalf("unconfirmed finalization deleted recovery receipt: %+v %v", receipt, err)
	}
	if state := f.readState(t, "foo"); state.Version != "1.0.0" {
		t.Fatalf("finalization did not actually execute before stream EOF: %+v", state)
	}
	dropExit.Store(false)
	if _, err := f.svc.Refresh(f.ctx(), testBot, testTarget); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.store.get(f.key("foo")); rec.Status != StatusInstalled || rec.OperationID != "" {
		t.Fatalf("receipt failed to converge after stream recovery: %+v", rec)
	}
}

type missingExitStream struct {
	grpc.ServerStream
	dropExit *atomic.Bool
}

func (s *missingExitStream) SendMsg(message any) error {
	if output, ok := message.(*pb.ExecOutput); ok && output.GetStream() == pb.ExecOutput_EXIT && s.dropExit.Load() {
		return nil
	}
	return s.ServerStream.SendMsg(message)
}

func newFinalizationClient(t *testing.T, dropExit *atomic.Bool) *bridge.Client {
	t.Helper()
	listener := bufconn.Listen(testBufSize)
	server := grpc.NewServer(grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		return next(srv, &missingExitStream{ServerStream: stream, dropExit: dropExit})
	}))
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: t.TempDir(), AllowHostAbsolute: true}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() { server.Stop(); <-done })
	conn, err := grpc.NewClient("passthrough://finalization", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}
