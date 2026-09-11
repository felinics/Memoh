package apps

import (
	"context"
	"errors"
	"fmt"

	"github.com/felinics/memoh/internal/connectors"
	"github.com/felinics/memoh/internal/workspacedeps"
)

// Removal actions and reasons reported by RemovalPreview.
const (
	RemovalActionRemove     = "remove"
	RemovalActionKeep       = "keep"
	RemovalActionDisconnect = "disconnect"
	RemovalActionNone       = "none"

	RemovalReasonShared = "shared"
	RemovalReasonImage  = "image"
	RemovalReasonAbsent = "absent"
)

// RemovalPreviewDependency says what removing the App does to one
// dependency reference.
type RemovalPreviewDependency struct {
	ID     string
	Action string
	Reason string
}

// RemovalPreviewConnector says what removing the App does to one
// connector reference.
type RemovalPreviewConnector struct {
	Type         string
	ConnectionID string
	Action       string
	Reason       string
}

// RemovalPreview is the plan of an App removal.
type RemovalPreview struct {
	Installation Installation
	Dependencies []RemovalPreviewDependency
	Connectors   []RemovalPreviewConnector
	// RequiredApps are auto-installed Apps that no other App
	// would reference once this one is gone.
	RequiredApps []Installation
}

// RemoveOptions tunes Remove.
type RemoveOptions struct {
	// RemoveUnreferencedRequired also removes the auto-installed Apps
	// listed by RemovalPreview.RequiredApps.
	RemoveUnreferencedRequired bool
}

// RemovalPreview reports what Remove would do without changing anything.
func (s *Service) RemovalPreview(ctx context.Context, botID, installationID string) (RemovalPreview, error) {
	inst, err := s.store.GetByID(ctx, botID, installationID)
	if err != nil {
		return RemovalPreview{}, err
	}
	return s.plan(ctx, inst, false)
}

func (s *Service) plan(ctx context.Context, inst Installation, prepareWorkspace bool) (RemovalPreview, error) {
	preview := RemovalPreview{Installation: inst, Dependencies: []RemovalPreviewDependency{}, Connectors: []RemovalPreviewConnector{}, RequiredApps: []Installation{}}
	depRefs, err := s.store.ListDependencyRefs(ctx, inst.ID)
	if err != nil {
		return RemovalPreview{}, fmt.Errorf("apps: list dependency references: %w", err)
	}
	targetRefs, err := s.store.ListTargetDependencyRefs(ctx, inst.BotID, inst.WorkspaceTargetID)
	if err != nil {
		return RemovalPreview{}, fmt.Errorf("apps: list dependency references: %w", err)
	}
	var states map[string]workspacedeps.Entry
	if prepareWorkspace {
		states, err = s.cleanupDependencyStates(ctx, inst, depRefs, targetRefs)
	} else {
		states, err = s.dependencyStates(ctx, inst.BotID, inst.WorkspaceTargetID)
	}
	if err != nil {
		return RemovalPreview{}, fail("inspect dependencies before removal", err)
	}
	for _, ref := range depRefs {
		item := RemovalPreviewDependency{ID: ref.DependencyID, Action: RemovalActionRemove}
		entry, known := states[ref.DependencyID]
		switch {
		case referencedByOthers(targetRefs, ref.DependencyID, inst.ID):
			item.Action, item.Reason = RemovalActionKeep, RemovalReasonShared
		case !known || !entry.Observed.Present:
			item.Action, item.Reason = RemovalActionKeep, RemovalReasonAbsent
		case entry.Observed.Source != workspacedeps.SourceManaged:
			item.Action, item.Reason = RemovalActionKeep, RemovalReasonImage
		}
		preview.Dependencies = append(preview.Dependencies, item)
	}
	connRefs, err := s.store.ListConnectorRefs(ctx, inst.ID)
	if err != nil {
		return RemovalPreview{}, fmt.Errorf("apps: list connector references: %w", err)
	}
	botRefs, err := s.store.ListBotConnectorRefs(ctx, inst.BotID)
	if err != nil {
		return RemovalPreview{}, fmt.Errorf("apps: list connector references: %w", err)
	}
	for _, ref := range connRefs {
		item := RemovalPreviewConnector{Type: ref.ConnectorType, ConnectionID: ref.ConnectionID, Action: RemovalActionDisconnect}
		switch {
		case ref.ConnectionID == "":
			item.Action = RemovalActionNone
		case connectionReferencedByOthers(botRefs, ref.ConnectionID, inst.ID):
			item.Action, item.Reason = RemovalActionKeep, RemovalReasonShared
		}
		preview.Connectors = append(preview.Connectors, item)
	}
	required, err := s.orphanedRequired(ctx, inst, targetRefs)
	if err != nil {
		return RemovalPreview{}, err
	}
	preview.RequiredApps = required
	return preview, nil
}

