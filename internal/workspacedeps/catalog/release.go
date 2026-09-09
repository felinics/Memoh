package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strings"
)

const (
	OfficialRegistry = "memoh"
	MaxArtifactBytes = 1024 * 1024
	MaxArtifactFiles = 32
	MaxCatalogBytes  = 4 * 1024 * 1024
	MaxDependencies  = 4096
)

var (
	digestPattern      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	archiveNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
)

func ValidRevision(revision string) bool { return digestPattern.MatchString(revision) }

type Artifact struct {
	Format           string `json:"format"`
	Digest           string `json:"digest"`
	Size             int64  `json:"size"`
	UncompressedSize int64  `json:"uncompressed_size"`
	ArchiveSize      int64  `json:"archive_size"`
	FileCount        int    `json:"file_count"`
	ContentType      string `json:"content_type"`
}

type IconAsset struct {
	Digest      string `json:"digest"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

// Release is the immutable Supermarket wire document. Its revision is the
// SHA-256 of the original JSON bytes, not of a re-serialized Go value.
type Release struct {
	SchemaVersion  string          `json:"schema_version"`
	RegistryID     string          `json:"registry_id"`
	DependencyID   string          `json:"dependency_id"`
	Manifest       json.RawMessage `json:"manifest"`
	ManifestDigest string          `json:"manifest_digest"`
	Artifact       Artifact        `json:"artifact"`
	Icon           *IconAsset      `json:"icon,omitempty"`
}

type Descriptor struct {
	Release
	Revision string `json:"revision"`
}

type Index struct {
	Total    int          `json:"total"`
	Page     int          `json:"page"`
	Limit    int          `json:"limit"`
	Revision string       `json:"revision,omitempty"`
	Data     []Descriptor `json:"data"`
}

// Definition contains validated scripts and metadata. It has no mutators:
// catalogs and concurrent operations may safely share an instance.
type Definition struct{ loaded *entry }

func (d Definition) Dependency() Dependency { return d.loaded.dep.clone() }

func (d Definition) Icon() []byte { return bytes.Clone(d.loaded.iconBytes) }

func (d Definition) WithRetired(retired bool) Definition {
	loaded := *d.loaded
	loaded.dep = loaded.dep.clone()
	loaded.dep.Retired = retired
	return Definition{loaded: &loaded}
}

func New(definitions []Definition) (*Catalog, error) {
	c := Empty()
	for _, definition := range definitions {
		if definition.loaded == nil {
			return nil, errors.New("catalog: empty definition")
		}
		loaded := definition.loaded
		c.entries = append(c.entries, loaded)
		if _, exists := c.byID[loaded.dep.ID]; !exists {
			c.byID[loaded.dep.ID] = loaded
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Using freezes a prepared revision while retaining prerequisite definitions
// from this snapshot. Other requests keep their own immutable snapshot.
func (c *Catalog) Using(definition Definition) (*Catalog, error) {
	if definition.loaded == nil {
		return nil, errors.New("catalog: empty definition")
	}
	definitions := make([]Definition, 0, len(c.entries)+1)
	for _, loaded := range c.entries {
		if loaded.dep.ID != definition.loaded.dep.ID {
			definitions = append(definitions, Definition{loaded})
		}
	}
	return New(append(definitions, definition))
}

func (r Release) Validate() error {
	if r.SchemaVersion != "1" || r.RegistryID != OfficialRegistry || !idPattern.MatchString(r.DependencyID) || len(r.DependencyID) > 80 {
		return errors.New("catalog: invalid dependency release identity or schema")
	}
	a := r.Artifact
	if a.Format != "memoh_dependency_v1" || a.ContentType != "application/gzip" || !ValidRevision(a.Digest) {
		return errors.New("catalog: invalid dependency artifact format or digest")
	}
	for _, size := range []int64{a.Size, a.UncompressedSize, a.ArchiveSize} {
		if size <= 0 || size > MaxArtifactBytes {
			return errors.New("catalog: dependency artifact exceeds byte budget")
		}
	}
	if a.FileCount < 1 || a.FileCount > MaxArtifactFiles {
		return errors.New("catalog: dependency artifact exceeds file budget")
	}
	if !strings.HasPrefix(r.ManifestDigest, "sha256:") || !ValidRevision(strings.TrimPrefix(r.ManifestDigest, "sha256:")) {
		return errors.New("catalog: invalid manifest digest")
	}
	if r.Icon != nil && (!ValidRevision(r.Icon.Digest) || r.Icon.Size <= 0 || r.Icon.Size > 512*1024 || r.Icon.ContentType != "image/svg+xml") {
		return errors.New("catalog: invalid dependency icon")
	}
	return nil
}

func DecodeRelease(metadata []byte, revision string) (Release, error) {
	var release Release
	if len(metadata) > MaxArtifactBytes || !matchesDigest(metadata, revision) {
		return release, errors.New("catalog: release revision mismatch")
	}
	if err := strictJSON(metadata, &release); err != nil {
		return release, err
	}
	return release, release.Validate()
}

func DecodeIndex(data []byte) (Index, error) {
	var index Index
	if len(data) > MaxCatalogBytes {
		return index, errors.New("catalog: index exceeds byte budget")
	}
	if err := strictJSON(data, &index); err != nil {
		return index, err
	}
	if index.Total < 0 || index.Total > MaxDependencies || index.Page < 1 || index.Limit < 1 || index.Limit > MaxDependencies || index.Page > max(1, (index.Total+index.Limit-1)/index.Limit) || len(index.Data) != min(index.Limit, max(0, index.Total-(index.Page-1)*index.Limit)) {
		return index, errors.New("catalog: incomplete or oversized dependency index")
	}
	if index.Revision != "" && !ValidRevision(index.Revision) {
		return index, errors.New("catalog: invalid index revision")
	}
	seen := map[string]bool{}
	for _, descriptor := range index.Data {
		if !ValidRevision(descriptor.Revision) || seen[descriptor.DependencyID] {
			return index, errors.New("catalog: invalid or duplicate dependency descriptor")
		}
		if err := descriptor.Validate(); err != nil {
			return index, err
		}
		seen[descriptor.DependencyID] = true
	}
	return index, nil
}

// FromArtifact verifies all three identities: the immutable release,
// compressed artifact, and manifest-plus-script digest. No file is extracted
// onto either the host filesystem or the workspace.
func FromArtifact(metadata []byte, revision string, archive []byte, sourceURL string) (Definition, error) {
	release, err := DecodeRelease(metadata, revision)
	if err != nil {
		return Definition{}, err
	}
	if int64(len(archive)) != release.Artifact.Size || !matchesDigest(archive, release.Artifact.Digest) {
		return Definition{}, errors.New("catalog: artifact digest mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return Definition{}, err
	}
	defer func() { _ = gz.Close() }()
	uncompressed, err := io.ReadAll(io.LimitReader(gz, MaxArtifactBytes+1))
	if err != nil {
		return Definition{}, err
	}
	if int64(len(uncompressed)) != release.Artifact.ArchiveSize || len(uncompressed) > MaxArtifactBytes {
		return Definition{}, errors.New("catalog: archive size mismatch")
	}
	files := map[string][]byte{}
	seen := map[string]bool{}
	reader := tar.NewReader(bytes.NewReader(uncompressed))
	var contentBytes int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Definition{}, err
		}
		if header.Typeflag != tar.TypeReg || !archiveNamePattern.MatchString(header.Name) || seen[strings.ToLower(header.Name)] || header.Size < 0 || header.Size > MaxArtifactBytes {
			return Definition{}, errors.New("catalog: invalid archive entry")
		}
		if len(files) >= MaxArtifactFiles {
			return Definition{}, errors.New("catalog: too many archive entries")
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			return Definition{}, err
		}
		files[header.Name] = body
		seen[strings.ToLower(header.Name)] = true
		contentBytes += int64(len(body))
	}
	if len(files) != release.Artifact.FileCount || contentBytes != release.Artifact.UncompressedSize {
		return Definition{}, errors.New("catalog: archive content budget mismatch")
	}
	loaded, err := loadEntryFiles(release.DependencyID, func(name string) ([]byte, error) {
		body, ok := files[name]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return body, nil
	})
	if err != nil {
		return Definition{}, err
	}
	dep := &loaded.dep
	if dep.SchemaVersion != "1" || dep.ID != release.DependencyID || dep.ManifestDigest != release.ManifestDigest {
		return Definition{}, errors.New("catalog: manifest identity mismatch")
	}
	var projected Dependency
	if err := strictJSON(release.Manifest, &projected); err != nil {
		return Definition{}, err
	}
	projected.Timeouts = projected.Timeouts.withDefaults()
	left, _ := json.Marshal(*dep)
	right, _ := json.Marshal(projected)
	if !bytes.Equal(left, right) {
		return Definition{}, errors.New("catalog: release metadata does not match archived manifest")
	}
	expected := map[string]bool{ManifestFileName: true}
	for _, ref := range dep.Scripts.configured() {
		expected[ref.file] = true
	}
	if dep.Icon != "" {
		expected[dep.Icon] = true
		icon, ok := files[dep.Icon]
		if !ok || release.Icon == nil || int64(len(icon)) != release.Icon.Size || !matchesDigest(icon, release.Icon.Digest) {
			return Definition{}, errors.New("catalog: icon mismatch")
		}
		dep.IconDigest = release.Icon.Digest
		loaded.iconBytes = bytes.Clone(icon)
	} else if release.Icon != nil {
		return Definition{}, errors.New("catalog: icon is not declared in manifest")
	}
	for name := range files {
		if !expected[name] {
			return Definition{}, fmt.Errorf("catalog: undeclared archive file %q", name)
		}
	}
	// Structural validation here; prerequisites and cycles are validated when
	// complete definitions are assembled into a snapshot by New.
	validation := Empty()
	validation.byID[dep.ID] = loaded
	for _, required := range dep.Requires {
		validation.byID[required] = &entry{dep: Dependency{ID: required}}
	}
	if err := errors.Join(loaded.validate(validation, map[string]bool{})...); err != nil {
		return Definition{}, err
	}
	dep.SourceURL = sourceURL
	dep.RegistryID = release.RegistryID
	dep.Revision = revision
	return Definition{loaded}, nil
}

func matchesDigest(data []byte, digest string) bool {
	if !ValidRevision(digest) {
		return false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == digest
}

func strictJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("catalog: trailing JSON value")
	}
	return nil
}
