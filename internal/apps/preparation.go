package apps

import (
	"context"
	"errors"
	"fmt"
	"strings"

	skillset "github.com/felinics/memoh/internal/skills"
	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// DependencyConfirmation identifies the exact executable publication and version
// approved with an App operation. Linking an existing dependency needs none.
type DependencyConfirmation struct {
	DependencyID       string
	Action             catalog.Action
	Version            string
	DefinitionRevision string
	SourceURL          string
	RegistryID         string
	ManifestDigest     string
}

// PrepareRequest selects an App operation before the user confirms its scripts.
// Resume names an installation; install names an immutable release; update names
// an App and the release/dependencies selected for update.
type PrepareRequest struct {
	Action         string
	RegistryID     string
	AppID          string
	Revision       string
	InstallationID string
	Release        bool
	Dependencies   []string
}

// PreparedOperation contains only dependencies that will execute a script.
// Revision pins the App release, including a release selected for update.
type PreparedOperation struct {
	RegistryID   string
	AppID        string
	Revision     string
	Dependencies []DependencyConfirmation
}

// Prepare resolves exact versions in an explicit management operation. It never
// installs a dependency, publishes Skills or grants automatic repair authority.
func (s *Service) Prepare(ctx context.Context, botID string, req PrepareRequest) (PreparedOperation, error) {
	var release supermarket.AppDescriptor
	var err error
	selected := uniqueDependencyIDs(req.Dependencies)
	materializeRelease := true
	switch req.Action {
	case "install":
		if !validAppRelease(req.RegistryID, req.AppID, req.Revision) {
			return PreparedOperation{}, ErrInvalidRequest
		}
		release, err = s.registry.FetchRelease(ctx, req.RegistryID, req.AppID, req.Revision)
	case "resume":
		var inst Installation
		inst, err = s.store.GetByID(ctx, botID, strings.TrimSpace(req.InstallationID))
		if err == nil {
			release, err = s.releaseFor(ctx, inst)
		}
	case "update":
		if !skillset.IsValidRegistryID(req.RegistryID) || !skillset.IsValidRegistryComponent(req.AppID) || (!req.Release && len(selected) == 0) {
			return PreparedOperation{}, ErrInvalidRequest
		}
		var inst Installation
		inst, err = s.store.Get(ctx, botID, req.RegistryID, req.AppID)
		if errors.Is(err, ErrNotInstalled) && !req.Release {
			err = nil
			release.RegistryID, release.AppID = req.RegistryID, req.AppID
			release.Dependencies = []string{req.AppID}
		} else if err == nil {
			release, err = s.releaseFor(ctx, inst)
		}
		if err == nil {
			err = validateSelectedDependencies(release, selected)
		}
		if err == nil && req.Release {
			var current supermarket.AppDescriptor
			current, err = s.registry.FetchCurrentApp(ctx, inst.RegistryID, inst.AppID)
			if err == nil && current.Revision == inst.Revision && (inst.Status == StatusInstalled || inst.Status == StatusPartial) {
				materializeRelease = false
			}
			if err == nil {
				release, err = s.registry.FetchRelease(ctx, inst.RegistryID, inst.AppID, current.Revision)
			}
		}
	default:
		return PreparedOperation{}, ErrInvalidRequest
	}
	if err != nil {
		return PreparedOperation{}, err
	}
	if err := validateReferences(release); err != nil {
		return PreparedOperation{}, err
	}
	if req.Action != "update" && len(selected) > 0 {
		return PreparedOperation{}, ErrInvalidRequest
	}
	out := PreparedOperation{RegistryID: release.RegistryID, AppID: release.AppID, Revision: release.Revision, Dependencies: []DependencyConfirmation{}}
	needed := release.Dependencies
	if req.Action == "update" && (!req.Release || !materializeRelease) {
		needed = nil
	}
	if len(needed)+len(selected) == 0 {
		return out, nil
	}
	if s.dependencies == nil {
		return PreparedOperation{}, ErrDependenciesUnavailable
	}
	if err := s.dependencies.EnsureRunning(ctx, botID); err != nil {
		return PreparedOperation{}, err
	}
	states, err := s.dependencyStates(ctx, botID)
	if err != nil {
		return PreparedOperation{}, err
	}
	actions := dependencyActions(needed, selected, states)
	// Release order keeps prerequisite ordering authored in the immutable App;
	// selected updates run first, as they do during materialization.
	for _, id := range uniqueDependencyIDs(append(append([]string{}, selected...), needed...)) {
		action, execute := actions[id]
		if !execute {
			continue
		}
		target, err := s.dependencies.PrepareInstall(ctx, botID, id, action, "")
		if err != nil {
			return PreparedOperation{}, err
		}
		out.Dependencies = append(out.Dependencies, confirmation(target))
	}
	return out, nil
}

func validAppRelease(registryID, appID, revision string) bool {
	return skillset.IsValidRegistryID(registryID) && skillset.IsValidRegistryComponent(appID) && supermarket.IsCanonicalSHA256(revision)
}

func validateSelectedDependencies(release supermarket.AppDescriptor, selected []string) error {
	if len(selected) > 0 && release.RegistryID != DependencyRegistryID {
		return ErrInvalidRequest
	}
	allowed := make(map[string]bool, len(release.Dependencies))
	for _, id := range release.Dependencies {
		allowed[id] = true
	}
	for _, id := range selected {
		if !allowed[id] {
			return fmt.Errorf("%w: App does not reference dependency %s", ErrInvalidRequest, id)
		}
	}
	return nil
}

func dependencyActions(required, selected []string, states map[string]workspacedeps.Entry) map[string]catalog.Action {
	actions := make(map[string]catalog.Action)
	for _, id := range required {
		if !dependencyPresent(states[id]) {
			actions[id] = catalog.ActionInstall
		}
	}
	for _, id := range selected {
		actions[id] = catalog.ActionUpdate
	}
	return actions
}

func confirmation(target workspacedeps.PreparedInstall) DependencyConfirmation {
	return DependencyConfirmation{DependencyID: target.DependencyID, Action: target.Action, Version: target.Version, DefinitionRevision: target.DefinitionRevision, SourceURL: target.SourceURL, RegistryID: target.RegistryID, ManifestDigest: target.ManifestDigest}
}

// validateDependencyConfirmations does not resolve mutable versions or run
// metadata scripts. All required targets are checked before the first mutation.
func (s *Service) validateDependencyConfirmations(ctx context.Context, botID string, required, selected []string, states map[string]workspacedeps.Entry, confirmations []DependencyConfirmation) (map[string]DependencyConfirmation, error) {
	actions := dependencyActions(required, selected, states)
	allowed := make(map[string]bool)
	for _, id := range append(append([]string{}, required...), selected...) {
		allowed[id] = true
	}
	confirmed := make(map[string]DependencyConfirmation, len(confirmations))
	for _, target := range confirmations {
		if !allowed[target.DependencyID] || confirmed[target.DependencyID].DependencyID != "" || !workspacedeps.ExactVersion(target.Version) || !supermarket.IsCanonicalSHA256(target.DefinitionRevision) || (target.Action != catalog.ActionInstall && target.Action != catalog.ActionUpdate) {
			return nil, fmt.Errorf("%w: invalid dependency confirmation", ErrInvalidRequest)
		}
		confirmed[target.DependencyID] = target
	}
	for id, action := range actions {
		target, ok := confirmed[id]
		if !ok || target.Action != action {
			return nil, fmt.Errorf("%w: dependency %s requires a prepared confirmation", ErrInvalidRequest, id)
		}
		if s.dependencies == nil {
			return nil, ErrDependenciesUnavailable
		}
		resolved, err := s.dependencies.PrepareInstall(workspacedeps.WithDefinitionRevision(ctx, target.DefinitionRevision), botID, id, action, target.Version)
		if err != nil {
			return nil, err
		}
		if confirmation(resolved) != target {
			return nil, fmt.Errorf("%w: dependency confirmation changed", ErrInvalidRequest)
		}
	}
	return confirmed, nil
}
