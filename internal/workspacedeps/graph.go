package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrPrerequisiteMissing  = errors.New("required dependency is missing; repair before checking updates")
	ErrPlanChanged          = errors.New("dependency plan must be prepared again")
	ErrDependencyReferenced = errors.New("dependency is still referenced")
	ErrGraphUnresolved      = errors.New("historical dependency relationships are unresolved")
)

// DependencyRelationships records installation intent separately from the
// source of the executable. Pending edges survive failures and Server restarts.
type DependencyRelationships struct {
	PlanID   string   `json:"plan_id,omitempty"`
	Version  string   `json:"version,omitempty"`
	Revision string   `json:"revision"`
	Digest   string   `json:"digest"`
	Requires []string `json:"requires"`
	Pending  []string `json:"pending,omitempty"`
	Explicit bool     `json:"explicit,omitempty"`
	Known    bool     `json:"known"`
}
type DependencyGraph map[string]DependencyRelationships

type AppDependencyUser struct {
	InstallationID string
	AppID          string
}

// GraphStore extends installation claims with workspace-wide mutation admission.
// No database transaction is held while a workspace script runs.
type GraphStore interface {
	SavePlan(context.Context, string, Plan) error
	LoadPlan(context.Context, string, string) (Plan, error)
	ReadGraph(context.Context, string) (DependencyGraph, error)
	ClaimGraph(context.Context, string, string) (DependencyGraph, error)
	RenewGraph(context.Context, string, string) error
	WriteGraph(context.Context, string, string, DependencyGraph) error
	ReleaseGraph(context.Context, string, string) error
	AppUsers(context.Context, string, string) ([]AppDependencyUser, error)
}

type (
	graphContextKey struct{}
	graphAdmission  struct {
		service      *Service
		botID, owner string
		graph        DependencyGraph
	}
)

func (s *Service) graphStore() (GraphStore, error) {
	store, ok := s.store.(GraphStore)
	if !ok {
		return nil, ErrGraphUnresolved
	}
	return store, nil
}

// AcquireGraph is also used by App mutations, before acquiring App or node
// locks. Context propagation makes nested dependency operations reentrant.
func (s *Service) AcquireGraph(ctx context.Context, botID string) (context.Context, func(), error) {
	if a, ok := ctx.Value(graphContextKey{}).(*graphAdmission); ok && a.service == s && a.botID == botID {
		return ctx, func() {}, nil
	}
	store, err := s.graphStore()
	if err != nil {
		return nil, nil, err
	}
	owner := uuid.NewString()
	graph, err := store.ClaimGraph(ctx, botID, owner)
	if err != nil {
		return nil, nil, err
	}
	if graph == nil {
		graph = DependencyGraph{}
	}
	runCtx, cancel := context.WithCancel(ctx)
	admission := &graphAdmission{s, botID, owner, graph}
	runCtx = context.WithValue(runCtx, graphContextKey{}, admission)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				renewal, end := context.WithTimeout(runCtx, 5*time.Second)
				err := store.RenewGraph(renewal, botID, owner)
				end()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	release := func() {
		once.Do(func() {
			close(stop)
			cancel()
			<-done
			final, end := finalizeContext(ctx)
			defer end()
			if err := store.ReleaseGraph(final, botID, owner); err != nil {
				s.logger.WarnContext(final, "release dependency graph", slog.Any("error", err))
			}
		})
	}
	return runCtx, release, nil
}

func (s *Service) saveGraph(ctx context.Context) error {
	a, ok := ctx.Value(graphContextKey{}).(*graphAdmission)
	if !ok || a.service != s {
		return ErrGraphUnresolved
	}
	store, err := s.graphStore()
	if err != nil {
		return err
	}
	return store.WriteGraph(ctx, a.botID, a.owner, a.graph)
}

