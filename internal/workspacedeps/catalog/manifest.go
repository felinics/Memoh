package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ManifestFileName is the manifest every dependency directory must contain.
const ManifestFileName = "dependency.yaml"

// Category groups dependencies by what they are to the user.
type Category string

// Dependency categories.
const (
	// CategoryAgent is an external coding agent CLI (Codex, Claude Code). Its
	// target version is pinned by the Server and never queried upstream.
	CategoryAgent Category = "agent"
	// CategoryRuntime is a language runtime such as Node.js or Python.
	CategoryRuntime Category = "runtime"
	// CategoryTool is any other command-line tool.
	CategoryTool Category = "tool"
)

func (c Category) valid() bool {
	switch c {
	case CategoryAgent, CategoryRuntime, CategoryTool:
		return true
	default:
		return false
	}
}

// Source says who provides the dependency's files.
type Source string

// Dependency sources.
const (
	// SourceManaged dependencies are installed by the catalog scripts into
	// MEMOH_DEP_HOME and can be updated, rolled back, and removed.
	SourceManaged Source = "managed"
	// SourceImage dependencies ship with the workspace image. They have no
	// scripts and cannot be removed (WD-CAT-001).
	SourceImage Source = "image"
)

func (s Source) valid() bool {
	switch s {
	case SourceManaged, SourceImage:
		return true
	default:
		return false
	}
}

// Action is one of the scripted operations a dependency may support.
type Action string

// Scripted actions. Rollback is intentionally absent: it is a pure data
// operation performed by the runner and never backed by a script (§4.3).
const (
	ActionInstall     Action = "install"
	ActionUpdate      Action = "update"
	ActionRemove      Action = "remove"
	ActionReinstall   Action = "reinstall"
	ActionCheckUpdate Action = "check_update"
	ActionVersion     Action = "version"
)

// Platform is one (os, arch set, libc) tuple a dependency can be installed on.
// An empty Libc means the libc flavour is irrelevant for that OS (darwin).
type Platform struct {
	OS   string   `yaml:"os"`
	Arch []string `yaml:"arch"`
	Libc string   `yaml:"libc,omitempty"`
}

// VersionSpec describes which version an install should produce. Agent
// dependencies must set Pin (WD-CAT-004); tool dependencies may follow a
// Channel and use a check_update script instead.
type VersionSpec struct {
	Channel string `yaml:"channel,omitempty"`
	Pin     string `yaml:"pin,omitempty"`
}

// Default per-action timeouts in seconds, applied when the manifest omits one.
const (
	DefaultInstallTimeout     = 1200 // aligned with the image's MEMOH_APT_COMMAND_TIMEOUT
	DefaultRemoveTimeout      = 300
	DefaultCheckUpdateTimeout = 60
	DefaultVersionTimeout     = 30
)

// Timeouts holds per-action script timeouts in seconds. Update defaults to
// Install because an update is a full install into a fresh versions/ directory.
type Timeouts struct {
	Install     int `yaml:"install,omitempty"`
	CheckUpdate int `yaml:"check_update,omitempty"`
	Update      int `yaml:"update,omitempty"`
	Remove      int `yaml:"remove,omitempty"`
	Version     int `yaml:"version,omitempty"`
}

func (t Timeouts) withDefaults() Timeouts {
	if t.Install == 0 {
		t.Install = DefaultInstallTimeout
	}
	if t.Update == 0 {
		t.Update = t.Install
	}
	if t.Remove == 0 {
		t.Remove = DefaultRemoveTimeout
	}
	if t.CheckUpdate == 0 {
		t.CheckUpdate = DefaultCheckUpdateTimeout
	}
	if t.Version == 0 {
		t.Version = DefaultVersionTimeout
	}
	return t
}

// For returns the timeout in seconds for action. Reinstall is orchestrated as
// remove followed by install, so its budget is the sum of both. Unknown
// actions return 0.
func (t Timeouts) For(action Action) int {
	switch action {
	case ActionInstall:
		return t.Install
	case ActionUpdate:
		return t.Update
	case ActionRemove:
		return t.Remove
	case ActionReinstall:
		return t.Remove + t.Install
	case ActionCheckUpdate:
		return t.CheckUpdate
	case ActionVersion:
		return t.Version
	default:
		return 0
	}
}

// Duration is For expressed as a time.Duration.
func (t Timeouts) Duration(action Action) time.Duration {
	return time.Duration(t.For(action)) * time.Second
}

// Scripts maps actions to script file names relative to the dependency
// directory. An empty entry means the action is not scripted: reinstall then
// falls back to remove → install and update falls back to install.
type Scripts struct {
	Install     string `yaml:"install,omitempty"`
	CheckUpdate string `yaml:"check_update,omitempty"`
	Update      string `yaml:"update,omitempty"`
	Remove      string `yaml:"remove,omitempty"`
	Reinstall   string `yaml:"reinstall,omitempty"`
	Version     string `yaml:"version,omitempty"`
}

