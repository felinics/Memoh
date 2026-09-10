package packages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	connectsdk "github.com/felinics/connect-it/sdk/go"

	"github.com/felinics/memoh/internal/connectors"
	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

const (
	testBotID  = "bot-1"
	testTarget = "native"
)

// --- fakes ---

type memoryStore struct {
	mu            sync.Mutex
	next          int
	installations map[string]Installation
	depRefs       map[string]map[string]bool // installation -> dep ids
	connRefs      map[string]map[string]ConnectorRef
}

func newMemoryStore() *memoryStore {
	return &memoryStore{installations: map[string]Installation{}, depRefs: map[string]map[string]bool{}, connRefs: map[string]map[string]ConnectorRef{}}
}

func (m *memoryStore) Get(_ context.Context, botID, target, registryID, packageID string) (Installation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inst := range m.installations {
		if inst.BotID == botID && inst.WorkspaceTargetID == target && inst.RegistryID == registryID && inst.PackageID == packageID {
			return inst, nil
		}
	}
	return Installation{}, ErrNotInstalled
}

func (m *memoryStore) GetByID(_ context.Context, botID, id string) (Installation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.installations[id]
	if !ok || inst.BotID != botID {
		return Installation{}, ErrNotInstalled
	}
	return inst, nil
}

func (m *memoryStore) ListForBot(_ context.Context, botID string) ([]Installation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Installation
	for _, inst := range m.installations {
		if inst.BotID == botID {
			out = append(out, inst)
		}
	}
	return out, nil
}

func (m *memoryStore) ListForTarget(_ context.Context, botID, target string) ([]Installation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Installation
	for _, inst := range m.installations {
		if inst.BotID == botID && inst.WorkspaceTargetID == target {
			out = append(out, inst)
		}
	}
	return out, nil
}

func (m *memoryStore) Upsert(_ context.Context, in UpsertInstallation) (Installation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, inst := range m.installations {
		if inst.BotID == in.BotID && inst.WorkspaceTargetID == in.WorkspaceTargetID && inst.RegistryID == in.RegistryID && inst.PackageID == in.PackageID {
			inst.Revision, inst.Version, inst.Status, inst.Release, inst.LastError = in.Revision, in.Version, in.Status, in.Release, ""
			if in.Reason == ReasonUser {
				inst.Reason = ReasonUser
			}
			m.installations[id] = inst
			return inst, nil
		}
	}
	m.next++
	inst := Installation{
		ID: fmt.Sprintf("inst-%d", m.next), BotID: in.BotID, WorkspaceTargetID: in.WorkspaceTargetID,
		RegistryID: in.RegistryID, PackageID: in.PackageID, Revision: in.Revision, Version: in.Version,
		Status: in.Status, Reason: in.Reason, Release: in.Release, InstalledAt: time.Now(), UpdatedAt: time.Now(),
	}
	m.installations[inst.ID] = inst
	return inst, nil
}

func (m *memoryStore) SetStatus(_ context.Context, botID, id string, status Status, lastError string) (Installation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.installations[id]
	if !ok || inst.BotID != botID {
		return Installation{}, ErrNotInstalled
	}
	inst.Status, inst.LastError = status, lastError
	m.installations[id] = inst
	return inst, nil
}

func (m *memoryStore) SetRelease(_ context.Context, botID, id, revision, version string, release []byte) (Installation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.installations[id]
	if !ok || inst.BotID != botID {
		return Installation{}, ErrNotInstalled
	}
	inst.Revision, inst.Version, inst.Release, inst.AvailableRevision, inst.AvailableVersion = revision, version, release, "", ""
	m.installations[id] = inst
	return inst, nil
}

func (m *memoryStore) SetCheck(_ context.Context, botID, id, availableRevision, availableVersion string, checkedAt time.Time) (Installation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.installations[id]
	if !ok || inst.BotID != botID {
		return Installation{}, ErrNotInstalled
	}
	inst.AvailableRevision, inst.AvailableVersion = availableRevision, availableVersion
	checked := checkedAt
	inst.LastCheckedAt = &checked
	m.installations[id] = inst
	return inst, nil
}

