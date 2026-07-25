package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	ownerTablePattern = regexp.MustCompile(
		`(?i)CREATE TABLE(?:\s+IF NOT EXISTS)?\s+(\w+)\.(\w+)`,
	)
	sqlTableRefPattern = regexp.MustCompile(
		`(?i)\b(?:FROM|JOIN|INTO|UPDATE|DELETE\s+FROM)\s+([A-Za-z_][\w.]*)`,
	)
	legacyTeamFuncPattern = regexp.MustCompile(`(?i)\bpublic\.memoh_current_team_id\b`)
)

// TestRawSQLQualifiesOwnerSchema keeps hand-written SQL honest about Epoch v2
// physical boundaries.
//
// Every business table lives in an owner schema, and production runs on a
// single pool with a single role, so an unqualified table name does not resolve
// through any per-role search_path. Such a reference compiles, passes every
// unit test, and only fails once it reaches PostgreSQL. The same applies to
// memoh_current_team_id, which moved from public to iam.
//
// This guard reads owner ownership from the baselines rather than a hard-coded
// list, and it inspects only string literals so that prose mentioning a table
// name in a comment is not flagged.
//
// Scope is production code. Test fixtures are excluded because replaying v1
// migrations inside a temporary schema legitimately uses unqualified v1 names;
// fixture correctness is covered by running the integration tests against a
// real Epoch v2 database instead.
func TestRawSQLQualifiesOwnerSchema(t *testing.T) {
	root := repoRoot(t)
	tableOwners := ownerTablesFromBaselines(t, root)

	sources := repositoryFiles(t, root, func(relPath string) bool {
		if filepath.Ext(relPath) != ".go" || strings.HasSuffix(relPath, "_test.go") {
			return false
		}
		return !isGeneratedOrVendored(relPath)
	})

	var violations []string
	for _, relPath := range sources {
		data, err := os.ReadFile(filepath.Join(root, relPath)) //nolint:gosec // repository-owned source path
		if err != nil {
			t.Fatalf("read %s: %v", relPath, err)
		}
		if isGeneratedGoSource(data) {
			continue
		}
		if generated, err := isGeneratedSQLCGoSource(relPath, data); err != nil {
			t.Fatalf("inspect %s: %v", relPath, err)
		} else if generated {
			continue
		}
		for _, literal := range stringLiterals(t, relPath, data) {
			for _, table := range unqualifiedTables(literal, tableOwners) {
				violations = append(violations, fmt.Sprintf(
					"%s references %q without its %q schema",
					relPath, table, tableOwners[table],
				))
			}
			if legacyTeamFuncPattern.MatchString(literal) {
				violations = append(violations, fmt.Sprintf(
					"%s calls public.memoh_current_team_id; the function lives in iam",
					relPath,
				))
			}
		}
	}

	if len(violations) > 0 {
		slices.Sort(violations)
		violations = slices.Compact(violations)
		t.Fatalf(
			"database epoch gate: hand-written SQL must name the owner schema explicitly:\n  %s",
			strings.Join(violations, "\n  "),
		)
	}
}

func unqualifiedTables(sql string, tableOwners map[string]string) []string {
	var found []string
	for _, match := range sqlTableRefPattern.FindAllStringSubmatch(sql, -1) {
		ref := match[1]
		if strings.Contains(ref, ".") {
			continue
		}
		if _, owned := tableOwners[ref]; !owned {
			continue
		}
		if !slices.Contains(found, ref) {
			found = append(found, ref)
		}
	}
	return found
}

// ownerTablesFromBaselines maps every table to the owner schema that creates it.
func ownerTablesFromBaselines(t *testing.T, root string) map[string]string {
	t.Helper()
	baselines := repositoryFiles(t, root, func(relPath string) bool {
		return strings.HasPrefix(relPath, "db/postgres/") &&
			strings.Contains(relPath, "/migrations/") &&
			filepath.Ext(relPath) == ".sql" &&
			!strings.Contains(relPath, "/legacy/")
	})
	if len(baselines) == 0 {
		t.Fatal("no Epoch v2 owner migrations found; the guard would silently pass")
	}

	owners := make(map[string]string)
	for _, relPath := range baselines {
		data, err := os.ReadFile(filepath.Join(root, relPath)) //nolint:gosec // repository-owned migration path
		if err != nil {
			t.Fatalf("read migration %s: %v", relPath, err)
		}
		for _, match := range ownerTablePattern.FindAllStringSubmatch(string(data), -1) {
			schema, table := strings.ToLower(match[1]), strings.ToLower(match[2])
			if table == "goose_db_version" {
				continue
			}
			owners[table] = schema
		}
	}
	if len(owners) == 0 {
		t.Fatal("owner baselines declared no tables; the guard would silently pass")
	}
	return owners
}

func stringLiterals(t *testing.T, relPath string, data []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, relPath, data, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", relPath, err)
	}
	var literals []string
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			// Unquote only fails on malformed literals, which cannot compile.
			return true
		}
		literals = append(literals, value)
		return true
	})
	return literals
}

// isGeneratedGoSource recognizes the conventional generated-code banner, which
// must appear before the package clause.
func isGeneratedGoSource(data []byte) bool {
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if strings.Contains(line, "Code generated") && strings.Contains(line, "DO NOT EDIT") {
			return true
		}
	}
	return false
}

func isGeneratedOrVendored(relPath string) bool {
	parts := strings.Split(relPath, "/")
	for _, part := range parts {
		switch part {
		case "sqlc", "vendor", "node_modules", "bridgepb", "pb":
			return true
		}
	}
	return strings.HasSuffix(relPath, ".pb.go")
}
