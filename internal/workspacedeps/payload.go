package workspacedeps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/payloadlease"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// effectiveStoreRoot uses the fixed distribution layout. Agent Homes stay persistent.
func (s *Service) effectiveStoreRoot(dataRoot string) string {
	if s.dependencyStoreRoot != "" {
		return s.dependencyStoreRoot
	}
	return DepsRoot(dataRoot)
}

// Validate persisted cleanup paths before they are used for deletion.
func validateDependencyStoreRoot(root string) error {
	if root == "" {
		return nil
	}
	if !path.IsAbs(root) || path.Clean(root) != root || strings.ContainsAny(root, "\x00\r\n") {
		return errors.New("dependency store root must be a clean absolute sandbox path")
	}
	for _, protected := range []string{"/", "/bin", "/sbin", "/usr", "/etc", "/proc", "/sys", "/dev", "/opt/memoh/toolkit"} {
		if root == protected || strings.HasPrefix(root, protected+"/") || strings.HasPrefix(protected, root+"/") {
			return errors.New("dependency store root overlaps a protected directory")
		}
	}
	return nil
}

func (s *Service) validateProvision(ctx context.Context, op *operation, previous *State) error {
	if err := s.validateDesiredOperation(ctx, op); err != nil {
		return err
	}
	if op.dep.StorageLayout == "isolated" || previous == nil || (op.version != "" && op.version != previous.Version) {
		return nil
	}
	// Old recipes can overwrite versions/<version> in place. They remain usable
	// for missing copies, but a live same-version replacement needs a reviewed
	// isolated recipe, not an unannounced change to MEMOH_DEP_HOME.
	for _, executable := range previous.Entrypoints {
		result, err := op.client.ExecWithOptions(ctx, "test -x "+shellQuote(executable), "", 10, nil, bridge.ExecOptions{})
		if err != nil {
			return err
		}
		if result.ExitCode == 0 {
			return ErrLegacyReinstallUnsafe
		}
	}
	return nil
}

func (s *Service) installationState(ctx context.Context, op *operation, state *State) error {
	if op.receipt == nil {
		return nil
	}
	state.FormatVersion = 2
	state.InstallationID = op.operationID
	state.StoreRoot = op.storeRoot
	state.DesiredRevision = op.desiredRevision
	if !op.repair {
		state.DesiredRevision = op.operationID
	}
	if op.dep.StorageLayout != "isolated" {
		// Legacy publication is kept readable. Its recipe owns the switch, and the
		// path is accepted only beneath that dependency's legacy versions directory.
		result, err := op.client.ExecWithOptions(ctx, "readlink "+shellQuote(CurrentDir(op.home)), "", 10, nil, bridge.ExecOptions{})
		if err != nil {
			return err
		}
		if result.ExitCode == 0 {
			candidate := strings.TrimSpace(result.Stdout)
			if !path.IsAbs(candidate) {
				candidate = path.Join(op.home, candidate)
			}
			if path.Dir(candidate) == VersionsDir(op.home) && isPlainFileName(path.Base(candidate)) {
				state.PayloadPath = candidate
				state.StoreRoot = DepsRoot(op.dataRoot)
			}
		}
		return nil
	}
	expected := path.Join(op.storeRoot, op.dep.ID, "installs", op.operationID)
	if op.restorePayloadPath != "" {
		store, ok := s.store.(DesiredStore)
		if !ok || !op.repair {
			return ErrDesiredTargetChanged
		}
		target, err := store.GetDesired(ctx, op.key)
		if err != nil {
			return err
		}
		if target.Revision != op.desiredRevision || target.InstallationID != op.restoreInstallationID || target.PayloadPath != op.restorePayloadPath {
			return ErrDesiredTargetChanged
		}
		expected = op.restorePayloadPath
		state.InstallationID = op.restoreInstallationID
	}
	candidate, err := readReceiptFile(ctx, op.client, path.Join(op.receipt.Directory, "candidate"), 4096)
	if err != nil {
		return errors.Join(errInvalidResult, fmt.Errorf("read published candidate: %w", err))
	}
	if strings.TrimSpace(string(candidate)) != expected {
		return fmt.Errorf("%w: recipe did not publish its isolated candidate", errInvalidResult)
	}
	state.PayloadPath = expected
	for name, executable := range state.Entrypoints {
		current := CurrentDir(op.home)
		if strings.HasPrefix(executable, current+"/") {
			executable = expected + strings.TrimPrefix(executable, current)
		}
		if !strings.HasPrefix(executable, expected+"/") || path.Clean(executable) != executable {
			return fmt.Errorf("%w: entrypoint outside installation", errInvalidResult)
		}
		state.Entrypoints[name] = executable
	}
	for _, command := range op.dep.Provides {
		if state.Entrypoints[command] == "" {
			return fmt.Errorf("%w: missing declared entrypoint %s", errInvalidResult, command)
		}
	}
	script, configured := op.catalog.Script(op.dep.ID, catalog.ActionVersion)
	if op.frozenDefinition != nil {
		script, configured = op.frozenDefinition.Script(catalog.ActionVersion)
	}
	version, err := s.probeInstallationVersionScript(ctx, op.client, op.dep, op.home, op.dataRoot, state.Entrypoints[op.dep.Provides[0]], op.platform, script, configured)
	if err != nil {
		return errors.Join(errInvalidResult, err)
	}
	if version != state.Version {
		return fmt.Errorf("%w: candidate version does not match result", errInvalidResult)
	}
	return nil
}

