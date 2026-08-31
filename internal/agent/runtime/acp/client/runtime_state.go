package client

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const (
	runtimeStateRoot = "/tmp/memoh-acp-runtime"
	runtimeCacheRoot = "/tmp/memoh-acp-cache"
)

type runtimeLease struct {
	client    *bridge.Client
	root      string
	agentID   string
	botID     string
	agentEnv  []string
	toolEnv   []string
	unsetEnv  []string
	cleanupMu sync.Mutex
	cleaned   bool
}

func prepareRuntimeLease(ctx context.Context, client *bridge.Client, opts processOptions) (*runtimeLease, error) {
	if opts.RuntimeSyncGuard == nil {
		return prepareRuntimeLeaseUnguarded(ctx, client, opts)
	}
	var lease *runtimeLease
	err := opts.RuntimeSyncGuard(ctx, func(guardCtx context.Context) error {
		var prepareErr error
		lease, prepareErr = prepareRuntimeLeaseUnguarded(guardCtx, client, opts)
		return prepareErr
	})
	if err != nil {
		if lease != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = lease.cleanup(cleanupCtx)
		}
		return nil, err
	}
	return lease, nil
}

func prepareRuntimeLeaseUnguarded(ctx context.Context, client *bridge.Client, opts processOptions) (*runtimeLease, error) {
	if client == nil {
		return nil, errors.New("workspace bridge client is required")
	}
	profile, ok := acpprofile.Lookup(opts.AgentID)
	if !ok {
		return nil, fmt.Errorf("ACP profile %q is not registered", opts.AgentID)
	}
	modeName := string(normalizeSetupMode(opts.SetupMode))
	if _, ok := profile.RuntimeStorage.Modes[modeName]; !ok {
		return nil, fmt.Errorf("ACP profile %q has no runtime storage policy for setup mode %q", profile.ID, modeName)
	}
	agentID := acpprofile.NormalizeAgentID(profile.ID)
	if !safeRuntimeAgentID(agentID) {
		return nil, fmt.Errorf("ACP profile %q has an unsafe runtime directory name", profile.ID)
	}

	agentRoot := path.Join(runtimeStateRoot, agentID)
	inspector := &runtimeLease{client: client}
	if err := inspector.ensureSafeDirectory(ctx, "/tmp", agentRoot); err != nil {
		return nil, fmt.Errorf("prepare ACP runtime parent: %w", err)
	}
	root := path.Join(agentRoot, uuid.NewString())
	if err := inspector.ensureSafeDirectory(ctx, "/tmp", root); err != nil {
		return nil, fmt.Errorf("create ACP runtime directory: %w", err)
	}
	lease := &runtimeLease{
		client:  client,
		root:    root,
		agentID: agentID,
		botID:   strings.TrimSpace(opts.BotID),
	}
	abort := func(err error) (*runtimeLease, error) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = lease.cleanup(cleanupCtx)
		return nil, err
	}

	if result, err := client.Exec(ctx, "chmod 0700 "+escapeShellArg(root), dataMountPath, 5); err != nil {
		return abort(fmt.Errorf("secure ACP runtime directory: %w", err))
	} else if result.ExitCode != 0 {
		return abort(fmt.Errorf("secure ACP runtime directory: chmod exited with status %d", result.ExitCode))
	}
	if entry, exists, err := lease.safeEntry(ctx, "/tmp", root); err != nil {
		return abort(fmt.Errorf("validate ACP runtime directory: %w", err))
	} else if !exists || !entry.GetIsDir() {
		return abort(errors.New("ACP runtime path is not a safe directory"))
	}
	agentEnv, toolEnv, unsetEnv, err := lease.buildEnvironments(ctx, opts, profile.RuntimeStorage.AgentEnv)
	if err != nil {
		return abort(err)
	}
	lease.agentEnv = agentEnv
	lease.toolEnv = toolEnv
	lease.unsetEnv = unsetEnv
	return lease, nil
}

func (l *runtimeLease) buildEnvironments(ctx context.Context, opts processOptions, bindings []acpprofile.RuntimeEnvBinding) ([]string, []string, []string, error) {
	ownedNames := runtimeOwnedEnvNames(bindings)
	base := withoutBlockedEnvNames(opts.Env, opts.UnsetEnv)
	base = withoutEnvKeys(base, ownedNames...)
	base = withoutEnvKeys(base, "PATH")

	agentEnv := append([]string(nil), base...)
	agentEnv = append(agentEnv, "PATH="+defaultContainerPath)
	for _, binding := range bindings {
		value := strings.TrimSpace(binding.Value)
		if runtimePath := strings.TrimSpace(binding.RuntimePath); runtimePath != "" {
			value = path.Join(l.root, runtimePath)
			if err := l.ensureSafeDirectory(ctx, l.root, value); err != nil {
				return nil, nil, nil, fmt.Errorf("prepare ACP runtime environment %s: %w", binding.Name, err)
			}
		} else if strings.HasPrefix(value, runtimeCacheRoot+"/") || value == runtimeCacheRoot {
			if err := l.ensureSafeDirectory(ctx, "/tmp", value); err != nil {
				return nil, nil, nil, fmt.Errorf("prepare ACP shared cache for %s: %w", binding.Name, err)
			}
		}
		agentEnv = append(agentEnv, binding.Name+"="+value)
	}

	toolEnv := append([]string(nil), base...)
	toolEnv = append(toolEnv, "HOME="+dataMountPath, "PATH="+defaultContainerPath)
	unsetEnv := mergeEnvNames(opts.UnsetEnv, ownedNames)
	return agentEnv, toolEnv, unsetEnv, nil
}

