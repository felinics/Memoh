package arch

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var forbiddenGlobalPersistenceTargets = map[string]bool{
	"common":       true,
	"global":       true,
	"helper":       true,
	"helpers":      true,
	"repository":   true,
	"repositories": true,
	"shared":       true,
	"store":        true,
	"util":         true,
	"utils":        true,
}

// TestEpochHasExpectedSQLCConfigs keeps owner generation explicit while the
// root config remains available for legacy migration fixtures.
func TestEpochHasExpectedSQLCConfigs(t *testing.T) {
	root := repoRoot(t)
	candidates := repositoryFiles(t, root, func(relPath string) bool {
		ext := strings.ToLower(filepath.Ext(relPath))
		return ext == ".yaml" || ext == ".yml"
	})
	var configs []string
	for _, relPath := range candidates {
		data, err := os.ReadFile(filepath.Join(root, relPath)) //nolint:gosec // repository-owned config candidate
		if err != nil {
			t.Fatalf("read SQLC config candidate %s: %v", relPath, err)
		}
		if isSQLCConfig(data) {
			configs = append(configs, relPath)
		}
	}
	// repositoryFiles returns paths sorted, so this list is sorted too. Keeping
	// it sorted rather than in owner order is what makes a renamed owner show
	// up as a one-line diff instead of a silent position mismatch.
	expected := []string{
		"db/postgres/agent/sqlc.yaml",
		"db/postgres/api/sqlc.yaml",
		"db/postgres/channel/sqlc.yaml",
		"db/postgres/iam/sqlc.yaml",
		"db/postgres/media/sqlc.yaml",
		"db/postgres/memory/sqlc.yaml",
		"db/postgres/model/sqlc.yaml",
		"db/postgres/runtime/sqlc.yaml",
		"sqlc.yaml",
	}
	if !slices.IsSorted(expected) {
		t.Fatalf("expected list must stay sorted to match repositoryFiles: %v", expected)
	}
	if !slices.Equal(configs, expected) {
		t.Fatalf("database epoch SQLC configs = %v, want %v", configs, expected)
	}
}

func isSQLCConfig(data []byte) bool {
	var probe struct {
		Version string      `yaml:"version"`
		SQL     []yaml.Node `yaml:"sql"`
	}
	return yaml.Unmarshal(data, &probe) == nil && strings.TrimSpace(probe.Version) != "" && len(probe.SQL) > 0
}

func TestSQLCConfigDetectionDoesNotDependOnFilename(t *testing.T) {
	if !isSQLCConfig([]byte("version: '2'\nsql:\n  - engine: postgresql\n")) {
		t.Fatal("custom-named SQLC config was not detected")
	}
	if isSQLCConfig([]byte("version: 2\nservices:\n  - channel\n")) {
		t.Fatal("unrelated versioned YAML was detected as SQLC config")
	}
}

func TestEpochOwnerSQLCConfigsUseOnlyOwnerMigrationDirectories(t *testing.T) {
	root := repoRoot(t)
	owners := []string{"iam", "api", "model", "media", "agent", "channel", "memory", "runtime"}
	for _, owner := range owners {
		t.Run(owner, func(t *testing.T) {
			relPath := filepath.ToSlash(filepath.Join("db", "postgres", owner, "sqlc.yaml"))
			data, err := os.ReadFile(filepath.Join(root, relPath)) //nolint:gosec // repository-owned SQLC config
			if err != nil {
				t.Fatalf("read %s: %v", relPath, err)
			}
			var config struct {
				SQL []struct {
					Schema yaml.Node `yaml:"schema"`
					Gen    struct {
						Go struct {
							OmitUnusedStructs bool `yaml:"omit_unused_structs"`
						} `yaml:"go"`
					} `yaml:"gen"`
				} `yaml:"sql"`
			}
			if err := yaml.Unmarshal(data, &config); err != nil {
				t.Fatalf("parse %s: %v", relPath, err)
			}
			if len(config.SQL) != 1 {
				t.Fatalf("%s sql entries = %d, want 1", relPath, len(config.SQL))
			}
			sources, err := sqlcSchemaSources(config.SQL[0].Schema)
			if err != nil {
				t.Fatalf("parse %s schema sources: %v", relPath, err)
			}
			want := []string{"migrations"}
			if !slices.Equal(sources, want) {
				t.Errorf("%s schema sources = %v, want %v", relPath, sources, want)
			}
			if owner == "iam" {
				return
			}
			for _, source := range sources {
				if strings.Contains(filepath.ToSlash(source), "iam/migrations") {
					t.Errorf("%s references IAM migrations via %q", relPath, source)
				}
			}
			if !config.SQL[0].Gen.Go.OmitUnusedStructs {
				t.Errorf("%s must enable omit_unused_structs", relPath)
			}
		})
	}
}

