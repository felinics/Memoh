package apps

import (
	"context"

	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

type PrepareRequest struct {
	Action         string   `json:"action"`
	RegistryID     string   `json:"registry_id,omitempty"`
	AppID          string   `json:"app_id,omitempty"`
	Revision       string   `json:"revision,omitempty"`
	InstallationID string   `json:"installation_id,omitempty"`
	Release        bool     `json:"release,omitempty"`
	Dependencies   []string `json:"dependencies,omitempty"`
}
type PreparedOperation struct {
	Revision string             `json:"revision"`
	Plan     workspacedeps.Plan `json:"plan"`
}

func (s *Service) Prepare(ctx context.Context, botID string, req PrepareRequest) (PreparedOperation, error) {
	var release supermarket.AppDescriptor
	var err error
	switch req.Action {
	case "install":
		release, err = s.registry.FetchRelease(ctx, req.RegistryID, req.AppID, req.Revision)
	case "resume":
		var inst Installation
		inst, err = s.store.GetByID(ctx, botID, req.InstallationID)
		if err == nil {
			release, err = s.releaseFor(ctx, inst)
		}
	case "update":
		if req.Release {
			if req.Revision != "" {
				release, err = s.registry.FetchRelease(ctx, req.RegistryID, req.AppID, req.Revision)
			} else {
				release, err = s.registry.FetchCurrentApp(ctx, req.RegistryID, req.AppID)
			}
		} else {
			inst, e := s.store.Get(ctx, botID, req.RegistryID, req.AppID)
			switch {
			case e == nil:
				release, err = s.releaseFor(ctx, inst)
			case isNotFound(e):
				release.RegistryID, release.AppID, release.Dependencies = req.RegistryID, req.AppID, []string{req.AppID}
			default:
				err = e
			}
		}
	default:
		return PreparedOperation{}, ErrInvalidRequest
	}
	if err != nil {
		return PreparedOperation{}, err
	}
	if err = validateReferences(release); err != nil {
		return PreparedOperation{}, err
	}
	roots := dependencyRoots(release.Dependencies)
	if req.Action == "update" {
		if !req.Release {
			roots = nil
		}
		allowed := map[string]bool{}
		for _, id := range release.Dependencies {
			allowed[id] = true
		}
		if req.Release && len(req.Dependencies) > 0 {
			inst, err := s.store.Get(ctx, botID, req.RegistryID, req.AppID)
			if err != nil {
				return PreparedOperation{}, err
			}
			installed, err := s.releaseFor(ctx, inst)
			if err != nil {
				return PreparedOperation{}, err
			}
			for _, id := range installed.Dependencies {
				allowed[id] = true
			}
		}
		for _, id := range uniqueDependencyIDs(req.Dependencies) {
			if !allowed[id] {
				return PreparedOperation{}, ErrInvalidRequest
			}
			found := false
			for i := range roots {
				if roots[i].DependencyID == id {
					roots[i].Action = catalog.ActionUpdate
					roots[i].Ensure = false
					found = true
				}
			}
			if !found {
				roots = append(roots, workspacedeps.PlanRoot{DependencyID: id, Action: catalog.ActionUpdate})
			}
		}
	}
	if len(roots) == 0 {
		return PreparedOperation{Revision: release.Revision, Plan: workspacedeps.Plan{Roots: roots, Nodes: []workspacedeps.PlanNode{}}}, nil
	}
	if s.dependencies == nil {
		return PreparedOperation{}, ErrDependenciesUnavailable
	}
	plan, err := s.dependencies.PreparePlan(ctx, botID, roots)
	return PreparedOperation{Revision: release.Revision, Plan: plan}, err
}
