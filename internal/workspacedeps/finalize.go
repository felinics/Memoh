package workspacedeps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/controlpath"
	"github.com/felinics/memoh/internal/workspace/payloadlease"
)

// isPlainFileName rejects names that would escape the target directory.
func isPlainFileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00") && path.Base(name) == name
}

// finalizeFilesystem runs every state/shim change in the process that owns the
// workspace kernel lock. Canceling an RPC cannot leave an independently running
// rename outside that lock. The durable operation remains claimed until the
// command has demonstrably exited and FinishOperation confirms its identity.
func (s *Service) finalizeFilesystem(ctx context.Context, op *operation, state, previous *State) error {
	rec, err := s.store.Get(ctx, op.key)
	if err != nil {
		return err
	}
	if rec.OperationID != op.operationID || !rec.Status.InProgress() {
		return ErrBusy
	}
	var script strings.Builder
	script.WriteString("set -eu\n")
	if op.receipt != nil {
		current := path.Join(operationRoot(op.home, op.dep.ID), "current")
		fmt.Fprintf(&script, "[ \"$(cat %s 2>/dev/null)\" = %s ] || exit 76\n", shellQuote(current), shellQuote(op.receipt.ID))
	}
	obsolete := shimNames(op.dep, previous)
	if state != nil {
		if state.PayloadPath != "" {
			for _, entrypoint := range state.Entrypoints {
				fmt.Fprintf(&script, "test -x %s\n", shellQuote(entrypoint))
			}
			if op.dep.StorageLayout == "isolated" {
				for directory := state.PayloadPath; directory != "/"; directory = path.Dir(directory) {
					fmt.Fprintf(&script, "[ ! -L %s ] || exit 77\n", shellQuote(directory))
				}
				link := CurrentDir(op.home) + "." + op.operationID + ".tmp"
				fmt.Fprintf(&script, "ln -sfn %s %s\n", shellQuote(state.PayloadPath), shellQuote(link))
				if op.platform.OS == "darwin" {
					fmt.Fprintf(&script, "mv -fh %s %s\n", shellQuote(link), shellQuote(CurrentDir(op.home)))
				} else {
					fmt.Fprintf(&script, "mv -Tf %s %s\n", shellQuote(link), shellQuote(CurrentDir(op.home)))
				}
			}
		}
		data, err := json.Marshal(state)
		if err != nil {
			return errors.Join(errInvalidResult, err)
		}
		fmt.Fprintf(&script, "mkdir -p %s %s\n", shellQuote(op.home), shellQuote(op.shimDir))
		names := make([]string, 0, len(state.Entrypoints))
		for name, entrypoint := range state.Entrypoints {
			if !isPlainFileName(name) || strings.TrimSpace(entrypoint) == "" {
				return fmt.Errorf("%w: invalid shim %q", errInvalidResult, name)
			}
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			target := path.Join(op.shimDir, name)
			tmp := path.Join(op.shimDir, "."+name+"."+op.operationID+".tmp")
			fmt.Fprintf(&script, "printf '%%s' %s > %s\nchmod 0755 %s\nmv -f %s %s\n", shellQuote(LeasedShimScript(state.Entrypoints[name], op.dep.IsAgent(), payloadlease.EntrypointLockPath(op.dataRoot, op.dep.ID, state.Entrypoints[name]))), shellQuote(tmp), shellQuote(tmp), shellQuote(tmp), shellQuote(target))
		}
		stateTmp := StatePath(op.home) + "." + op.operationID + ".tmp"
		fmt.Fprintf(&script, "printf '%%s' %s > %s\nchmod 0600 %s\nmv -f %s %s\n", shellQuote(string(data)), shellQuote(stateTmp), shellQuote(stateTmp), shellQuote(stateTmp), shellQuote(StatePath(op.home)))
	}
	for _, name := range obsolete {
		if state != nil {
			if _, kept := state.Entrypoints[name]; kept {
				continue
			}
		}
		if isPlainFileName(name) {
			fmt.Fprintf(&script, "rm -f %s\n", shellQuote(path.Join(op.shimDir, name)))
		}
	}
	if state == nil {
		fmt.Fprintf(&script, "rm -f -- %s %s\n", shellQuote(StatePath(op.home)), shellQuote(CurrentDir(op.home)))
	}
	if previous != nil && (state == nil || previous.PayloadPath != state.PayloadPath) {
		cleanup, err := s.queueCleanupScript(ctx, op, previous, false)
		if err != nil {
			return err
		}
		script.WriteString(cleanup)
	}
	return runFilesystemScript(ctx, op.client, op.home, op.dep.ID, script.String())
}

// runFilesystemScript proves the lock-owning command exited before the caller
// can release database ownership. Stream EOF alone never confirms a result.
func runFilesystemScript(ctx context.Context, client *bridge.Client, home, depID, script string) error {
	stream, err := client.ExecStreamWithOptions(ctx, scriptExecCommand, defaultWorkDir, 15, bridge.ExecOptions{Env: []string{
		"MEMOH_DEP_HOME=" + home, "MEMOH_DEP_ID=" + depID,
		"MEMOH_DEP_CONTROL_ROOT=" + controlpath.Directory(path.Dir(home)),
	}})
	if err != nil {
		return errors.Join(ErrOperationUncertain, err)
	}
	defer func() { _ = stream.Close() }()
	if err := stream.SendStdin([]byte(script)); err != nil && !errors.Is(err, io.EOF) {
		return errors.Join(ErrOperationUncertain, err)
	}
	if err := stream.CloseSend(); err != nil && !errors.Is(err, io.EOF) {
		return errors.Join(ErrOperationUncertain, err)
	}
	result, err := forwardOutput(stream, LogFunc(func(string, string) {}))
	if err != nil {
		return errors.Join(ErrOperationUncertain, err)
	}
	if result.code == 76 {
		return ErrBusy
	}
	if result.code != 0 {
		cause := fmt.Errorf("workspace dependency finalization exited %d: %s", result.code, strings.TrimSpace(result.stderrTail))
		if result.code == exitCodeLocked {
			return errors.Join(ErrOperationUncertain, ErrBusy, cause)
		}
		return errors.Join(ErrOperationUncertain, cause)
	}
	return nil
}