func TestEpochOwnerQueriesStayWithinOwnerSchemas(t *testing.T) {
	root := repoRoot(t)
	owners := []string{"iam", "api", "model", "media", "agent", "channel", "memory", "runtime"}
	for _, owner := range owners {
		t.Run(owner, func(t *testing.T) {
			queryRoot := filepath.Join(root, "db", "postgres", owner, "queries")
			entries, err := os.ReadDir(queryRoot)
			if os.IsNotExist(err) {
				return
			}
			if err != nil {
				t.Fatalf("read %s: %v", queryRoot, err)
			}
			for _, entry := range entries {
				if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".sql" {
					continue
				}
				data, err := os.ReadFile(filepath.Join(queryRoot, entry.Name())) //nolint:gosec // repository-owned query
				if err != nil {
					t.Fatalf("read %s: %v", entry.Name(), err)
				}
				source := strings.ReplaceAll(string(data), "iam.memoh_current_team_id()", "")
				for _, foreignOwner := range owners {
					if foreignOwner == owner {
						continue
					}
					if strings.Contains(source, foreignOwner+".") {
						t.Errorf("db/postgres/%s/queries/%s references foreign owner schema %s", owner, entry.Name(), foreignOwner)
					}
				}
			}
		})
	}
}

func sqlcSchemaSources(node yaml.Node) ([]string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		return []string{node.Value}, nil
	case yaml.SequenceNode:
		sources := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("schema source kind = %d, want scalar", item.Kind)
			}
			sources = append(sources, item.Value)
		}
		return sources, nil
	default:
		return nil, fmt.Errorf("schema kind = %d, want scalar or sequence", node.Kind)
	}
}

// TestPreEpochDBTreeHasNoGeneratedOrGlobalPersistencePackages keeps generated
// Go and ownerless persistence targets out of the schema asset tree.
func TestPreEpochDBTreeHasNoGeneratedOrGlobalPersistencePackages(t *testing.T) {
	root := repoRoot(t)
	dbRoot := filepath.Join(root, "db")
	var violations []string
	err := filepath.WalkDir(dbRoot, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)
		if entry.IsDir() {
			if filePath != dbRoot && forbiddenGlobalPersistenceTargets[entry.Name()] {
				violations = append(violations, fmt.Sprintf("%s is an ownerless persistence target", relPath))
			}
			return nil
		}
		if filepath.Ext(filePath) != ".go" {
			return nil
		}
		data, err := os.ReadFile(filePath) //nolint:gosec // repository-owned source path
		if err != nil {
			return err
		}
		generated, err := isGeneratedSQLCGoSource(relPath, data)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", relPath, err)
		}
		if generated {
			violations = append(violations, fmt.Sprintf("%s is a generated SQLC Go package inside db", relPath))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan db tree: %v", err)
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("pre-epoch/generated-cleanup gate: db must remain schema assets plus db/embed.go:\n  %s\nput owner persistence code outside the global db tree", strings.Join(violations, "\n  "))
	}
}

func isGeneratedSQLCGoSource(relPath string, data []byte) (bool, error) {
	parts := strings.Split(filepath.ToSlash(path.Dir(relPath)), "/")
	if slices.Contains(parts, "sqlc") {
		return true, nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			break
		}
		if strings.HasPrefix(line, "// Code generated by sqlc") {
			return true, nil
		}
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, relPath, data, parser.PackageClauseOnly)
	if err != nil {
		return false, err
	}
	return file.Name.Name == "sqlc", nil
}