func (m *memoryStore) Delete(_ context.Context, botID, id string) (Installation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.installations[id]
	if !ok || inst.BotID != botID {
		return Installation{}, ErrNotInstalled
	}
	delete(m.installations, id)
	delete(m.depRefs, id)
	delete(m.connRefs, id)
	return inst, nil
}

func (m *memoryStore) ListDependencyRefs(_ context.Context, id string) ([]DependencyRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []DependencyRef
	for dep := range m.depRefs[id] {
		out = append(out, DependencyRef{InstallationID: id, DependencyID: dep})
	}
	return out, nil
}

func (m *memoryStore) ListTargetDependencyRefs(_ context.Context, botID, target string) ([]TargetDependencyRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []TargetDependencyRef
	for id, deps := range m.depRefs {
		inst, ok := m.installations[id]
		if !ok || inst.BotID != botID || inst.WorkspaceTargetID != target {
			continue
		}
		for dep := range deps {
			out = append(out, TargetDependencyRef{DependencyRef: DependencyRef{InstallationID: id, DependencyID: dep}, RegistryID: inst.RegistryID, PackageID: inst.PackageID})
		}
	}
	return out, nil
}

func (m *memoryStore) AddDependencyRef(_ context.Context, id, dep string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.depRefs[id] == nil {
		m.depRefs[id] = map[string]bool{}
	}
	m.depRefs[id][dep] = true
	return nil
}

func (m *memoryStore) RemoveDependencyRef(_ context.Context, id, dep string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.depRefs[id], dep)
	return nil
}

func (m *memoryStore) ListConnectorRefs(_ context.Context, id string) ([]ConnectorRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ConnectorRef
	for _, ref := range m.connRefs[id] {
		out = append(out, ref)
	}
	return out, nil
}

func (m *memoryStore) ListBotConnectorRefs(_ context.Context, botID string) ([]BotConnectorRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []BotConnectorRef
	for id, refs := range m.connRefs {
		inst, ok := m.installations[id]
		if !ok || inst.BotID != botID {
			continue
		}
		for _, ref := range refs {
			out = append(out, BotConnectorRef{ConnectorRef: ref, RegistryID: inst.RegistryID, PackageID: inst.PackageID, WorkspaceTargetID: inst.WorkspaceTargetID})
		}
	}
	return out, nil
}

func (m *memoryStore) UpsertConnectorRef(_ context.Context, ref ConnectorRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connRefs[ref.InstallationID] == nil {
		m.connRefs[ref.InstallationID] = map[string]ConnectorRef{}
	}
	if existing, ok := m.connRefs[ref.InstallationID][ref.ConnectorType]; ok && ref.ConnectionID == "" {
		ref.ConnectionID = existing.ConnectionID
	}
	m.connRefs[ref.InstallationID][ref.ConnectorType] = ref
	return nil
}

func (m *memoryStore) SetConnectorRefConnection(_ context.Context, id, connectorType, connectionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ref, ok := m.connRefs[id][connectorType]
	if !ok {
		return ErrNotInstalled
	}
	ref.ConnectionID = connectionID
	m.connRefs[id][connectorType] = ref
	return nil
}

func (m *memoryStore) ClearConnectorRefConnection(_ context.Context, connectionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, refs := range m.connRefs {
		for key, ref := range refs {
			if ref.ConnectionID == connectionID {
				ref.ConnectionID = ""
				refs[key] = ref
			}
		}
	}
	return nil
}

func (m *memoryStore) RemoveConnectorRef(_ context.Context, id, connectorType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.connRefs[id], connectorType)
	return nil
}

type fakeRegistry struct {
	releases map[string]supermarket.SkillPackageDescriptor // key registry/package/revision
	current  map[string]supermarket.SkillPackageDescriptor // key registry/package
}

func (f *fakeRegistry) FetchRelease(_ context.Context, registryID, packageID, revision string) (supermarket.SkillPackageDescriptor, error) {
	pkg, ok := f.releases[registryID+"/"+packageID+"/"+revision]
	if !ok {
		return supermarket.SkillPackageDescriptor{}, &supermarket.ProtocolError{Kind: supermarket.ErrorNotFound, Op: "fetch"}
	}
	return pkg, nil
}

