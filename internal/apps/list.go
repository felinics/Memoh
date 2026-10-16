package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/connectors"
	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

// DependencyItem is one dependency reference of an App as seen on a
// bot workspace.
type DependencyItem struct {
	ID string
	// Entry is the reconciled dependency, nil when the catalog does not
	// know the ID.
	Entry *workspacedeps.Entry
	// Shared is set when another installed App references the same
	// dependency on this target.
	Shared bool
}

// ConnectorItem is one connector reference of an App.
type ConnectorItem struct {
	Type         string
	Required     bool
	ConnectionID string
	// Connector is the bot's linked connection, nil until authorized.
	Connector *connectors.Connector
}

// Item is one App in a bot's isolated workspace: an installation record with its
// components, or a dependency the workspace already carries shown through
// its canonical App.
type Item struct {
	// Installation is nil for a discovered App.
	Installation *Installation
	RegistryID   string
	AppID        string
	Revision     string
	Version      string
	// Release is the materialized release; nil for discovered Apps and
	// for records whose cached release cannot be decoded.
	Release      *supermarket.AppDescriptor
	Discovered   bool
	Dependencies []DependencyItem
	Connectors   []ConnectorItem
}

// ListResult is the App view of one bot's isolated workspace.
type ListResult struct {
	Workspace              workspacedeps.WorkspaceState
	DataRoot               string
	DependencyCatalogStale bool
	// DependencyCatalogRefreshing is set while the dependency catalog is
	// refreshed in the background; listing again shortly may show more.
	DependencyCatalogRefreshing bool
	// UpdateChecksIncomplete is set by CheckUpdates when its budget ran out
	// before every App and dependency check finished; the unfinished ones
	// keep their last check.
	UpdateChecksIncomplete bool
	Items                  []Item
}

// List returns every installed App of a bot workspace together with
// the canonical Apps of dependencies no installation references.
func (s *Service) List(ctx context.Context, botID string, refresh bool) (ListResult, error) {
	installations, err := s.store.ListForBot(ctx, botID)
	if err != nil {
		return ListResult{}, fmt.Errorf("apps: list installations: %w", err)
	}
	var deps workspacedeps.ListResult
	if s.dependencies != nil {
		if refresh {
			deps, err = s.dependencies.Refresh(ctx, botID)
		} else {
			deps, err = s.dependencies.List(ctx, botID)
		}
		if err != nil {
			return ListResult{}, err
		}
	}
	return s.assemble(ctx, botID, installations, deps)
}

// Get returns one installation with its components.
func (s *Service) Get(ctx context.Context, botID, installationID string) (Item, error) {
	inst, err := s.store.GetByID(ctx, botID, installationID)
	if err != nil {
		return Item{}, err
	}
	var deps workspacedeps.ListResult
	if s.dependencies != nil {
		deps, err = s.dependencies.List(ctx, botID)
		if err != nil {
			return Item{}, err
		}
	}
	result, err := s.assemble(ctx, botID, []Installation{inst}, deps)
	if err != nil {
		return Item{}, err
	}
	for _, item := range result.Items {
		if item.Installation != nil && item.Installation.ID == inst.ID {
			return item, nil
		}
	}
	return Item{}, ErrNotInstalled
}