// orphanedRequired lists auto-installed Apps on the target whose every
// dependency would lose its last reference once inst is removed.
func (s *Service) orphanedRequired(ctx context.Context, inst Installation, targetRefs []TargetDependencyRef) ([]Installation, error) {
	installations, err := s.store.ListForTarget(ctx, inst.BotID, inst.WorkspaceTargetID)
	if err != nil {
		return nil, fmt.Errorf("apps: list installations: %w", err)
	}
	remaining := make([]TargetDependencyRef, 0, len(targetRefs))
	for _, ref := range targetRefs {
		if ref.InstallationID != inst.ID {
			remaining = append(remaining, ref)
		}
	}
	result := []Installation{}
	for _, candidate := range installations {
		if candidate.ID == inst.ID || candidate.Reason != ReasonRequired {
			continue
		}
		own := make([]string, 0)
		for _, ref := range remaining {
			if ref.InstallationID == candidate.ID {
				own = append(own, ref.DependencyID)
			}
		}
		if len(own) == 0 {
			// A previous removal may have finished its components but failed
			// before deleting this auto-installed App's record. Include that
			// orphan on retry even though it has no references left to release.
			if candidate.Status == StatusFailed || candidate.Status == StatusRemoving {
				result = append(result, candidate)
			}
			continue
		}
		orphaned := true
		for _, depID := range own {
			if referencedByOthers(remaining, depID, candidate.ID) {
				orphaned = false
				break
			}
		}
		if orphaned {
			result = append(result, candidate)
		}
	}
	return result, nil
}

func referencedByOthers(refs []TargetDependencyRef, dependencyID, installationID string) bool {
	for _, ref := range refs {
		if ref.DependencyID == dependencyID && ref.InstallationID != installationID {
			return true
		}
	}
	return false
}

func connectionReferencedByOthers(refs []BotConnectorRef, connectionID, installationID string) bool {
	for _, ref := range refs {
		if ref.ConnectionID == connectionID && ref.InstallationID != installationID {
			return true
		}
	}
	return false
}

