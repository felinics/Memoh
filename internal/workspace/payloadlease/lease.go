// Package payloadlease coordinates dependency execution and payload collection
// through stable kernel locks inside the workspace, across Server processes.
package payloadlease

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/controlpath"
)

// EpochCommand identifies a whole Linux workspace lifetime, rather than a
// bridge process lifetime. A bridge-only restart does not terminate its old
// children and therefore must never authorize deleting their payloads.
const EpochCommand = `if [ "$(uname -s)" != Linux ]; then exit 0; fi
[ -r /proc/sys/kernel/random/boot_id ] && [ -r /proc/1/stat ] || exit 0
boot=$(cat /proc/sys/kernel/random/boot_id) || exit 0
started=$(awk '{sub(/^.*\) /, ""); print $20}' /proc/1/stat) || exit 0
# Self avoids cross-UID ptrace permission; one NSpid proves /proc/1 belongs
# to the same namespace rather than an ancestor procfs mount.
awk '$1 == "NSpid:" { if (NF == 2 && $2 ~ /^[0-9]+$/ && $2 > 0) ok=1; exit } END { exit !ok }' /proc/self/status || exit 0
namespace=$(readlink /proc/self/ns/pid) || exit 0
case "$boot" in ''|*[!a-fA-F0-9-]*) exit 0 ;; esac
case "$started" in ''|*[!0-9]*) exit 0 ;; esac
[ -n "$namespace" ] || exit 0
printf '%s:%s:%s\n' "$boot" "$started" "$namespace"
`

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ReadEpoch returns empty when the runtime cannot prove an execution lifetime.
// Callers must retain published payloads when this evidence is unavailable.
func ReadEpoch(ctx context.Context, client *bridge.Client) (string, error) {
	result, err := client.ExecWithOptions(ctx, EpochCommand, "", 10, nil, bridge.ExecOptions{})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", nil
	}
	return strings.TrimSpace(result.Stdout), nil
}

func LockPath(dataRoot, dependencyID string) string {
	return path.Join(controlpath.Directory(path.Join(dataRoot, ".memoh", "deps")), ".leases", dependencyID+".lock")
}

// EntrypointLockPath narrows ownership to one immutable installation when
// the launcher is pinned. Legacy or externally discovered paths retain the
// namespace lease because their payload identity cannot be inferred safely.
func EntrypointLockPath(dataRoot, dependencyID, entrypoint string) string {
	parts := strings.Split(entrypoint, "/")
	for i, part := range parts {
		if part == "installs" && i+2 < len(parts) && len(parts[i+1]) == 32 {
			if _, err := hex.DecodeString(parts[i+1]); err == nil {
				return LockPath(dataRoot, dependencyID+"-"+parts[i+1])
			}
		}
	}
	return LockPath(dataRoot, dependencyID)
}

// SharedGuard acquires a shared lock inherited by the command and its children.
// Unsupported platforms retain obsolete payloads until an adapter can prove
// quiescence; the guard therefore does not pretend to provide locking there.
func SharedGuard(lockPath, epoch string) string {
	var script strings.Builder
	script.WriteString("if [ \"$(uname -s)\" = Linux ]; then\n")
	for directory := path.Dir(lockPath); directory != "/"; directory = path.Dir(directory) {
		script.WriteString("[ ! -L " + quote(directory) + " ] || exit 76\n")
	}
	script.WriteString("[ ! -L " + quote(lockPath) + " ] || exit 76\nmkdir -p " + quote(path.Dir(lockPath)) + "\nexec 9>> " + quote(lockPath) + "\nflock -s 9 || exit 76\n")
	if epoch != "" {
		script.WriteString("actual_epoch=$(\n" + EpochCommand + ")\n[ \"$actual_epoch\" = " + quote(epoch) + " ] || exit 76\n")
	}
	script.WriteString("fi\n")
	return script.String()
}

// Command wraps a command already quoted for the workspace shell. The epoch
// fence rejects a resolution surviving a complete workspace restart.
func Command(command, lockPath, epoch string) string {
	if lockPath == "" {
		return command
	}
	return SharedGuard(lockPath, epoch) + "exec " + command
}

// Lease protects the interval before a resolved launcher has a process. The
// command also inherits a shared lock, so a lost Server/lease stream cannot
// release a running CLI's ownership.
type Lease struct {
	Path   string
	Epoch  string
	stream *bridge.ExecStream
	done   chan struct{}
	once   sync.Once
}

func Acquire(ctx context.Context, client *bridge.Client, lockPath string) (*Lease, error) {
	epoch, err := ReadEpoch(ctx, client)
	if err != nil {
		return nil, err
	}
	if epoch == "" {
		return &Lease{}, nil
	}
	command := SharedGuard(lockPath, epoch) + "printf '__MEMOH_PAYLOAD_LEASE__\\n'\ncat >/dev/null\n"
	stream, err := client.ExecStreamWithOptions(context.WithoutCancel(ctx), command, "", -1, bridge.ExecOptions{})
	if err != nil {
		return nil, err
	}
	lease := &Lease{Path: lockPath, Epoch: epoch, stream: stream, done: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		defer close(lease.done)
		confirmed := false
		var output strings.Builder
		for {
			event, err := stream.Recv()
			if err != nil {
				if !confirmed {
					ready <- err
				}
				return
			}
			if event.GetStream() == pb.ExecOutput_EXIT {
				if !confirmed {
					ready <- fmt.Errorf("payload lease exited %d", event.GetExitCode())
				}
				return
			}
			if event.GetStream() == pb.ExecOutput_STDOUT && !confirmed {
				output.Write(event.GetData())
				if strings.Contains(output.String(), "__MEMOH_PAYLOAD_LEASE__\n") {
					confirmed = true
					ready <- nil
				}
			}
		}
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case err := <-ready:
		if err != nil {
			lease.Release()
			return nil, err
		}
		return lease, nil
	case <-ctx.Done():
		lease.Release()
		return nil, ctx.Err()
	case <-timer.C:
		lease.Release()
		return nil, errors.New("timed out acquiring dependency execution lease")
	}
}

// Release is idempotent. Kernel ownership survives until the actual holder
// exits; failed stream cancellation therefore cannot claim a successful unlock.
func (l *Lease) Release() {
	if l == nil || l.stream == nil {
		return
	}
	l.once.Do(func() { _ = l.stream.CloseSend(); _ = l.stream.Close() })
}
