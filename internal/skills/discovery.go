package skills

import (
	"context"
	"path"
	"slices"
	"strings"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func appendDiscoveryRoots(roots []Root, extra ...Root) []Root {
	if len(extra) == 0 {
		return roots
	}
	seen := make(map[string]struct{}, len(roots)+len(extra))
	for _, root := range roots {
		seen[root.Path] = struct{}{}
	}
	for _, root := range extra {
		if root.Path == "" {
			continue
		}
		if _, ok := seen[root.Path]; ok {
			continue
		}
		seen[root.Path] = struct{}{}
		roots = append(roots, root)
	}
	return roots
}

// orderedDiscoveryRoots defines the source precedence consumed by resolve():
// user > built-in > legacy > compat > Registry.
func orderedDiscoveryRoots(ctx context.Context, client fileClient, rawCompatRoots []string) []Root {
	userRoots, registryRoots := discoverManagedSkillRoots(ctx, client)
	roots := appendDiscoveryRoots(userRoots, DiscoveryRoots(rawCompatRoots)...)
	roots = appendDiscoveryRoots(roots, registryRoots...)
	return roots
}

// discoverManagedSkillRoots walks the canonical namespace/package/Skill layout.
func discoverManagedSkillRoots(ctx context.Context, client fileClient) (userRoots, registryRoots []Root) {
	if client == nil {
		return nil, nil
	}
	namespaces, err := client.ListDirAll(ctx, ManagedDirPath, false)
	if err != nil {
		return nil, nil
	}
	slices.SortFunc(namespaces, func(a, b *pb.FileEntry) int {
		return strings.Compare(a.GetPath(), b.GetPath())
	})
	userRoots = make([]Root, 0)
	registryRoots = make([]Root, 0)
	for _, namespaceEntry := range namespaces {
		if !namespaceEntry.GetIsDir() {
			continue
		}
		namespaceID := path.Base(namespaceEntry.GetPath())
		if !IsValidName(namespaceID) {
			continue
		}
		namespacePath, err := skillNamespaceDirForID(namespaceID)
		if err != nil {
			continue
		}
		packages, err := client.ListDirAll(ctx, namespacePath, false)
		if err != nil {
			continue
		}
		slices.SortFunc(packages, func(a, b *pb.FileEntry) int {
			return strings.Compare(a.GetPath(), b.GetPath())
		})
		for _, packageEntry := range packages {
			if !packageEntry.GetIsDir() {
				continue
			}
			packageID := path.Base(packageEntry.GetPath())
			if !IsValidName(packageID) {
				continue
			}
			packagePath, err := SkillPackageDirForIDs(namespaceID, packageID)
			if err != nil {
				continue
			}
			if namespaceID == UserSkillNamespace {
				userRoots = append(userRoots, Root{Path: packagePath, Kind: SourceKindManaged, Managed: true})
				continue
			}
			skills, err := client.ListDirAll(ctx, packagePath, false)
			if err != nil {
				continue
			}
			slices.SortFunc(skills, func(a, b *pb.FileEntry) int {
				return strings.Compare(a.GetPath(), b.GetPath())
			})
			for _, skillEntry := range skills {
				if !skillEntry.GetIsDir() {
					continue
				}
				skillID := path.Base(skillEntry.GetPath())
				if !IsValidName(skillID) {
					continue
				}
				skillPath, err := SkillDirForIDs(namespaceID, packageID, skillID)
				if err != nil {
					continue
				}
				registryRoots = append(registryRoots, Root{
					Path:    skillPath,
					Kind:    SourceKindRegistry,
					Managed: true,
				})
			}
		}
	}
	return userRoots, registryRoots
}
