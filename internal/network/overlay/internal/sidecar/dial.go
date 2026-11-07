package sidecar

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// maxSocketPath is the longest path a Linux unix socket address holds:
// sun_path is 108 bytes including the trailing NUL.
const maxSocketPath = 107

// DialUnix connects to the client API socket a sidecar exposes under the
// workspace data root. That path grows with data_root and can exceed the
// unix socket limit, so a long path is reached through the socket's
// directory opened as /proc/self/fd/N, which keeps the address short.
func DialUnix(ctx context.Context, socketPath string) (net.Conn, error) {
	var dialer net.Dialer
	if len(socketPath) <= maxSocketPath {
		return dialer.DialContext(ctx, "unix", socketPath)
	}
	dir, err := os.Open(filepath.Dir(socketPath))
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	short := fmt.Sprintf("/proc/self/fd/%d/%s", dir.Fd(), filepath.Base(socketPath))
	return dialer.DialContext(ctx, "unix", short)
}