// Management and repair use the frozen definition's probe when supplied. The
// default matches discovery, but rejects missing loaders and wrong versions
// instead of falling back to the recipe's claimed state.
func (s *Service) probeInstallationVersion(ctx context.Context, client *bridge.Client, cat *catalog.Catalog, dep catalog.Dependency, home, dataRoot, candidate string, platform Platform) (string, error) {
	script, configured := cat.Script(dep.ID, catalog.ActionVersion)
	return s.probeInstallationVersionScript(ctx, client, dep, home, dataRoot, candidate, platform, script, configured)
}

func (s *Service) probeInstallationVersionScript(ctx context.Context, client *bridge.Client, dep catalog.Dependency, home, dataRoot, candidate string, platform Platform, script string, configured bool) (string, error) {
	if configured {
		result, err := s.run(ctx, client, RunSpec{DepID: dep.ID, Action: catalog.ActionVersion, Script: script, Home: home, ShimDir: ShimDir(dataRoot), Candidate: candidate, Platform: platform, Timeout: dep.Timeouts.Duration(catalog.ActionVersion)}, nil)
		return strings.TrimSpace(result.Version), err
	}
	probeCtx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()
	result, err := client.ExecWithOptions(probeCtx, "exec "+shellQuote(candidate)+" --version", defaultWorkDir, int32(versionProbeTimeout/time.Second), nil, bridge.ExecOptions{})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("candidate version probe exited %d", result.ExitCode)
	}
	return extractVersion(result.Stdout + "\n" + result.Stderr), nil
}

// PayloadCleanup is a pending transaction cleanup, never a selectable past
// installation. Its durable file survives both Server restart and rootfs loss.
type PayloadCleanup struct {
	RetirementEpoch string `json:"retirement_epoch,omitempty"`
	Unpublished     bool   `json:"unpublished,omitempty"`
	OperationID     string `json:"operation_id"`
	DependencyID    string `json:"dependency_id"`
	StoreRoot       string `json:"store_root"`
	PayloadPath     string `json:"payload_path"`
}

func cleanupPath(op *operation) string {
	return path.Join(operationRoot(op.home, op.dep.ID), ".cleanup", op.operationID+".json")
}

