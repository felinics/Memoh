package workspacedeps

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// PreparedInstall freezes the executable definition and exact version a
// management client must confirm. Preparing never runs a provision script.
type PreparedInstall struct {
	DependencyID       string
	Action             catalog.Action
	Version            string
	DefinitionRevision string
	SourceURL          string
	RegistryID         string
	ManifestDigest     string
}

// PrepareInstall is an explicit Manage-authorized metadata operation. A blank
// version may run the frozen recipe's check_update action in a running workspace;
// read-only discovery and script preview never call this method.
func (s *Service) PrepareInstall(ctx context.Context, botID, depID string, action catalog.Action, version string) (PreparedInstall, error) {
	if action != catalog.ActionInstall && action != catalog.ActionUpdate && action != catalog.ActionReinstall {
		return PreparedInstall{}, ErrActionUnsupported
	}
	version = strings.TrimSpace(version)
	if !ValidRequestedVersion(version) {
		return PreparedInstall{}, ErrInvalidVersion
	}
	cat, err := s.operationCatalog(ctx, depID)
	if err != nil {
		return PreparedInstall{}, err
	}
	ctx = context.WithValue(ctx, catalogContextKey{}, CatalogResult{Catalog: cat})
	dep, err := s.dependency(ctx, depID)
	if err != nil {
		return PreparedInstall{}, err
	}
	if dep.Retired {
		return PreparedInstall{}, ErrDefinitionUnavailable
	}
	if !ActionSupported(dep, action) {
		return PreparedInstall{}, ErrActionUnsupported
	}
	if pin := strings.TrimSpace(dep.Version.Pin); pin != "" {
		if version != "" && version != pin {
			return PreparedInstall{}, fmt.Errorf("%w: requested version differs from recipe pin", ErrInvalidVersion)
		}
		version = pin
	}
	if version == "" {
		release, err := s.acquireOperation(ctx, InstallationKey{BotID: botID, DependencyID: dep.ID})
		if err != nil {
			return PreparedInstall{}, err
		}
		defer release()
		if err := s.ensureWorkspace(ctx, botID); err != nil {
			return PreparedInstall{}, err
		}
		client, root, err := s.target(ctx, botID)
		if err != nil {
			return PreparedInstall{}, err
		}
		platform, err := s.platformFor(ctx, botID, client)
		if err != nil {
			return PreparedInstall{}, err
		}
		if !dep.SupportsPlatform(platform.OS, platform.Arch, platform.Libc) {
			return PreparedInstall{}, ErrPlatformUnsupported
		}
		check, err := s.checkUpdate(ctx, client, root, platform, dep, "")
		if err != nil {
			return PreparedInstall{}, err
		}
		version = check.Latest
	}
	if !ExactVersion(version) {
		return PreparedInstall{}, fmt.Errorf("%w: an exact version is required", ErrInvalidVersion)
	}
	return PreparedInstall{
		DependencyID: dep.ID, Action: action, Version: version,
		DefinitionRevision: dep.Revision, SourceURL: dep.SourceURL,
		RegistryID: dep.RegistryID, ManifestDigest: dep.ManifestDigest,
	}, nil
}

// ExactVersion accepts normalized release labels, including distribution
// epochs, four-part versions and build identifiers. Syntax excludes aliases
// and ranges; the frozen recipe must still report exactly this label before
// publication, so a partial request cannot silently authorize its expansion.
var (
	exactVersionPattern    = regexp.MustCompile(`^[0-9][0-9A-Za-z.+_-]{0,99}$`)
	wildcardVersionSegment = regexp.MustCompile(`(?:^|[._+-])[xX](?:[._+-]|$)`)
)

func ExactVersion(version string) bool {
	return ValidRequestedVersion(version) && exactVersionPattern.MatchString(version) && !wildcardVersionSegment.MatchString(version)
}
