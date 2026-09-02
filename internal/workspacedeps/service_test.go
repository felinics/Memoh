package workspacedeps

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

const (
	svcAgentYAML = `id: agent-x
name: Agent X
category: agent
source: managed
provides: [agent-x]
platforms:
  - { os: linux, arch: [amd64, arm64], libc: glibc }
  - { os: darwin, arch: [arm64, amd64] }
version:
  pin: "2.0.0"
scripts:
  install: install.sh
  update: update.sh
  remove: remove.sh
`
	svcToolYAML = `id: tool-y
name: Tool Y
category: tool
source: managed
provides: [tool-y]
platforms:
  - { os: linux, arch: [amd64, arm64], libc: glibc }
  - { os: darwin, arch: [arm64, amd64] }
scripts:
  install: install.sh
  remove: remove.sh
  check_update: check-update.sh
`
	svcImageYAML = `id: img-z
name: Image Z
category: runtime
source: image
provides: [z]
platforms:
  - { os: linux, arch: [amd64, arm64], libc: glibc }
  - { os: darwin, arch: [arm64, amd64] }
`
	svcMacOnlyYAML = `id: mac-only
name: Mac Only
category: tool
source: managed
provides: [mac-only]
platforms:
  - { os: darwin, arch: [arm64] }
scripts:
  install: install.sh
  remove: remove.sh
`
	svcToolInstallScript = "dep_log installing tool-y\n"
	svcToolRemoveScript  = "dep_log removing tool-y\n"
	svcToolCheckScript   = "dep_result '{\"installed\":\"1.0.0\",\"latest\":\"1.2.0\",\"update_available\":true}'\n"
)

func serviceCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	noop := "dep_log noop\n"
	fsys := fstest.MapFS{
		"agent-x/dependency.yaml":  &fstest.MapFile{Data: []byte(svcAgentYAML)},
		"agent-x/install.sh":       &fstest.MapFile{Data: []byte("dep_log install agent\n")},
		"agent-x/update.sh":        &fstest.MapFile{Data: []byte("dep_log update agent\n")},
		"agent-x/remove.sh":        &fstest.MapFile{Data: []byte(noop)},
		"tool-y/dependency.yaml":   &fstest.MapFile{Data: []byte(svcToolYAML)},
		"tool-y/install.sh":        &fstest.MapFile{Data: []byte(svcToolInstallScript)},
		"tool-y/remove.sh":         &fstest.MapFile{Data: []byte(svcToolRemoveScript)},
		"tool-y/check-update.sh":   &fstest.MapFile{Data: []byte(svcToolCheckScript)},
		"img-z/dependency.yaml":    &fstest.MapFile{Data: []byte(svcImageYAML)},
		"mac-only/dependency.yaml": &fstest.MapFile{Data: []byte(svcMacOnlyYAML)},
		"mac-only/install.sh":      &fstest.MapFile{Data: []byte(noop)},
		"mac-only/remove.sh":       &fstest.MapFile{Data: []byte(noop)},
	}
	cat, err := catalog.LoadFS(fsys)
	if err != nil {
		t.Fatalf("LoadFS: %v", err)
	}
	return cat
}

// fakeStore is an in-memory Store that records every status transition.
type fakeStore struct {
	mu      sync.Mutex
	now     func() time.Time
	records map[InstallationKey]Installation
	history map[InstallationKey][]Status
	writes  int
	nextID  int
}

func newFakeStore(now func() time.Time) *fakeStore {
	return &fakeStore{
		now:     now,
		records: make(map[InstallationKey]Installation),
		history: make(map[InstallationKey][]Status),
	}
}

func keyOf(rec Installation) InstallationKey {
	return InstallationKey{BotID: rec.BotID, WorkspaceTargetID: rec.WorkspaceTargetID, DependencyID: rec.DependencyID}
}

// seed stores rec verbatim without counting it as a write.
func (f *fakeStore) seed(rec Installation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rec.ID == "" {
		f.nextID++
		rec.ID = "rec-" + strconv.Itoa(f.nextID)
	}
	if rec.WorkspaceTargetID == "" {
		rec.WorkspaceTargetID = TargetNative
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = f.now()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = rec.UpdatedAt
	}
	f.records[keyOf(rec)] = rec
}

func (f *fakeStore) get(key InstallationKey) (Installation, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.records[key]
	return rec, ok
}

func (f *fakeStore) statuses(key InstallationKey) []Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Status(nil), f.history[key]...)
}

func (f *fakeStore) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

func (f *fakeStore) Get(_ context.Context, key InstallationKey) (Installation, error) {
	rec, ok := f.get(key)
	if !ok {
		return Installation{}, ErrInstallationNotFound
	}
	return rec, nil
}

func (f *fakeStore) list(match func(Installation) bool) []Installation {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Installation
	for _, rec := range f.records {
		if match(rec) {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.BotID != b.BotID {
			return a.BotID < b.BotID
		}
		if a.WorkspaceTargetID != b.WorkspaceTargetID {
			return a.WorkspaceTargetID < b.WorkspaceTargetID
		}
		return a.DependencyID < b.DependencyID
	})
	return out
}

func (f *fakeStore) ListForTarget(_ context.Context, botID, targetID string) ([]Installation, error) {
	return f.list(func(rec Installation) bool { return rec.BotID == botID && rec.WorkspaceTargetID == targetID }), nil
}

func (f *fakeStore) ListForBot(_ context.Context, botID string) ([]Installation, error) {
	return f.list(func(rec Installation) bool { return rec.BotID == botID }), nil
}

func (f *fakeStore) ListByStatus(_ context.Context, status Status) ([]Installation, error) {
	return f.list(func(rec Installation) bool { return rec.Status == status }), nil
}

func (f *fakeStore) ListStaleOperations(_ context.Context, olderThan time.Duration) ([]Installation, error) {
	now := f.now()
	return f.list(func(rec Installation) bool {
		return rec.Status.InProgress() && now.Sub(rec.UpdatedAt) >= olderThan
	}), nil
}

func (f *fakeStore) Upsert(_ context.Context, in UpsertInstallation) (Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	rec, ok := f.records[in.InstallationKey]
	if !ok {
		f.nextID++
		rec = Installation{ID: "rec-" + strconv.Itoa(f.nextID), BotID: in.BotID, WorkspaceTargetID: in.WorkspaceTargetID, DependencyID: in.DependencyID, CreatedAt: f.now()}
	}
	rec.Source = in.Source
	rec.Status = in.Status
	rec.InstalledVersion = in.InstalledVersion
	rec.ManifestDigest = in.ManifestDigest
	rec.LastError = ""
	rec.UpdatedAt = f.now()
	f.records[in.InstallationKey] = rec
	f.history[in.InstallationKey] = append(f.history[in.InstallationKey], in.Status)
	return rec, nil
}

func (f *fakeStore) SetStatus(_ context.Context, key InstallationKey, status Status, lastError string) (Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.records[key]
	if !ok {
		return Installation{}, ErrInstallationNotFound
	}
	f.writes++
	rec.Status = status
	rec.LastError = lastError
	rec.UpdatedAt = f.now()
	f.records[key] = rec
	f.history[key] = append(f.history[key], status)
	return rec, nil
}

