package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

var (
	// ErrRequired means other installed dependencies still need the dependency
	// being removed. Callers read the dependents from RequiredError.
	ErrRequired = errors.New("workspace dependency is required by other dependencies")
	// ErrPrerequisitesChanged means a missing prerequisite was not confirmed,
	// or was confirmed at another revision; the operation must be reviewed
	// again.
	ErrPrerequisitesChanged = errors.New("workspace dependency prerequisites changed since confirmation")
)

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
// for platform support, an install script, and its confirmed revision before
// any script runs. Prerequisites resolve against op's catalog with the
// confirmed revisions applied, so a pinned root and its prerequisites run
// exactly what the user reviewed.
func (s *Service) ensureRequires(ctx context.Context, op *operation, sink LogSink) error {
	if len(op.dep.Requires) == 0 {
		return nil
	}
	cat, err := s.prerequisiteCatalog(ctx, op)
	if err != nil {
		return err
	}
	op.requires = cat
	order, err := installOrder(cat, op.dep.Requires)
	if err != nil {
		return err
	}
	// Discover against the operation's own catalog: the request context may
	// carry no catalog snapshot, and pinned revisions change its fingerprint.
	observed, err := s.discover(ctx, op.client, cat, op.dataRoot, order, op.platform)
	if err != nil {
		return fmt.Errorf("workspacedeps: inspect prerequisites of %s: %w", op.dep.ID, err)
	}
	confirmed, pinned := PrerequisiteRevisions(ctx)
	missing := make([]catalog.Dependency, 0, len(order))
	for _, id := range order {
		if observed[id].Present {
			continue
		}
		dep, _ := cat.Get(id)
		switch {
		case pinned && confirmed[id] != dep.Revision:
			return fmt.Errorf("%w: prerequisite %s of %s", ErrPrerequisitesChanged, id, op.dep.ID)
		case dep.Retired:
			return fmt.Errorf("%w: prerequisite %s of %s is retired", ErrDependencyNotFound, id, op.dep.ID)
		case !dep.SupportsPlatform(op.platform.OS, op.platform.Arch, op.platform.Libc):
			return fmt.Errorf("%w: prerequisite %s of %s on %s/%s/%s", ErrPlatformUnsupported, id, op.dep.ID, op.platform.OS, op.platform.Arch, op.platform.Libc)
		}
		if _, ok := cat.Script(id, catalog.ActionInstall); !ok {
			return fmt.Errorf("%w: prerequisite %s of %s has no install script", ErrActionUnsupported, id, op.dep.ID)
		}
		missing = append(missing, dep)
	}
	for _, dep := range missing {
		if sink != nil {
			sink.Log(StreamStdout, fmt.Sprintf("Installing %s, required by %s", dep.Name, op.dep.Name))
		}
		if err := s.installPrerequisite(ctx, cat, op.key.BotID, dep.ID, sink); err != nil {
			return fmt.Errorf("workspacedeps: install prerequisite %s of %s: %w", dep.ID, op.dep.ID, err)
		}
	}
	return nil
}

// prerequisiteCatalog is op's catalog with every confirmed prerequisite
// revision applied. Definitions are immutable and verified, so a revision the
// catalog has since replaced still resolves to what the preview showed.
func (s *Service) prerequisiteCatalog(ctx context.Context, op *operation) (*catalog.Catalog, error) {
	confirmed, pinned := PrerequisiteRevisions(ctx)
	if !pinned {
		return op.catalog, nil
	}
	ids := make([]string, 0, len(confirmed))
	for id := range confirmed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	cat := op.catalog
	for _, id := range ids {
		current, known := cat.Get(id)
		if known && current.Revision == confirmed[id] {
			continue
		}
		if s.provider == nil {
			return nil, fmt.Errorf("%w: prerequisite %s", ErrPrerequisitesChanged, id)
		}
		definition, err := s.provider.Definition(ctx, id, confirmed[id])
		if err != nil {
			return nil, err
		}
		next, err := cat.Using(definition.WithRetired(!known || current.Retired))
		if err != nil {
			return nil, fmt.Errorf("%w: prerequisite %s: %w", ErrPrerequisitesChanged, id, err)
		}
		cat = next
	}
	return cat, nil
}

// verifyRequires runs once op has claimed its record, which makes op a
// dependent every later removal sees. A removal that claimed a prerequisite
// first, or finished before the claim, is caught here instead: both sides
// write their claim before reading the other's, so at least one sees the
// other and backs off.
func (s *Service) verifyRequires(ctx context.Context, op *operation) error {
	if len(op.dep.Requires) == 0 {
		return nil
	}
	cat := op.requires
	if cat == nil {
		cat = op.catalog
	}
	order, err := installOrder(cat, op.dep.Requires)
	if err != nil {
		return err
	}
	records, err := s.store.ListForBot(ctx, op.key.BotID)
	if err != nil {
		return fmt.Errorf("workspacedeps: list installations: %w", err)
	}
	for _, rec := range records {
		if rec.Status == StatusRemoving && slices.Contains(order, rec.DependencyID) {
			return fmt.Errorf("%w: prerequisite %s of %s is being removed", ErrBusy, rec.DependencyID, op.dep.ID)
		}
	}
	observed, err := s.discover(ctx, op.client, cat, op.dataRoot, order, op.platform)
	if err != nil {
		return fmt.Errorf("workspacedeps: inspect prerequisites of %s: %w", op.dep.ID, err)
	}
	for _, id := range order {
		if !observed[id].Present {
			return fmt.Errorf("%w: prerequisite %s of %s was removed", ErrBusy, id, op.dep.ID)
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
	// installOrder put this prerequisite's own requires before it.
	op.requires = cat
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
// a script-installed copy carries the manifest digest of its state.json. A
// failed update or reinstall, and an unfinished removal, keep that copy and
// its digest.
func scriptInstalled(rec Installation) bool {
	if rec.Source != InstallationSourceManaged {
		return false
	}
	switch rec.Status {
	case StatusInstalling, StatusUpdating:
		return true
	case StatusInstalled, StatusFailed, StatusRemoving:
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
// dependencies still need. Remove calls it after claiming its record; see
// verifyRequires for the other half.
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
