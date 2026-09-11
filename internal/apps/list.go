package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/felinics/memoh/internal/connectors"
	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

// DependencyItem is one dependency reference of an App as seen on a
// workspace target.
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

// Item is one App on a workspace target: an installation record with its
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

// ListResult is the App view of one workspace target.
type ListResult struct {
	WorkspaceTargetID      string
	Workspace              workspacedeps.WorkspaceState
	DataRoot               string
	DependencyCatalogStale bool
	Items                  []Item
}

// List returns every installed App of a workspace target together with
// the canonical Apps of dependencies no installation references.
func (s *Service) List(ctx context.Context, botID, targetID string, refresh bool) (ListResult, error) {
	targetID, err := s.skills.ResolveTargetID(ctx, botID, targetID)
	if err != nil {
		return ListResult{}, err
	}
	installations, err := s.store.ListForTarget(ctx, botID, targetID)
	if err != nil {
		return ListResult{}, fmt.Errorf("apps: list installations: %w", err)
	}
	var deps workspacedeps.ListResult
	if s.dependencies != nil {
		if refresh {
			deps, err = s.dependencies.Refresh(ctx, botID, targetID)
		} else {
			deps, err = s.dependencies.List(ctx, botID, targetID)
		}
		if err != nil {
			return ListResult{}, err
		}
	}
	return s.assemble(ctx, botID, targetID, installations, deps)
}

// Get returns one installation with its components.
func (s *Service) Get(ctx context.Context, botID, installationID string) (Item, error) {
	inst, err := s.store.GetByID(ctx, botID, installationID)
	if err != nil {
		return Item{}, err
	}
	var deps workspacedeps.ListResult
	if s.dependencies != nil {
		deps, err = s.dependencies.List(ctx, botID, inst.WorkspaceTargetID)
		if err != nil {
			return Item{}, err
		}
	}
	result, err := s.assemble(ctx, botID, inst.WorkspaceTargetID, []Installation{inst}, deps)
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

func (s *Service) assemble(ctx context.Context, botID, targetID string, installations []Installation, deps workspacedeps.ListResult) (ListResult, error) {
	entries := indexEntries(deps)
	targetRefs, err := s.store.ListTargetDependencyRefs(ctx, botID, targetID)
	if err != nil {
		return ListResult{}, fmt.Errorf("apps: list dependency references: %w", err)
	}
	refCounts := make(map[string]int, len(targetRefs))
	for _, ref := range targetRefs {
		refCounts[ref.DependencyID]++
	}
	connectionsByID := s.connectionsByID(ctx, botID)
	result := ListResult{
		WorkspaceTargetID:      targetID,
		Workspace:              deps.Workspace,
		DataRoot:               deps.DataRoot,
		DependencyCatalogStale: deps.CatalogStale,
		Items:                  make([]Item, 0, len(installations)),
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
			s.logger.Warn("decode cached App release", slog.String("installation_id", inst.ID), slog.Any("error", err))
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
		s.logger.Warn("list bot connectors", slog.String("bot_id", botID), slog.Any("error", err))
		return result
	}
	for _, item := range items {
		result[item.ConnectionID] = item
	}
	return result
}

// CheckUpdates compares every installed App with the registry's current
// release, records what is available, refreshes the dependency update
// checks, and returns the refreshed view.
func (s *Service) CheckUpdates(ctx context.Context, botID, targetID string) (ListResult, error) {
	targetID, err := s.skills.ResolveTargetID(ctx, botID, targetID)
	if err != nil {
		return ListResult{}, err
	}
	installations, err := s.store.ListForTarget(ctx, botID, targetID)
	if err != nil {
		return ListResult{}, fmt.Errorf("apps: list installations: %w", err)
	}
	now := s.now().UTC()
	for _, inst := range installations {
		current, err := s.registry.FetchCurrentApp(ctx, inst.RegistryID, inst.AppID)
		switch {
		case err == nil && current.Revision != inst.Revision:
			_, err = s.store.SetCheck(ctx, botID, inst.ID, current.Revision, current.Version, now)
		case err == nil:
			_, err = s.store.SetCheck(ctx, botID, inst.ID, "", "", now)
		case supermarket.ErrorKindOf(err) == supermarket.ErrorNotFound || isNotFound(err):
			// The App left the registry; nothing newer can be offered.
			_, err = s.store.SetCheck(ctx, botID, inst.ID, "", "", now)
		default:
			return ListResult{}, err
		}
		if err != nil {
			return ListResult{}, fmt.Errorf("apps: record update check: %w", err)
		}
	}
	var deps workspacedeps.ListResult
	if s.dependencies != nil {
		deps, err = s.dependencies.CheckUpdates(ctx, botID, targetID)
		if err != nil {
			s.logger.Warn("check dependency updates", slog.String("bot_id", botID), slog.Any("error", err))
			deps, err = s.dependencies.List(ctx, botID, targetID)
			if err != nil {
				return ListResult{}, err
			}
		}
	}
	installations, err = s.store.ListForTarget(ctx, botID, targetID)
	if err != nil {
		return ListResult{}, fmt.Errorf("apps: list installations: %w", err)
	}
	return s.assemble(ctx, botID, targetID, installations, deps)
}

func isNotFound(err error) bool {
	var notFound interface{ NotFound() bool }
	if errors.As(err, &notFound) {
		return notFound.NotFound()
	}
	return false
}