func (f *fakeStore) UpdateObserved(_ context.Context, key InstallationKey, upd ObservedUpdate) (Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.records[key]
	if !ok {
		return Installation{}, ErrInstallationNotFound
	}
	f.writes++
	if upd.Source != nil {
		rec.Source = *upd.Source
	}
	if upd.InstalledVersion != nil {
		rec.InstalledVersion = *upd.InstalledVersion
	}
	if upd.LatestVersion != nil {
		rec.LatestVersion = *upd.LatestVersion
	}
	if upd.LastCheckedAt != nil {
		at := *upd.LastCheckedAt
		rec.LastCheckedAt = &at
	}
	if upd.LastError != nil {
		rec.LastError = *upd.LastError
	}
	if upd.ManifestDigest != nil {
		rec.ManifestDigest = *upd.ManifestDigest
	}
	rec.UpdatedAt = f.now()
	f.records[key] = rec
	return rec, nil
}

func (f *fakeStore) Delete(_ context.Context, key InstallationKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.records[key]; !ok {
		return ErrInstallationNotFound
	}
	f.writes++
	delete(f.records, key)
	return nil
}

// fakeWorkspace is a WorkspaceAccess over one bridge client and data root
// with per-target states.
type fakeWorkspace struct {
	mu          sync.Mutex
	client      *bridge.Client
	dataRoot    string
	states      map[string]WorkspaceState
	ensureErr   error
	ensureCalls []string
	resetFns    []func(string)
}

func newFakeWorkspace(client *bridge.Client, dataRoot string) *fakeWorkspace {
	return &fakeWorkspace{client: client, dataRoot: dataRoot, states: make(map[string]WorkspaceState)}
}

func (f *fakeWorkspace) setState(botID, targetID string, state WorkspaceState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[botID+"/"+normalizeTargetID(targetID)] = state
}

func (f *fakeWorkspace) Client(_ context.Context, _, _ string) (*bridge.Client, error) {
	return f.client, nil
}

func (f *fakeWorkspace) DataRoot(_ context.Context, _, _ string) (string, error) {
	return f.dataRoot, nil
}

func (f *fakeWorkspace) State(_ context.Context, botID, targetID string) (WorkspaceState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if state, ok := f.states[botID+"/"+normalizeTargetID(targetID)]; ok {
		return state, nil
	}
	return WorkspaceRunning, nil
}

func (f *fakeWorkspace) EnsureRunning(_ context.Context, botID, targetID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls = append(f.ensureCalls, botID+"/"+normalizeTargetID(targetID))
	if f.ensureErr != nil {
		return f.ensureErr
	}
	f.states[botID+"/"+normalizeTargetID(targetID)] = WorkspaceRunning
	return nil
}

func (f *fakeWorkspace) OnBridgeReset(fn func(botID string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetFns = append(f.resetFns, fn)
}

func (f *fakeWorkspace) reset(botID string) {
	f.mu.Lock()
	fns := slices.Clone(f.resetFns)
	f.mu.Unlock()
	for _, fn := range fns {
		fn(botID)
	}
}

// serviceFixture wires a Service to fakes. The bridge client is real (an
// in-process bridgesvc over a temporary data root) so state.json and shim
// writes are exercised for real, while probe, discovery, and script runs are
// replaced by the fixture's functions.
type serviceFixture struct {
	t        *testing.T
	now      time.Time
	store    *fakeStore
	ws       *fakeWorkspace
	cat      *catalog.Catalog
	svc      *Service
	platform Platform
	dataRoot string
	client   *bridge.Client

	mu            sync.Mutex
	observed      map[string]Observed
	discoverCalls int
	runs          []RunSpec
	runFn         func(RunSpec) (Result, error)
	env           []string
}

const (
	testBot    = "bot-a"
	testTarget = TargetNative
)

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	f := &serviceFixture{
		t:        t,
		now:      time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		cat:      serviceCatalog(t),
		observed: make(map[string]Observed),
	}
	f.client = newExecTestClient(t)
	f.dataRoot = t.TempDir()
	f.platform = Platform{OS: "linux", Arch: "amd64", Libc: "glibc", TmpDir: t.TempDir()}
	now := func() time.Time { return f.now }
	f.store = newFakeStore(now)
	f.ws = newFakeWorkspace(f.client, f.dataRoot)
	f.svc = NewService(Options{
		Workspace: f.ws,
		Store:     f.store,
		Catalog:   f.cat,
		Logger:    slog.New(slog.DiscardHandler),
		Now:       now,
		ScriptEnv: func(context.Context) []string { return f.env },
	})
	f.svc.probe = func(context.Context, *bridge.Client) (Platform, error) { return f.platform, nil }
	f.svc.discover = func(_ context.Context, _ *bridge.Client, _ *catalog.Catalog, _ string, ids []string, _ Platform) (map[string]Observed, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.discoverCalls++
		out := make(map[string]Observed, len(ids))
		for _, id := range ids {
			if obs, ok := f.observed[id]; ok {
				out[id] = obs
			} else {
				out[id] = Observed{DepID: id}
			}
		}
		return out, nil
	}
	f.svc.run = func(_ context.Context, _ *bridge.Client, spec RunSpec, _ LogSink) (Result, error) {
		f.mu.Lock()
		f.runs = append(f.runs, spec)
		fn := f.runFn
		f.mu.Unlock()
		if fn == nil {
			return Result{}, nil
		}
		return fn(spec)
	}
	return f
}

func (f *serviceFixture) ctx() context.Context {
	return testContext(f.t)
}

func (*serviceFixture) key(depID string) InstallationKey {
	return InstallationKey{BotID: testBot, WorkspaceTargetID: testTarget, DependencyID: depID}
}

func (f *serviceFixture) home(depID string) string {
	return Home(f.dataRoot, depID)
}

// present marks depID as discovered.
func (f *serviceFixture) present(depID string, source Source, version string, state *State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	command := filepath.Join(f.home(depID), "current", "bin", depID)
	f.observed[depID] = Observed{
		DepID:       depID,
		Present:     true,
		Source:      source,
		Version:     version,
		Command:     command,
		Entrypoints: map[string]string{depID: command},
		State:       state,
		Candidates:  []Candidate{{Source: source, Path: command, Version: version}},
	}
}

func (f *serviceFixture) absent(depID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.observed, depID)
}

func (f *serviceFixture) setRun(fn func(RunSpec) (Result, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runFn = fn
}

func (f *serviceFixture) runSpecs() []RunSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RunSpec(nil), f.runs...)
}

func (f *serviceFixture) discovered() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.discoverCalls
}

func (f *serviceFixture) seed(depID string, status Status, version string) {
	f.store.seed(Installation{BotID: testBot, WorkspaceTargetID: testTarget, DependencyID: depID, Source: InstallationSourceManaged, Status: status, InstalledVersion: version})
}

func (*serviceFixture) entry(t *testing.T, result ListResult, depID string) Entry {
	t.Helper()
	for _, entry := range result.Entries {
		if entry.Dependency.ID == depID {
			return entry
		}
	}
	t.Fatalf("no entry for %s in %+v", depID, result.Entries)
	return Entry{}
}