func (s *Service) assemble(ctx context.Context, botID string, installations []Installation, deps workspacedeps.ListResult) (ListResult, error) {
	entries := indexEntries(deps)
	botRefs, err := s.store.ListBotDependencyRefs(ctx, botID)
	if err != nil {
		return ListResult{}, fmt.Errorf("apps: list dependency references: %w", err)
	}
	refCounts := make(map[string]int, len(botRefs))
	for _, ref := range botRefs {
		refCounts[ref.DependencyID]++
	}
	connectionsByID := s.connectionsByID(ctx, botID)
	result := ListResult{
		Workspace:                   deps.Workspace,
		DataRoot:                    deps.DataRoot,
		DependencyCatalogStale:      deps.CatalogStale,
		DependencyCatalogRefreshing: deps.CatalogRefreshing,
		Items:                       make([]Item, 0, len(installations)),
	}
	for _, inst := range installations {
		item, err := s.item(ctx, inst, entries, refCounts, connectionsByID)
		if err != nil {
			return ListResult{}, err
		}
		result.Items = append(result.Items, item)
	}
	for _, entry := range deps.Entries {
		if refCounts[entry.Dependency.ID] > 0 || entry.Dependency.Retired {
			continue
		}
		if !entry.Observed.Present && entry.Installation == nil {
			continue
		}
		entry := entry
		result.Items = append(result.Items, Item{
			RegistryID: DependencyRegistryID, AppID: entry.Dependency.ID, Discovered: true,
			Dependencies: []DependencyItem{{ID: entry.Dependency.ID, Entry: &entry}},
			Connectors:   []ConnectorItem{},
		})
	}
	sort.SliceStable(result.Items, func(i, j int) bool {
		if result.Items[i].RegistryID != result.Items[j].RegistryID {
			return result.Items[i].RegistryID < result.Items[j].RegistryID
		}
		return result.Items[i].AppID < result.Items[j].AppID
	})
	return result, nil
}

func (s *Service) item(ctx context.Context, inst Installation, entries map[string]workspacedeps.Entry, refCounts map[string]int, connectionsByID map[string]connectors.Connector) (Item, error) {
	installation := inst
	item := Item{
		Installation: &installation,
		RegistryID:   inst.RegistryID, AppID: inst.AppID,
		Revision: inst.Revision, Version: inst.Version,
		Dependencies: []DependencyItem{}, Connectors: []ConnectorItem{},
	}
	if len(inst.Release) > 0 {
		var release supermarket.AppDescriptor
		if err := json.Unmarshal(inst.Release, &release); err == nil {
			release = normalizeRelease(release)
			item.Release = &release
		} else {
			s.logger.WarnContext(ctx, "decode cached App release", slog.String("installation_id", inst.ID), slog.Any("error", err))
		}
	}
	depRefs, err := s.store.ListDependencyRefs(ctx, inst.ID)
	if err != nil {
		return Item{}, fmt.Errorf("apps: list dependency references: %w", err)
	}
	for _, ref := range depRefs {
		dep := DependencyItem{ID: ref.DependencyID, Shared: refCounts[ref.DependencyID] > 1}
		if entry, ok := entries[ref.DependencyID]; ok {
			entry := entry
			dep.Entry = &entry
		}
		item.Dependencies = append(item.Dependencies, dep)
	}
	connRefs, err := s.store.ListConnectorRefs(ctx, inst.ID)
	if err != nil {
		return Item{}, fmt.Errorf("apps: list connector references: %w", err)
	}
	for _, ref := range connRefs {
		conn := ConnectorItem{Type: ref.ConnectorType, Required: ref.Required, ConnectionID: ref.ConnectionID}
		if linked, ok := connectionsByID[ref.ConnectionID]; ok && ref.ConnectionID != "" {
			linked := linked
			conn.Connector = &linked
		}
		item.Connectors = append(item.Connectors, conn)
	}
	return item, nil
}

func (s *Service) connectionsByID(ctx context.Context, botID string) map[string]connectors.Connector {
	result := make(map[string]connectors.Connector)
	if s.connectors == nil || !s.connectors.Configured() {
		return result
	}
	items, err := s.connectors.List(ctx, botID)
	if err != nil {
		s.logger.WarnContext(ctx, "list bot connectors", slog.String("bot_id", botID), slog.Any("error", err))
		return result
	}
	for _, item := range items {
		result[item.ConnectionID] = item
	}
	return result
}

// appCheckConcurrency bounds the registry requests one manual update check
// makes at once.
const appCheckConcurrency = 8

