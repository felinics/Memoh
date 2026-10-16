package supermarket

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/workspace"
)

const maxConcurrentAppPreparations = 2

var appPreparationTokens = make(chan struct{}, maxConcurrentAppPreparations)

type resourceLock struct {
	token chan struct{}
	refs  int
}

var installationResourceLocks = struct {
	sync.Mutex
	items map[string]*resourceLock
}{items: make(map[string]*resourceLock)}

// WorkspaceResolver is the slice of *workspace.Manager the installer needs.
type WorkspaceResolver interface {
	ResolveWorkspaceTarget(ctx context.Context, botID, targetID string) (workspace.ResolvedWorkspaceTarget, error)
}

// Installer materializes the Skills of an App release into a workspace.
// It owns no installation records: the apps service records what it
// installed and commits or rolls back the workspace change accordingly.
type Installer struct {
	client     *Client
	workspaces WorkspaceResolver
	logger     *slog.Logger
}

func NewInstaller(client *Client, workspaces WorkspaceResolver, logger *slog.Logger) *Installer {
	return &Installer{client: client, workspaces: workspaces, logger: logger}
}

type WorkspaceTargetError struct{ Err error }

func (e *WorkspaceTargetError) Error() string { return e.Err.Error() }
func (e *WorkspaceTargetError) Unwrap() error { return e.Err }

func (i *Installer) acquirePreparation(ctx context.Context) (func(), error) {
	if i == nil {
		return nil, errs.New("supermarket installer is not configured")
	}
	select {
	case appPreparationTokens <- struct{}{}:
		return func() { <-appPreparationTokens }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// AcquireInstallationResources serializes callers on the given keys. Keys are
// taken in sorted order so two callers holding overlapping sets cannot
// deadlock. The returned function releases every key once.
func AcquireInstallationResources(ctx context.Context, keys ...string) (func(), error) {
	keys = uniqueSortedStrings(keys)
	releases := make([]func(), 0, len(keys))
	for _, key := range keys {
		release, err := acquireInstallationResource(ctx, key)
		if err != nil {
			for index := len(releases) - 1; index >= 0; index-- {
				releases[index]()
			}
			return nil, err
		}
		releases = append(releases, release)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for index := len(releases) - 1; index >= 0; index-- {
				releases[index]()
			}
		})
	}, nil
}

func acquireInstallationResource(ctx context.Context, key string) (func(), error) {
	installationResourceLocks.Lock()
	item := installationResourceLocks.items[key]
	if item == nil {
		item = &resourceLock{token: make(chan struct{}, 1)}
		item.token <- struct{}{}
		installationResourceLocks.items[key] = item
	}
	item.refs++
	installationResourceLocks.Unlock()

	select {
	case <-ctx.Done():
		releaseInstallationResourceRef(key, item)
		return nil, ctx.Err()
	case <-item.token:
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			item.token <- struct{}{}
			releaseInstallationResourceRef(key, item)
		})
	}, nil
}

func releaseInstallationResourceRef(key string, item *resourceLock) {
	installationResourceLocks.Lock()
	defer installationResourceLocks.Unlock()
	item.refs--
	if item.refs == 0 && installationResourceLocks.items[key] == item {
		delete(installationResourceLocks.items, key)
	}
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// AppInstallationLockKey names the resource one App installation on
// one bot's isolated workspace occupies.
func AppInstallationLockKey(botID, registryID, appID string) string {
	return strings.Join([]string{"app", botID, registryID, appID}, "\x00")
}