func (f *serviceFixture) writeState(t *testing.T, depID string, state State) {
	t.Helper()
	home := f.home(depID)
	if err := os.MkdirAll(home, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", home, err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(StatePath(home), data, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func (f *serviceFixture) readState(t *testing.T, depID string) State {
	t.Helper()
	data, err := os.ReadFile(StatePath(f.home(depID)))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if strings.Contains(strings.TrimSpace(string(data)), "\n") {
		t.Errorf("state.json must be a single line, got %q", data)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode state.json %q: %v", data, err)
	}
	return state
}

func (f *serviceFixture) shimPath(name string) string {
	return filepath.Join(ShimDir(f.dataRoot), name)
}

// writeShim plants a pre-existing shim, as a previous install would have.
func (f *serviceFixture) writeShim(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(ShimDir(f.dataRoot), 0o750); err != nil {
		t.Fatalf("mkdir shim dir: %v", err)
	}
	writeExecutable(t, ShimDir(f.dataRoot), name, "exit 0\n")
}

// installResult is the result an install-like fake run reports.
func (f *serviceFixture) installResult(depID, version string) Result {
	return Result{Version: version, Entrypoints: map[string]string{depID: filepath.Join(f.home(depID), "current", "bin", depID)}}
}

func actionsOf(entry Entry) string {
	parts := make([]string, 0, len(entry.Actions))
	for _, action := range entry.Actions {
		parts = append(parts, string(action))
	}
	return strings.Join(parts, ",")
}

func statusesEqual(got []Status, want ...Status) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestListReconcilesThreeStatesAndCaches(t *testing.T) {
	f := newServiceFixture(t)
	f.seed("agent-x", StatusInstalled, "1.9.0")
	f.seed("tool-y", StatusInstalled, "1.0.0")
	f.present("agent-x", SourceManaged, "2.0.0", &State{Version: "2.0.0", ManifestDigest: "sha256:aaa"})
	f.present("img-z", SourceToolkit, "22.0.0", nil)

	result, err := f.svc.List(f.ctx(), testBot, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result.Workspace != WorkspaceRunning || result.Platform != f.platform {
		t.Errorf("result = %+v, want running on %+v", result, f.platform)
	}

	agent := f.entry(t, result, "agent-x")
	if agent.Status != StatusInstalled || agent.InstalledVersion != "2.0.0" || agent.RequiredVersion != "2.0.0" || agent.LatestVersion != "2.0.0" || agent.NeedsAlignment {
		t.Errorf("agent-x entry = %+v", agent)
	}
	if rec, _ := f.store.get(f.key("agent-x")); rec.InstalledVersion != "2.0.0" || rec.LatestVersion != "2.0.0" || rec.ManifestDigest != "sha256:aaa" {
		t.Errorf("agent-x record not corrected: %+v", rec)
	}
	if got := actionsOf(agent); got != "update,reinstall,remove" {
		t.Errorf("agent-x actions = %s", got)
	}

	tool := f.entry(t, result, "tool-y")
	if tool.Status != StatusMissing || tool.InstalledVersion != "1.0.0" {
		t.Errorf("tool-y entry = %+v, want missing", tool)
	}
	if got := actionsOf(tool); got != "install,remove" {
		t.Errorf("tool-y actions = %s", got)
	}

	image := f.entry(t, result, "img-z")
	if image.Status != StatusInstalled || image.Installation == nil || image.Installation.Source != InstallationSourceImage || image.InstalledVersion != "22.0.0" {
		t.Errorf("img-z entry = %+v, want adopted as image", image)
	}
	if len(image.Actions) != 0 {
		t.Errorf("img-z actions = %v, want none", image.Actions)
	}

	mac := f.entry(t, result, "mac-only")
	if mac.Status != "" || mac.Installation != nil || mac.PlatformSupported || actionsOf(mac) != "install" {
		t.Errorf("mac-only entry = %+v", mac)
	}

	if f.discovered() != 1 {
		t.Fatalf("discover calls = %d, want 1", f.discovered())
	}
	if _, err := f.svc.List(f.ctx(), testBot, testTarget); err != nil {
		t.Fatalf("List (cached): %v", err)
	}
	if f.discovered() != 1 {
		t.Errorf("discover calls after cached List = %d, want 1", f.discovered())
	}
	if _, err := f.svc.Refresh(f.ctx(), testBot, testTarget); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if f.discovered() != 2 {
		t.Errorf("discover calls after Refresh = %d, want 2", f.discovered())
	}
	f.ws.reset(testBot)
	if _, err := f.svc.List(f.ctx(), testBot, testTarget); err != nil {
		t.Fatalf("List after bridge reset: %v", err)
	}
	if f.discovered() != 3 {
		t.Errorf("discover calls after bridge reset = %d, want 3", f.discovered())
	}
}

func TestListLeavesInProgressAndFailedRecordsAlone(t *testing.T) {
	f := newServiceFixture(t)
	f.seed("agent-x", StatusInstalling, "")
	f.store.seed(Installation{BotID: testBot, DependencyID: "tool-y", Source: InstallationSourceManaged, Status: StatusFailed, InstalledVersion: "1.0.0", LastError: "boom"})
	f.present("agent-x", SourceManaged, "1.9.0", nil)
	f.present("tool-y", SourceManaged, "1.0.1", nil)
	before := f.store.writeCount()

	result, err := f.svc.List(f.ctx(), testBot, testTarget)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	agent := f.entry(t, result, "agent-x")
	if agent.Status != StatusInstalling || len(agent.Actions) != 0 || agent.InstalledVersion != "1.9.0" {
		t.Errorf("in-progress entry = %+v", agent)
	}
	if rec, _ := f.store.get(f.key("agent-x")); rec.InstalledVersion != "" {
		t.Errorf("in-progress record was written: %+v", rec)
	}
	tool := f.entry(t, result, "tool-y")
	if tool.Status != StatusFailed || tool.InstalledVersion != "1.0.1" || tool.Installation.LastError != "boom" {
		t.Errorf("failed entry = %+v", tool)
	}
	if rec, _ := f.store.get(f.key("tool-y")); rec.Status != StatusFailed || rec.InstalledVersion != "1.0.1" {
		t.Errorf("failed record = %+v, want facts corrected and status kept", rec)
	}
	if f.store.writeCount() != before+1 {
		t.Errorf("store writes = %d, want exactly one (tool-y facts)", f.store.writeCount()-before)
	}

	f.absent("tool-y")
	f.svc.cache.Invalidate(testBot)
	result, err = f.svc.List(f.ctx(), testBot, testTarget)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if tool := f.entry(t, result, "tool-y"); tool.Status != StatusFailed || actionsOf(tool) != "install,remove" {
		t.Errorf("failed+absent entry = %+v", tool)
	}
}

func TestListAgentNeedsAlignmentAndToolUpdateAvailable(t *testing.T) {
	f := newServiceFixture(t)
	f.present("agent-x", SourceToolkit, "1.9.0", nil)
	f.store.seed(Installation{BotID: testBot, DependencyID: "tool-y", Source: InstallationSourceManaged, Status: StatusInstalled, InstalledVersion: "1.0.0", LatestVersion: "1.2.0"})
	f.present("tool-y", SourceManaged, "1.0.0", &State{Version: "1.0.0", PreviousVersion: "0.9.0"})

	result, err := f.svc.List(f.ctx(), testBot, testTarget)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	agent := f.entry(t, result, "agent-x")
	if !agent.NeedsAlignment || agent.InstalledVersion != "1.9.0" || agent.RequiredVersion != "2.0.0" || agent.Installation.Source != InstallationSourceImage {
		t.Errorf("agent-x entry = %+v", agent)
	}
	tool := f.entry(t, result, "tool-y")
	if !tool.UpdateAvailable || tool.LatestVersion != "1.2.0" || tool.RequiredVersion != "" {
		t.Errorf("tool-y entry = %+v", tool)
	}
	if got := actionsOf(tool); got != "update,reinstall,remove,rollback,check_update" {
		t.Errorf("tool-y actions = %s", got)
	}
}

func TestListWhenWorkspaceUnavailable(t *testing.T) {
	f := newServiceFixture(t)
	f.seed("tool-y", StatusInstalled, "1.0.0")
	for _, state := range []WorkspaceState{WorkspaceNotRunning, WorkspaceMissing} {
		f.ws.setState(testBot, testTarget, state)
		result, err := f.svc.List(f.ctx(), testBot, testTarget)
		if err != nil {
			t.Fatalf("List(%s): %v", state, err)
		}
		if result.Workspace != state || len(result.Entries) != len(f.cat.List()) {
			t.Errorf("List(%s) = %+v", state, result)
		}
		tool := f.entry(t, result, "tool-y")
		if tool.Status != StatusInstalled || tool.InstalledVersion != "1.0.0" || len(tool.Actions) != 0 || !tool.PlatformSupported {
			t.Errorf("offline entry = %+v", tool)
		}
		if agent := f.entry(t, result, "agent-x"); agent.RequiredVersion != "2.0.0" {
			t.Errorf("offline agent entry = %+v", agent)
		}
	}
	if f.discovered() != 0 {
		t.Errorf("discover ran %d times on an unavailable workspace", f.discovered())
	}
	if len(f.ws.ensureCalls) != 0 {
		t.Errorf("List started the workspace: %v", f.ws.ensureCalls)
	}
}

func TestPreflight(t *testing.T) {
	f := newServiceFixture(t)
	f.present("agent-x", SourceManaged, "2.0.0", nil)

	result, err := f.svc.Preflight(f.ctx(), testBot, "", []string{"agent-x", "tool-y", "mac-only", "nope"})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if result.Workspace != WorkspaceRunning || len(result.Items) != 4 {
		t.Fatalf("Preflight = %+v", result)
	}
	want := map[string]PreflightItem{
		"agent-x":  {DependencyID: "agent-x", Satisfied: true, InstalledVersion: "2.0.0", RequiredVersion: "2.0.0"},
		"tool-y":   {DependencyID: "tool-y", Reason: PreflightReasonMissing},
		"mac-only": {DependencyID: "mac-only", Reason: PreflightReasonPlatformUnsupported},
		"nope":     {DependencyID: "nope", Reason: PreflightReasonUnknownDependency},
	}
	for _, item := range result.Items {
		if item != want[item.DependencyID] {
			t.Errorf("item = %+v, want %+v", item, want[item.DependencyID])
		}
	}

	f.present("agent-x", SourceManaged, "1.9.0", nil)
	f.svc.cache.Invalidate(testBot)
	result, err = f.svc.Preflight(f.ctx(), testBot, testTarget, []string{"agent-x"})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if item := result.Items[0]; item.Satisfied || item.Reason != PreflightReasonVersionMismatch || item.InstalledVersion != "1.9.0" || item.RequiredVersion != "2.0.0" {
		t.Errorf("mismatch item = %+v", item)
	}

	f.ws.setState(testBot, testTarget, WorkspaceMissing)
	result, err = f.svc.Preflight(f.ctx(), testBot, testTarget, []string{"agent-x"})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if result.Workspace != WorkspaceMissing || len(result.Items) != 0 {
		t.Errorf("Preflight on missing workspace = %+v", result)
	}
	if len(f.ws.ensureCalls) != 0 {
		t.Errorf("Preflight started the workspace: %v", f.ws.ensureCalls)
	}
}

func TestInstallSucceeds(t *testing.T) {
	f := newServiceFixture(t)
	f.env = []string{"NPM_MIRROR=https://mirror"}
	f.setRun(func(spec RunSpec) (Result, error) { return f.installResult(spec.DepID, "1.0.0"), nil })
	if _, err := f.svc.List(f.ctx(), testBot, testTarget); err != nil {
		t.Fatalf("List: %v", err)
	}
	sink := newRecordingSink()

	result, err := f.svc.Install(f.ctx(), testBot, "", "tool-y", sink)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.DependencyID != "tool-y" || result.Action != catalog.ActionInstall || result.Version != "1.0.0" || result.Installation.Status != StatusInstalled {
		t.Errorf("result = %+v", result)
	}
	specs := f.runSpecs()
	if len(specs) != 1 {
		t.Fatalf("runs = %d, want 1", len(specs))
	}
	spec := specs[0]
	if spec.Action != catalog.ActionInstall || spec.Script != svcToolInstallScript || spec.Home != f.home("tool-y") || spec.ShimDir != ShimDir(f.dataRoot) || spec.Version != "" || spec.Platform != f.platform {
		t.Errorf("spec = %+v", spec)
	}
	if spec.Timeout != time.Duration(catalog.DefaultInstallTimeout)*time.Second || len(spec.ExtraEnv) != 1 || spec.ExtraEnv[0] != "NPM_MIRROR=https://mirror" {
		t.Errorf("spec timeout/env = %v %v", spec.Timeout, spec.ExtraEnv)
	}

	if got := f.store.statuses(f.key("tool-y")); !statusesEqual(got, StatusInstalling, StatusInstalled) {
		t.Errorf("status history = %v", got)
	}
	rec, _ := f.store.get(f.key("tool-y"))
	dep := f.cat.MustGet("tool-y")
	if rec.Source != InstallationSourceManaged || rec.InstalledVersion != "1.0.0" || rec.ManifestDigest != dep.ManifestDigest {
		t.Errorf("record = %+v", rec)
	}

	state := f.readState(t, "tool-y")
	if state.DependencyID != "tool-y" || state.Version != "1.0.0" || state.ManifestDigest != dep.ManifestDigest || !state.InstalledAt.Equal(f.now) || state.PreviousVersion != "" {
		t.Errorf("state.json = %+v", state)
	}
	if state.Entrypoints["tool-y"] != result.Entrypoints["tool-y"] {
		t.Errorf("entrypoints = %v vs %v", state.Entrypoints, result.Entrypoints)
	}
	info, err := os.Stat(f.shimPath("tool-y"))
	if err != nil {
		t.Fatalf("shim: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("shim mode = %v, want executable", info.Mode())
	}

	if f.discovered() != 1 {
		t.Fatalf("discover calls = %d", f.discovered())
	}
	if _, err := f.svc.List(f.ctx(), testBot, testTarget); err != nil {
		t.Fatalf("List: %v", err)
	}
	if f.discovered() != 2 {
		t.Errorf("cache was not invalidated after install (discover calls = %d)", f.discovered())
	}

	if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "agent-x", nil); err != nil {
		t.Fatalf("Install agent: %v", err)
	}
	if agentSpec := f.runSpecs()[1]; agentSpec.Version != "2.0.0" {
		t.Errorf("agent spec version = %q, want the pin", agentSpec.Version)
	}
	shim, err := os.ReadFile(f.shimPath("agent-x"))
	if err != nil {
		t.Fatalf("agent shim: %v", err)
	}
	if !strings.Contains(string(shim), "SSL_CERT_FILE") {
		t.Errorf("agent shim = %q, want the CA bundle export", shim)
	}
}

func TestInstallFailureRecordsError(t *testing.T) {
	f := newServiceFixture(t)
	f.setRun(func(RunSpec) (Result, error) {
		return Result{ExitCode: 3}, &ExitError{Code: 3, StderrTail: "boom " + strings.Repeat("x", 4000)}
	})

	_, err := f.svc.Install(f.ctx(), testBot, testTarget, "tool-y", nil)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Install error = %v, want *ExitError", err)
	}
	if got := f.store.statuses(f.key("tool-y")); !statusesEqual(got, StatusInstalling, StatusFailed) {
		t.Errorf("status history = %v", got)
	}
	rec, _ := f.store.get(f.key("tool-y"))
	if !strings.Contains(rec.LastError, "boom") || len(rec.LastError) > lastErrorLimit {
		t.Errorf("last_error = %d bytes %q", len(rec.LastError), rec.LastError[:40])
	}
	if _, err := os.Stat(StatePath(f.home("tool-y"))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state.json must not be written on failure (stat err = %v)", err)
	}

	f.setRun(func(RunSpec) (Result, error) { return Result{Version: "1.0.0"}, nil })
	if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "tool-y", nil); err == nil || !strings.Contains(err.Error(), "entrypoints") {
		t.Errorf("Install without entrypoints error = %v", err)
	}
	if rec, _ := f.store.get(f.key("tool-y")); rec.Status != StatusFailed {
		t.Errorf("record = %+v, want failed", rec)
	}
}