func (f *fakeRegistry) FetchCurrentPackage(_ context.Context, registryID, packageID string) (supermarket.SkillPackageDescriptor, error) {
	pkg, ok := f.current[registryID+"/"+packageID]
	if !ok {
		return supermarket.SkillPackageDescriptor{}, &supermarket.ProtocolError{Kind: supermarket.ErrorNotFound, Op: "fetch"}
	}
	return pkg, nil
}

type fakeTx struct {
	committed, rolledBack bool
}

func (t *fakeTx) Commit(context.Context) error   { t.committed = true; return nil }
func (t *fakeTx) Rollback(context.Context) error { t.rolledBack = true; return nil }

type fakePublisher struct {
	publishErr error
	published  []string
	removed    []string
	txs        []*fakeTx
}

func (*fakePublisher) ResolveTargetID(_ context.Context, _, targetID string) (string, error) {
	if targetID == "" {
		return testTarget, nil
	}
	return targetID, nil
}

func (f *fakePublisher) PublishSkills(_ context.Context, _, _ string, pkg supermarket.SkillPackageDescriptor, _ string) (SkillTransaction, []supermarket.InstallSkillResponse, error) {
	if f.publishErr != nil {
		return nil, nil, f.publishErr
	}
	f.published = append(f.published, pkg.PackageID+"@"+pkg.Revision)
	tx := &fakeTx{}
	f.txs = append(f.txs, tx)
	installed := make([]supermarket.InstallSkillResponse, 0, len(pkg.Skills))
	for _, skill := range pkg.Skills {
		installed = append(installed, supermarket.InstallSkillResponse{OK: true, SkillID: skill.SkillID})
	}
	return tx, installed, nil
}

func (f *fakePublisher) RemoveSkills(_ context.Context, _, _, _, packageID, _ string) (SkillTransaction, error) {
	f.removed = append(f.removed, packageID)
	tx := &fakeTx{}
	f.txs = append(f.txs, tx)
	return tx, nil
}

type fakeDeps struct {
	present    map[string]workspacedeps.Entry
	installErr map[string]error
	installed  []string
	removed    []string
}

func (f *fakeDeps) list() workspacedeps.ListResult {
	result := workspacedeps.ListResult{Workspace: workspacedeps.WorkspaceRunning, DataRoot: "/data"}
	for _, entry := range f.present {
		result.Entries = append(result.Entries, entry)
	}
	return result
}

func (f *fakeDeps) List(context.Context, string, string) (workspacedeps.ListResult, error) {
	return f.list(), nil
}

func (f *fakeDeps) Refresh(context.Context, string, string) (workspacedeps.ListResult, error) {
	return f.list(), nil
}

func (f *fakeDeps) CheckUpdates(context.Context, string, string) (workspacedeps.ListResult, error) {
	return f.list(), nil
}

func (f *fakeDeps) Install(_ context.Context, _, _, depID, _ string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error) {
	if err := f.installErr[depID]; err != nil {
		return workspacedeps.OperationResult{}, err
	}
	if sink != nil {
		sink.Log("stdout", "installing "+depID)
	}
	f.installed = append(f.installed, depID)
	entry := f.present[depID]
	entry.Dependency.ID = depID
	entry.Observed = workspacedeps.Observed{DepID: depID, Present: true, Source: workspacedeps.SourceManaged, Version: "1.0.0"}
	entry.Status = workspacedeps.StatusInstalled
	entry.InstalledVersion = "1.0.0"
	f.present[depID] = entry
	return workspacedeps.OperationResult{DependencyID: depID, Version: "1.0.0"}, nil
}

func (f *fakeDeps) Remove(_ context.Context, _, _, depID string, _ workspacedeps.LogSink) (workspacedeps.OperationResult, error) {
	f.removed = append(f.removed, depID)
	delete(f.present, depID)
	return workspacedeps.OperationResult{DependencyID: depID}, nil
}

type fakeConnectors struct {
	configured  bool
	connections []connectors.Connector
	deleted     []string
	next        int
}

func (f *fakeConnectors) Configured() bool { return f.configured }

