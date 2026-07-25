package arch

import (
	"bufio"
	"bytes"
	"fmt"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var broadPackageNames = map[string]bool{
	"common":       true,
	"global":       true,
	"helper":       true,
	"helpers":      true,
	"repository":   true,
	"repositories": true,
	"shared":       true,
	"util":         true,
	"utils":        true,
}

// repositoryFiles returns owned source paths relative to root. It ignores
// dependency and fixture trees because these guards govern this module.
func repositoryFiles(t *testing.T, root string, include func(string) bool) []string {
	t.Helper()

	var files []string
	err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "testdata", "third_party", "vendor":
				if filePath != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if include(rel) {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository source files: %v", err)
	}
	slices.Sort(files)
	return files
}

func repositoryGoFiles(t *testing.T, root string, includeTests bool) []string {
	t.Helper()
	return repositoryFiles(t, root, func(relPath string) bool {
		return strings.HasSuffix(relPath, ".go") &&
			(includeTests || !strings.HasSuffix(relPath, "_test.go"))
	})
}

func repositoryBuildConstraintFiles(t *testing.T, root string) []string {
	t.Helper()
	return repositoryFiles(t, root, isBuildConstraintSource)
}

func isBuildConstraintSource(relPath string) bool {
	switch filepath.Ext(relPath) {
	case ".go", ".s", ".S", ".c", ".cc", ".cpp", ".cxx":
		return true
	default:
		return false
	}
}

// TestPeerDomainsDoNotImportEachOthersInternal prevents one domain from
// reaching through another domain's public contract into its internals.
func TestPeerDomainsDoNotImportEachOthersInternal(t *testing.T) {
	root := repoRoot(t)
	var violations []string
	for _, file := range repositoryGoFiles(t, root, true) {
		for _, importPath := range imports(t, root, file) {
			owner, ok := peerDomainInternalImport(file, importPath)
			if !ok {
				continue
			}
			violations = append(violations, fmt.Sprintf("%s imports private implementation of domain %q: %s", file, owner, importPath))
		}
	}
	if len(violations) > 0 {
		t.Fatalf("peer domains may depend only on public contracts:\n  %s", strings.Join(violations, "\n  "))
	}
}

func peerDomainInternalImport(importer, imported string) (string, bool) {
	consumerParts := strings.Split(filepath.ToSlash(importer), "/")
	if len(consumerParts) < 3 || consumerParts[0] != "domains" {
		return "", false
	}

	if !strings.HasPrefix(imported, modulePrefix) {
		return "", false
	}
	importParts := strings.Split(strings.TrimPrefix(imported, modulePrefix), "/")
	if len(importParts) < 3 || importParts[0] != "domains" || !slices.Contains(importParts[2:], "internal") {
		return "", false
	}
	if consumerParts[1] == importParts[1] {
		return "", false
	}
	return importParts[1], true
}

// TestSplitBuildTagIsConfinedToAgentProfiles keeps topology selection at the
// Agent composition root. Other constraints such as unix, windows, cgo, or
// integration remain unrestricted.
func TestSplitBuildTagIsConfinedToAgentProfiles(t *testing.T) {
	root := repoRoot(t)
	var violations []string
	for _, file := range repositoryBuildConstraintFiles(t, root) {
		for _, expression := range buildConstraints(t, root, file) {
			if !constraintContainsTag(expression, "split") || isAgentProfile(file) {
				continue
			}
			violations = append(violations, fmt.Sprintf("%s uses topology tag split in build constraint %q", file, expression.String()))
		}
	}
	if len(violations) > 0 {
		t.Fatalf("the topology tag split is allowed only in cmd/agent/profile_*.go:\n  %s", strings.Join(violations, "\n  "))
	}
}

func buildConstraints(t *testing.T, root, relPath string) []constraint.Expr {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, relPath)) //nolint:gosec // repository-owned source path
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	expressions, err := buildConstraintsFromHeader(data)
	if err != nil {
		t.Fatalf("parse build constraint in %s: %v", relPath, err)
	}
	return expressions
}

// buildConstraintsFromHeader reads only the leading blank/comment region used
// for Go build constraints. It never interprets Go, cgo, C/C++, or asm bodies.
func buildConstraintsFromHeader(data []byte) ([]constraint.Expr, error) {
	var expressions []constraint.Expr
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "//") {
			if constraint.IsGoBuild(line) || constraint.IsPlusBuild(line) {
				expression, err := constraint.Parse(line)
				if err != nil {
					return nil, err
				}
				expressions = append(expressions, expression)
			}
			continue
		}
		// Go only recognizes constraints preceded by blank lines and line
		// comments. A block comment or source token ends the header.
		return expressions, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return expressions, nil
}

func constraintContainsTag(expression constraint.Expr, tag string) bool {
	switch expression := expression.(type) {
	case *constraint.TagExpr:
		return expression.Tag == tag
	case *constraint.NotExpr:
		return constraintContainsTag(expression.X, tag)
	case *constraint.AndExpr:
		return constraintContainsTag(expression.X, tag) || constraintContainsTag(expression.Y, tag)
	case *constraint.OrExpr:
		return constraintContainsTag(expression.X, tag) || constraintContainsTag(expression.Y, tag)
	default:
		return false
	}
}

func isAgentProfile(relPath string) bool {
	return path.Dir(relPath) == "cmd/agent" &&
		strings.HasPrefix(path.Base(relPath), "profile_") &&
		strings.HasSuffix(relPath, ".go")
}

// TestNoNewBroadPackageTargets blocks ownerless catch-all packages. The only
// exemption is the pre-existing Channel common package; it may shrink or move
// to owned packages, but this list must not grow.
func TestNoNewBroadPackageTargets(t *testing.T) {
	exemptTargets := map[string]string{
		"domains/channel/internal/common": "legacy Channel helpers awaiting owner-specific placement",
	}

	root := repoRoot(t)
	violations := make(map[string]bool)
	for _, file := range repositoryGoFiles(t, root, false) {
		dir := path.Dir(file)
		for _, target := range broadDirectoryTargets(dir) {
			if !isExactBroadTargetExemption(dir, target, exemptTargets) {
				if target == "domains/api/http/shared" {
					continue
				}
				violations[fmt.Sprintf("%s uses broad package directory %q", file, target)] = true
			}
		}

		packageName := sourcePackageName(t, root, file)
		if broadPackageNames[packageName] {
			if _, exempt := exemptTargets[dir]; !exempt {
				violations[fmt.Sprintf("%s declares broad package name %q", file, packageName)] = true
			}
		}
	}
	if len(violations) == 0 {
		return
	}
	messages := make([]string, 0, len(violations))
	for message := range violations {
		messages = append(messages, message)
	}
	slices.Sort(messages)
	t.Fatalf("new ownerless package targets are forbidden:\n  %s\nname the package after its domain owner or concrete capability", strings.Join(messages, "\n  "))
}

func isExactBroadTargetExemption(dir, target string, exemptions map[string]string) bool {
	_, ok := exemptions[target]
	return ok && dir == target
}

func broadDirectoryTargets(dir string) []string {
	parts := strings.Split(filepath.ToSlash(dir), "/")
	var targets []string
	for i, part := range parts {
		if broadPackageNames[part] {
			targets = append(targets, strings.Join(parts[:i+1], "/"))
		}
	}
	return targets
}

func sourcePackageName(t *testing.T, root, relPath string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, relPath), nil, parser.PackageClauseOnly)
	if err != nil {
		t.Fatalf("parse package clause in %s: %v", relPath, err)
	}
	return file.Name.Name
}