// reconcileRelationships only trusts the exact installed publication. Missing
// history is an unresolved relationship, never permission to delete a tool.
func (s *Service) reconcileRelationships(ctx context.Context, botID string) error {
	a, ok := ctx.Value(graphContextKey{}).(*graphAdmission)
	if !ok {
		return ErrGraphUnresolved
	}
	records, err := s.store.ListForBot(ctx, botID)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if rec.Source != InstallationSourceManaged {
			continue
		}
		if rel, exists := a.graph[rec.DependencyID]; exists {
			if rel.Known {
				continue
			}
			// A new automatic intent is not a legacy user install. A failed first
			// attempt has no installed publication to reconstruct, but its pending
			// edges were already recorded from a verified plan.
			if !rel.Explicit && rec.InstalledVersion == "" {
				rel.Known = true
				a.graph[rec.DependencyID] = rel
				continue
			}
		}

		rel := a.graph[rec.DependencyID]
		dep, exists := s.catalogFor(ctx).Get(rec.DependencyID)
		if s.provider != nil && rec.DefinitionRevision != "" {
			def, err := s.provider.StoredDefinition(ctx, DefinitionKey{rec.SourceURL, rec.DependencyID, rec.DefinitionRevision})
			exists = err == nil
			if exists {
				dep = def.Dependency()
			}
		}
		if exists && rec.ManifestDigest != "" && dep.ManifestDigest == rec.ManifestDigest && dep.Revision == rec.DefinitionRevision {
			rel.Requires, rel.Known, rel.Revision, rel.Digest = dep.Requires, true, dep.Revision, dep.ManifestDigest
		}
		// Legacy installs have no reliable user/automatic provenance; retain them.
		rel.Explicit = true
		a.graph[rec.DependencyID] = rel
	}
	return s.saveGraph(ctx)
}

// RemovalBlockers combines App references, dependency edges (including pending
// intentions), explicit installation provenance and unresolved legacy records.
func (s *Service) RemovalBlockers(ctx context.Context, botID, depID, excludingApp string, automatic bool) ([]string, error) {
	store, err := s.graphStore()
	if err != nil {
		return nil, err
	}
	graph, err := store.ReadGraph(ctx, botID)
	if err != nil {
		return nil, err
	}
	records, err := s.store.ListForBot(ctx, botID)
	if err != nil {
		return nil, err
	}
	var users []string
	for _, rec := range records {
		if rec.DependencyID != depID && rec.Source == InstallationSourceManaged && !graph[rec.DependencyID].Known {
			users = append(users, "unresolved:"+rec.DependencyID)
		}
	}
	for parent, rel := range graph {
		if parent != depID && (slices.Contains(rel.Requires, depID) || slices.Contains(rel.Pending, depID)) {
			users = append(users, parent)
		}
	}
	if automatic && graph[depID].Explicit {
		users = append(users, "user")
	}
	apps, err := store.AppUsers(ctx, botID, depID)
	if err != nil {
		return nil, err
	}
	for _, app := range apps {
		if app.InstallationID != excludingApp {
			users = append(users, "app:"+app.AppID)
		}
	}
	slices.Sort(users)
	return slices.Compact(users), nil
}

type ReferencedError struct {
	DependencyID string
	Users        []string
}

func (e *ReferencedError) Error() string {
	return fmt.Sprintf("%s: %s (%v)", ErrDependencyReferenced, e.DependencyID, e.Users)
}
func (*ReferencedError) Unwrap() error { return ErrDependencyReferenced }

type appRemovalContextKey struct{}

func WithAppRemoval(ctx context.Context, installationID string) context.Context {
	return context.WithValue(ctx, appRemovalContextKey{}, installationID)
}

type automaticContextKey struct{}

func WithAutomaticDependencies(ctx context.Context) context.Context {
	return context.WithValue(ctx, automaticContextKey{}, true)
}

func GraphOwner(ctx context.Context) string {
	if a, ok := ctx.Value(graphContextKey{}).(*graphAdmission); ok {
		return a.owner
	}
	return ""
}

type removalDefinitionsKey struct{}

// WithRemovalDefinitions binds a reviewed App removal to installed immutable
// publications. The map never supplies scripts or authorizes a different source.
func WithRemovalDefinitions(ctx context.Context, revisions map[string]string) context.Context {
	return context.WithValue(ctx, removalDefinitionsKey{}, revisions)
}
