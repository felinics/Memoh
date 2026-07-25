package epoch

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"
)

func TestLoadManifest(t *testing.T) {
	fsys, want := validAssets(t)

	got, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Epoch != want.Epoch || !slices.Equal(got.Order, want.Order) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	if _, err := Parse([]byte("epoch: 2\norder: []\nowners: []\nunknown: true\n")); err == nil {
		t.Fatal("Parse() error = nil, want unknown-field error")
	}
}

func TestManifestRejectsInvalidPlan(t *testing.T) {
	fsys, valid := validAssets(t)
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{
			name: "order differs from owners",
			mutate: func(manifest *Manifest) {
				manifest.Order[1], manifest.Order[2] = manifest.Order[2], manifest.Order[1]
			},
		},
		{
			name: "unknown owner",
			mutate: func(manifest *Manifest) {
				manifest.Owners[1].Name = "billing"
			},
		},
		{
			name: "dependency follows owner",
			mutate: func(manifest *Manifest) {
				manifest.Owners[1].Dependencies = []string{"runtime"}
			},
		},
		{
			name: "dependency does not reach iam",
			mutate: func(manifest *Manifest) {
				manifest.Owners[1].Dependencies = nil
			},
		},
		{
			name: "file outside migrations dir",
			mutate: func(manifest *Manifest) {
				manifest.Owners[0].Files[0].Path = "api/migrations/00001_baseline.sql"
			},
		},
		{
			name: "unsafe file path",
			mutate: func(manifest *Manifest) {
				manifest.Owners[0].Files[0].Path = "../00001_baseline.sql"
			},
		},
		{
			name: "version is not continuous",
			mutate: func(manifest *Manifest) {
				manifest.Owners[0].Files[0].Version = 2
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := cloneManifest(valid)
			tt.mutate(&manifest)
			if err := manifest.Validate(fsys); err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
		})
	}
}

func TestManifestRejectsChangedFileSet(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(fstest.MapFS)
	}{
		{
			name: "modified",
			mutate: func(fsys fstest.MapFS) {
				fsys["iam/migrations/00001_baseline.sql"].Data = []byte("modified")
			},
		},
		{
			name: "missing",
			mutate: func(fsys fstest.MapFS) {
				delete(fsys, "iam/migrations/00001_baseline.sql")
			},
		},
		{
			name: "unknown",
			mutate: func(fsys fstest.MapFS) {
				fsys["iam/migrations/00002_extra.sql"] = &fstest.MapFile{Data: []byte("SELECT 2;")}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys, manifest := validAssets(t)
			tt.mutate(fsys)
			if err := manifest.Validate(fsys); err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
		})
	}
}

func validAssets(t *testing.T) (fstest.MapFS, Manifest) {
	t.Helper()
	order := slices.Clone(ownerNames)
	fsys := make(fstest.MapFS, len(order)+1)
	manifest := Manifest{
		Epoch:  CurrentEpoch,
		Order:  order,
		Owners: make([]Owner, 0, len(order)),
	}
	for _, name := range order {
		filePath := name + "/migrations/00001_baseline.sql"
		content := []byte("-- +goose Up\nSELECT 1;\n")
		fsys[filePath] = &fstest.MapFile{Data: content}
		sum := sha256.Sum256(content)
		dependencies := []string{"iam"}
		if name == "iam" {
			dependencies = nil
		}
		manifest.Owners = append(manifest.Owners, Owner{
			Name:          name,
			Schema:        name,
			VersionTable:  name + ".goose_db_version",
			MigrationsDir: name + "/migrations",
			Version:       1,
			Dependencies:  dependencies,
			Files: []MigrationFile{{
				Path:     filePath,
				Version:  1,
				Checksum: "sha256:" + hex.EncodeToString(sum[:]),
			}},
		})
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	fsys[ManifestPath] = &fstest.MapFile{Data: data}
	return fsys, manifest
}

func cloneManifest(manifest Manifest) Manifest {
	clone := manifest
	clone.Order = slices.Clone(manifest.Order)
	clone.Owners = slices.Clone(manifest.Owners)
	for i := range clone.Owners {
		clone.Owners[i].Dependencies = slices.Clone(manifest.Owners[i].Dependencies)
		clone.Owners[i].Files = slices.Clone(manifest.Owners[i].Files)
	}
	return clone
}
