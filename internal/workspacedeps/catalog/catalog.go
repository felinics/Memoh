// Package catalog validates immutable workspace dependency definitions.
// Official definitions are published by Supermarket; the Server contains
// only their parser and execution contract, never an embedded catalog.
package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

var idPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// Catalog is an immutable, validated set of dependencies. Methods are safe
// for concurrent use once Load or LoadFS has returned.
type Catalog struct {
	// entries keeps directory order so Validate reports problems
	// deterministically, including duplicate IDs that byID cannot hold.
	entries []*entry
	byID    map[string]*entry
}

type entry struct {
	dir       string
	dep       Dependency
	scripts   map[Action]scriptFile
	iconBytes []byte
}

type scriptFile struct {
	name    string
	content string
	found   bool
}

// Empty constructs a valid catalog before any remote definitions are cached.
func Empty() *Catalog { return &Catalog{byID: make(map[string]*entry)} }

// Fingerprint binds discovery results to the definitions that produced them.
func (c *Catalog) Fingerprint() string {
	files := make(map[string][]byte, len(c.byID))
	for id, loaded := range c.byID {
		dep := loaded.dep
		files[id] = []byte(dep.SourceURL + "\x00" + dep.Revision + "\x00" + dep.ManifestDigest)
	}
	return DigestFiles(files)
}

// LoadFS reads a catalog from fsys, treating every directory at its root as
// one dependency. Files at the root are ignored. The result is validated;
// a catalog that fails validation is not returned.
func LoadFS(fsys fs.FS) (*Catalog, error) {
	dirs, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("catalog: read root: %w", err)
	}
	c := &Catalog{byID: make(map[string]*entry, len(dirs))}
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		loaded, err := loadEntry(fsys, dir.Name())
		if err != nil {
			return nil, err
		}
		c.entries = append(c.entries, loaded)
		if _, dup := c.byID[loaded.dep.ID]; !dup {
			c.byID[loaded.dep.ID] = loaded
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func loadEntry(fsys fs.FS, dir string) (*entry, error) {
	return loadEntryFiles(dir, func(name string) ([]byte, error) {
		return fs.ReadFile(fsys, path.Join(dir, name))
	})
}

func loadEntryFiles(dir string, read func(string) ([]byte, error)) (*entry, error) {
	manifest, err := read(ManifestFileName)
	if err != nil {
		return nil, fmt.Errorf("catalog: %s: read %s: %w", dir, ManifestFileName, err)
	}
	dep, err := decodeManifest(manifest)
	if err != nil {
		return nil, fmt.Errorf("catalog: %s: decode %s: %w", dir, ManifestFileName, err)
	}
	loaded := &entry{dir: dir, dep: dep, scripts: make(map[Action]scriptFile)}
	files := map[string][]byte{ManifestFileName: manifest}
	for _, ref := range dep.Scripts.configured() {
		script := scriptFile{name: ref.file}
		if isPlainFileName(ref.file) {
			content, err := read(ref.file)
			switch {
			case err == nil:
				script.found = true
				script.content = string(content)
				files[ref.file] = content
			case errors.Is(err, fs.ErrNotExist):
				// Reported by Validate with the dependency id attached.
			default:
				return nil, fmt.Errorf("catalog: %s: read script %s: %w", dir, ref.file, err)
			}
		}
		loaded.scripts[ref.action] = script
	}
	loaded.dep.ManifestDigest = DigestFiles(files)
	return loaded, nil
}

// isPlainFileName rejects paths that would escape the dependency directory.
func isPlainFileName(name string) bool {
	return name != "" && name != "." && name != ".." && path.Base(name) == name
}

// Validate checks every structural rule from design §4.2 and returns all
// violations joined into one error, each naming the offending dependency.
// Load and LoadFS already call it; it is exported for start-up checks.
func (c *Catalog) Validate() error {
	var errs []error
	seen := make(map[string]bool, len(c.entries))
	for _, loaded := range c.entries {
		errs = append(errs, loaded.validate(c, seen)...)
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("catalog: dependency cycle at %q", id)
		}
		if visited[id] {
			return nil
		}
		loaded, ok := c.byID[id]
		if !ok {
			return nil
		} // Missing references are reported above.
		visiting[id] = true
		for _, required := range loaded.dep.Requires {
			if err := visit(required); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for _, loaded := range c.entries {
		if err := visit(loaded.dep.ID); err != nil {
			errs = append(errs, err)
			break
		}
	}
	return errors.Join(errs...)
}

func (e *entry) validate(c *Catalog, seen map[string]bool) []error {
	dep := e.dep
	label := dep.ID
	if label == "" {
		label = e.dir
	}
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("catalog: dependency %q: %s", label, fmt.Sprintf(format, args...)))
	}

	switch {
	case dep.ID == "":
		fail("id must not be empty (directory %q)", e.dir)
	case !idPattern.MatchString(dep.ID):
		fail("id %q must only contain [a-z0-9-]", dep.ID)
	}
	if dep.ID != "" {
		if seen[dep.ID] {
			fail("id %q is declared by more than one directory", dep.ID)
		}
		seen[dep.ID] = true
		if dep.ID != e.dir {
			fail("id %q does not match directory name %q", dep.ID, e.dir)
		}
	}
	if dep.Name == "" {
		fail("name must not be empty")
	}
	if !dep.Category.valid() {
		fail("category %q must be one of agent, runtime, tool", dep.Category)
	}
	if !dep.Source.valid() {
		fail("source %q must be one of managed, image", dep.Source)
	}

	e.validateProvidesAndPlatforms(fail)

	for _, required := range dep.Requires {
		switch required = strings.TrimSpace(required); required {
		case "":
			fail("requires must not contain empty ids")
		case dep.ID:
			fail("requires must not reference itself")
		default:
			if _, ok := c.byID[required]; !ok {
				fail("requires unknown dependency %q", required)
			}
		}
	}

	// Script consistency. A managed dependency exists only through its
	// scripts, so install is mandatory. An image-baseline dependency may
	// declare scripts to install overlay versions, or none at all. Either way
	// anything that installs must be removable, and every other scripted
	// action presumes an installed overlay.
	configured := dep.Scripts.configured()
	if dep.Source == SourceManaged && dep.Scripts.Install == "" {
		fail("source managed requires scripts.install")
	}
	if dep.Source == SourceImage && dep.Category == CategoryAgent {
		fail("source image cannot be used with category agent")
	}
	if dep.Scripts.Install != "" && dep.Scripts.Remove == "" {
		fail("scripts.remove is required when scripts.install is set")
	}
	if dep.Scripts.Install == "" {
		for _, action := range []Action{ActionUpdate, ActionReinstall, ActionCheckUpdate, ActionVersion} {
			if dep.Scripts.For(action) != "" {
				fail("scripts.%s requires scripts.install", action)
			}
		}
	}
	for _, ref := range configured {
		script := e.scripts[ref.action]
		switch {
		case !isPlainFileName(ref.file):
			fail("scripts.%s: %q must be a plain file name inside the dependency directory", ref.action, ref.file)
		case !script.found:
			fail("scripts.%s: file %q does not exist", ref.action, ref.file)
		case strings.TrimSpace(script.content) == "":
			fail("scripts.%s: file %q is empty", ref.action, ref.file)
		}
	}
	for _, action := range scriptActions {
		if dep.Timeouts.For(action) <= 0 {
			fail("timeouts.%s must be positive", action)
		}
	}
	return errs
}