// CheckUpdates compares every installed App with the registry's current
// release, records what is available, refreshes the dependency update
// checks, and returns the refreshed view.
//
// The App and dependency checks run side by side, each concurrently, on a
// context detached from the request and bounded by the update check budget,
// so a reverse proxy's response timeout does not cut the request off and a
// dropped connection does not abandon the checks halfway. A check the budget
// cuts short keeps its last result and marks the view UpdateChecksIncomplete.
func (s *Service) CheckUpdates(ctx context.Context, botID string) (ListResult, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.updateCheckBudget)
	defer cancel()
	installations, err := s.store.ListForBot(ctx, botID)
	if err != nil {
		return ListResult{}, fmt.Errorf("apps: list installations: %w", err)
	}
	var (
		deps     workspacedeps.ListResult
		depsErr  error
		depsDone = make(chan struct{})
	)
	if s.dependencies != nil {
		go func() {
			defer close(depsDone)
			deps, depsErr = s.dependencies.CheckUpdates(ctx, botID)
		}()
	} else {
		close(depsDone)
	}
	appsIncomplete, appsErr := s.checkAppUpdates(ctx, botID, installations)
	<-depsDone
	if appsErr != nil {
		return ListResult{}, appsErr
	}

	// The budget may be spent; assembling the recorded results gets its own
	// short deadline.
	listCtx, cancelList := context.WithTimeout(context.WithoutCancel(ctx), updateCheckListTimeout)
	defer cancelList()
	if depsErr != nil {
		s.logger.WarnContext(ctx, "check dependency updates", slog.String("bot_id", botID), slog.Any("error", depsErr))
		deps, err = s.dependencies.List(listCtx, botID)
		if err != nil {
			return ListResult{}, err
		}
	}
	installations, err = s.store.ListForBot(listCtx, botID)
	if err != nil {
		return ListResult{}, fmt.Errorf("apps: list installations: %w", err)
	}
	result, err := s.assemble(listCtx, botID, installations, deps)
	if err != nil {
		return ListResult{}, err
	}
	result.UpdateChecksIncomplete = appsIncomplete || deps.ChecksIncomplete
	return result, nil
}

// updateCheckListTimeout bounds reading back the results of an update check
// whose budget may already be spent.
const updateCheckListTimeout = 10 * time.Second

// checkAppUpdates asks the registry for the current release of every
// installation and records the answer. incomplete reports installations the
// budget on ctx left unchecked; err is the first registry or store failure.
func (s *Service) checkAppUpdates(ctx context.Context, botID string, installations []Installation) (incomplete bool, err error) {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		slots = make(chan struct{}, appCheckConcurrency)
	)
	now := s.now().UTC()
	for _, inst := range installations {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			mu.Lock()
			incomplete = true
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(inst Installation) {
			defer wg.Done()
			defer func() { <-slots }()
			checkErr := s.checkAppUpdate(ctx, botID, inst, now)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case checkErr == nil:
			case ctx.Err() != nil:
				// Cut short by the budget: the record keeps its last check.
				incomplete = true
			case err == nil:
				err = checkErr
			}
		}(inst)
	}
	wg.Wait()
	return incomplete, err
}

func (s *Service) checkAppUpdate(ctx context.Context, botID string, inst Installation, now time.Time) error {
	current, err := s.registry.FetchCurrentApp(ctx, inst.RegistryID, inst.AppID)
	switch {
	case err == nil && current.Revision != inst.Revision:
		_, err = s.store.SetCheck(ctx, botID, inst.ID, current.Revision, current.Version, now)
	case err == nil:
		_, err = s.store.SetCheck(ctx, botID, inst.ID, "", "", now)
	case errors.Is(err, supermarket.ErrAppNotFound):
		// The App left the registry; nothing newer can be offered.
		_, err = s.store.SetCheck(ctx, botID, inst.ID, "", "", now)
	default:
		return err
	}
	if err != nil {
		return fmt.Errorf("apps: record update check: %w", err)
	}
	return nil
}
