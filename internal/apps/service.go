package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	connectsdk "github.com/felinics/connect-it/sdk/go"

	"github.com/felinics/memoh/internal/connectors"
	skillset "github.com/felinics/memoh/internal/skills"
	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

// DependencyRegistryID is the only registry whose Apps may reference
// workspace dependencies and connectors.
const DependencyRegistryID = "memoh"

// lastErrorLimit caps the text stored in last_error.
const lastErrorLimit = 2048

// Sentinel errors returned by Service.
var (
	// ErrInvalidRequest means the request names an invalid App, revision
	// or reference.
	ErrInvalidRequest = errors.New("app request is invalid")
	// ErrConnectorNotReferenced means the installation does not reference the
	// connector type.
	ErrConnectorNotReferenced = errors.New("app does not reference this connector")
	// ErrDependenciesUnavailable means the App needs workspace
	// dependencies but no dependency service is configured.
	ErrDependenciesUnavailable = errors.New("workspace dependency service is not configured")
)

// RegistryClient fetches App descriptors from the Supermarket.
type RegistryClient interface {
	FetchRelease(ctx context.Context, registryID, appID, revision string) (supermarket.AppDescriptor, error)
	FetchCurrentApp(ctx context.Context, registryID, appID string) (supermarket.AppDescriptor, error)
}