func TestInstallBusy(t *testing.T) {
	f := newServiceFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.setRun(func(spec RunSpec) (Result, error) {
		if spec.DepID == "tool-y" {
			once.Do(func() { close(started) })
			<-release
		}
		return f.installResult(spec.DepID, "1.0.0"), nil
	})

	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Install(f.ctx(), testBot, testTarget, "tool-y", nil)
		done <- err
	}()
	<-started
	if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "tool-y", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent Install error = %v, want ErrBusy", err)
	}
	if _, err := f.svc.Remove(f.ctx(), testBot, testTarget, "tool-y", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent Remove error = %v, want ErrBusy", err)
	}
	// Another dependency of the same bot is not blocked.
	if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "agent-x", nil); err != nil {
		t.Errorf("Install of another dependency: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if len(f.runSpecs()) != 2 {
		t.Errorf("runs = %d, want 2 (the busy calls never ran a script)", len(f.runSpecs()))
	}

	// The prelude's lock (another Server instance) is also reported as busy
	// and leaves the record to that instance.
	f.setRun(func(RunSpec) (Result, error) { return Result{ExitCode: exitCodeLocked}, ErrLocked })
	if _, err := f.svc.Update(f.ctx(), testBot, testTarget, "tool-y", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("locked Update error = %v, want ErrBusy", err)
	}
	if rec, _ := f.store.get(f.key("tool-y")); rec.Status != StatusUpdating {
		t.Errorf("record after locked run = %+v, want updating left in place", rec)
	}
}