// For returns the file name configured for action, or "" when unset.
func (s Scripts) For(action Action) string {
	switch action {
	case ActionInstall:
		return s.Install
	case ActionUpdate:
		return s.Update
	case ActionRemove:
		return s.Remove
	case ActionReinstall:
		return s.Reinstall
	case ActionCheckUpdate:
		return s.CheckUpdate
	case ActionVersion:
		return s.Version
	default:
		return ""
	}
}

type scriptRef struct {
	action Action
	file   string
}

// scriptActions is the stable iteration order for configured scripts.
var scriptActions = []Action{
	ActionInstall,
	ActionUpdate,
	ActionRemove,
	ActionReinstall,
	ActionCheckUpdate,
	ActionVersion,
}

// configured returns every (action, file) pair with a non-empty file name.
func (s Scripts) configured() []scriptRef {
	refs := make([]scriptRef, 0, len(scriptActions))
	for _, action := range scriptActions {
		if file := strings.TrimSpace(s.For(action)); file != "" {
			refs = append(refs, scriptRef{action: action, file: file})
		}
	}
	return refs
}

// Dependency is one validated catalog entry.
type Dependency struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description,omitempty"`
	Icon        string   `yaml:"icon,omitempty"`
	Category    Category `yaml:"category"`
	Source      Source   `yaml:"source"`
	// Requires lists catalog IDs that must be present before this dependency
	// can be installed.
	Requires []string `yaml:"requires,omitempty"`
	// Provides lists the commands that must resolve after installation.
	Provides  []string    `yaml:"provides"`
	Platforms []Platform  `yaml:"platforms"`
	Version   VersionSpec `yaml:"version,omitempty"`
	Timeouts  Timeouts    `yaml:"timeouts,omitempty"`
	Scripts   Scripts     `yaml:"scripts,omitempty"`
	// ManifestDigest is "sha256:<hex>" over dependency.yaml and every script
	// file the manifest references, sorted by file name. It is recorded in
	// the workspace state.json so a changed manifest can be detected.
	ManifestDigest string `yaml:"-"`
}

// IsAgent reports whether the dependency is an external coding agent.
func (d Dependency) IsAgent() bool {
	return d.Category == CategoryAgent
}

// IsImageProvided reports whether the dependency ships with the workspace
// image rather than being installed by catalog scripts.
func (d Dependency) IsImageProvided() bool {
	return d.Source == SourceImage
}

// SupportsPlatform reports whether the dependency can be installed on the
// given platform. Comparison is case-insensitive. Platform entries with an
// empty libc match any libc, so callers may pass "" when libc is unknown.
func (d Dependency) SupportsPlatform(osName, arch, libc string) bool {
	osName = normalizeToken(osName)
	arch = normalizeToken(arch)
	libc = normalizeToken(libc)
	for _, platform := range d.Platforms {
		if normalizeToken(platform.OS) != osName {
			continue
		}
		if !slices.ContainsFunc(platform.Arch, func(candidate string) bool {
			return normalizeToken(candidate) == arch
		}) {
			continue
		}
		if platform.Libc != "" && normalizeToken(platform.Libc) != libc {
			continue
		}
		return true
	}
	return false
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// clone returns a deep copy so callers cannot mutate catalog state through
// the returned slices.
func (d Dependency) clone() Dependency {
	d.Requires = slices.Clone(d.Requires)
	d.Provides = slices.Clone(d.Provides)
	platforms := make([]Platform, len(d.Platforms))
	for i, platform := range d.Platforms {
		platform.Arch = slices.Clone(platform.Arch)
		platforms[i] = platform
	}
	d.Platforms = platforms
	return d
}

// decodeManifest parses dependency.yaml strictly (unknown fields are errors)
// and applies timeout defaults. Structural validation happens in Validate.
func decodeManifest(data []byte) (Dependency, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var dep Dependency
	if err := decoder.Decode(&dep); err != nil {
		if errors.Is(err, io.EOF) {
			return Dependency{}, errors.New("manifest is empty")
		}
		return Dependency{}, err
	}
	dep.ID = strings.TrimSpace(dep.ID)
	dep.Name = strings.TrimSpace(dep.Name)
	dep.Description = strings.TrimSpace(dep.Description)
	dep.Icon = strings.TrimSpace(dep.Icon)
	dep.Version.Channel = strings.TrimSpace(dep.Version.Channel)
	dep.Version.Pin = strings.TrimSpace(dep.Version.Pin)
	dep.Timeouts = dep.Timeouts.withDefaults()
	return dep, nil
}

// DigestFiles computes the manifest digest over files keyed by file name.
// Names, lengths, and contents are hashed in sorted name order so that
// renaming a script or moving bytes between files changes the digest.
func DigestFiles(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		content := files[name]
		hash.Write([]byte(name))
		hash.Write([]byte{0})
		hash.Write([]byte(strconv.Itoa(len(content))))
		hash.Write([]byte{0})
		hash.Write(content)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