// SkillTransaction is a staged workspace change that the service commits
// after recording it, or rolls back.
type SkillTransaction interface {
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// SkillPublisher materializes App Skills in a workspace target.
type SkillPublisher interface {
	ResolveTargetID(ctx context.Context, botID, targetID string) (string, error)
	PublishSkills(ctx context.Context, botID, targetID string, pkg supermarket.AppDescriptor, expectedRevision string) (SkillTransaction, []supermarket.InstallSkillResponse, error)
	RemoveSkills(ctx context.Context, botID, targetID, registryID, appID, revision string) (SkillTransaction, error)
}

// DependencyManager is the slice of *workspacedeps.Service the service uses.
type DependencyManager interface {
	List(ctx context.Context, botID, targetID string) (workspacedeps.ListResult, error)
	Refresh(ctx context.Context, botID, targetID string) (workspacedeps.ListResult, error)
	CheckUpdates(ctx context.Context, botID, targetID string) (workspacedeps.ListResult, error)
	Install(ctx context.Context, botID, targetID, depID, version string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error)
	Update(ctx context.Context, botID, targetID, depID, version string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error)
	Remove(ctx context.Context, botID, targetID, depID string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error)
	// EnsureRunning starts a stopped native workspace before a mutating
	// step; remote targets are never started.
	EnsureRunning(ctx context.Context, botID, targetID string) error
}

// ConnectorManager is the slice of *connectors.Service the service uses.
type ConnectorManager interface {
	Configured() bool
	List(ctx context.Context, botID string) ([]connectors.Connector, error)
	BeginOAuth(ctx context.Context, botID, connectorType, authMethod string) (connectsdk.OAuthAuthorization, error)
	CreateCredential(ctx context.Context, botID, connectorType, authMethod string, fields map[string]string) (connectors.Connector, error)
	Delete(ctx context.Context, botID, connectionID string) error
}

// Options configures NewService. Store, Registry and Skills are required;
// Dependencies and Connectors may be nil when the deployment lacks them.
type Options struct {
	Store        Store
	Registry     RegistryClient
	Skills       SkillPublisher
	Dependencies DependencyManager
	Connectors   ConnectorManager
	Logger       *slog.Logger
	Now          func() time.Time
}

// Service installs, lists, updates and removes Apps for bots.
type Service struct {
	store        Store
	registry     RegistryClient
	skills       SkillPublisher
	dependencies DependencyManager
	connectors   ConnectorManager
	logger       *slog.Logger
	now          func() time.Time
}

// NewService wires a Service. It panics when a required option is nil, which
// is a wiring error.
func NewService(opts Options) *Service {
	switch {
	case opts.Store == nil:
		panic("apps: Options.Store is nil")
	case opts.Registry == nil:
		panic("apps: Options.Registry is nil")
	case opts.Skills == nil:
		panic("apps: Options.Skills is nil")
	}
	s := &Service{
		store: opts.Store, registry: opts.Registry, skills: opts.Skills,
		dependencies: opts.Dependencies, connectors: opts.Connectors,
		logger: opts.Logger, now: opts.Now,
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	s.logger = s.logger.With(slog.String("component", "apps"))
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// InstallRequest names one immutable App release to install.
type InstallRequest struct {
	RegistryID        string
	AppID             string
	Revision          string
	WorkspaceTargetID string
	// Reason defaults to ReasonUser.
	Reason Reason
}

// Install materializes an App release into a workspace target: it
// installs missing dependencies, publishes the Skills and links connectors.
// A dependency failure or an unauthorized required connector leaves the
// installation partial instead of failing it; a Skill failure fails it.
func (s *Service) Install(ctx context.Context, botID string, req InstallRequest, sink EventSink) (OperationResult, error) {
	registryID := strings.TrimSpace(req.RegistryID)
	appID := strings.TrimSpace(req.AppID)
	revision := strings.TrimSpace(req.Revision)
	if !skillset.IsValidRegistryID(registryID) || !skillset.IsValidRegistryComponent(appID) || !supermarket.IsCanonicalSHA256(revision) {
		return OperationResult{}, ErrInvalidRequest
	}
	targetID, err := s.skills.ResolveTargetID(ctx, botID, req.WorkspaceTargetID)
	if err != nil {
		return OperationResult{}, err
	}
	release, err := s.registry.FetchRelease(ctx, registryID, appID, revision)
	if err != nil {
		return OperationResult{}, err
	}
	if err := validateReferences(release); err != nil {
		return OperationResult{}, err
	}
	reason := req.Reason
	if reason == "" {
		reason = ReasonUser
	}
	unlock, err := lockInstallation(ctx, botID, targetID, registryID, appID)
	if err != nil {
		return OperationResult{}, err
	}
	defer unlock()
	return s.materialize(ctx, botID, targetID, release, reason, StatusInstalling, sink, false)
}

// Resume continues a partial installation: dependencies that are still
// missing are installed again, Skills are reconciled and connectors are
// linked when a connection appeared since.
func (s *Service) Resume(ctx context.Context, botID, installationID string, sink EventSink) (OperationResult, error) {
	inst, err := s.store.GetByID(ctx, botID, installationID)
	if err != nil {
		return OperationResult{}, err
	}
	release, err := s.releaseFor(ctx, inst)
	if err != nil {
		return OperationResult{}, err
	}
	unlock, err := lockInstallation(ctx, botID, inst.WorkspaceTargetID, inst.RegistryID, inst.AppID)
	if err != nil {
		return OperationResult{}, err
	}
	defer unlock()
	return s.materialize(ctx, botID, inst.WorkspaceTargetID, release, inst.Reason, StatusInstalling, sink, false)
}

func validateReferences(release supermarket.AppDescriptor) error {
	if release.RegistryID != DependencyRegistryID && (len(release.Dependencies) > 0 || len(release.Connectors) > 0) {
		return fmt.Errorf("%w: dependencies and connectors are only supported in the %s registry", ErrInvalidRequest, DependencyRegistryID)
	}
	return nil
}

func lockInstallation(ctx context.Context, botID, targetID, registryID, appID string) (func(), error) {
	return supermarket.AcquireInstallationResources(ctx, supermarket.AppInstallationLockKey(botID, targetID, registryID, appID))
}

// releaseFor returns the release an installation materialized, from its
// cached document or, failing that, from the registry.
func (s *Service) releaseFor(ctx context.Context, inst Installation) (supermarket.AppDescriptor, error) {
	if len(inst.Release) > 0 {
		var release supermarket.AppDescriptor
		if err := json.Unmarshal(inst.Release, &release); err == nil && release.Revision == inst.Revision {
			return normalizeRelease(release), nil
		}
	}
	release, err := s.registry.FetchRelease(ctx, inst.RegistryID, inst.AppID, inst.Revision)
	if err != nil {
		return supermarket.AppDescriptor{}, err
	}
	return normalizeRelease(release), nil
}

func normalizeRelease(release supermarket.AppDescriptor) supermarket.AppDescriptor {
	if release.Dependencies == nil {
		release.Dependencies = []string{}
	}
	if release.Connectors == nil {
		release.Connectors = []supermarket.AppConnectorReference{}
	}
	if release.Tags == nil {
		release.Tags = []string{}
	}
	for index := range release.Skills {
		if release.Skills[index].Artifact.DownloadURL == "" {
			release.Skills[index].Artifact.DownloadURL = "/api/artifacts/skill/" + release.Skills[index].Artifact.Digest
		}
	}
	return release
}

// materialize is the shared body of Install, Resume and Update. announced
// is set when the caller already sent the started event.
func (s *Service) materialize(ctx context.Context, botID, targetID string, release supermarket.AppDescriptor, reason Reason, transient Status, sink EventSink, announced bool) (OperationResult, error) {
	sink = nonNilSink(sink)
	release = normalizeRelease(release)
	releaseBytes, err := json.Marshal(release)
	if err != nil {
		return OperationResult{}, fmt.Errorf("apps: encode release: %w", err)
	}
	expectedRevision := ""
	if current, err := s.store.Get(ctx, botID, targetID, release.RegistryID, release.AppID); err == nil {
		expectedRevision = current.Revision
		if current.Reason == ReasonUser {
			reason = ReasonUser
		}
	} else if !errors.Is(err, ErrNotInstalled) {
		return OperationResult{}, fmt.Errorf("apps: read installation: %w", err)
	}
	inst, err := s.store.Upsert(ctx, UpsertInstallation{
		BotID: botID, WorkspaceTargetID: targetID,
		RegistryID: release.RegistryID, AppID: release.AppID,
		Revision: release.Revision, Version: release.Version,
		Status: transient, Reason: reason, Release: releaseBytes,
	})
	if err != nil {
		return OperationResult{}, fmt.Errorf("apps: record installation: %w", err)
	}
	if !announced {
		sink.Send(Event{Type: EventStarted, Kind: KindApp, ID: release.AppID, Version: release.Version})
	}
	result := OperationResult{Installation: inst}
	partial := false
	var problems []string
	record := func(step StepResult) {
		result.Steps = append(result.Steps, step)
		if step.Status == StepFailed {
			partial = true
			problems = append(problems, step.Kind+" "+step.ID+": "+step.Error)
		}
		sink.Send(Event{Type: EventStepDone, Kind: step.Kind, ID: step.ID, Status: step.Status, Version: step.Version, Message: step.Error})
	}

	// 1. Dependencies: link present ones, install missing ones.
	states, statesErr := s.dependencyStates(ctx, botID, targetID)
	for _, depID := range release.Dependencies {
		sink.Send(Event{Type: EventStep, Kind: KindDependency, ID: depID})
		if err := s.store.AddDependencyRef(ctx, inst.ID, depID); err != nil {
			return result, s.failInstallation(ctx, inst, fail("record dependency reference "+depID, err))
		}
		switch {
		case s.dependencies == nil:
			record(StepResult{Kind: KindDependency, ID: depID, Status: StepFailed, Error: ErrDependenciesUnavailable.Error()})
		case statesErr != nil:
			record(StepResult{Kind: KindDependency, ID: depID, Status: StepFailed, Error: publicCause(statesErr)})
		default:
			entry, known := states[depID]
			if known && dependencyPresent(entry) {
				record(StepResult{Kind: KindDependency, ID: depID, Status: StepLinked, Version: entry.InstalledVersion})
				continue
			}
			res, err := s.dependencies.Install(ctx, botID, targetID, depID, "", logSink(sink, KindDependency, depID))
			if err != nil {
				record(StepResult{Kind: KindDependency, ID: depID, Status: StepFailed, Error: err.Error()})
				continue
			}
			record(StepResult{Kind: KindDependency, ID: depID, Status: StepInstalled, Version: res.Version})
		}
	}

	// 2. Skills: atomic replacement, committed once the record is updated.
	sink.Send(Event{Type: EventStep, Kind: KindSkills, ID: release.AppID})
	tx, installed, err := s.skills.PublishSkills(ctx, botID, targetID, release, expectedRevision)
	if err != nil {
		return result, s.failInstallation(ctx, inst, fail("publish Skills", err))
	}
	if _, err := s.store.SetRelease(ctx, botID, inst.ID, release.Revision, release.Version, releaseBytes); err != nil {
		rollbackErr := tx.Rollback(ctx)
		return result, s.failInstallation(ctx, inst, errors.Join(fail("record release", err), rollbackErr))
	}
	if err := tx.Commit(ctx); err != nil {
		s.logger.Warn("cleanup replaced App Skills failed", slog.String("app_id", release.AppID), slog.Any("error", err))
	}
	record(StepResult{Kind: KindSkills, ID: release.AppID, Status: StepInstalled, Version: strconv.Itoa(len(installed))})

	// 3. Connectors: reuse the bot's active connection of each type.
	existing := s.activeConnections(ctx, botID)
	for _, ref := range release.Connectors {
		sink.Send(Event{Type: EventStep, Kind: KindConnector, ID: ref.Type})
		connectionID := ""
		if conn, ok := existing[ref.Type]; ok {
			connectionID = conn.ConnectionID
		}
		if err := s.store.UpsertConnectorRef(ctx, ConnectorRef{InstallationID: inst.ID, ConnectorType: ref.Type, ConnectionID: connectionID, Required: ref.Required}); err != nil {
			return result, s.failInstallation(ctx, inst, fail("record connector reference "+ref.Type, err))
		}
		if connectionID != "" {
			record(StepResult{Kind: KindConnector, ID: ref.Type, Status: StepLinked})
			continue
		}
		if ref.Required {
			partial = true
		}
		record(StepResult{Kind: KindConnector, ID: ref.Type, Status: StepNeedsAuth})
	}

	// Cleanup is part of materialization so Install/Resume and same-release
	// update retries all recover retained references before reporting done.
	if err := s.pruneReferences(ctx, inst, release, sink, &result); err != nil {
		return result, s.failInstallation(ctx, inst, err)
	}

	status := StatusInstalled
	if partial {
		status = StatusPartial
	}
	inst, err = s.store.SetStatus(ctx, botID, inst.ID, status, truncateMessage(strings.Join(problems, "; ")))
	if err != nil {
		return result, fmt.Errorf("apps: record installation status: %w", err)
	}
	result.Installation = inst
	sink.Send(Event{Type: EventDone, Kind: KindApp, ID: release.AppID, Status: string(status), Version: release.Version})
	return result, nil
}

// failInstallation records a failed operation. last_error keeps only the
// public description; the cause with its infrastructure detail is logged.
func (s *Service) failInstallation(ctx context.Context, inst Installation, cause error) error {
	s.logger.Warn("App operation failed",
		slog.String("installation_id", inst.ID), slog.String("app_id", inst.AppID), slog.Any("error", cause))
	if _, err := s.store.SetStatus(ctx, inst.BotID, inst.ID, StatusFailed, truncateMessage(publicMessage(cause))); err != nil {
		s.logger.Warn("record failed App installation", slog.String("installation_id", inst.ID), slog.Any("error", err))
	}
	return cause
}

func logSink(sink EventSink, kind, id string) workspacedeps.LogSink {
	return workspacedeps.LogFunc(func(stream, line string) {
		sink.Send(Event{Type: EventLog, Kind: kind, ID: id, Stream: stream, Data: line})
	})
}

// dependencyStates returns the reconciled dependency entries of a target by
// dependency ID. It never starts the workspace.
func (s *Service) dependencyStates(ctx context.Context, botID, targetID string) (map[string]workspacedeps.Entry, error) {
	if s.dependencies == nil {
		return map[string]workspacedeps.Entry{}, nil
	}
	result, err := s.dependencies.List(ctx, botID, targetID)
	if err != nil {
		return nil, err
	}
	return indexEntries(result), nil
}

func indexEntries(result workspacedeps.ListResult) map[string]workspacedeps.Entry {
	states := make(map[string]workspacedeps.Entry, len(result.Entries))
	for _, entry := range result.Entries {
		states[entry.Dependency.ID] = entry
	}
	return states
}

// dependencyPresent reports whether a usable copy of the dependency exists.
func dependencyPresent(entry workspacedeps.Entry) bool {
	return entry.Observed.Present && entry.Status != workspacedeps.StatusFailed && entry.Status != workspacedeps.StatusMissing
}

// activeConnections indexes the bot's active Connect-It connections by
// connector type. Without a configured Connect-It it is empty.
func (s *Service) activeConnections(ctx context.Context, botID string) map[string]connectors.Connector {
	result := make(map[string]connectors.Connector)
	if s.connectors == nil || !s.connectors.Configured() {
		return result
	}
	items, err := s.connectors.List(ctx, botID)
	if err != nil {
		s.logger.Warn("list bot connectors", slog.String("bot_id", botID), slog.Any("error", err))
		return result
	}
	for _, item := range items {
		if item.Status != "active" {
			continue
		}
		if _, exists := result[item.ConnectorType]; !exists {
			result[item.ConnectorType] = item
		}
	}
	return result
}

// BeginConnectorOAuth starts authorization for a connector the installation
// references and links the resulting connection to it.
func (s *Service) BeginConnectorOAuth(ctx context.Context, botID, installationID, connectorType, authMethod string) (connectsdk.OAuthAuthorization, error) {
	inst, ref, err := s.connectorRef(ctx, botID, installationID, connectorType)
	if err != nil {
		return connectsdk.OAuthAuthorization{}, err
	}
	if s.connectors == nil {
		return connectsdk.OAuthAuthorization{}, connectors.ErrNotConfigured
	}
	result, err := s.connectors.BeginOAuth(ctx, botID, ref.ConnectorType, authMethod)
	if err != nil {
		return connectsdk.OAuthAuthorization{}, err
	}
	if err := s.store.SetConnectorRefConnection(ctx, inst.ID, ref.ConnectorType, result.ConnectionID); err != nil {
		return connectsdk.OAuthAuthorization{}, fmt.Errorf("apps: link connector: %w", err)
	}
	s.reconcileStatus(ctx, inst)
	return result, nil
}

// CreateConnectorCredential connects an API-key connector the installation
// references and links the connection to it.
func (s *Service) CreateConnectorCredential(ctx context.Context, botID, installationID, connectorType, authMethod string, fields map[string]string) (connectors.Connector, error) {
	inst, ref, err := s.connectorRef(ctx, botID, installationID, connectorType)
	if err != nil {
		return connectors.Connector{}, err
	}
	if s.connectors == nil {
		return connectors.Connector{}, connectors.ErrNotConfigured
	}
	result, err := s.connectors.CreateCredential(ctx, botID, ref.ConnectorType, authMethod, fields)
	if err != nil {
		return connectors.Connector{}, err
	}
	if err := s.store.SetConnectorRefConnection(ctx, inst.ID, ref.ConnectorType, result.ConnectionID); err != nil {
		return connectors.Connector{}, fmt.Errorf("apps: link connector: %w", err)
	}
	s.reconcileStatus(ctx, inst)
	return result, nil
}

func (s *Service) connectorRef(ctx context.Context, botID, installationID, connectorType string) (Installation, ConnectorRef, error) {
	inst, err := s.store.GetByID(ctx, botID, installationID)
	if err != nil {
		return Installation{}, ConnectorRef{}, err
	}
	refs, err := s.store.ListConnectorRefs(ctx, inst.ID)
	if err != nil {
		return Installation{}, ConnectorRef{}, fmt.Errorf("apps: list connector references: %w", err)
	}
	connectorType = strings.TrimSpace(connectorType)
	for _, ref := range refs {
		if ref.ConnectorType == connectorType {
			return inst, ref, nil
		}
	}
	return Installation{}, ConnectorRef{}, ErrConnectorNotReferenced
}

// reconcileStatus promotes a partial installation to installed once every
// required connector is linked and no dependency step failed.
func (s *Service) reconcileStatus(ctx context.Context, inst Installation) {
	if inst.Status != StatusPartial || inst.LastError != "" {
		return
	}
	refs, err := s.store.ListConnectorRefs(ctx, inst.ID)
	if err != nil {
		return
	}
	for _, ref := range refs {
		if ref.Required && ref.ConnectionID == "" {
			return
		}
	}
	if _, err := s.store.SetStatus(ctx, inst.BotID, inst.ID, StatusInstalled, ""); err != nil {
		s.logger.Warn("promote App installation", slog.String("installation_id", inst.ID), slog.Any("error", err))
	}
}

func truncateMessage(message string) string {
	if len(message) <= lastErrorLimit {
		return message
	}
	return message[:lastErrorLimit]
}
