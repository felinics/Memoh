package agentprocess

import (
	"context"
	"io"
	"net"
	"runtime"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

func TestProcessStdinEOFDrainsOutputAndReportsExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bridge requires /bin/sh")
	}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: t.TempDir(), AllowHostAbsolute: true}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough://bufnet", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := bridge.NewClientFromConn(conn)
	for _, code := range []string{"0", "3"} {
		t.Run(code, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			proc, err := Start(ctx, client, "while read line; do :; done; printf flushed; exit "+code, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = proc.Close() }()
			if _, err := proc.Write([]byte("input\n")); err != nil {
				t.Fatal(err)
			}
			proc.CloseStdin()
			output, err := io.ReadAll(proc)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-proc.Done():
			case <-ctx.Done():
				t.Fatal("stdin EOF never reached the process")
			}
			if string(output) != "flushed" || (proc.Err() == nil) != (code == "0") {
				t.Fatalf("output %q, exit error %v", output, proc.Err())
			}
		})
	}
}
