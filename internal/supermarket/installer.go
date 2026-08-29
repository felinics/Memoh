package supermarket

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/skillpackages"
	"github.com/felinics/memoh/internal/workspace"
)

const maxConcurrentPackagePreparations = 2

var packagePreparationTokens = make(chan struct{}, maxConcurrentPackagePreparations)

type resourceLock struct {
	token chan struct{}
	refs  int
}

var installationResourceLocks = struct {
	sync.Mutex
	items map[string]*resourceLock
}{items: make(map[string]*resourceLock)}

type Installer struct {
	client     *Client
	packages   *skillpackages.Service
	workspaces *workspace.Manager
	logger     *slog.Logger
}

func NewInstaller(client *Client, packages *skillpackages.Service, workspaces *workspace.Manager, logger *slog.Logger) *Installer {
	return &Installer{
		client: client, packages: packages, workspaces: workspaces, logger: logger,
	}
}

type StatusError struct {
	Status  int
	Message string
	Err     error
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return http.StatusText(e.Status)
}

func (e *StatusError) Unwrap() error { return e.Err }

type WorkspaceTargetError struct{ Err error }

func (e *WorkspaceTargetError) Error() string { return e.Err.Error() }
func (e *WorkspaceTargetError) Unwrap() error { return e.Err }

func (i *Installer) acquirePreparation(ctx context.Context) (func(), error) {
	if i == nil {
		return nil, errors.New("supermarket installer is not configured")
	}
	select {
	case packagePreparationTokens <- struct{}{}:
		return func() { <-packagePreparationTokens }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func acquireInstallationResources(ctx context.Context, keys ...string) (func(), error) {
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

func packageInstallationLockKey(botID, targetID, registryID, packageID string) string {
	return strings.Join([]string{"package", botID, targetID, registryID, packageID}, "\x00")
}
