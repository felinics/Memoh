package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// ErrRequired means other installed dependencies still need the dependency
// being removed. Callers read the dependents from RequiredError.
var ErrRequired = errors.New("workspace dependency is required by other dependencies")

// RequiredError lists the installed dependencies whose requires name the
// dependency a removal targets.
type RequiredError struct {
	DependencyID string
	Dependents   []string
}

func (e *RequiredError) Error() string {
	return fmt.Sprintf("%s: %s is required by %s", ErrRequired, e.DependencyID, strings.Join(e.Dependents, ", "))
}

func (*RequiredError) Is(target error) bool { return target == ErrRequired }

// installOrder returns ids together with everything they transitively
// require, prerequisites before the dependencies that need them. Each id
// appears once; ties keep the order of ids and of each manifest's requires.
// The catalog validates requires on load, so an unknown id or a cycle only
// comes from a caller passing an id the catalog does not list.
func installOrder(cat *catalog.Catalog, ids []string) ([]string, error) {
	const (
		visiting = 1
		done     = 2
	)
	state := make(map[string]int, len(ids))
	order := make([]string, 0, len(ids))
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("%w: dependency cycle at %q", ErrDefinitionInvalid, id)
		}
		dep, ok := cat.Get(id)
		if !ok {
			return fmt.Errorf("%w: %q", ErrDependencyNotFound, id)
		}
		state[id] = visiting
		for _, required := range dep.Requires {
			if err := visit(required); err != nil {
				return err
			}
		}
		state[id] = done
		order = append(order, id)
		return nil
	}
	for _, id := range ids {
		if err := visit(strings.TrimSpace(id)); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// ensureRequires installs the prerequisites of op.dep that the workspace
// lacks, in dependency order, before op runs its own script. Present copies
// are reused whatever their source. Every missing prerequisite is checked
// for platform support and an install script before any script runs.
// Prerequisites come from op's catalog, so a pinned root revision and its
// prerequisites resolve against the same snapshot.
func (s *Service) ensureRequires(ctx context.Context, op *operation, sink LogSink) error {
	if len(op.dep.Requires) == 0 {
		return nil
	}
	order, err := installOrder(op.catalog, op.dep.Requires)
	if err != nil {
		return err
	}
	// Discover against op's own catalog: the request context may carry no
	// catalog snapshot, and a pinned root changes the catalog fingerprint.
	observed, err := s.discover(ctx, op.client, op.catalog, op.dataRoot, order, op.platform)
	if err != nil {
		return fmt.Errorf("workspacedeps: inspect prerequisites of %s: %w", op.dep.ID, err)
	}
	missing := make([]catalog.Dependency, 0, len(order))
	for _, id := range order {
		if observed[id].Present {
			continue
		}
		dep, _ := op.catalog.Get(id)
		switch {
		case dep.Retired:
			return fmt.Errorf("%w: prerequisite %s of %s is retired", ErrDependencyNotFound, id, op.dep.ID)
		case !dep.SupportsPlatform(op.platform.OS, op.platform.Arch, op.platform.Libc):
			return fmt.Errorf("%w: prerequisite %s of %s on %s/%s/%s", ErrPlatformUnsupported, id, op.dep.ID, op.platform.OS, op.platform.Arch, op.platform.Libc)
		}
		if _, ok := op.catalog.Script(id, catalog.ActionInstall); !ok {
			return fmt.Errorf("%w: prerequisite %s of %s has no install script", ErrActionUnsupported, id, op.dep.ID)
		}
		missing = append(missing, dep)
	}
	for _, dep := range missing {
		if sink != nil {
			sink.Log(StreamStdout, fmt.Sprintf("Installing %s, required by %s", dep.Name, op.dep.Name))
		}
		if err := s.installPrerequisite(ctx, op.catalog, op.key.BotID, dep.ID, sink); err != nil {
			return fmt.Errorf("workspacedeps: install prerequisite %s of %s: %w", dep.ID, op.dep.ID, err)
		}
	}
	return nil
}

// InstallOrder expands ids with their transitive requires from the current
// catalog, prerequisites first. Callers that install several dependencies
// use it to show and run prerequisites as steps of their own.
func (s *Service) InstallOrder(ctx context.Context, ids []string) ([]string, error) {
	ctx, _, err := s.prepareCatalog(ctx, false, false)
	if err != nil {
		return nil, err
	}
	return installOrder(s.catalogFor(ctx), ids)
}

func (s *Service) installPrerequisite(ctx context.Context, cat *catalog.Catalog, botID, depID string, sink LogSink) error {
	op, err := s.beginWith(ctx, cat, botID, depID, "", true)
	if err != nil {
		return err
	}
	defer op.release()
	_, err = s.provision(ctx, op, catalog.ActionInstall, StatusInstalling, sink)
	return err
}

// Dependents lists the script-installed dependencies on the bot whose
// requires name depID. A dependent's requires are read from the definition
// it was installed from when that is cached, and from the current catalog as
// well, so neither an older nor a newer revision loses a relationship. A
// dependent with neither definition available counts as needing depID.
func (s *Service) Dependents(ctx context.Context, botID, depID string) ([]string, error) {
	ctx, _, err := s.prepareCatalog(ctx, false, false)
	if err != nil {
		return nil, err
	}
	return s.dependents(ctx, s.catalogFor(ctx), botID, depID)
}

func (s *Service) dependents(ctx context.Context, cat *catalog.Catalog, botID, depID string) ([]string, error) {
	records, err := s.store.ListForBot(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("workspacedeps: list installations: %w", err)
	}
	var dependents []string
	for _, rec := range records {
		if rec.DependencyID == depID || !scriptInstalled(rec) {
			continue
		}
		requires, known := s.installedRequires(ctx, cat, rec)
		if !known || slices.Contains(requires, depID) {
			dependents = append(dependents, rec.DependencyID)
		}
	}
	slices.Sort(dependents)
	return dependents, nil
}

// scriptInstalled reports whether a record stands for a copy a catalog
// script installed, or is installing. Image and PATH copies need nothing
// from the catalog; adopted PATH copies are recorded as managed too, but only
// a script-installed copy carries the manifest digest of its state.json.
func scriptInstalled(rec Installation) bool {
	if rec.Source != InstallationSourceManaged {
		return false
	}
	switch rec.Status {
	case StatusInstalling, StatusUpdating:
		return true
	case StatusInstalled:
		return rec.ManifestDigest != ""
	default:
		return false
	}
}

func (s *Service) installedRequires(ctx context.Context, cat *catalog.Catalog, rec Installation) ([]string, bool) {
	var requires []string
	known := false
	if current, ok := cat.Get(rec.DependencyID); ok {
		requires, known = append(requires, current.Requires...), true
	}
	if s.provider != nil && rec.DefinitionRevision != "" {
		definition, err := s.provider.StoredDefinition(ctx, DefinitionKey{SourceURL: rec.SourceURL, DependencyID: rec.DependencyID, Revision: rec.DefinitionRevision})
		if err == nil {
			requires, known = append(requires, definition.Dependency().Requires...), true
		}
	}
	return requires, known
}

// checkNotRequired refuses to remove a dependency other installed
// dependencies still need.
func (s *Service) checkNotRequired(ctx context.Context, op *operation) error {
	botID, depID := op.key.BotID, op.dep.ID
	dependents, err := s.dependents(ctx, op.catalog, botID, depID)
	if err != nil {
		return err
	}
	if len(dependents) > 0 {
		return &RequiredError{DependencyID: depID, Dependents: dependents}
	}
	return nil
}
