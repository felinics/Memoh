package workspacedeps

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

var (
	ErrRepairAuthorizationRequired = errors.New("dependency repair requires management authorization")
	ErrDesiredTargetChanged        = errors.New("dependency desired target changed")
	ErrRepairPending               = errors.New("dependency repair is pending")
	ErrRepairManualRequired        = errors.New("dependency repair requires management attention")
)

type RepairStatus string

const (
	RepairReady          RepairStatus = "ready"
	RepairQueued         RepairStatus = "queued"
	RepairInstalling     RepairStatus = "installing"
	RepairBackoff        RepairStatus = "backoff"
	RepairManualRequired RepairStatus = "manual_required"
)

// DesiredInstallation is a confirmed target. Discovery must never write it.
// Authorization covers only this exact version and immutable publication.
type DesiredInstallation struct {
	InstallationKey
	Revision                string
	Version                 string
	SourceURL               string
	RegistryID              string
	DefinitionRevision      string
	ManifestDigest          string
	AutoRepairAuthorizedAt  time.Time
	AuthorizedByOperationID string
	AuthorizedByActor       string
	Platform                Platform
	Entrypoints             map[string]string
	InstallationID          string
	PayloadPath             string
	StoreRoot               string
	RepairStatus            RepairStatus
	RepairOperationID       string
	RepairAttempts          int
	RepairNextAttemptAt     *time.Time
	RepairLastErrorCode     string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// DesiredStore is separate from Store so observation-only adapters cannot
// accidentally grant script execution authority. Production stores implement
// both; tests and read-only integrations may implement only Store.
type DesiredStore interface {
	GetDesired(context.Context, InstallationKey) (DesiredInstallation, error)
	ListDesired(context.Context, string) ([]DesiredInstallation, error)
	ListDesiredPage(context.Context, InstallationKey, int) ([]DesiredInstallation, error)
	QueueRepair(context.Context, DesiredInstallation, string, time.Time, bool) (DesiredInstallation, error)
	SetRepairResult(context.Context, DesiredInstallation, RepairStatus, int, *time.Time, string) (DesiredInstallation, error)
	ClaimRepair(context.Context, DesiredInstallation, *OperationReceipt) (Installation, error)
	RevokeDesired(context.Context, InstallationKey, string, string) error
	FinishAuthorized(context.Context, DesiredInstallation, string) (Installation, error)
	FinishRepair(context.Context, DesiredInstallation, string) (Installation, error)
}

type (
	repairActorKey         struct{}
	frozenRepairCatalogKey struct{}
	repairTargetKey        struct{}
)

// WithRepairActor carries the authenticated management identity into a durable
// authorization audit. Automatic repairs preserve the original actor.
func WithRepairActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, repairActorKey{}, strings.TrimSpace(actor))
}

func (*Service) bindDesiredOperation(ctx context.Context, op *operation) {
	op.authorizedByActor, _ = ctx.Value(repairActorKey{}).(string)
	if target, ok := ctx.Value(repairTargetKey{}).(DesiredInstallation); ok {
		op.repair, op.desiredRevision = true, target.Revision
		op.operationID = target.RepairOperationID
		op.authorizedByActor = target.AuthorizedByActor
	}
}

func (s *Service) claimDesiredOperation(ctx context.Context, op *operation, in UpsertInstallation) (Installation, error) {
	if !op.repair {
		in.AuthorizedByActor = op.authorizedByActor
		return s.store.ClaimOperation(ctx, in, op.operationID)
	}
	store, ok := s.store.(DesiredStore)
	if !ok {
		return Installation{}, ErrRepairAuthorizationRequired
	}
	target, err := store.GetDesired(ctx, op.key)
	if err != nil {
		return Installation{}, err
	}
	if target.Revision != op.desiredRevision || target.RepairOperationID != op.operationID {
		return Installation{}, ErrDesiredTargetChanged
	}
	return store.ClaimRepair(ctx, target, in.OperationIntent)
}