func (f *fakeConnectors) List(context.Context, string) ([]connectors.Connector, error) {
	return append([]connectors.Connector{}, f.connections...), nil
}

func (f *fakeConnectors) BeginOAuth(_ context.Context, _, connectorType, authMethod string) (connectsdk.OAuthAuthorization, error) {
	f.next++
	id := fmt.Sprintf("conn-%d", f.next)
	f.connections = append(f.connections, connectors.Connector{ConnectionID: id, ConnectorType: connectorType, AuthMethod: authMethod, Status: "pending", Enabled: true})
	return connectsdk.OAuthAuthorization{ConnectionID: id, AuthorizationURL: "https://auth.example/" + id}, nil
}

func (f *fakeConnectors) CreateCredential(_ context.Context, _, connectorType, authMethod string, _ map[string]string) (connectors.Connector, error) {
	f.next++
	conn := connectors.Connector{ConnectionID: fmt.Sprintf("conn-%d", f.next), ConnectorType: connectorType, AuthMethod: authMethod, Status: "active", Enabled: true}
	f.connections = append(f.connections, conn)
	return conn, nil
}

func (f *fakeConnectors) Delete(_ context.Context, _, connectionID string) error {
	f.deleted = append(f.deleted, connectionID)
	kept := f.connections[:0]
	for _, conn := range f.connections {
		if conn.ConnectionID != connectionID {
			kept = append(kept, conn)
		}
	}
	f.connections = kept
	return nil
}

type recorder struct {
	events []Event
}

func (r *recorder) Send(event Event) { r.events = append(r.events, event) }