func cleanupFor(op *operation, previous *State) *PayloadCleanup {
	if previous == nil || previous.PayloadPath == "" {
		return nil
	}
	root := previous.StoreRoot
	// Only the currently configured namespace or the original metadata namespace
	// can be reclaimed without an operator-confirmed storage migration.
	if root != op.storeRoot && root != DepsRoot(op.dataRoot) {
		return nil
	}
	parent := path.Join(root, op.dep.ID, "installs")
	legacy := VersionsDir(op.home)
	candidate := previous.PayloadPath
	if path.Dir(candidate) == parent && validReceiptID(path.Base(candidate)) || path.Dir(candidate) == legacy && isPlainFileName(path.Base(candidate)) {
		return &PayloadCleanup{OperationID: op.operationID, DependencyID: op.dep.ID, StoreRoot: root, PayloadPath: candidate}
	}
	return nil
}

func cleanupScript(home string, job PayloadCleanup, jobFile string, quiescent bool) (string, error) {
	if !validReceiptID(job.OperationID) || !isPlainFileName(job.DependencyID) || validateDependencyStoreRoot(job.StoreRoot) != nil {
		return "", errors.New("invalid payload cleanup identity")
	}
	parent := path.Join(job.StoreRoot, job.DependencyID, "installs")
	legacy := VersionsDir(home)
	validPath := path.Dir(job.PayloadPath) == parent && validReceiptID(path.Base(job.PayloadPath)) || path.Dir(job.PayloadPath) == legacy && isPlainFileName(path.Base(job.PayloadPath))
	if !validPath {
		return "", errors.New("invalid payload cleanup path")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "set -eu\npayload=%s\njob=%s\n", shellQuote(job.PayloadPath), shellQuote(jobFile))
	// Reject symlinked ancestors, including a replaced store root; never traverse
	// an arbitrary workspace-controlled path while deleting recursively.
	for directory := path.Dir(job.PayloadPath); directory != "/"; directory = path.Dir(directory) {
		fmt.Fprintf(&b, "[ ! -L %s ] || exit 0\n", shellQuote(directory))
	}
	// Rootfs loss already removed the payload; its durable queue entry can
	// be acknowledged without claiming that any running process was drained.
	b.WriteString("if [ ! -e \"$payload\" ] && [ ! -L \"$payload\" ]; then rm -f -- \"$job\"; exit 0; fi\n")
	fmt.Fprintf(&b, "current=$(cd %s 2>/dev/null && pwd -P || true)\ncase \"$current\" in \"$payload\"|\"$payload\"/*) exit 0 ;; esac\n", shellQuote(CurrentDir(home)))
	// Inspection cannot prove that a process has not already resolved an old
	// path or retained an interpreter/module reference. Published payloads wait
	// for a whole workspace restart, never a timeout or an empty /proc scan.
	if !job.Unpublished {
		if !quiescent || job.RetirementEpoch == "" {
			return b.String() + "exit 0\n", nil
		}
		fmt.Fprintf(&b, "epoch=$(\n%s)\n[ -n \"$epoch\" ] && [ \"$epoch\" != %s ] || exit 0\n", payloadlease.EpochCommand, shellQuote(job.RetirementEpoch))
	}
	lock := payloadlease.LockPath(path.Dir(path.Dir(path.Dir(home))), job.DependencyID)
	for directory := path.Dir(lock); directory != "/"; directory = path.Dir(directory) {
		fmt.Fprintf(&b, "[ ! -L %s ] || exit 0\n", shellQuote(directory))
	}
	fmt.Fprintf(&b, "[ ! -L %s ] || exit 0\n", shellQuote(lock))
	fmt.Fprintf(&b, "if [ \"$(uname -s)\" = Linux ]; then\nmkdir -p %s\nexec 8>> %s\nflock -xn 8 || exit 0\nfi\n", shellQuote(path.Dir(lock)), shellQuote(lock))
	payloadLock := payloadlease.EntrypointLockPath(path.Dir(path.Dir(path.Dir(home))), job.DependencyID, path.Join(job.PayloadPath, "entrypoint"))
	if payloadLock != lock {
		fmt.Fprintf(&b, "[ ! -L %s ] || exit 0\n", shellQuote(payloadLock))
		fmt.Fprintf(&b, "if [ \"$(uname -s)\" = Linux ]; then\nexec 7>> %s\nflock -xn 7 || exit 0\nfi\n", shellQuote(payloadLock))
	}
	if !job.Unpublished {
		b.WriteString(payloadReferenceGuard)
	}
	b.WriteString("rm -rf -- \"$payload\"\nrm -f -- \"$job\"\n")
	return b.String(), nil
}

