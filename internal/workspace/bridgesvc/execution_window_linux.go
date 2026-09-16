//go:build linux

package bridgesvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/felinics/memoh/internal/workspace/controlpath"
)

var errPayloadWindowClosed = errors.New("dependency cleanup requires an unused workspace lifetime")

type executionWindowState struct {
	Epoch string `json:"epoch"`
	Owner string `json:"owner"`
	Used  bool   `json:"used"`
}

func workspaceProcessEpoch() (string, error) {
	return workspaceProcessEpochAt("/proc")
}

func workspaceProcessEpochAt(procRoot string) (string, error) {
	boot, err := os.ReadFile(filepath.Join(procRoot, "sys/kernel/random/boot_id")) //nolint:gosec // Production passes the fixed /proc root; only tests pass a fixture directory.
	if err != nil {
		return "", err
	}
	stat, err := os.ReadFile(filepath.Join(procRoot, "1/stat")) //nolint:gosec // Production passes the fixed /proc root; only tests pass a fixture directory.
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(stat), ") ")
	if end < 0 {
		return "", errors.New("invalid PID 1 state")
	}
	fields := strings.Fields(string(stat[end+2:]))
	if len(fields) < 20 {
		return "", errors.New("invalid PID 1 start time")
	}
	// Reading another UID's namespace link requires ptrace permission. Self is
	// readable without it; a single NSpid proves this procfs mount represents
	// our PID namespace, so /proc/1 is also its init, not an ancestor's init.
	status, err := os.ReadFile(filepath.Join(procRoot, "self/status")) //nolint:gosec // Production passes the fixed /proc root; only tests pass a fixture directory.
	if err != nil {
		return "", err
	}
	sameNamespace := false
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "NSpid:" {
			if len(fields) == 2 {
				pid, err := strconv.ParseUint(fields[1], 10, 64)
				sameNamespace = err == nil && pid > 0
			}
			break
		}
	}
	if !sameNamespace {
		return "", errors.New("procfs does not prove the current PID namespace")
	}
	namespace, err := os.Readlink(filepath.Join(procRoot, "self/ns/pid"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(boot)) + ":" + fields[19] + ":" + namespace, nil
}

// beginExecution serializes fresh-workspace maintenance with admission of every
// bridge exec, including PTYs. The durable used bit survives bridge crashes and
// prevents a restarted bridge from declaring its surviving children quiescent.
func (s *Server) beginExecution(ctx context.Context, maintenance bool) (func(), error) {
	if s.executionUsed.Load() {
		if maintenance {
			return nil, errPayloadWindowClosed
		}
		return func() {}, nil
	}
	epoch, err := workspaceProcessEpoch()
	if err != nil {
		return nil, err
	}
	root := s.resolvePath(s.dataMount)
	if s.allowHostAbsolute {
		root = s.defaultWorkDir
	}
	directory := filepath.Join(root, ".memoh", "deps")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	for ancestor := directory; ancestor != "/"; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("dependency execution metadata has a symlinked ancestor")
		}
	}
	control := controlpath.Directory(directory)
	if err := os.MkdirAll(control, 0o700); err != nil {
		return nil, err
	}
	for ancestor := control; ancestor != "/"; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("dependency control path has a symlinked ancestor")
		}
	}
	fd, err := syscall.Open(filepath.Join(control, ".execution-window.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "dependency-execution-window") //nolint:gosec // Successful open returns a nonnegative kernel file descriptor.
	release := func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = lock.Close() }
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = lock.Close()
			return nil, err
		}
		if maintenance {
			_ = lock.Close()
			return nil, errPayloadWindowClosed
		}
		select {
		case <-ctx.Done():
			_ = lock.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	statePath := filepath.Join(directory, ".execution-window.json")
	state := executionWindowState{}
	bytes, readErr := readExecutionWindow(statePath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		release()
		return nil, readErr
	}
	known := readErr == nil && json.Unmarshal(bytes, &state) == nil && state.Epoch != "" && state.Owner != ""
	switch {
	case !known:
		// Enrollment cannot prove that an older bridge did not already execute
		// commands. The first lifetime is deliberately ineligible for cleanup.
		state = executionWindowState{Epoch: epoch, Owner: s.executionOwner, Used: true}
	case state.Epoch != epoch:
		state = executionWindowState{Epoch: epoch, Owner: s.executionOwner}
	case state.Owner != s.executionOwner:
		state.Owner, state.Used = s.executionOwner, true
	}
	if !maintenance {
		state.Used = true
	}
	data, err := json.Marshal(state)
	if err != nil {
		release()
		return nil, err
	}
	file, err := os.CreateTemp(directory, ".execution-window-")
	if err != nil {
		release()
		return nil, err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, statePath) //nolint:gosec // Both paths are inside the checked, server-owned execution metadata directory.
	}
	if err != nil {
		release()
		return nil, fmt.Errorf("persist dependency execution boundary: %w", err)
	}
	if state.Used {
		s.executionUsed.Store(true)
	}
	if maintenance && state.Used {
		release()
		return nil, errPayloadWindowClosed
	}
	if !maintenance {
		release()
		return func() {}, nil
	}
	return release, nil
}

// Never follow a metadata link supplied through the writable workspace. A
// corrupt regular record only closes the window; it cannot grant admission.
func readExecutionWindow(filename string) ([]byte, error) {
	fd, err := syscall.Open(filename, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filename) //nolint:gosec // Successful open returns a nonnegative kernel file descriptor.
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return nil, errors.New("invalid execution boundary metadata")
	}
	return io.ReadAll(io.LimitReader(file, 8193))
}