func TestInstallGuards(t *testing.T) {
	f := newServiceFixture(t)
	f.setRun(func(spec RunSpec) (Result, error) { return f.installResult(spec.DepID, "1.0.0"), nil })

	if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "img-z", nil); !errors.Is(err, ErrActionUnsupported) {
		t.Errorf("image install error = %v", err)
	}
	if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "nope", nil); !errors.Is(err, ErrDependencyNotFound) {
		t.Errorf("unknown install error = %v", err)
	}
	if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "mac-only", nil); !errors.Is(err, ErrPlatformUnsupported) {
		t.Errorf("unsupported platform install error = %v", err)
	}
	if _, err := f.svc.Remove(f.ctx(), testBot, testTarget, "mac-only", nil); err != nil {
		t.Errorf("Remove does not need platform support: %v", err)
	}
	if _, ok := f.store.get(f.key("mac-only")); ok {
		t.Error("remove of an unrecorded dependency left a record behind")
	}

	f.ws.setState(testBot, testTarget, WorkspaceMissing)
	if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "tool-y", nil); !errors.Is(err, ErrWorkspaceMissing) {
		t.Errorf("missing workspace install error = %v", err)
	}
	f.ws.setState(testBot, "remote-1", WorkspaceRemoteOffline)
	if _, err := f.svc.Install(f.ctx(), testBot, "remote-1", "tool-y", nil); !errors.Is(err, ErrRemoteOffline) {
		t.Errorf("offline remote install error = %v", err)
	}
	if len(f.ws.ensureCalls) != 0 {
		t.Errorf("guards must not start anything: %v", f.ws.ensureCalls)
	}

	f.ws.setState(testBot, testTarget, WorkspaceNotRunning)
	if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "tool-y", nil); err != nil {
		t.Fatalf("install on stopped native workspace: %v", err)
	}
	if len(f.ws.ensureCalls) != 1 || f.ws.ensureCalls[0] != testBot+"/"+testTarget {
		t.Errorf("EnsureRunning calls = %v", f.ws.ensureCalls)
	}

	f.ws.setState(testBot, testTarget, WorkspaceNotRunning)
	f.ws.ensureErr = errors.New("containerd down")
	_, err := f.svc.Install(f.ctx(), testBot, testTarget, "agent-x", nil)
	if !errors.Is(err, ErrWorkspaceNotRunning) || !strings.Contains(err.Error(), "containerd down") {
		t.Errorf("failed start error = %v", err)
	}
	if _, ok := f.store.get(f.key("agent-x")); ok {
		t.Error("a refused install must not create a record")
	}
}

