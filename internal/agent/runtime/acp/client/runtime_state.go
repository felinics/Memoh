package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
	"github.com/memohai/memoh/internal/workspace/bridge"
	pb "github.com/memohai/memoh/internal/workspace/bridgepb"
)

const (
	runtimeStateRoot       = "/tmp/memoh-acp-runtime"
	runtimeCacheRoot       = "/tmp/memoh-acp-cache"
	runtimeArtifactMaxSize = 8 * 1024 * 1024
	runtimeArtifactMaxFile = 4096
)

var (
	runtimeSyncInterval = 30 * time.Second
	runtimeSyncLocks    sync.Map
)

type runtimeFileVersion struct {
	exists bool
	hash   [sha256.Size]byte
}

type runtimeArtifactSnapshot struct {
	rule           acpprofile.RuntimeArtifact
	persistentPath string
	runtimePath    string
	executable     bool
	baseline       runtimeFileVersion
	runtime        runtimeFileVersion
}

type runtimeLease struct {
	client           *bridge.Client
	logger           *slog.Logger
	root             string
	agentID          string
	botID            string
	mode             acpprofile.RuntimeStorageMode
	sessionRoots     []string
	agentEnv         []string
	toolEnv          []string
	unsetEnv         []string
	snapshots        map[string]*runtimeArtifactSnapshot
	runtimeSyncGuard RuntimeSyncGuard
	syncMu           sync.Mutex
	cleanupMu        sync.Mutex
	cleaned          bool
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
	storageMode, ok := profile.RuntimeStorage.Modes[modeName]
	if !ok {
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
		client:           client,
		logger:           opts.Logger,
		root:             root,
		agentID:          agentID,
		botID:            strings.TrimSpace(opts.BotID),
		mode:             storageMode,
		sessionRoots:     append([]string(nil), profile.RuntimeStorage.SessionRoots...),
		snapshots:        make(map[string]*runtimeArtifactSnapshot),
		runtimeSyncGuard: opts.RuntimeSyncGuard,
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

	for _, generated := range storageMode.Generated {
		if err := lease.writeGenerated(ctx, generated.RuntimePath, []byte(generated.Content)); err != nil {
			return abort(fmt.Errorf("generate ACP runtime file %q: %w", generated.RuntimePath, err))
		}
	}
	for _, artifact := range storageMode.Artifacts {
		if err := lease.stageArtifact(ctx, artifact); err != nil {
			return abort(fmt.Errorf("stage ACP runtime artifact %q: %w", artifact.PersistentPath, err))
		}
	}
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

func (l *runtimeLease) writeGenerated(ctx context.Context, runtimePath string, content []byte) error {
	if !safeLeaseRelativePath(runtimePath) {
		return errors.New("generated path escapes the ACP runtime directory")
	}
	if len(content) > runtimeArtifactMaxSize {
		return fmt.Errorf("generated file exceeds %d bytes", runtimeArtifactMaxSize)
	}
	target := path.Join(l.root, runtimePath)
	if err := l.ensureSafeDirectory(ctx, l.root, path.Dir(target)); err != nil {
		return err
	}
	_, err := l.client.WriteRaw(ctx, target, bytes.NewReader(content))
	return err
}

func (l *runtimeLease) stageArtifact(ctx context.Context, artifact acpprofile.RuntimeArtifact) error {
	switch artifact.Kind {
	case acpprofile.RuntimeArtifactFile:
		return l.stageFile(ctx, artifact, artifact.PersistentPath, artifact.RuntimePath, false)
	case acpprofile.RuntimeArtifactTree:
		return l.stageTree(ctx, artifact)
	default:
		return fmt.Errorf("unsupported artifact kind %q", artifact.Kind)
	}
}

func (l *runtimeLease) stageTree(ctx context.Context, artifact acpprofile.RuntimeArtifact) error {
	sourceRoot := path.Join(dataMountPath, artifact.PersistentPath)
	entry, exists, err := l.safeEntry(ctx, dataMountPath, sourceRoot)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !entry.GetIsDir() {
		return errors.New("persistent tree is not a directory")
	}
	entries, err := l.client.ListDirAll(ctx, sourceRoot, true)
	if err != nil {
		return err
	}
	if len(entries) > runtimeArtifactMaxFile {
		return fmt.Errorf("persistent tree contains %d entries; limit is %d", len(entries), runtimeArtifactMaxFile)
	}
	for _, child := range entries {
		rel := cleanListedRelativePath(child.GetPath())
		if rel == "" {
			return fmt.Errorf("persistent tree returned unsafe path %q", child.GetPath())
		}
		if isSymlinkMode(child.GetMode()) {
			return fmt.Errorf("persistent tree contains symbolic link %q", child.GetPath())
		}
		if child.GetIsDir() {
			if err := l.ensureSafeDirectory(ctx, l.root, path.Join(l.root, artifact.RuntimePath, rel)); err != nil {
				return err
			}
			continue
		}
		if child.GetSize() > runtimeArtifactMaxSize {
			return fmt.Errorf("persistent file %q exceeds %d bytes", child.GetPath(), runtimeArtifactMaxSize)
		}
		if err := l.stageFile(ctx, artifact,
			path.Join(artifact.PersistentPath, rel),
			path.Join(artifact.RuntimePath, rel),
			isExecutableMode(child.GetMode())); err != nil {
			return err
		}
	}
	return nil
}

func (l *runtimeLease) stageFile(ctx context.Context, rule acpprofile.RuntimeArtifact, persistentRel, runtimeRel string, executable bool) error {
	persistentPath := path.Join(dataMountPath, persistentRel)
	runtimePath := path.Join(l.root, runtimeRel)
	data, exists, err := l.readOptionalSafeFile(ctx, dataMountPath, persistentPath)
	if err != nil {
		return err
	}
	baseline := versionOf(data, exists)
	if exists {
		data = filterRuntimeArtifact(rule.Filter, data)
		if err := l.ensureSafeDirectory(ctx, l.root, path.Dir(runtimePath)); err != nil {
			return err
		}
		if _, err := l.client.WriteRaw(ctx, runtimePath, bytes.NewReader(data)); err != nil {
			return err
		}
		if executable {
			if err := l.chmodExecutable(ctx, runtimePath); err != nil {
				return err
			}
		}
	}
	if rule.Sync != acpprofile.RuntimeSyncNone {
		l.snapshots[runtimePath] = &runtimeArtifactSnapshot{
			rule:           rule,
			persistentPath: persistentPath,
			runtimePath:    runtimePath,
			executable:     executable,
			baseline:       baseline,
			runtime:        versionOf(data, exists),
		}
	}
	return nil
}

func (l *runtimeLease) Sync(ctx context.Context) error {
	return l.withRuntimeSyncGuard(ctx, func(guardCtx context.Context) error {
		return l.sync(guardCtx, true, nil)
	})
}

// syncLiveState refreshes Codex OAuth credentials. Full compare-and-swap
// trees are synchronized only at process exit.
func (l *runtimeLease) syncLiveState(ctx context.Context) error {
	return l.withRuntimeSyncGuard(ctx, func(guardCtx context.Context) error {
		return l.sync(guardCtx, false, func(snapshot *runtimeArtifactSnapshot) bool {
			return snapshot != nil && snapshot.rule.Sync == acpprofile.RuntimeSyncCodexAuth
		})
	})
}

func (l *runtimeLease) withRuntimeSyncGuard(ctx context.Context, fn func(context.Context) error) error {
	if l == nil {
		return nil
	}
	if l.runtimeSyncGuard == nil {
		return fn(ctx)
	}
	// Keep the database guard outside syncMu/runtimeSyncLock. Reset
	// publication also takes the database bot lock before touching workspace
	// state; reversing that order here would create a distributed deadlock.
	return l.runtimeSyncGuard(ctx, fn)
}

func (l *runtimeLease) sync(
	ctx context.Context,
	discoverTrees bool,
	include func(*runtimeArtifactSnapshot) bool,
) error {
	if l == nil {
		return nil
	}
	l.syncMu.Lock()
	defer l.syncMu.Unlock()
	l.cleanupMu.Lock()
	cleaned := l.cleaned
	l.cleanupMu.Unlock()
	if cleaned {
		return nil
	}
	lock := runtimeSyncLock(l.botID + "|" + l.agentID)
	lock.Lock()
	defer lock.Unlock()

	var syncErrors []error
	if discoverTrees {
		if err := l.discoverRuntimeTreeFiles(ctx); err != nil {
			syncErrors = append(syncErrors, err)
		}
	}
	keys := make([]string, 0, len(l.snapshots))
	for key, snapshot := range l.snapshots {
		if include != nil && !include(snapshot) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := l.syncFile(ctx, l.snapshots[key]); err != nil {
			syncErrors = append(syncErrors, err)
		}
	}
	return errors.Join(syncErrors...)
}

func (l *runtimeLease) discoverRuntimeTreeFiles(ctx context.Context) error {
	for _, rule := range l.mode.Artifacts {
		if rule.Kind != acpprofile.RuntimeArtifactTree || rule.Sync == acpprofile.RuntimeSyncNone {
			continue
		}
		runtimeRoot := path.Join(l.root, rule.RuntimePath)
		entry, exists, err := l.safeEntry(ctx, l.root, runtimeRoot)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if !entry.GetIsDir() {
			return fmt.Errorf("runtime artifact tree %q is not a directory", rule.RuntimePath)
		}
		entries, err := l.client.ListDirAll(ctx, runtimeRoot, true)
		if err != nil {
			return err
		}
		if len(entries) > runtimeArtifactMaxFile {
			return fmt.Errorf("runtime artifact tree %q contains %d entries; limit is %d", rule.RuntimePath, len(entries), runtimeArtifactMaxFile)
		}
		for _, child := range entries {
			rel := cleanListedRelativePath(child.GetPath())
			if rel == "" || isSymlinkMode(child.GetMode()) {
				return fmt.Errorf("runtime artifact tree %q contains unsafe path %q", rule.RuntimePath, child.GetPath())
			}
			if child.GetIsDir() {
				continue
			}
			runtimePath := path.Join(runtimeRoot, rel)
			if _, known := l.snapshots[runtimePath]; known {
				continue
			}
			persistentPath := path.Join(dataMountPath, rule.PersistentPath, rel)
			l.snapshots[runtimePath] = &runtimeArtifactSnapshot{
				rule:           rule,
				persistentPath: persistentPath,
				runtimePath:    runtimePath,
				executable:     isExecutableMode(child.GetMode()),
				// The file did not exist when this lease staged the tree. Treat a
				// now-existing durable file as a concurrent create instead of
				// adopting and overwriting it as this process's baseline.
				baseline: runtimeFileVersion{},
				runtime:  runtimeFileVersion{},
			}
		}
	}
	return nil
}

func (l *runtimeLease) syncFile(ctx context.Context, snapshot *runtimeArtifactSnapshot) error {
	data, exists, err := l.readOptionalSafeFile(ctx, l.root, snapshot.runtimePath)
	if err != nil {
		return fmt.Errorf("read runtime artifact %q: %w", snapshot.rule.RuntimePath, err)
	}
	if !exists {
		// Durable deletes are intentionally not propagated. A missing runtime
		// file can result from a failed agent rewrite and must not erase the last
		// known-good credential or configuration.
		return nil
	}
	data = filterRuntimeArtifact(snapshot.rule.Filter, data)
	runtimeVersion := versionOf(data, true)
	if runtimeVersion == snapshot.runtime {
		return nil
	}

	persistentData, persistentExists, err := l.readOptionalSafeFile(ctx, dataMountPath, snapshot.persistentPath)
	if err != nil {
		return fmt.Errorf("read durable artifact %q: %w", snapshot.rule.PersistentPath, err)
	}
	persistentVersion := versionOf(persistentData, persistentExists)
	if persistentVersion == runtimeVersion {
		snapshot.baseline = persistentVersion
		snapshot.runtime = runtimeVersion
		return nil
	}

	switch snapshot.rule.Sync {
	case acpprofile.RuntimeSyncCodexAuth:
		write, err := shouldWriteCodexAuth(data, persistentData, persistentExists, snapshot.baseline == persistentVersion)
		if err != nil {
			return fmt.Errorf("compare Codex auth freshness: %w", err)
		}
		if !write {
			snapshot.baseline = persistentVersion
			snapshot.runtime = runtimeVersion
			return nil
		}
	case acpprofile.RuntimeSyncCompareAndSwap:
		if snapshot.baseline != persistentVersion {
			return fmt.Errorf("durable ACP artifact %q changed concurrently; runtime copy was not written", snapshot.rule.PersistentPath)
		}
	default:
		return nil
	}

	if err := l.ensureSafeWriteParent(ctx, snapshot.persistentPath); err != nil {
		return fmt.Errorf("validate durable artifact %q: %w", snapshot.rule.PersistentPath, err)
	}
	if _, err := l.client.WriteRaw(ctx, snapshot.persistentPath, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write durable ACP artifact %q: %w", snapshot.rule.PersistentPath, err)
	}
	if snapshot.executable {
		if err := l.chmodExecutable(ctx, snapshot.persistentPath); err != nil {
			return fmt.Errorf("restore executable mode for durable ACP artifact %q: %w", snapshot.rule.PersistentPath, err)
		}
	}
	snapshot.baseline = runtimeVersion
	snapshot.runtime = runtimeVersion
	return nil
}

func (l *runtimeLease) chmodExecutable(ctx context.Context, target string) error {
	result, err := l.client.Exec(ctx, "chmod 0700 "+escapeShellArg(target), dataMountPath, 5)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("chmod exited with status %d", result.ExitCode)
	}
	return nil
}

func shouldWriteCodexAuth(runtimeData, persistentData []byte, persistentExists, persistentMatchesBaseline bool) (bool, error) {
	runtimeRefresh, runtimeHasRefresh, err := codexAuthLastRefresh(runtimeData)
	if err != nil {
		return false, fmt.Errorf("runtime auth.json: %w", err)
	}
	if !persistentExists {
		return runtimeHasRefresh || persistentMatchesBaseline, nil
	}
	persistentRefresh, persistentHasRefresh, err := codexAuthLastRefresh(persistentData)
	if err != nil {
		return false, fmt.Errorf("durable auth.json: %w", err)
	}
	if runtimeHasRefresh && persistentHasRefresh {
		return runtimeRefresh.After(persistentRefresh), nil
	}
	if runtimeHasRefresh && !persistentHasRefresh {
		return true, nil
	}
	if !runtimeHasRefresh && persistentHasRefresh {
		return false, nil
	}
	return persistentMatchesBaseline, nil
}

func codexAuthLastRefresh(data []byte) (time.Time, bool, error) {
	var document struct {
		LastRefresh string `json:"last_refresh"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return time.Time{}, false, err
	}
	value := strings.TrimSpace(document.LastRefresh)
	if value == "" {
		return time.Time{}, false, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false, err
	}
	return parsed, true, nil
}

func (l *runtimeLease) ensureSafeWriteParent(ctx context.Context, target string) error {
	parent := path.Dir(target)
	if err := l.ensureSafeDirectory(ctx, dataMountPath, parent); err != nil {
		return err
	}
	entry, exists, err := l.safeEntry(ctx, dataMountPath, target)
	if err != nil {
		return err
	}
	if exists && entry.GetIsDir() {
		return errors.New("durable artifact target is a directory")
	}
	return nil
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

func (l *runtimeLease) readOptionalSafeFile(ctx context.Context, anchor, target string) ([]byte, bool, error) {
	entry, exists, err := l.safeEntry(ctx, anchor, target)
	if err != nil || !exists {
		return nil, exists, err
	}
	if entry.GetIsDir() {
		return nil, false, errors.New("artifact path is a directory")
	}
	if entry.GetSize() > runtimeArtifactMaxSize {
		return nil, false, fmt.Errorf("artifact exceeds %d bytes", runtimeArtifactMaxSize)
	}
	reader, err := l.client.ReadRaw(ctx, target)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, runtimeArtifactMaxSize+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > runtimeArtifactMaxSize {
		return nil, false, fmt.Errorf("artifact exceeds %d bytes", runtimeArtifactMaxSize)
	}
	return data, true, nil
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

func filterRuntimeArtifact(filter acpprofile.RuntimeArtifactFilter, data []byte) []byte {
	if filter != acpprofile.RuntimeArtifactFilterDotEnv {
		return append([]byte(nil), data...)
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		candidate := strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
		name, _, hasValue := strings.Cut(candidate, "=")
		if hasValue && blockedRuntimeDotEnvName(strings.TrimSpace(name)) {
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

func blockedRuntimeDotEnvName(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "HOME" || name == "PATH" || name == "TMP" || name == "TEMP" || name == "TMPDIR" {
		return true
	}
	for _, prefix := range []string{"CODEX_", "CLAUDE_CONFIG_", "HERMES_", "MEMOH_HERMES_", "UV_", "NPM_CONFIG_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (l *runtimeLease) finalize(ctx context.Context, commit bool) error {
	if l == nil {
		return nil
	}
	if !commit {
		return l.cleanup(ctx)
	}
	if err := l.Sync(ctx); err != nil {
		if errors.Is(err, ErrRuntimeSyncGuardRejected) {
			// This generation is forbidden from publishing. Its UUID-owned
			// process state is no longer recoverable and retaining it on every
			// reset would leak runtime directories indefinitely.
			return l.cleanup(ctx)
		}
		// Preserve this one UUID directory for manual recovery when durable
		// credential synchronization failed.
		return err
	}
	return l.cleanup(ctx)
}

func (l *runtimeLease) cleanup(ctx context.Context) error {
	// Serialize deletion with Sync. Without this guard, a prompt completion
	// racing natural process exit could recreate the UUID directory after the
	// finalizer removed it.
	l.syncMu.Lock()
	defer l.syncMu.Unlock()
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

func runtimeSyncLock(key string) *sync.Mutex {
	value, _ := runtimeSyncLocks.LoadOrStore(key, &sync.Mutex{})
	return value.(*sync.Mutex)
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

func safeLeaseRelativePath(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	cleaned := path.Clean(value)
	return value != "" && value == cleaned && cleaned != "." && cleaned != ".." &&
		!strings.HasPrefix(cleaned, "/") && !strings.HasPrefix(cleaned, "../")
}

func cleanListedRelativePath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if !safeLeaseRelativePath(value) {
		return ""
	}
	return value
}

func isSymlinkMode(mode string) bool {
	return strings.HasPrefix(strings.TrimSpace(mode), "L")
}

func isExecutableMode(mode string) bool {
	return strings.Contains(strings.TrimSpace(mode), "x")
}

func versionOf(data []byte, exists bool) runtimeFileVersion {
	if !exists {
		return runtimeFileVersion{}
	}
	return runtimeFileVersion{exists: true, hash: sha256.Sum256(data)}
}