func runtimeOwnedEnvNames(bindings []acpprofile.RuntimeEnvBinding) []string {
	set := map[string]struct{}{
		"HOME": {},
		"PATH": {},
	}
	for _, binding := range bindings {
		if name := strings.TrimSpace(binding.Name); name != "" {
			set[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func mergeEnvNames(existing, additional []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(additional))
	out := make([]string, 0, len(existing)+len(additional))
	for _, values := range [][]string{existing, additional} {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

// ensureSafeDirectory creates target one path component at a time. Existing
// components are inspected before any mkdir call, and every newly-created
// component is inspected again immediately. This prevents a pre-planted
// symlink below a writable anchor such as /tmp from receiving MkdirAll side
// effects outside the declared runtime/cache root.
func (l *runtimeLease) ensureSafeDirectory(ctx context.Context, anchor, target string) error {
	anchor = path.Clean(anchor)
	target = path.Clean(target)
	if target != anchor && !strings.HasPrefix(target, anchor+"/") {
		return errors.New("directory path escapes its allowed root")
	}
	inspectionAnchor := anchor
	if l != nil && l.root != "" && anchor == path.Clean(l.root) {
		// The UUID directory is not a trust anchor: inspect it through /tmp so
		// replacing the UUID itself with a symlink is still detected.
		inspectionAnchor = "/tmp"
	}
	anchorEntry, exists, err := l.safeEntry(ctx, inspectionAnchor, anchor)
	if err != nil {
		return err
	}
	if !exists || !anchorEntry.GetIsDir() {
		return errors.New("directory anchor is not a directory")
	}
	if target == anchor {
		return nil
	}

	current := anchor
	for _, component := range strings.Split(strings.TrimPrefix(target, anchor+"/"), "/") {
		current = path.Join(current, component)
		entry, exists, err := l.safeEntry(ctx, inspectionAnchor, current)
		if err != nil {
			return err
		}
		if exists {
			if !entry.GetIsDir() {
				return fmt.Errorf("directory path component %q is not a directory", current)
			}
			continue
		}
		if err := l.client.Mkdir(ctx, current); err != nil {
			return err
		}
		entry, exists, err = l.safeEntry(ctx, inspectionAnchor, current)
		if err != nil {
			return err
		}
		if !exists || !entry.GetIsDir() {
			return fmt.Errorf("created directory path component %q is not a safe directory", current)
		}
	}
	return nil
}

func (l *runtimeLease) safeEntry(ctx context.Context, anchor, target string) (*pb.FileEntry, bool, error) {
	anchor = path.Clean(anchor)
	target = path.Clean(target)
	if target != anchor && !strings.HasPrefix(target, anchor+"/") {
		return nil, false, errors.New("artifact path escapes its allowed root")
	}
	if target == anchor {
		entry, err := l.client.Stat(ctx, anchor)
		if errors.Is(err, bridge.ErrNotFound) {
			return nil, false, nil
		}
		return entry, err == nil, err
	}

	current := anchor
	parts := strings.Split(strings.TrimPrefix(target, anchor+"/"), "/")
	for index, part := range parts {
		entries, err := l.client.ListDirAll(ctx, current, false)
		if errors.Is(err, bridge.ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		var found *pb.FileEntry
		for _, entry := range entries {
			if entry.GetPath() == part {
				found = entry
				break
			}
		}
		if found == nil {
			return nil, false, nil
		}
		if isSymlinkMode(found.GetMode()) {
			return nil, false, fmt.Errorf("artifact path contains symbolic link %q", path.Join(current, part))
		}
		if index < len(parts)-1 && !found.GetIsDir() {
			return nil, false, fmt.Errorf("artifact path component %q is not a directory", path.Join(current, part))
		}
		current = path.Join(current, part)
		if index == len(parts)-1 {
			return found, true, nil
		}
	}
	return nil, false, nil
}

func (l *runtimeLease) finalize(ctx context.Context) error {
	if l == nil {
		return nil
	}
	return l.cleanup(ctx)
}

func (l *runtimeLease) cleanup(ctx context.Context) error {
	l.cleanupMu.Lock()
	defer l.cleanupMu.Unlock()
	if l.cleaned {
		return nil
	}
	if !validOwnedRuntimeRoot(l.root, l.agentID) {
		return fmt.Errorf("refusing to remove unsafe ACP runtime path %q", l.root)
	}
	entry, exists, err := l.safeEntry(ctx, "/tmp", l.root)
	if err != nil {
		return fmt.Errorf("validate ACP runtime cleanup path: %w", err)
	}
	if !exists {
		l.cleaned = true
		return nil
	}
	if !entry.GetIsDir() {
		return fmt.Errorf("refusing to remove non-directory ACP runtime path %q", l.root)
	}
	if err := l.client.DeleteFile(ctx, l.root, true); err != nil {
		return err
	}
	l.cleaned = true
	return nil
}

func validOwnedRuntimeRoot(value, agentID string) bool {
	value = path.Clean(value)
	agentRoot := path.Join(runtimeStateRoot, agentID)
	if !strings.HasPrefix(value, agentRoot+"/") || path.Dir(value) != agentRoot {
		return false
	}
	_, err := uuid.Parse(path.Base(value))
	return err == nil
}

func safeRuntimeAgentID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func isSymlinkMode(mode string) bool {
	return strings.HasPrefix(strings.TrimSpace(mode), "L")
}
