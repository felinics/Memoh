package epoch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"

	"gopkg.in/yaml.v3"
)

const (
	CurrentEpoch = 2
	ManifestPath = "manifest.yaml"
)

var (
	ownerNames = []string{
		"iam",
		"api",
		"model",
		"media",
		"agent",
		"channel",
		"memory",
		"runtime",
	}
	migrationNamePattern = regexp.MustCompile(`^([0-9]{5})_[A-Za-z0-9][A-Za-z0-9._-]*\.sql$`)
	checksumPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Manifest defines the complete active Epoch v2 migration plan.
type Manifest struct {
	Epoch  int      `yaml:"epoch" json:"epoch"`
	Order  []string `yaml:"order" json:"order"`
	Owners []Owner  `yaml:"owners" json:"owners"`
}

// Owner defines one independently versioned schema migration stream.
type Owner struct {
	Name          string          `yaml:"name" json:"name"`
	Schema        string          `yaml:"schema" json:"schema"`
	VersionTable  string          `yaml:"version_table" json:"version_table"`
	MigrationsDir string          `yaml:"migrations_dir" json:"migrations_dir"`
	Version       int64           `yaml:"version" json:"version"`
	Dependencies  []string        `yaml:"dependencies" json:"dependencies"`
	Files         []MigrationFile `yaml:"files" json:"files"`
}

// MigrationFile identifies one immutable published migration.
type MigrationFile struct {
	Path     string `yaml:"path" json:"path"`
	Version  int64  `yaml:"version" json:"version"`
	Checksum string `yaml:"checksum" json:"checksum"`
}

// Parse decodes one strict Manifest YAML document.
func Parse(data []byte) (Manifest, error) {
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, errors.New("parse manifest: multiple YAML documents are not allowed")
		}
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	if err := manifest.validatePlan(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Load reads and validates the active manifest and migration assets.
func Load(fsys fs.FS) (Manifest, error) {
	if fsys == nil {
		return Manifest{}, errors.New("load manifest: filesystem is nil")
	}
	data, err := fs.ReadFile(fsys, ManifestPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("load manifest: %w", err)
	}
	manifest, err := Parse(data)
	if err != nil {
		return Manifest{}, err
	}
	if err := manifest.Validate(fsys); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate verifies the plan and every declared migration file.
func (m Manifest) Validate(fsys fs.FS) error {
	if fsys == nil {
		return errors.New("validate manifest: filesystem is nil")
	}
	if err := m.validatePlan(); err != nil {
		return err
	}
	for _, owner := range m.Owners {
		if err := validateOwnerFiles(fsys, owner); err != nil {
			return fmt.Errorf("validate owner %q files: %w", owner.Name, err)
		}
	}
	return nil
}

func (m Manifest) validatePlan() error {
	if m.Epoch != CurrentEpoch {
		return fmt.Errorf("validate manifest: epoch must be %d", CurrentEpoch)
	}
	if len(m.Order) != len(ownerNames) || len(m.Owners) != len(ownerNames) {
		return fmt.Errorf("validate manifest: order and owners must each contain %d entries", len(ownerNames))
	}

	allowed := make(map[string]struct{}, len(ownerNames))
	for _, name := range ownerNames {
		allowed[name] = struct{}{}
	}
	positions := make(map[string]int, len(m.Owners))
	for i, owner := range m.Owners {
		if m.Order[i] != owner.Name {
			return fmt.Errorf(
				"validate manifest: order[%d]=%q does not match owners[%d].name=%q",
				i,
				m.Order[i],
				i,
				owner.Name,
			)
		}
		if _, ok := allowed[owner.Name]; !ok {
			return fmt.Errorf("validate manifest: owner %q is not allowed", owner.Name)
		}
		if _, duplicate := positions[owner.Name]; duplicate {
			return fmt.Errorf("validate manifest: duplicate owner %q", owner.Name)
		}
		positions[owner.Name] = i
		if err := validateOwner(owner); err != nil {
			return fmt.Errorf("validate owner %q: %w", owner.Name, err)
		}
	}
	for _, name := range ownerNames {
		if _, ok := positions[name]; !ok {
			return fmt.Errorf("validate manifest: missing owner %q", name)
		}
	}
	if m.Order[0] != "iam" {
		return errors.New(`validate manifest: owner "iam" must be first`)
	}

	for i, owner := range m.Owners {
		seen := make(map[string]struct{}, len(owner.Dependencies))
		for _, dependency := range owner.Dependencies {
			if _, duplicate := seen[dependency]; duplicate {
				return fmt.Errorf("validate owner %q: duplicate dependency %q", owner.Name, dependency)
			}
			seen[dependency] = struct{}{}
			position, ok := positions[dependency]
			if !ok {
				return fmt.Errorf("validate owner %q: unknown dependency %q", owner.Name, dependency)
			}
			if position >= i {
				return fmt.Errorf("validate owner %q: dependency %q must precede it", owner.Name, dependency)
			}
		}
		if owner.Name != "iam" && !dependsOnIAM(owner.Name, m.Owners, positions, nil) {
			return fmt.Errorf("validate owner %q: dependency graph must reach iam", owner.Name)
		}
	}
	return nil
}

func validateOwner(owner Owner) error {
	if owner.Schema != owner.Name {
		return fmt.Errorf("schema must be %q", owner.Name)
	}
	wantDir := path.Join(owner.Name, "migrations")
	if owner.MigrationsDir != wantDir || !fs.ValidPath(owner.MigrationsDir) {
		return fmt.Errorf("migrations_dir must be the safe owner path %q", wantDir)
	}
	wantTable := owner.Schema + ".goose_db_version"
	if owner.VersionTable != wantTable {
		return fmt.Errorf("version_table must be %q", wantTable)
	}
	if owner.Version < 1 {
		return errors.New("version must be positive")
	}
	if int64(len(owner.Files)) != owner.Version {
		return fmt.Errorf("files count %d does not equal version %d", len(owner.Files), owner.Version)
	}

	paths := make(map[string]struct{}, len(owner.Files))
	for i, file := range owner.Files {
		wantVersion := int64(i + 1)
		if file.Version != wantVersion {
			return fmt.Errorf("file %q version is %d, want %d", file.Path, file.Version, wantVersion)
		}
		if !fs.ValidPath(file.Path) || path.Dir(file.Path) != owner.MigrationsDir {
			return fmt.Errorf("file path %q must be directly under %q", file.Path, owner.MigrationsDir)
		}
		if _, duplicate := paths[file.Path]; duplicate {
			return fmt.Errorf("duplicate file path %q", file.Path)
		}
		paths[file.Path] = struct{}{}
		matches := migrationNamePattern.FindStringSubmatch(path.Base(file.Path))
		if matches == nil {
			return fmt.Errorf("invalid migration filename %q", file.Path)
		}
		filenameVersion, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil || filenameVersion != file.Version {
			return fmt.Errorf("filename version for %q does not equal %d", file.Path, file.Version)
		}
		if !checksumPattern.MatchString(file.Checksum) {
			return fmt.Errorf("file %q checksum must use sha256:<lowercase hex>", file.Path)
		}
	}
	return nil
}

func validateOwnerFiles(fsys fs.FS, owner Owner) error {
	entries, err := fs.ReadDir(fsys, owner.MigrationsDir)
	if err != nil {
		return fmt.Errorf("read migrations_dir: %w", err)
	}
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && path.Ext(entry.Name()) == ".sql" {
			actual = append(actual, path.Join(owner.MigrationsDir, entry.Name()))
		}
	}
	declared := make([]string, 0, len(owner.Files))
	for _, file := range owner.Files {
		declared = append(declared, file.Path)
		content, err := fs.ReadFile(fsys, file.Path)
		if err != nil {
			return fmt.Errorf("version %d read %q: %w", file.Version, file.Path, err)
		}
		sum := sha256.Sum256(content)
		got := "sha256:" + hex.EncodeToString(sum[:])
		if got != file.Checksum {
			return fmt.Errorf(
				"version %d checksum mismatch for %q: got %s, want %s",
				file.Version,
				file.Path,
				got,
				file.Checksum,
			)
		}
	}
	slices.Sort(actual)
	slices.Sort(declared)
	if !slices.Equal(actual, declared) {
		return fmt.Errorf("published .sql set differs: got %v, want %v", actual, declared)
	}
	return nil
}

func dependsOnIAM(
	name string,
	owners []Owner,
	positions map[string]int,
	seen map[string]struct{},
) bool {
	if name == "iam" {
		return true
	}
	if seen == nil {
		seen = make(map[string]struct{})
	}
	if _, ok := seen[name]; ok {
		return false
	}
	seen[name] = struct{}{}
	for _, dependency := range owners[positions[name]].Dependencies {
		if dependsOnIAM(dependency, owners, positions, seen) {
			return true
		}
	}
	return false
}