func (s *Service) cleanPreviousPayload(ctx context.Context, op *operation) {
	if err := s.cleanPayload(ctx, op, false); err != nil {
		s.logger.Warn("clean obsolete dependency payload", slog.String("dependency_id", op.dep.ID), slog.Any("error", err))
	}
}

func (s *Service) cleanPayload(ctx context.Context, op *operation, quiescent bool) error {
	file := cleanupPath(op)
	data, err := readReceiptFile(ctx, op.client, file, 8192)
	if errors.Is(err, bridge.ErrNotFound) {
		return nil
	}
	if err != nil {
		s.logger.Warn("read pending payload cleanup", slog.Any("error", err))
		return nil
	}
	var job PayloadCleanup
	if err := json.Unmarshal(data, &job); err != nil || job.OperationID != op.operationID || job.DependencyID != op.dep.ID {
		return nil
	}
	if !job.Unpublished && job.RetirementEpoch == "" && !quiescent {
		// Pre-lease cleanup records have no lifetime evidence. Start a fresh
		// retirement fence; they become collectible only after the next restart.
		epoch, epochErr := payloadlease.ReadEpoch(ctx, op.client)
		if epochErr != nil || epoch == "" {
			script, scriptErr := cleanupScript(op.home, job, file, false)
			if scriptErr != nil {
				return scriptErr
			}
			return runFilesystemScript(ctx, op.client, op.home, op.dep.ID, script)
		}
		job.RetirementEpoch = epoch
		metadata, marshalErr := json.Marshal(job)
		if marshalErr != nil {
			return nil
		}
		script := fmt.Sprintf("set -eu\nprintf '%%s' %s > %s\n", shellQuote(string(metadata)), shellQuote(file))
		_ = runFilesystemScript(ctx, op.client, op.home, op.dep.ID, script)
		return nil
	}
	script, err := cleanupScript(op.home, job, file, quiescent)
	if err != nil {
		return err
	}
	if !quiescent {
		return runFilesystemScript(ctx, op.client, op.home, op.dep.ID, script)
	}
	// Hold the same per-dependency transaction lock as install/remove while
	// checking current and deleting, inside the admitted maintenance process.
	lock := executionLockPath(op.home, op.dep.ID, "linux")
	guard := fmt.Sprintf("set -eu\n[ ! -L %s ] && [ ! -L %s ] || exit 0\nmkdir -p %s\nexec 6>> %s\nflock -xn 6 || exit 0\n", shellQuote(path.Dir(lock)), shellQuote(lock), shellQuote(path.Dir(lock)), shellQuote(lock))
	result, err := op.client.ExecQuiescent(ctx, guard+script)
	if err == nil && result.ExitCode != 0 {
		err = fmt.Errorf("payload cleanup exited %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return err
}

func (*Service) queueCleanupScript(ctx context.Context, op *operation, previous *State, unpublished bool) (string, error) {
	job := cleanupFor(op, previous)
	if job == nil {
		return "", nil
	}
	job.Unpublished = unpublished
	if !unpublished {
		epoch, err := payloadlease.ReadEpoch(ctx, op.client)
		if err != nil {
			return "", err
		}
		job.RetirementEpoch = epoch
	}
	data, err := json.Marshal(job)
	if err != nil {
		return "", err
	}
	file := cleanupPath(op)
	return fmt.Sprintf("mkdir -p %s\nprintf '%%s' %s > %s\n", shellQuote(path.Dir(file)), shellQuote(string(data)), shellQuote(file)), nil
}

// ReapPayloads inspects running workspaces without starting them. It reclaims
// unpublished candidates and acknowledges already missing payloads. Published
// payloads remain queued until a controlled workspace startup window.
func (s *Service) ReapPayloads(ctx context.Context, botID string) error {
	if state, err := s.workspace.State(ctx, botID); err != nil || state != WorkspaceRunning {
		return err
	}
	client, root, err := s.target(ctx, botID)
	if err != nil {
		return err
	}
	return s.reapPayloads(ctx, botID, client, root, false)
}

// ReapPayloadsAtStartup uses the connected bridge directly to avoid recursive
// workspace startup. A bridge that cannot prove quiescence retains the queue.
func (s *Service) ReapPayloadsAtStartup(ctx context.Context, botID string, client *bridge.Client) error {
	root, err := s.workspace.DataRoot(ctx, botID)
	if err != nil {
		return err
	}
	return s.reapPayloads(ctx, botID, client, root, true)
}

func (s *Service) reapPayloads(ctx context.Context, botID string, client *bridge.Client, root string, quiescent bool) error {
	command := "find " + shellQuote(path.Join(DepsRoot(root), ".operations")) + " -path '*/.cleanup/*.json' -type f -print\n"
	var result *bridge.ExecResult
	var err error
	if quiescent {
		// Old bridges do not acknowledge the header. Bound their empty-shell wait
		// without ever sending the destructive body or delaying startup for long.
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		result, err = client.ExecQuiescent(probeCtx, command)
		cancel()
	} else {
		result, err = client.ExecWithOptions(ctx, command, "", 10, nil, bridge.ExecOptions{})
	}
	if errors.Is(err, bridge.ErrPayloadWindowClosed) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, file := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		depID := path.Base(path.Dir(path.Dir(file)))
		operationID := strings.TrimSuffix(path.Base(file), ".json")
		if !isPlainFileName(depID) || !validReceiptID(operationID) {
			continue
		}
		key := InstallationKey{BotID: botID, DependencyID: depID}
		rec, err := s.store.Get(ctx, key)
		if err != nil && !errors.Is(err, ErrInstallationNotFound) {
			continue
		}
		if rec.Status.InProgress() {
			continue
		}
		op := &operation{key: key, dep: catalog.Dependency{ID: depID}, client: client, dataRoot: root, home: Home(root, depID), operationID: operationID}
		cleanupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = s.cleanPayload(cleanupCtx, op, quiescent)
		cancel()
		if errors.Is(err, bridge.ErrPayloadWindowClosed) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// A startup window excludes bridge command admission. Also refuse deletion
// when bootstrap processes reference the payload or /proc access is incomplete:
// unreadable cross-UID process state is not evidence that a payload is unused.
const payloadReferenceGuard = `for proc in /proc/[0-9]*; do
  [ -d "$proc" ] || continue
  process_state=$(awk '{sub(/^.*\) /, ""); print $1}' "$proc/stat" 2>/dev/null) || { [ ! -d "$proc" ] && continue; exit 0; }
  # Zombies have already released their address space and file references.
  case "$process_state" in Z|X) continue ;; '') exit 0 ;; esac
  if [ ! -r "$proc/maps" ] || [ ! -r "$proc/cmdline" ] || [ ! -r "$proc/fd" ] || [ ! -x "$proc/fd" ]; then
    [ ! -d "$proc" ] && continue
    exit 0
  fi
  if grep -F "$payload/" "$proc/maps" >/dev/null 2>&1; then exit 0; else
    rc=$?; [ "$rc" -eq 1 ] || { [ ! -d "$proc" ] && continue; exit 0; }
  fi
  args=$(tr '\000' ' ' < "$proc/cmdline") || { [ ! -d "$proc" ] && continue; exit 0; }
  case "$args" in *"$payload"*) exit 0 ;; esac
  for link in "$proc/exe" "$proc/cwd" "$proc"/fd/*; do
    if target=$(readlink "$link" 2>/dev/null); then
      case "$target" in "$payload"|"$payload"/*) exit 0 ;; esac
    elif [ -L "$link" ]; then
      [ ! -d "$proc" ] && break
      exit 0
    fi
  done
done
`

// restoreDesiredEntrypoints republishes an already verified desired payload
// through the same receipt and CAS transaction without running a download.
func (s *Service) restoreDesiredEntrypoints(ctx context.Context, target DesiredInstallation, cat *catalog.Catalog) error {
	ctx = context.WithValue(ctx, repairTargetKey{}, target)
	ctx = context.WithValue(ctx, frozenRepairCatalogKey{}, cat)
	op, err := s.begin(ctx, target.BotID, target.DependencyID, target.Version, true)
	if err != nil {
		return err
	}
	defer op.release()
	if op.dep.StorageLayout != "isolated" {
		return ErrLegacyReinstallUnsafe
	}
	op.restorePayloadPath, op.restoreInstallationID = target.PayloadPath, target.InstallationID
	op.storeRoot = target.StoreRoot
	previous := s.readStateBestEffort(ctx, op)
	if err := s.markInProgress(ctx, op, StatusInstalling, catalog.ActionReinstall); err != nil {
		return err
	}
	entries := cloneStringMap(target.Entrypoints)
	resultData, err := json.Marshal(map[string]any{"version": target.Version, "entrypoints": entries})
	if err != nil {
		return s.fail(ctx, op, err)
	}
	script := "dep_switch " + shellQuote(target.PayloadPath) + "\ndep_result " + shellQuote(string(resultData)) + "\n"
	result, err := s.runScript(ctx, op, catalog.ActionReinstall, script, target.Version, target.Version, 0, nil)
	if err != nil {
		return s.fail(ctx, op, err)
	}
	_, err = s.commit(ctx, op, catalog.ActionReinstall, result, previous)
	return err
}

func (s *Service) discardFailedPayload(ctx context.Context, op *operation) {
	if op.receipt == nil || !op.receipt.Completed || op.dep.StorageLayout != "isolated" || op.restorePayloadPath != "" {
		return
	}
	failed := &State{PayloadPath: path.Join(op.storeRoot, op.dep.ID, "installs", op.operationID), StoreRoot: op.storeRoot}
	script, err := s.queueCleanupScript(ctx, op, failed, true)
	if err != nil {
		return
	}
	stage := path.Join(op.storeRoot, op.dep.ID, ".staging-"+op.operationID)
	var cleanup strings.Builder
	cleanup.WriteString(script)
	for directory := path.Dir(stage); directory != "/"; directory = path.Dir(directory) {
		fmt.Fprintf(&cleanup, "[ ! -L %s ] || exit 0\n", shellQuote(directory))
	}
	cleanup.WriteString("rm -rf -- " + shellQuote(stage) + "\n")
	if err = runFilesystemScript(ctx, op.client, op.home, op.dep.ID, cleanup.String()); err != nil {
		return
	}
	s.cleanPreviousPayload(ctx, op)
	s.cleanupReceipt(ctx, op)
}

// Recipe caches are disposable and bounded after each operation. The same
// dependency kernel lock prevents concurrent cache collection and installation.
func (s *Service) trimPayloadCache(ctx context.Context, op *operation) {
	if op.dep.StorageLayout != "isolated" {
		return
	}
	cache := path.Join(op.storeRoot, op.dep.ID, "cache")
	var b strings.Builder
	b.WriteString("set -eu\n")
	for directory := cache; directory != "/"; directory = path.Dir(directory) {
		fmt.Fprintf(&b, "[ ! -L %s ] || exit 0\n", shellQuote(directory))
	}
	fmt.Fprintf(&b, "cache=%s\n[ -d \"$cache\" ] || exit 0\n", shellQuote(cache))
	b.WriteString(`find "$cache" -type f -mtime +7 -delete
size=$(du -sk "$cache" | awk '{print $1}')
case "$size" in ''|*[!0-9]*) exit 0 ;; esac
if [ "$size" -gt 524288 ]; then rm -rf -- "$cache"; fi
`)
	if err := runFilesystemScript(ctx, op.client, op.home, op.dep.ID, b.String()); err != nil {
		s.logger.Warn("trim dependency cache", slog.String("dependency_id", op.dep.ID), slog.Any("error", err))
	}
}
