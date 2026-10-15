//go:build !windows

package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

func TestExecTerminateAcknowledgesExitWithoutClosingTransport(t *testing.T) {
	client := newTestClient(t, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: t.TempDir(), AllowHostAbsolute: true}))
	for _, pty := range []bool{false, true} {
		name := "pipe"
		if pty {
			name = "pty"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			var stream *ExecStream
			var err error
			if pty {
				stream, err = client.ExecStreamPTY(ctx, "printf ready; sleep 30", "", 80, 24)
			} else {
				stream, err = client.ExecStream(ctx, "printf ready; sleep 30", "", -1)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			ready, err := stream.Recv()
			if err != nil || !strings.Contains(string(ready.GetData()), "ready") {
				t.Fatalf("process did not start: output=%+v err=%v", ready, err)
			}
			if err := stream.Terminate(); err != nil {
				t.Fatal(err)
			}
			for {
				output, err := stream.Recv()
				if err != nil {
					t.Fatalf("termination did not receive an EXIT acknowledgement: %v", err)
				}
				if output.GetStream() == pb.ExecOutput_EXIT {
					if output.ExitCode == 0 {
						t.Fatal("terminated process reported successful completion")
					}
					break
				}
			}
		})
	}
}