func (r *recorder) types() string {
	parts := make([]string, 0, len(r.events))
	for _, event := range r.events {
		part := event.Type
		if event.Kind != "" && event.Kind != KindPackage {
			part += ":" + event.Kind + ":" + event.ID
		}
		if event.Status != "" {
			part += "=" + event.Status
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

// --- fixtures ---

func revisionOf(seed string) string {
	return strings.Repeat(seed[:1], 64)
}

func release(registryID, packageID, seed, version string, skills []string, deps []string, conns []supermarket.PackageConnectorReference) supermarket.SkillPackageDescriptor {
	pkg := supermarket.SkillPackageDescriptor{Revision: revisionOf(seed)}
	pkg.SchemaVersion = "1"
	pkg.RegistryID, pkg.PackageID, pkg.Name, pkg.Version = registryID, packageID, packageID, version
	pkg.Tags = []string{}
	pkg.Category, pkg.CategoryName = "tool", "Tools"
	pkg.Dependencies = append([]string{}, deps...)
	pkg.Connectors = append([]supermarket.PackageConnectorReference{}, conns...)
	for _, skill := range skills {
		pkg.Skills = append(pkg.Skills, supermarket.CatalogSkill{
			SchemaVersion: "1", RegistryID: registryID, PackageID: packageID, SkillID: skill,
			InstallID: registryID + "+" + packageID + "+" + skill, Name: skill,
			Artifact: supermarket.SkillArtifact{Format: "memoh_skill_v1", Digest: revisionOf("f"), Size: 1, UncompressedSize: 1, ArchiveSize: 1, FileCount: 1, ContentType: "application/gzip", DownloadURL: "/api/artifacts/skill/" + revisionOf("f")},
		})
	}
	pkg.SkillCount = len(pkg.Skills)
	pkg.DependencyCount = len(pkg.Dependencies)
	pkg.ConnectorCount = len(pkg.Connectors)
	return pkg
}

func absentDep(id string) workspacedeps.Entry {
	return workspacedeps.Entry{Dependency: catalog.Dependency{ID: id, Name: id, Category: catalog.CategoryTool, Source: catalog.SourceManaged}, PlatformSupported: true}
}

func presentDep(id string, source workspacedeps.Source) workspacedeps.Entry {
	entry := absentDep(id)
	entry.Observed = workspacedeps.Observed{DepID: id, Present: true, Source: source, Version: "1.0.0"}
	entry.Status = workspacedeps.StatusInstalled
	entry.InstalledVersion = "1.0.0"
	return entry
}

type harness struct {
	store      *memoryStore
	registry   *fakeRegistry
	publisher  *fakePublisher
	deps       *fakeDeps
	connectors *fakeConnectors
	service    *Service
}

func newHarness() *harness {
	h := &harness{
		store:      newMemoryStore(),
		registry:   &fakeRegistry{releases: map[string]supermarket.SkillPackageDescriptor{}, current: map[string]supermarket.SkillPackageDescriptor{}},
		publisher:  &fakePublisher{},
		deps:       &fakeDeps{present: map[string]workspacedeps.Entry{}, installErr: map[string]error{}},
		connectors: &fakeConnectors{configured: true},
	}
	h.service = NewService(Options{Store: h.store, Registry: h.registry, Skills: h.publisher, Dependencies: h.deps, Connectors: h.connectors})
	return h
}

func (h *harness) publish(pkg supermarket.SkillPackageDescriptor) {
	h.registry.releases[pkg.RegistryID+"/"+pkg.PackageID+"/"+pkg.Revision] = pkg
	h.registry.current[pkg.RegistryID+"/"+pkg.PackageID] = pkg
}

func (h *harness) install(t *testing.T, pkg supermarket.SkillPackageDescriptor) (OperationResult, *recorder) {
	t.Helper()
	rec := &recorder{}
	result, err := h.service.Install(context.Background(), testBotID, InstallRequest{RegistryID: pkg.RegistryID, PackageID: pkg.PackageID, Revision: pkg.Revision}, rec)
	if err != nil {
		t.Fatalf("Install(%s): %v", pkg.PackageID, err)
	}
	return result, rec
}

// --- tests ---

func TestInstallLinksPresentDependenciesAndInstallsMissingOnes(t *testing.T) {
	h := newHarness()
	h.deps.present["node"] = presentDep("node", workspacedeps.SourceToolkit)
	h.deps.present["codex"] = absentDep("codex")
	pkg := release("memoh", "codex", "a", "1.0.0", nil, []string{"node", "codex"}, nil)
	h.publish(pkg)

	result, rec := h.install(t, pkg)
	if result.Installation.Status != StatusInstalled {
		t.Fatalf("status = %s, want installed: %+v", result.Installation.Status, result)
	}
	if strings.Join(h.deps.installed, ",") != "codex" {
		t.Fatalf("installed dependencies = %v, want only codex", h.deps.installed)
	}
	if !strings.Contains(rec.types(), "step_done:dependency:node=linked") || !strings.Contains(rec.types(), "step_done:dependency:codex=installed") {
		t.Fatalf("events = %s", rec.types())
	}
	if !strings.Contains(rec.types(), "log:dependency:codex") {
		t.Fatalf("dependency logs must be forwarded: %s", rec.types())
	}
	if len(h.publisher.published) != 1 || !h.publisher.txs[0].committed {
		t.Fatalf("skills must be published and committed: %+v", h.publisher)
	}
	refs, _ := h.store.ListDependencyRefs(context.Background(), result.Installation.ID)
	if len(refs) != 2 {
		t.Fatalf("dependency refs = %v", refs)
	}
	if !strings.HasSuffix(rec.types(), "done=installed") {
		t.Fatalf("events = %s", rec.types())
	}
}

func TestInstallIsPartialWhenADependencyFailsAndResumeCompletesIt(t *testing.T) {
	h := newHarness()
	h.deps.present["codex"] = absentDep("codex")
	h.deps.installErr["codex"] = errors.New("npm exploded")
	pkg := release("memoh", "codex", "a", "1.0.0", []string{"codex-workflows"}, []string{"codex"}, nil)
	h.publish(pkg)

	result, rec := h.install(t, pkg)
	if result.Installation.Status != StatusPartial || !strings.Contains(result.Installation.LastError, "npm exploded") {
		t.Fatalf("installation = %+v", result.Installation)
	}
	if !strings.Contains(rec.types(), "step_done:dependency:codex=failed") || !strings.HasSuffix(rec.types(), "done=partial") {
		t.Fatalf("events = %s", rec.types())
	}

	delete(h.deps.installErr, "codex")
	resumed, err := h.service.Resume(context.Background(), testBotID, result.Installation.ID, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Installation.Status != StatusInstalled || resumed.Installation.LastError != "" {
		t.Fatalf("resumed installation = %+v", resumed.Installation)
	}
	if len(h.publisher.published) != 2 {
		t.Fatalf("resume must reconcile skills again: %v", h.publisher.published)
	}
}

func TestInstallFailsWhenSkillsCannotBePublished(t *testing.T) {
	h := newHarness()
	h.publisher.publishErr = errors.New("bridge down")
	pkg := release("memoh", "pdf", "a", "1.0.0", []string{"pdf"}, nil, nil)
	h.publish(pkg)
	rec := &recorder{}
	_, err := h.service.Install(context.Background(), testBotID, InstallRequest{RegistryID: "memoh", PackageID: "pdf", Revision: pkg.Revision}, rec)
	if err == nil || !strings.Contains(err.Error(), "bridge down") {
		t.Fatalf("Install error = %v", err)
	}
	inst, err := h.store.Get(context.Background(), testBotID, testTarget, "memoh", "pdf")
	if err != nil || inst.Status != StatusFailed {
		t.Fatalf("installation = %+v, %v", inst, err)
	}
}

func TestInstallRejectsReferencesOutsideTheOfficialRegistry(t *testing.T) {
	h := newHarness()
	pkg := release("openai", "tools", "a", "1.0.0", []string{"demo"}, []string{"node"}, nil)
	h.publish(pkg)
	_, err := h.service.Install(context.Background(), testBotID, InstallRequest{RegistryID: "openai", PackageID: "tools", Revision: pkg.Revision}, nil)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if _, err := h.service.Install(context.Background(), testBotID, InstallRequest{RegistryID: "bad id", PackageID: "tools", Revision: pkg.Revision}, nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid registry error = %v", err)
	}
}

func TestInstallLinksActiveConnectorsAndMarksMissingRequiredOnesPartial(t *testing.T) {
	h := newHarness()
	h.connectors.connections = []connectors.Connector{{ConnectionID: "conn-gh", ConnectorType: "github", Status: "active"}}
	pkg := release("memoh", "review", "a", "1.0.0", []string{"review"}, nil, []supermarket.PackageConnectorReference{{Type: "github", Required: true}, {Type: "notion", Required: true}, {Type: "slack", Required: false}})
	h.publish(pkg)

	result, rec := h.install(t, pkg)
	if result.Installation.Status != StatusPartial {
		t.Fatalf("status = %s, want partial", result.Installation.Status)
	}
	if !strings.Contains(rec.types(), "step_done:connector:github=linked") || !strings.Contains(rec.types(), "step_done:connector:notion=needs_auth") {
		t.Fatalf("events = %s", rec.types())
	}
	refs, _ := h.store.ListConnectorRefs(context.Background(), result.Installation.ID)
	linked := map[string]string{}
	for _, ref := range refs {
		linked[ref.ConnectorType] = ref.ConnectionID
	}
	if linked["github"] != "conn-gh" || linked["notion"] != "" || linked["slack"] != "" {
		t.Fatalf("refs = %v", linked)
	}

	// Authorizing the required connector completes the installation; the
	// optional one never blocked it.
	auth, err := h.service.BeginConnectorOAuth(context.Background(), testBotID, result.Installation.ID, "notion", "oauth")
	if err != nil || auth.ConnectionID == "" {
		t.Fatalf("BeginConnectorOAuth = %+v, %v", auth, err)
	}
	inst, _ := h.store.GetByID(context.Background(), testBotID, result.Installation.ID)
	if inst.Status != StatusInstalled {
		t.Fatalf("status after authorization = %s", inst.Status)
	}
	if _, err := h.service.BeginConnectorOAuth(context.Background(), testBotID, result.Installation.ID, "jira", "oauth"); !errors.Is(err, ErrConnectorNotReferenced) {
		t.Fatalf("unreferenced connector error = %v", err)
	}
}

func TestRemoveReleasesOnlyUnsharedDependenciesAndConnections(t *testing.T) {
	h := newHarness()
	h.deps.present["node"] = absentDep("node")
	h.deps.present["codex"] = absentDep("codex")
	h.connectors.connections = []connectors.Connector{{ConnectionID: "conn-gh", ConnectorType: "github", Status: "active"}}
	codex := release("memoh", "codex", "a", "1.0.0", nil, []string{"node", "codex"}, []supermarket.PackageConnectorReference{{Type: "github", Required: true}})
	tools := release("memoh", "tools", "b", "1.0.0", []string{"tools"}, []string{"node"}, []supermarket.PackageConnectorReference{{Type: "github", Required: false}})
	h.publish(codex)
	h.publish(tools)
	codexResult, _ := h.install(t, codex)
	h.install(t, tools)

	preview, err := h.service.RemovalPreview(context.Background(), testBotID, codexResult.Installation.ID)
	if err != nil {
		t.Fatalf("RemovalPreview: %v", err)
	}
	actions := map[string]string{}
	for _, dep := range preview.Dependencies {
		actions[dep.ID] = dep.Action + "/" + dep.Reason
	}
	if actions["node"] != "keep/shared" || actions["codex"] != "remove/" {
		t.Fatalf("dependency plan = %v", actions)
	}
	if len(preview.Connectors) != 1 || preview.Connectors[0].Action != RemovalActionKeep {
		t.Fatalf("connector plan = %+v", preview.Connectors)
	}

	rec := &recorder{}
	if _, err := h.service.Remove(context.Background(), testBotID, codexResult.Installation.ID, RemoveOptions{}, rec); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if strings.Join(h.deps.removed, ",") != "codex" {
		t.Fatalf("removed dependencies = %v, want only codex", h.deps.removed)
	}
	if len(h.connectors.deleted) != 0 {
		t.Fatalf("shared connection must not be disconnected: %v", h.connectors.deleted)
	}
	if _, err := h.store.GetByID(context.Background(), testBotID, codexResult.Installation.ID); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("installation still present: %v", err)
	}
	if strings.Join(h.publisher.removed, ",") != "codex" {
		t.Fatalf("removed skills = %v", h.publisher.removed)
	}
	if !strings.HasSuffix(rec.types(), "done=removed") {
		t.Fatalf("events = %s", rec.types())
	}
}

func TestRemoveDisconnectsConnectionsNoPackageReferencesAndKeepsImageCopies(t *testing.T) {
	h := newHarness()
	h.deps.present["node"] = presentDep("node", workspacedeps.SourceToolkit)
	pkg := release("memoh", "node", "a", "1.0.0", nil, []string{"node"}, []supermarket.PackageConnectorReference{{Type: "github", Required: true}})
	h.publish(pkg)
	result, _ := h.install(t, pkg)
	if _, err := h.service.CreateConnectorCredential(context.Background(), testBotID, result.Installation.ID, "github", "token", map[string]string{"token": "x"}); err != nil {
		t.Fatalf("CreateConnectorCredential: %v", err)
	}
	if _, err := h.service.Remove(context.Background(), testBotID, result.Installation.ID, RemoveOptions{}, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(h.deps.removed) != 0 {
		t.Fatalf("image copy must be kept: %v", h.deps.removed)
	}
	if len(h.connectors.deleted) != 1 {
		t.Fatalf("unreferenced connection must be disconnected: %v", h.connectors.deleted)
	}
}

func TestListShowsDiscoveredDependenciesThroughTheirCanonicalPackage(t *testing.T) {
	h := newHarness()
	h.deps.present["node"] = presentDep("node", workspacedeps.SourceToolkit)
	h.deps.present["codex"] = presentDep("codex", workspacedeps.SourceManaged)
	pkg := release("memoh", "codex", "a", "1.0.0", nil, []string{"codex"}, nil)
	h.publish(pkg)
	h.install(t, pkg)

	result, err := h.service.List(context.Background(), testBotID, "", false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items = %+v", result.Items)
	}
	byID := map[string]Item{}
	for _, item := range result.Items {
		byID[item.PackageID] = item
	}
	if byID["codex"].Installation == nil || byID["codex"].Discovered || byID["codex"].Release == nil || len(byID["codex"].Dependencies) != 1 {
		t.Fatalf("codex item = %+v", byID["codex"])
	}
	if !byID["node"].Discovered || byID["node"].Installation != nil || byID["node"].Dependencies[0].Entry == nil {
		t.Fatalf("node item = %+v", byID["node"])
	}
}

func TestCheckUpdatesRecordsNewerRevisionAndUpdatePrunesDroppedReferences(t *testing.T) {
	h := newHarness()
	h.deps.present["node"] = absentDep("node")
	h.deps.present["uv"] = absentDep("uv")
	v1 := release("memoh", "python", "a", "1.0.0", []string{"py"}, []string{"node", "uv"}, nil)
	h.publish(v1)
	result, _ := h.install(t, v1)

	checked, err := h.service.CheckUpdates(context.Background(), testBotID, "")
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if checked.Items[0].Installation.AvailableRevision != "" || checked.Items[0].Installation.LastCheckedAt == nil {
		t.Fatalf("no update expected: %+v", checked.Items[0].Installation)
	}

	v2 := release("memoh", "python", "b", "1.1.0", []string{"py", "py-extra"}, []string{"uv"}, nil)
	h.publish(v2)
	checked, err = h.service.CheckUpdates(context.Background(), testBotID, "")
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if inst := checked.Items[0].Installation; inst.AvailableRevision != v2.Revision || inst.AvailableVersion != "1.1.0" {
		t.Fatalf("available = %+v", inst)
	}

	rec := &recorder{}
	updated, err := h.service.Update(context.Background(), testBotID, result.Installation.ID, rec)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Installation.Revision != v2.Revision || updated.Installation.Version != "1.1.0" || updated.Installation.Status != StatusInstalled {
		t.Fatalf("updated = %+v", updated.Installation)
	}
	if strings.Join(h.deps.removed, ",") != "node" {
		t.Fatalf("dropped dependency must be removed: %v", h.deps.removed)
	}
	refs, _ := h.store.ListDependencyRefs(context.Background(), result.Installation.ID)
	if len(refs) != 1 || refs[0].DependencyID != "uv" {
		t.Fatalf("refs after update = %v", refs)
	}
	if !strings.Contains(rec.types(), "step_done:dependency:node=removed") {
		t.Fatalf("events = %s", rec.types())
	}
	// A second update is a no-op.
	again, err := h.service.Update(context.Background(), testBotID, result.Installation.ID, nil)
	if err != nil || len(again.Steps) != 0 {
		t.Fatalf("no-op update = %+v, %v", again, err)
	}
}

func TestRemoveUnreferencedRequiredPackages(t *testing.T) {
	h := newHarness()
	h.deps.present["node"] = absentDep("node")
	node := release("memoh", "node", "a", "1.0.0", nil, []string{"node"}, nil)
	app := release("memoh", "app", "b", "1.0.0", []string{"app"}, []string{"node"}, nil)
	h.publish(node)
	h.publish(app)
	nodeRec := &recorder{}
	if _, err := h.service.Install(context.Background(), testBotID, InstallRequest{RegistryID: "memoh", PackageID: "node", Revision: node.Revision, Reason: ReasonRequired}, nodeRec); err != nil {
		t.Fatalf("install node: %v", err)
	}
	appResult, _ := h.install(t, app)

	preview, err := h.service.RemovalPreview(context.Background(), testBotID, appResult.Installation.ID)
	if err != nil {
		t.Fatalf("RemovalPreview: %v", err)
	}
	if len(preview.RequiredPackages) != 1 || preview.RequiredPackages[0].PackageID != "node" {
		t.Fatalf("required packages = %+v", preview.RequiredPackages)
	}
	if _, err := h.service.Remove(context.Background(), testBotID, appResult.Installation.ID, RemoveOptions{RemoveUnreferencedRequired: true}, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	remaining, _ := h.store.ListForTarget(context.Background(), testBotID, testTarget)
	if len(remaining) != 0 {
		t.Fatalf("remaining installations = %+v", remaining)
	}
	if strings.Join(h.deps.removed, ",") != "node" {
		t.Fatalf("removed dependencies = %v", h.deps.removed)
	}
}