// Remove uninstalls an App: its Skills, the dependencies no other App
// references, and the connections no other App references. Dependency
// and connector failures retain the installation and unfinished references
// for an explicit removal retry.
func (s *Service) Remove(ctx context.Context, botID, installationID string, opts RemoveOptions, sink EventSink) (OperationResult, error) {
	sink = nonNilSink(sink)
	inst, err := s.store.GetByID(ctx, botID, installationID)
	if err != nil {
		return OperationResult{}, err
	}
	unlock, err := lockInstallation(ctx, botID, inst.WorkspaceTargetID, inst.RegistryID, inst.AppID)
	if err != nil {
		return OperationResult{}, err
	}
	defer unlock()
	inst, err = s.store.GetByID(ctx, botID, installationID)
	if err != nil {
		return OperationResult{}, err
	}
	failRemoval := func(cause error) error {
		return s.failInstallation(ctx, inst, fail("App removal failed", cause))
	}
	plan, err := s.plan(ctx, inst, true)
	if err != nil {
		return OperationResult{}, failRemoval(err)
	}
	if inst, err = s.store.SetStatus(ctx, botID, inst.ID, StatusRemoving, ""); err != nil {
		return OperationResult{}, fmt.Errorf("apps: record removal: %w", err)
	}
	sink.Send(Event{Type: EventStarted, Kind: KindApp, ID: inst.AppID, Version: inst.Version})
	result := OperationResult{Installation: inst}
	record := func(step StepResult) {
		result.Steps = append(result.Steps, step)
		sink.Send(Event{Type: EventStepDone, Kind: step.Kind, ID: step.ID, Status: step.Status, Message: step.Error})
	}

	sink.Send(Event{Type: EventStep, Kind: KindSkills, ID: inst.AppID})
	tx, err := s.skills.RemoveSkills(ctx, botID, inst.WorkspaceTargetID, inst.RegistryID, inst.AppID, inst.Revision)
	if err != nil {
		return result, failRemoval(fail("remove Skills", err))
	}
	record(StepResult{Kind: KindSkills, ID: inst.AppID, Status: StepRemoved})

	for _, dep := range plan.Dependencies {
		sink.Send(Event{Type: EventStep, Kind: KindDependency, ID: dep.ID})
		if dep.Action == RemovalActionRemove && s.dependencies != nil {
			if _, err := s.dependencies.Remove(ctx, botID, inst.WorkspaceTargetID, dep.ID, logSink(sink, KindDependency, dep.ID)); err != nil {
				cause := fail("remove dependency "+dep.ID, err)
				record(StepResult{Kind: KindDependency, ID: dep.ID, Status: StepFailed, Error: publicMessage(cause)})
				return result, failRemoval(errors.Join(cause, tx.Rollback(ctx)))
			} else {
				record(StepResult{Kind: KindDependency, ID: dep.ID, Status: StepRemoved})
			}
		} else {
			record(StepResult{Kind: KindDependency, ID: dep.ID, Status: StepKept, Error: dep.Reason})
		}
		if err := s.store.RemoveDependencyRef(ctx, inst.ID, dep.ID); err != nil {
			return result, errors.Join(failRemoval(fail("drop dependency reference "+dep.ID, err)), tx.Rollback(ctx))
		}
	}

	for _, conn := range plan.Connectors {
		sink.Send(Event{Type: EventStep, Kind: KindConnector, ID: conn.Type})
		if conn.Action == RemovalActionDisconnect && s.connectors == nil {
			cause := fail("disconnect "+conn.Type, connectors.ErrNotConfigured)
			record(StepResult{Kind: KindConnector, ID: conn.Type, Status: StepFailed, Error: publicMessage(cause)})
			return result, failRemoval(errors.Join(cause, tx.Rollback(ctx)))
		}
		if conn.Action == RemovalActionDisconnect {
			if err := s.connectors.Delete(ctx, botID, conn.ConnectionID); err != nil && !isNotFound(err) && !errors.Is(err, errConnectorGone) {
				cause := fail("disconnect "+conn.Type, err)
				record(StepResult{Kind: KindConnector, ID: conn.Type, Status: StepFailed, Error: publicMessage(cause)})
				return result, failRemoval(errors.Join(cause, tx.Rollback(ctx)))
			} else {
				record(StepResult{Kind: KindConnector, ID: conn.Type, Status: StepDisconnected})
			}
		} else {
			record(StepResult{Kind: KindConnector, ID: conn.Type, Status: StepKept, Error: conn.Reason})
		}
		if err := s.store.RemoveConnectorRef(ctx, inst.ID, conn.Type); err != nil {
			return result, errors.Join(failRemoval(fail("drop connector reference "+conn.Type, err)), tx.Rollback(ctx))
		}
	}

	if opts.RemoveUnreferencedRequired {
		childSink := EventFunc(func(event Event) {
			if event.Type != EventStarted && event.Type != EventDone {
				sink.Send(event)
			}
		})
		for _, required := range plan.RequiredApps {
			nested, err := s.Remove(ctx, botID, required.ID, RemoveOptions{}, childSink)
			result.Steps = append(result.Steps, nested.Steps...)
			if err != nil {
				return result, failRemoval(errors.Join(fail("remove required App "+required.AppID, err), tx.Rollback(ctx)))
			}
		}
	}
	// Keep the record until workspace cleanup is confirmed. If the final DB
	// delete fails, the record remains retryable even though Skills are gone.
	if err := tx.Commit(ctx); err != nil {
		return result, failRemoval(fail("finish removing Skills", err))
	}
	removed, err := s.store.Delete(ctx, botID, inst.ID)
	if err != nil {
		return result, failRemoval(fail("delete installation", err))
	}
	result.Installation = removed
	sink.Send(Event{Type: EventDone, Kind: KindApp, ID: inst.AppID, Status: "removed"})
	return result, nil
}

// errConnectorGone matches a connection Connect-It no longer knows; the
// connectors service already treats a 404 as deleted, so this only guards
// wrapped sentinel errors from fakes.
var errConnectorGone = errors.New("connector connection is gone")