func TestUpdateFallsBackToInstallScriptAndKeepsPrevious(t *testing.T) {
	f := newServiceFixture(t)
	f.seed("tool-y", StatusInstalled, "1.0.0")
	f.writeState(t, "tool-y", State{DependencyID: "tool-y", Version: "1.0.0", Entrypoints: map[string]string{"tool-y": "/old"}})
	f.setRun(func(spec RunSpec) (Result, error) { return f.installResult(spec.DepID, "1.1.0"), nil })

	result, err := f.svc.Update(f.ctx(), testBot, testTarget, "tool-y", nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.Action != catalog.ActionUpdate || result.Version != "1.1.0" {
		t.Errorf("result = %+v", result)
	}
	spec := f.runSpecs()[0]
	if spec.Action != catalog.ActionUpdate || spec.Script != svcToolInstallScript || spec.CurrentVersion != "1.0.0" {
		t.Errorf("spec = %+v, want update action with the install script and current version", spec)
	}
	if got := f.store.statuses(f.key("tool-y")); !statusesEqual(got, StatusUpdating, StatusInstalled) {
		t.Errorf("status history = %v", got)
	}
	state := f.readState(t, "tool-y")
	if state.Version != "1.1.0" || state.PreviousVersion != "1.0.0" {
		t.Errorf("state.json = %+v", state)
	}

	// Updating to the same version keeps the older fallback.
	if _, err := f.svc.Update(f.ctx(), testBot, testTarget, "tool-y", nil); err != nil {
		t.Fatalf("Update again: %v", err)
	}
	if state := f.readState(t, "tool-y"); state.Version != "1.1.0" || state.PreviousVersion != "1.0.0" {
		t.Errorf("state.json after same-version update = %+v", state)
	}

	// The agent update script is used when the manifest has one.
	if _, err := f.svc.Update(f.ctx(), testBot, testTarget, "agent-x", nil); err != nil {
		t.Fatalf("Update agent: %v", err)
	}
	if spec := f.runSpecs()[2]; spec.Script != "dep_log update agent\n" || spec.Version != "2.0.0" {
		t.Errorf("agent update spec = %+v", spec)
	}
}

func TestReinstallOrchestratesRemoveThenInstall(t *testing.T) {
	f := newServiceFixture(t)
	f.seed("tool-y", StatusInstalled, "1.0.0")
	f.writeState(t, "tool-y", State{Version: "1.0.0", Entrypoints: map[string]string{"tool-y": "/x", "tool-y-extra": "/y"}})
	f.writeShim(t, "tool-y")
	f.writeShim(t, "tool-y-extra")
	f.setRun(func(spec RunSpec) (Result, error) {
		if spec.Action == catalog.ActionInstall {
			return f.installResult(spec.DepID, "1.0.0"), nil
		}
		return Result{}, nil
	})

	result, err := f.svc.Reinstall(f.ctx(), testBot, testTarget, "tool-y", nil)
	if err != nil {
		t.Fatalf("Reinstall: %v", err)
	}
	if result.Action != catalog.ActionReinstall || result.Version != "1.0.0" {
		t.Errorf("result = %+v", result)
	}
	specs := f.runSpecs()
	if len(specs) != 2 || specs[0].Action != catalog.ActionRemove || specs[0].Script != svcToolRemoveScript || specs[1].Action != catalog.ActionInstall || specs[1].Script != svcToolInstallScript {
		t.Errorf("runs = %+v", specs)
	}
	if specs[0].CurrentVersion != "1.0.0" {
		t.Errorf("remove step current version = %q", specs[0].CurrentVersion)
	}
	if got := f.store.statuses(f.key("tool-y")); !statusesEqual(got, StatusInstalling, StatusInstalled) {
		t.Errorf("status history = %v", got)
	}
	if _, err := os.Stat(f.shimPath("tool-y-extra")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale shim survived reinstall (err = %v)", err)
	}
	if _, err := os.Stat(f.shimPath("tool-y")); err != nil {
		t.Errorf("shim missing after reinstall: %v", err)
	}
	// The remove step deleted nothing in this fake, so the previous state
	// must not leak into the fresh install's previous_version.
	if state := f.readState(t, "tool-y"); state.PreviousVersion != "" {
		t.Errorf("state.json after reinstall = %+v", state)
	}

	f.setRun(func(RunSpec) (Result, error) {
		return Result{ExitCode: 1}, &ExitError{Code: 1, StderrTail: "cannot remove"}
	})
	_, err = f.svc.Reinstall(f.ctx(), testBot, testTarget, "tool-y", nil)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Reinstall error = %v, want *ExitError", err)
	}
	if specs := f.runSpecs(); len(specs) != 3 || specs[2].Action != catalog.ActionRemove {
		t.Errorf("a failed remove must stop before install: %+v", specs)
	}
	if rec, _ := f.store.get(f.key("tool-y")); rec.Status != StatusFailed || !strings.Contains(rec.LastError, "cannot remove") {
		t.Errorf("record = %+v", rec)
	}
}

func TestRemoveDeletesShimsAndRecord(t *testing.T) {
	f := newServiceFixture(t)
	f.seed("tool-y", StatusInstalled, "1.0.0")
	f.present("tool-y", SourceManaged, "1.0.0", nil)
	f.writeState(t, "tool-y", State{Version: "1.0.0", Entrypoints: map[string]string{"tool-y": "/x"}})
	f.writeShim(t, "tool-y")
	f.writeShim(t, "other")
	if _, err := f.svc.List(f.ctx(), testBot, testTarget); err != nil {
		t.Fatalf("List: %v", err)
	}

	result, err := f.svc.Remove(f.ctx(), testBot, testTarget, "tool-y", nil)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if result.Action != catalog.ActionRemove || result.Version != "" || result.DependencyID != "tool-y" {
		t.Errorf("result = %+v", result)
	}
	if spec := f.runSpecs()[0]; spec.Action != catalog.ActionRemove || spec.Timeout != time.Duration(catalog.DefaultRemoveTimeout)*time.Second {
		t.Errorf("spec = %+v", spec)
	}
	if got := f.store.statuses(f.key("tool-y")); !statusesEqual(got, StatusRemoving) {
		t.Errorf("status history = %v", got)
	}
	if _, ok := f.store.get(f.key("tool-y")); ok {
		t.Error("record survived remove")
	}
	if _, err := os.Stat(f.shimPath("tool-y")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("shim survived remove (err = %v)", err)
	}
	if _, err := os.Stat(f.shimPath("other")); err != nil {
		t.Errorf("unrelated shim was deleted: %v", err)
	}
	if f.discovered() != 1 {
		t.Fatalf("discover calls = %d", f.discovered())
	}
	if _, err := f.svc.List(f.ctx(), testBot, testTarget); err != nil {
		t.Fatalf("List: %v", err)
	}
	if f.discovered() != 2 {
		t.Errorf("cache was not invalidated after remove")
	}
}

