package sidecar

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDialUnixReachesSocketBeyondPathLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the long-path fallback relies on /proc/self/fd")
	}
	dir := filepath.Join(t.TempDir(), strings.Repeat("d", 60), strings.Repeat("e", 60))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(dir, "client.sock")
	if len(socketPath) <= maxSocketPath {
		t.Fatalf("test path is only %d bytes", len(socketPath))
	}
	// Bind through the same /proc trick, since a listener has the same limit.
	d, err := os.Open(dir) //nolint:gosec // test-owned temp directory.
	if err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "unix", fmt.Sprintf("/proc/self/fd/%d/client.sock", d.Fd()))
	_ = d.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		if conn, err := listener.Accept(); err == nil {
			_ = conn.Close()
		}
	}()

	var dialer net.Dialer
	if conn, err := dialer.DialContext(context.Background(), "unix", socketPath); err == nil {
		_ = conn.Close()
		t.Fatal("a plain dial of the long path unexpectedly worked; the test no longer covers the fallback")
	}
	conn, err := DialUnix(context.Background(), socketPath)
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	_ = conn.Close()
}
