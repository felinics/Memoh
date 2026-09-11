package apps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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
	return s.plan(ctx, inst)
}

func (s *Service) plan(ctx context.Context, inst Installation) (RemovalPreview, error) {
	preview := RemovalPreview{Installation: inst, Dependencies: []RemovalPreviewDependency{}, Connectors: []RemovalPreviewConnector{}, RequiredApps: []Installation{}}
	depRefs, err := s.store.ListDependencyRefs(ctx, inst.ID)
	if err != nil {
		return RemovalPreview{}, fmt.Errorf("apps: list dependency references: %w", err)
	}
	targetRefs, err := s.store.ListTargetDependencyRefs(ctx, inst.BotID, inst.WorkspaceTargetID)
	if err != nil {
		return RemovalPreview{}, fmt.Errorf("apps: list dependency references: %w", err)
	}
	states, statesErr := s.dependencyStates(ctx, inst.BotID, inst.WorkspaceTargetID)
	if statesErr != nil {
		s.logger.Warn("inspect dependencies before removal", slog.String("installation_id", inst.ID), slog.Any("error", statesErr))
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
// script failures are reported as steps and do not keep the record.
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
	plan, err := s.plan(ctx, inst)
	if err != nil {
		return OperationResult{}, err
	}
	if inst, err = s.store.SetStatus(ctx, botID, inst.ID, StatusRemoving, ""); err != nil {
		return OperationResult{}, fmt.Errorf("apps: record removal: %w", err)
	}
	sink.Send(Event{Type: EventStarted, Kind: KindApp, ID: inst.AppID, Version: inst.Version})
	result := OperationResult{Installation: inst}
	var problems []string
	record := func(step StepResult) {
		result.Steps = append(result.Steps, step)
		if step.Status == StepFailed {
			problems = append(problems, step.Kind+" "+step.ID+": "+step.Error)
		}
		sink.Send(Event{Type: EventStepDone, Kind: step.Kind, ID: step.ID, Status: step.Status, Message: step.Error})
	}

	sink.Send(Event{Type: EventStep, Kind: KindSkills, ID: inst.AppID})
	tx, err := s.skills.RemoveSkills(ctx, botID, inst.WorkspaceTargetID, inst.RegistryID, inst.AppID, inst.Revision)
	if err != nil {
		return result, s.failInstallation(ctx, inst, err)
	}
	record(StepResult{Kind: KindSkills, ID: inst.AppID, Status: StepRemoved})

	for _, dep := range plan.Dependencies {
		sink.Send(Event{Type: EventStep, Kind: KindDependency, ID: dep.ID})
		if dep.Action == RemovalActionRemove && s.dependencies != nil {
			if _, err := s.dependencies.Remove(ctx, botID, inst.WorkspaceTargetID, dep.ID, logSink(sink, KindDependency, dep.ID)); err != nil {
				record(StepResult{Kind: KindDependency, ID: dep.ID, Status: StepFailed, Error: err.Error()})
			} else {
				record(StepResult{Kind: KindDependency, ID: dep.ID, Status: StepRemoved})
			}
		} else {
			record(StepResult{Kind: KindDependency, ID: dep.ID, Status: StepKept, Error: dep.Reason})
		}
		if err := s.store.RemoveDependencyRef(ctx, inst.ID, dep.ID); err != nil {
			return result, errors.Join(s.failInstallation(ctx, inst, fmt.Errorf("apps: drop dependency reference %s: %w", dep.ID, err)), tx.Rollback(ctx))
		}
	}

	for _, conn := range plan.Connectors {
		sink.Send(Event{Type: EventStep, Kind: KindConnector, ID: conn.Type})
		if conn.Action == RemovalActionDisconnect && s.connectors != nil {
			if err := s.connectors.Delete(ctx, botID, conn.ConnectionID); err != nil && !isNotFound(err) && !errors.Is(err, errConnectorGone) {
				record(StepResult{Kind: KindConnector, ID: conn.Type, Status: StepFailed, Error: err.Error()})
			} else {
				record(StepResult{Kind: KindConnector, ID: conn.Type, Status: StepDisconnected})
			}
		} else {
			record(StepResult{Kind: KindConnector, ID: conn.Type, Status: StepKept, Error: conn.Reason})
		}
		if err := s.store.RemoveConnectorRef(ctx, inst.ID, conn.Type); err != nil {
			return result, errors.Join(s.failInstallation(ctx, inst, fmt.Errorf("apps: drop connector reference %s: %w", conn.Type, err)), tx.Rollback(ctx))
		}
	}

	removed, err := s.store.Delete(ctx, botID, inst.ID)
	if err != nil {
		return result, errors.Join(s.failInstallation(ctx, inst, fmt.Errorf("apps: delete installation: %w", err)), tx.Rollback(ctx))
	}
	if err := tx.Commit(ctx); err != nil {
		s.logger.Warn("cleanup removed App Skills failed", slog.String("app_id", inst.AppID), slog.Any("error", err))
	}
	result.Installation = removed
	if len(problems) > 0 {
		s.logger.Warn("App removed with component failures", slog.String("app_id", inst.AppID), slog.String("problems", strings.Join(problems, "; ")))
	}
	if opts.RemoveUnreferencedRequired {
		for _, required := range plan.RequiredApps {
			nested, err := s.Remove(ctx, botID, required.ID, RemoveOptions{}, sink)
			result.Steps = append(result.Steps, nested.Steps...)
			if err != nil {
				s.logger.Warn("remove auto-installed App", slog.String("app_id", required.AppID), slog.Any("error", err))
			}
		}
	}
	sink.Send(Event{Type: EventDone, Kind: KindApp, ID: inst.AppID, Status: "removed"})
	return result, nil
}

// errConnectorGone matches a connection Connect-It no longer knows; the
// connectors service already treats a 404 as deleted, so this only guards
// wrapped sentinel errors from fakes.
var errConnectorGone = errors.New("connector connection is gone")