func TestRollback(t *testing.T) {
	f := newServiceFixture(t)
	entrypoints := map[string]string{"tool-y": filepath.Join(f.home("tool-y"), "current", "bin", "tool-y")}
	f.seed("tool-y", StatusInstalled, "1.1.0")

	_, err := f.svc.Rollback(f.ctx(), testBot, testTarget, "tool-y")
	if !errors.Is(err, ErrRollbackUnavailable) {
		t.Errorf("Rollback without state error = %v", err)
	}
	f.writeState(t, "tool-y", State{Version: "1.1.0", Entrypoints: entrypoints})
	if _, err := f.svc.Rollback(f.ctx(), testBot, testTarget, "tool-y"); !errors.Is(err, ErrRollbackUnavailable) {
		t.Errorf("Rollback without previous version error = %v", err)
	}
	f.writeState(t, "tool-y", State{Version: "1.1.0", PreviousVersion: "1.0.0", ManifestDigest: "sha256:old", Entrypoints: entrypoints})
	if _, err := f.svc.Rollback(f.ctx(), testBot, testTarget, "tool-y"); !errors.Is(err, ErrRollbackUnavailable) {
		t.Errorf("Rollback with missing versions dir error = %v", err)
	}
	if len(f.runSpecs()) != 0 {
		t.Fatalf("unavailable rollbacks ran scripts: %+v", f.runSpecs())
	}
	if got := f.store.statuses(f.key("tool-y")); len(got) != 0 {
		t.Errorf("unavailable rollbacks changed status: %v", got)
	}

	if err := os.MkdirAll(filepath.Join(VersionsDir(f.home("tool-y")), "1.0.0"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	result, err := f.svc.Rollback(f.ctx(), testBot, testTarget, "tool-y")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.Action != ActionRollback || result.Version != "1.0.0" || result.Installation.InstalledVersion != "1.0.0" || result.Entrypoints["tool-y"] != entrypoints["tool-y"] {
		t.Errorf("result = %+v", result)
	}
	specs := f.runSpecs()
	if len(specs) != 1 {
		t.Fatalf("runs = %d, want 1", len(specs))
	}
	if spec := specs[0]; spec.Action != ActionRollback || spec.Script != rollbackScript || spec.Version != "1.0.0" || spec.CurrentVersion != "1.1.0" || spec.Timeout != rollbackTimeout {
		t.Errorf("spec = %+v", spec)
	}
	if got := f.store.statuses(f.key("tool-y")); !statusesEqual(got, StatusUpdating, StatusInstalled) {
		t.Errorf("status history = %v", got)
	}
	state := f.readState(t, "tool-y")
	if state.Version != "1.0.0" || state.PreviousVersion != "1.1.0" || state.ManifestDigest != "sha256:old" || state.Entrypoints["tool-y"] != entrypoints["tool-y"] || !state.InstalledAt.Equal(f.now) {
		t.Errorf("state.json = %+v", state)
	}
}

func TestReapStale(t *testing.T) {
	f := newServiceFixture(t)
	old := func(d time.Duration) time.Time { return f.now.Add(-d) }
	agent := f.cat.MustGet("agent-x")
	installingBudget := agent.Timeouts.Duration(catalog.ActionReinstall) + lockStaleGrace
	f.store.seed(Installation{BotID: "b1", DependencyID: "agent-x", Status: StatusInstalling, UpdatedAt: old(installingBudget)})
	f.store.seed(Installation{BotID: "b2", DependencyID: "agent-x", Status: StatusInstalling, UpdatedAt: old(installingBudget - time.Second)})
	f.store.seed(Installation{BotID: "b3", DependencyID: "tool-y", Status: StatusUpdating, UpdatedAt: old(time.Minute)})
	f.store.seed(Installation{BotID: "b4", DependencyID: "tool-y", Status: StatusRemoving, UpdatedAt: old(time.Hour)})
	f.store.seed(Installation{BotID: "b5", DependencyID: "gone-from-catalog", Status: StatusInstalling, UpdatedAt: old(2 * time.Hour)})
	f.store.seed(Installation{BotID: "b6", DependencyID: "tool-y", Status: StatusInstalled, UpdatedAt: old(48 * time.Hour)})
	f.store.seed(Installation{BotID: "b7", DependencyID: "tool-y", Status: StatusInstalling, UpdatedAt: old(48 * time.Hour)})
	// b7 is still running in this process.
	lockedKey := InstallationKey{BotID: "b7", WorkspaceTargetID: TargetNative, DependencyID: "tool-y"}
	f.svc.locks.tryLock(lockedKey)
	defer f.svc.locks.unlock(lockedKey)

	reaped, err := f.svc.ReapStale(f.ctx())
	if err != nil {
		t.Fatalf("ReapStale: %v", err)
	}
	if reaped != 3 {
		t.Errorf("reaped = %d, want 3", reaped)
	}
	want := map[string]Status{"b1": StatusFailed, "b2": StatusInstalling, "b3": StatusUpdating, "b4": StatusFailed, "b5": StatusFailed, "b6": StatusInstalled, "b7": StatusInstalling}
	for bot, status := range want {
		recs, _ := f.store.ListForBot(f.ctx(), bot)
		if len(recs) != 1 || recs[0].Status != status {
			t.Errorf("bot %s record = %+v, want %s", bot, recs, status)
		}
		if status == StatusFailed && recs[0].LastError != staleReapMessage {
			t.Errorf("bot %s last_error = %q", bot, recs[0].LastError)
		}
	}
}

func TestScriptPreview(t *testing.T) {
	f := newServiceFixture(t)
	preview, err := f.svc.ScriptPreview("tool-y", catalog.ActionInstall)
	if err != nil {
		t.Fatalf("ScriptPreview: %v", err)
	}
	if preview != WrapScript(svcToolInstallScript) || !strings.HasPrefix(preview, prelude) {
		t.Errorf("install preview = %q", preview)
	}
	if preview, err := f.svc.ScriptPreview("tool-y", catalog.ActionUpdate); err != nil || preview != WrapScript(svcToolInstallScript) {
		t.Errorf("update preview without script = %q, %v; want the install script", preview, err)
	}
	preview, err = f.svc.ScriptPreview("tool-y", catalog.ActionReinstall)
	if err != nil {
		t.Fatalf("reinstall preview: %v", err)
	}
	if !strings.Contains(preview, svcToolRemoveScript) || !strings.Contains(preview, svcToolInstallScript) || strings.Count(preview, prelude) != 2 {
		t.Errorf("reinstall preview = %q", preview)
	}
	if preview, err := f.svc.ScriptPreview("agent-x", ActionRollback); err != nil || !strings.Contains(preview, `dep_switch "$MEMOH_DEP_HOME/versions/$MEMOH_DEP_VERSION"`) {
		t.Errorf("rollback preview = %q, %v", preview, err)
	}
	if _, err := f.svc.ScriptPreview("agent-x", catalog.ActionCheckUpdate); !errors.Is(err, ErrActionUnsupported) {
		t.Errorf("agent check_update preview error = %v", err)
	}
	if _, err := f.svc.ScriptPreview("img-z", catalog.ActionInstall); !errors.Is(err, ErrActionUnsupported) {
		t.Errorf("image preview error = %v", err)
	}
	if _, err := f.svc.ScriptPreview("nope", catalog.ActionInstall); !errors.Is(err, ErrDependencyNotFound) {
		t.Errorf("unknown preview error = %v", err)
	}
}

func TestCheckUpdates(t *testing.T) {
	f := newServiceFixture(t)
	f.seed("tool-y", StatusInstalled, "1.0.0")
	f.present("tool-y", SourceManaged, "1.0.0", &State{Version: "1.0.0"})
	f.present("agent-x", SourceManaged, "1.9.0", nil)
	f.setRun(func(spec RunSpec) (Result, error) {
		if spec.Action != catalog.ActionCheckUpdate {
			t.Errorf("unexpected run %+v", spec)
		}
		return Result{Raw: json.RawMessage(`{"installed":"1.0.0","latest":"1.2.0","update_available":true}`)}, nil
	})

	result, err := f.svc.CheckUpdates(f.ctx(), testBot, testTarget)
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	specs := f.runSpecs()
	if len(specs) != 1 || specs[0].DepID != "tool-y" || specs[0].CurrentVersion != "1.0.0" || specs[0].Timeout != time.Duration(catalog.DefaultCheckUpdateTimeout)*time.Second {
		t.Errorf("runs = %+v, want one check_update of tool-y", specs)
	}
	tool := f.entry(t, result, "tool-y")
	if !tool.UpdateAvailable || tool.LatestVersion != "1.2.0" || tool.Installation.LastCheckedAt == nil || !tool.Installation.LastCheckedAt.Equal(f.now) {
		t.Errorf("tool-y entry = %+v", tool)
	}
	if agent := f.entry(t, result, "agent-x"); !agent.NeedsAlignment || agent.LatestVersion != "2.0.0" {
		t.Errorf("agent-x entry = %+v", agent)
	}
	if f.discovered() != 1 {
		t.Errorf("discover calls = %d, want a single forced discovery", f.discovered())
	}

	f.now = f.now.Add(time.Hour)
	f.setRun(func(RunSpec) (Result, error) { return Result{}, errors.New("registry unreachable") })
	result, err = f.svc.CheckUpdates(f.ctx(), testBot, testTarget)
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	tool = f.entry(t, result, "tool-y")
	if tool.Status != StatusInstalled || tool.LatestVersion != "1.2.0" || !strings.Contains(tool.Installation.LastError, "registry unreachable") || !tool.Installation.LastCheckedAt.Equal(f.now) {
		t.Errorf("tool-y entry after failed check = %+v / %+v", tool, tool.Installation)
	}

	f.ws.setState(testBot, testTarget, WorkspaceNotRunning)
	result, err = f.svc.CheckUpdates(f.ctx(), testBot, testTarget)
	if err != nil || result.Workspace != WorkspaceNotRunning || len(f.runSpecs()) != 2 {
		t.Errorf("CheckUpdates on stopped workspace = %+v, %v (runs = %d)", result.Workspace, err, len(f.runSpecs()))
	}
}

// End-to-end: real prelude, real scripts, real discovery over the in-process
// bridge. Only the platform probe is pinned so the runner's temporary files
// land in the test's directory.
const e2eFooYAML = `id: foo
name: Foo
category: tool
source: managed
provides: [foo]
platforms:
  - { os: linux, arch: [amd64, arm64], libc: glibc }
  - { os: darwin, arch: [arm64, amd64] }
timeouts:
  install: 60
  remove: 30
scripts:
  install: install.sh
  remove: remove.sh
`

// e2eFooInstall builds a fake CLI that prints its version, switches
// `current`, and reports the entrypoint. FOO_VERSION comes from ScriptEnv.
const e2eFooInstall = `version="${FOO_VERSION:-1.0.0}"
target="$MEMOH_DEP_HOME/versions/$version"
mkdir -p "$target/bin"
printf '#!/bin/sh\necho "foo %s"\n' "$version" > "$target/bin/foo"
chmod 0755 "$target/bin/foo"
dep_log "installed foo $version"
dep_switch "$target"
dep_result "{\"version\":\"$version\",\"entrypoints\":{\"foo\":\"$MEMOH_DEP_HOME/current/bin/foo\"}}"
`

const e2eFooRemove = `case "$MEMOH_DEP_HOME" in "" | / | */ ) exit 1 ;; esac
rm -rf "$MEMOH_DEP_HOME"
dep_result '{}'
`

func TestInstallUpdateRollbackRemoveEndToEnd(t *testing.T) {
	f := newServiceFixture(t)
	fsys := fstest.MapFS{
		"foo/dependency.yaml": &fstest.MapFile{Data: []byte(e2eFooYAML)},
		"foo/install.sh":      &fstest.MapFile{Data: []byte(e2eFooInstall)},
		"foo/remove.sh":       &fstest.MapFile{Data: []byte(e2eFooRemove)},
	}
	cat, err := catalog.LoadFS(fsys)
	if err != nil {
		t.Fatalf("LoadFS: %v", err)
	}
	f.cat = cat
	f.svc.catalog = cat
	f.svc.run = Run
	f.svc.discover = Discover
	platform, err := ProbePlatform(f.ctx(), f.client)
	if err != nil {
		t.Fatalf("ProbePlatform: %v", err)
	}
	platform.TmpDir = t.TempDir()
	f.platform = platform
	f.env = []string{"FOO_VERSION=1.0.0"}
	sink := newRecordingSink()

	result, err := f.svc.Install(f.ctx(), testBot, testTarget, "foo", sink)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Version != "1.0.0" || !sink.has(StreamStderr, "installed foo 1.0.0") {
		t.Errorf("result = %+v, stderr = %q", result, sink.get(StreamStderr))
	}
	shim := f.shimPath("foo")
	assertShimPrints := func(want string) {
		t.Helper()
		out, err := exec.CommandContext(f.ctx(), shim).Output() //nolint:gosec // G204: executes the shim this test just wrote.
		if err != nil {
			t.Fatalf("run shim: %v", err)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("shim printed %q, want %q", got, want)
		}
	}
	assertShimPrints("foo 1.0.0")
	if state := f.readState(t, "foo"); state.Version != "1.0.0" || state.Entrypoints["foo"] != filepath.Join(f.home("foo"), "current", "bin", "foo") {
		t.Errorf("state.json = %+v", state)
	}

	list, err := f.svc.List(f.ctx(), testBot, testTarget)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	foo := f.entry(t, list, "foo")
	if foo.Status != StatusInstalled || foo.Observed.Source != SourceManaged || foo.InstalledVersion != "1.0.0" || foo.Observed.Err != "" {
		t.Errorf("discovered entry = %+v / %+v", foo, foo.Observed)
	}

	f.env = []string{"FOO_VERSION=1.1.0"}
	if _, err := f.svc.Update(f.ctx(), testBot, testTarget, "foo", nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	assertShimPrints("foo 1.1.0")
	if state := f.readState(t, "foo"); state.Version != "1.1.0" || state.PreviousVersion != "1.0.0" {
		t.Errorf("state.json after update = %+v", state)
	}
	list, err = f.svc.List(f.ctx(), testBot, testTarget)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if foo := f.entry(t, list, "foo"); foo.InstalledVersion != "1.1.0" || !strings.Contains(actionsOf(foo), "rollback") {
		t.Errorf("entry after update = %+v", foo)
	}

	rolled, err := f.svc.Rollback(f.ctx(), testBot, testTarget, "foo")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolled.Version != "1.0.0" {
		t.Errorf("rollback result = %+v", rolled)
	}
	assertShimPrints("foo 1.0.0")
	if state := f.readState(t, "foo"); state.Version != "1.0.0" || state.PreviousVersion != "1.1.0" {
		t.Errorf("state.json after rollback = %+v", state)
	}
	if rec, _ := f.store.get(f.key("foo")); rec.InstalledVersion != "1.0.0" || rec.Status != StatusInstalled {
		t.Errorf("record after rollback = %+v", rec)
	}

	if _, err := f.svc.Remove(f.ctx(), testBot, testTarget, "foo", nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(f.home("foo")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dependency home survived remove (err = %v)", err)
	}
	if _, err := os.Stat(shim); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("shim survived remove (err = %v)", err)
	}
	if _, ok := f.store.get(f.key("foo")); ok {
		t.Error("record survived remove")
	}
	list, err = f.svc.List(f.ctx(), testBot, testTarget)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if foo := f.entry(t, list, "foo"); foo.Status != "" || foo.Observed.Present || actionsOf(foo) != "install" {
		t.Errorf("entry after remove = %+v", foo)
	}
	leftovers, err := os.ReadDir(platform.TmpDir)
	if err != nil {
		t.Fatalf("read tmp dir: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("runner left files behind: %v", leftovers)
	}
}