func (s *Service) validateDesiredOperation(ctx context.Context, op *operation) error {
	if !op.repair {
		return nil
	}
	store, ok := s.store.(DesiredStore)
	if !ok {
		return ErrRepairAuthorizationRequired
	}
	target, err := store.GetDesired(ctx, op.key)
	if errors.Is(err, ErrInstallationNotFound) {
		return ErrDesiredTargetChanged
	}
	if err != nil {
		return err
	}
	if target.Revision != op.desiredRevision || target.RepairOperationID != op.operationID || target.Version != op.version || target.DefinitionRevision != op.dep.Revision || target.ManifestDigest != op.dep.ManifestDigest || target.SourceURL != op.dep.SourceURL {
		return ErrDesiredTargetChanged
	}
	return nil
}

func (s *Service) invalidateDesired(ctx context.Context, key InstallationKey, operationID string) error {
	store, ok := s.store.(DesiredStore)
	if !ok {
		return nil
	}
	actor, _ := ctx.Value(repairActorKey{}).(string)
	return store.RevokeDesired(ctx, key, operationID, actor)
}

func (s *Service) finishManagedOperation(ctx context.Context, op *operation, state State, terminal Installation) (Installation, error) {
	store, ok := s.store.(DesiredStore)
	if !ok {
		return s.store.FinishOperation(ctx, op.key, op.operationID, &terminal)
	}
	// Unpublished fixture/legacy definitions remain usable but never become
	// an automatic execution authorization without an immutable publication.
	if !catalog.ValidRevision(state.DefinitionRevision) || !catalog.ValidRevision(strings.TrimPrefix(state.ManifestDigest, "sha256:")) || state.SourceURL == "" || state.RegistryID != "memoh" {
		if op.repair {
			return Installation{}, ErrDefinitionInvalid
		}
		return s.store.FinishOperation(ctx, op.key, op.operationID, &terminal)
	}
	target := DesiredInstallation{
		InstallationKey: op.key, Revision: op.operationID, Version: state.Version,
		SourceURL: state.SourceURL, RegistryID: state.RegistryID, DefinitionRevision: state.DefinitionRevision, ManifestDigest: state.ManifestDigest,
		AuthorizedByOperationID: op.operationID, AuthorizedByActor: op.authorizedByActor,
		Platform: op.platform, Entrypoints: cloneStringMap(state.Entrypoints), InstallationID: state.InstallationID, PayloadPath: state.PayloadPath, StoreRoot: state.StoreRoot,
	}
	if op.repair {
		if err := s.validateDesiredOperation(ctx, op); err != nil {
			return Installation{}, err
		}
		target.Revision, target.RepairOperationID = op.desiredRevision, op.operationID
		return store.FinishRepair(ctx, target, op.operationID)
	}
	return store.FinishAuthorized(ctx, target, op.operationID)
}

// DesiredForBot reads the trustworthy targets without checking a workspace,
// fetching a recipe, queuing work, or starting a stopped Bot.
func (s *Service) DesiredForBot(ctx context.Context, botID string) ([]DesiredInstallation, error) {
	store, ok := s.store.(DesiredStore)
	if !ok {
		return nil, nil
	}
	return store.ListDesired(ctx, botID)
}

// projectDesired keeps fallback discovery facts visible while describing
// whether the managed target the operator chose is actually present. It only
// changes this response; no observation can create or alter an authorization.
func projectDesired(entry *Entry, target *DesiredInstallation, observed bool) {
	entry.Desired = target
	if target == nil || !observed || entry.Status.InProgress() {
		return
	}
	state := entry.Observed.State
	primary := ""
	if len(entry.Dependency.Provides) > 0 {
		primary = entry.Dependency.Provides[0]
	}
	present := false
	for _, candidate := range entry.Observed.Candidates {
		if candidate.Source == SourceManaged && candidate.Path == target.Entrypoints[primary] && candidate.Version == target.Version {
			present = true
			break
		}
	}
	if !present || state == nil || state.InstallationID != target.InstallationID || state.PayloadPath != target.PayloadPath || state.Version != target.Version {
		entry.Status = StatusMissing
	}
	// Removing a lost overlay revokes its durable target even if the image
	// still provides a fallback executable. Toolkit presence is not ownership.
	if ActionSupported(entry.Dependency, catalog.ActionRemove) && !slices.Contains(entry.Actions, catalog.ActionRemove) {
		entry.Actions = append(entry.Actions, catalog.ActionRemove)
	}
}