// validateProvidesAndPlatforms is split out only to keep validate readable;
// it reports through the shared fail closure.
func (e *entry) validateProvidesAndPlatforms(fail func(string, ...any)) {
	dep := e.dep
	if len(dep.Provides) == 0 {
		fail("provides must list at least one command")
	}
	for _, command := range dep.Provides {
		if strings.TrimSpace(command) == "" {
			fail("provides must not contain empty command names")
			break
		}
	}
	if len(dep.Platforms) == 0 {
		fail("platforms must list at least one platform")
	}
	for i, platform := range dep.Platforms {
		if strings.TrimSpace(platform.OS) == "" {
			fail("platforms[%d]: os must not be empty", i)
		}
		if len(platform.Arch) == 0 {
			fail("platforms[%d]: arch must list at least one architecture", i)
		}
		for _, arch := range platform.Arch {
			if strings.TrimSpace(arch) == "" {
				fail("platforms[%d]: arch must not contain empty entries", i)
				break
			}
		}
	}
}

// Get returns a copy of the dependency with the given id.
func (c *Catalog) Get(id string) (Dependency, bool) {
	loaded, ok := c.byID[id]
	if !ok {
		return Dependency{}, false
	}
	return loaded.dep.clone(), true
}

// MustGet is Get for ids the caller knows exist, such as those declared by an
// external agent driver. It panics when the id is missing.
func (c *Catalog) MustGet(id string) Dependency {
	dep, ok := c.Get(id)
	if !ok {
		panic(fmt.Sprintf("catalog: dependency %q is not in the catalog", id))
	}
	return dep
}

// List returns copies of every dependency sorted by id.
func (c *Catalog) List() []Dependency {
	items := make([]Dependency, 0, len(c.byID))
	for _, loaded := range c.byID {
		items = append(items, loaded.dep.clone())
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

// Script returns the script body for action. ok is false when the dependency
// is unknown or its manifest does not configure that action; callers then
// fall back to the runner's built-in orchestration (reinstall, update) or
// treat the action as unsupported.
func (c *Catalog) Script(id string, action Action) (content string, ok bool) {
	loaded, found := c.byID[id]
	if !found {
		return "", false
	}
	script, configured := loaded.scripts[action]
	if !configured {
		return "", false
	}
	return script.content, true
}
