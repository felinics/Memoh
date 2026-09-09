package workspacedeps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// State is the on-disk state.json of a managed dependency. It is
// the source of truth for what the Server installed.
type State struct {
	SourceURL          string                `json:"source_url,omitempty"`
	RegistryID         string                `json:"registry_id,omitempty"`
	DefinitionRevision string                `json:"definition_revision,omitempty"`
	Previous           *PreviousInstallation `json:"previous,omitempty"`
	DependencyID       string                `json:"dependency_id"`
	Version            string                `json:"version"`
	InstalledAt        time.Time             `json:"installed_at"`
	ManifestDigest     string                `json:"manifest_digest"`
	Entrypoints        map[string]string     `json:"entrypoints"`
	PreviousVersion    string                `json:"previous_version,omitempty"`
}

// PreviousInstallation binds the rollback binary to its own publication and
// command entries. It does not authorize fetching or executing a script.
type PreviousInstallation struct {
	Version            string            `json:"version"`
	SourceURL          string            `json:"source_url,omitempty"`
	RegistryID         string            `json:"registry_id,omitempty"`
	DefinitionRevision string            `json:"definition_revision,omitempty"`
	ManifestDigest     string            `json:"manifest_digest"`
	Entrypoints        map[string]string `json:"entrypoints"`
}

// Source says where a discovered copy of a dependency comes from, in
// decreasing order of precedence.
type Source string

// Discovery sources. The empty Source means the dependency is absent.
const (
	// SourceManaged copies were installed by the Server and carry state.json.
	SourceManaged Source = "managed"
	// SourceToolkit copies live in the workspace image's toolkit bin.
	SourceToolkit Source = "toolkit"
	// SourcePath copies were found on PATH, typically installed by the agent
	// itself (npm i -g and the like).
	SourcePath Source = "path"
)

// Candidate is one discovered copy of a dependency's primary command.
type Candidate struct {
	Source  Source
	Path    string
	Version string
}

// Observed is the discovery result for one dependency.
type Observed struct {
	DepID   string
	Present bool
	// Source is the provider of the copy that wins precedence:
	// managed, then toolkit, then PATH.
	Source Source
	// Version is the probed version of the winning copy. For managed copies
	// it falls back to state.json when the probe yields nothing. It may be
	// empty.
	Version string
	// Command is the absolute path of the winning copy of the primary command
	// (provides[0]).
	Command string
	// Entrypoints maps command names to absolute paths: state.json for
	// managed copies, otherwise the paths found for each provides entry.
	Entrypoints map[string]string
	// State is the decoded state.json; nil unless the source is managed or
	// the file exists but its entrypoint is unusable.
	State *State
	// Candidates lists every copy found, in precedence order, so callers can
	// prefer a copy at the pinned version over the default winner.
	Candidates []Candidate
	// LockHeld reports a live lock owner, or a recent legacy lock whose
	// owner cannot be determined safely. An old mtime cannot override a
	// live process identity.
	LockHeld bool
	// LockAbandoned means the lock remains on disk but its recorded owner
	// is no longer alive. It permits immediate recovery without a time guess.
	LockAbandoned bool
	// Receipt is the current workspace operation's durable completion data.
	Receipt *OperationReceipt
	// Err records non-fatal problems such as an unreadable state.json or a
	// failed version probe. It never prevents discovery from returning.
	Err string
}

const (
	discoveryTimeout    = 2 * time.Minute
	versionProbeTimeout = 5 * time.Second

	markerPrefix       = "__MEMOH_"
	markerDep          = "__MEMOH_DEP__"
	markerStateBegin   = "__MEMOH_STATE_BEGIN__"
	markerStateEnd     = "__MEMOH_STATE_END__"
	markerManaged      = "__MEMOH_MANAGED__"
	markerToolkit      = "__MEMOH_TOOLKIT__"
	markerPath         = "__MEMOH_PATH__"
	markerLock         = "__MEMOH_LOCK__"
	markerAbandoned    = "__MEMOH_LOCK_ABANDONED__"
	markerVersionBegin = "__MEMOH_VERSION_BEGIN__"
	markerVersionEnd   = "__MEMOH_VERSION_END__"
	markerEnd          = "__MEMOH_END__"
)

// versionPattern extracts the first semantic-version-looking token from a
// `--version` output. An optional pre-release suffix is kept so
// a release candidate never passes for the release it precedes.
var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?`)

// discoveryPreamble defines the helpers the generated script relies on. It
// runs without `set -e`: a failing probe must not abort discovery of the
// remaining dependencies. Paths are separated from other fields by tabs so
// paths with spaces survive.
const discoveryPreamble = lockProbeHelpers + `memoh_resolve() {
  memoh_p=$(command -v "$1" 2>/dev/null) || memoh_p=''
  case "$memoh_p" in
    /*) printf '%s\n' "$memoh_p" ;;
    *) printf '\n' ;;
  esac
}
`

// Discover inspects the workspace for every dependency in depIDs with a
// read-only path scan: it reads state.json, checks the toolkit fallback and
// PATH, then bounds each distinct primary command's --version probe separately.
// Definitions with scripts.version use recorded state instead; catalog scripts
// require a separately authorized operation and never run during discovery.
//
// Unknown dependency ids are an error; everything else is reported per
// dependency through Observed.Err.
func Discover(ctx context.Context, client *bridge.Client, cat *catalog.Catalog, dataRoot string, depIDs []string, _ Platform) (map[string]Observed, error) {
	if client == nil {
		return nil, errors.New("workspacedeps: bridge client is nil")
	}
	if cat == nil {
		return nil, errors.New("workspacedeps: catalog is nil")
	}
	deps := make([]catalog.Dependency, 0, len(depIDs))
	for _, id := range depIDs {
		dep, ok := cat.Get(id)
		if !ok {
			return nil, fmt.Errorf("workspacedeps: unknown dependency %q", id)
		}
		deps = append(deps, dep)
	}
	observed := make(map[string]Observed, len(deps))
	if len(deps) == 0 {
		return observed, nil
	}

	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	script := buildDiscoveryScript(dataRoot, deps)
	result, err := client.ExecWithOptions(ctx, "exec sh -s", defaultWorkDir, int32(discoveryTimeout/time.Second), []byte(script), bridge.ExecOptions{})
	if err != nil {
		return nil, fmt.Errorf("workspacedeps: discovery exec: %w", err)
	}
	probes, complete := parseDiscoveryOutput(result.Stdout)
	if result.ExitCode != 0 || !complete {
		return nil, fmt.Errorf("workspacedeps: discovery script exited %d before finishing: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	// Decode state in Go before asking the workspace to validate the exact
	// entrypoint. JSON field order and escaping never affect path selection.
	if err := probeManagedEntrypoints(ctx, client, deps, probes); err != nil {
		return nil, err
	}
	versionProbes := map[string]versionProbeResult{}
	for _, dep := range deps {
		obs := resolveObserved(dep, probes[dep.ID])
		if dep.Scripts.Version == "" {
			probeCandidateVersions(ctx, &obs, versionProbes, func(ctx context.Context, command string) (string, error) {
				probeCtx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
				defer cancel()
				result, err := client.ExecWithOptions(probeCtx, "exec "+shellQuote(command)+" --version", defaultWorkDir, int32(versionProbeTimeout/time.Second), nil, bridge.ExecOptions{})
				if err != nil {
					return "", err
				}
				if result.ExitCode != 0 {
					return "", fmt.Errorf("exit status %d", result.ExitCode)
				}
				return result.Stdout + "\n" + result.Stderr, nil
			})
		}
		receipt, receiptErr := readOperationReceipt(ctx, client, Home(dataRoot, dep.ID), dep.ID)
		obs.Receipt = receipt
		obs.LockAbandoned = !obs.LockHeld && receipt != nil && !receipt.Completed
		if receiptErr != nil {
			obs.Err = strings.Trim(strings.Join([]string{obs.Err, "operation receipt: " + receiptErr.Error()}, "; "), "; ")
		}
		observed[dep.ID] = obs
	}
	return observed, nil
}

// buildDiscoveryScript renders the sh text for one Discover call.
func buildDiscoveryScript(dataRoot string, deps []catalog.Dependency) string {
	var b strings.Builder
	b.WriteString(discoveryPreamble)
	shimDir := ShimDir(dataRoot)
	for _, dep := range deps {
		home := Home(dataRoot, dep.ID)
		writeDependencyProbe(&b, dep, StatePath(home), lockPath(home, dep.ID), shimDir)
	}
	b.WriteString("printf '" + markerEnd + "\\n'\n")
	return b.String()
}

func writeDependencyProbe(b *strings.Builder, dep catalog.Dependency, statePath, lock, shimDir string) {
	fmt.Fprintf(b, "printf '%s\\t%%s\\n' %s\n", markerDep, shellQuote(dep.ID))
	// Probe the same stable kernel lock used by execution and finalization;
	// file presence alone does not imply that an operation is still running.
	fmt.Fprintf(b, "if memoh_lock_active %s; then printf '%s\\t%%s\\n' %s; elif [ -d %s ]; then printf '%s\\t%%s\\n' %s; fi\n", shellQuote(lock), markerLock, shellQuote(dep.ID), shellQuote(lock), markerAbandoned, shellQuote(dep.ID))
	fmt.Fprintf(b, "memoh_state=%s\n", shellQuote(statePath))
	b.WriteString("if [ -f \"$memoh_state\" ]; then\n")
	b.WriteString("  printf '" + markerStateBegin + "\\n'\n")
	b.WriteString("  cat \"$memoh_state\"\n")
	b.WriteString("  printf '\\n" + markerStateEnd + "\\n'\n")
	b.WriteString("fi\n")

	for _, command := range dep.Provides {
		toolkitPath := path.Join(toolkitBinDir, command)
		fmt.Fprintf(b, "if [ -x %s ]; then\n", shellQuote(toolkitPath))
		fmt.Fprintf(b, "  printf '%s\\t%%s\\t%%s\\n' %s %s\n", markerToolkit, shellQuote(command), shellQuote(toolkitPath))
		b.WriteString("fi\n")
		fmt.Fprintf(b, "memoh_p=$(memoh_resolve %s)\n", shellQuote(command))
		// Our own shims resolve to the managed copy; they are not a third
		// source.
		fmt.Fprintf(b, "case \"$memoh_p\" in %s/*) memoh_p='' ;; esac\n", shellQuote(shimDir))
		b.WriteString("if [ -n \"$memoh_p\" ]; then\n")
		fmt.Fprintf(b, "  printf '%s\\t%%s\\t%%s\\n' %s \"$memoh_p\"\n", markerPath, shellQuote(command))
		b.WriteString("fi\n")
	}
}

// probeManagedEntrypoints checks paths from decoded state without requiring
// a JSON parser or trusting a best-effort shell regex.
func probeManagedEntrypoints(ctx context.Context, client *bridge.Client, deps []catalog.Dependency, probes map[string]*rawProbe) error {
	var script strings.Builder
	for _, dep := range deps {
		probe := probes[dep.ID]
		if probe == nil || !probe.hasState {
			continue
		}
		var state State
		if json.Unmarshal([]byte(probe.stateText), &state) != nil {
			continue
		}
		command := dep.Provides[0]
		entrypoint := strings.TrimSpace(state.Entrypoints[command])
		if entrypoint == "" || !path.IsAbs(entrypoint) || strings.ContainsAny(entrypoint, "\t\r\n") {
			continue
		}
		fmt.Fprintf(&script, "printf '%s\\t%%s\\n' %s\n", markerDep, shellQuote(dep.ID))
		fmt.Fprintf(&script, "if [ -x %s ]; then memoh_ok=yes; else memoh_ok=no; fi\n", shellQuote(entrypoint))
		fmt.Fprintf(&script, "printf '%s\\t%%s\\t%%s\\t%%s\\n' %s %s \"$memoh_ok\"\n", markerManaged, shellQuote(command), shellQuote(entrypoint))
	}
	if script.Len() == 0 {
		return nil
	}
	script.WriteString("printf '" + markerEnd + "\\n'\n")
	result, err := client.ExecWithOptions(ctx, "exec sh -s", defaultWorkDir, int32(discoveryTimeout/time.Second), []byte(script.String()), bridge.ExecOptions{})
	if err != nil {
		return fmt.Errorf("workspacedeps: check managed entrypoints: %w", err)
	}
	checked, complete := parseDiscoveryOutput(result.Stdout)
	if result.ExitCode != 0 || !complete {
		return errors.New("workspacedeps: managed entrypoint check did not finish")
	}
	for id, probe := range checked {
		probes[id].managed = probe.managed
	}
	return nil
}

// rawProbe is the per-dependency output of the discovery script before
// interpretation.
type rawProbe struct {
	stateText string
	hasState  bool
	// managed maps the primary command to the state.json entrypoint the
	// script located and whether it is executable.
	managed map[string]managedProbe
	toolkit map[string]string
	path    map[string]string
	// versions maps a probed path to the raw `--version` output.
	versions map[string]string
	// lockHeld is set when the script saw the dependency's lock directory.
	lockHeld      bool
	lockAbandoned bool
}

type managedProbe struct {
	path       string
	executable bool
}

func newRawProbe() *rawProbe {
	return &rawProbe{
		managed:  make(map[string]managedProbe),
		toolkit:  make(map[string]string),
		path:     make(map[string]string),
		versions: make(map[string]string),
	}
}

// parseDiscoveryOutput splits the script's stdout into per-dependency probes.
// complete reports whether the end marker was seen, i.e. the script was not
// cut short by a timeout or a hanging `--version`.
func parseDiscoveryOutput(stdout string) (probes map[string]*rawProbe, complete bool) {
	probes = make(map[string]*rawProbe)
	var (
		current      *rawProbe
		block        []string
		inState      bool
		inVersion    bool
		versionPath  string
		versionsSeen = make(map[string]string)
	)
	for _, line := range strings.Split(stdout, "\n") {
		switch {
		case inState:
			if line == markerStateEnd {
				inState = false
				if current != nil {
					current.stateText = strings.TrimSpace(strings.Join(block, "\n"))
					current.hasState = true
				}
				block = nil
				continue
			}
			block = append(block, line)
			continue
		case inVersion:
			if line == markerVersionEnd {
				inVersion = false
				versionsSeen[versionPath] = strings.TrimSpace(strings.Join(block, "\n"))
				block = nil
				continue
			}
			block = append(block, line)
			continue
		}
		if !strings.HasPrefix(line, markerPrefix) {
			continue
		}
		fields := strings.Split(line, "\t")
		switch fields[0] {
		case markerDep:
			if len(fields) < 2 {
				continue
			}
			current = newRawProbe()
			probes[fields[1]] = current
		case markerStateBegin:
			inState = true
			block = nil
		case markerVersionBegin:
			if len(fields) < 2 {
				continue
			}
			inVersion = true
			versionPath = fields[1]
			block = nil
		case markerManaged:
			if current != nil && len(fields) >= 4 {
				current.managed[fields[1]] = managedProbe{path: fields[2], executable: fields[3] == "yes"}
			}
		case markerToolkit:
			if current != nil && len(fields) >= 3 {
				current.toolkit[fields[1]] = fields[2]
			}
		case markerPath:
			if current != nil && len(fields) >= 3 {
				current.path[fields[1]] = fields[2]
			}
		case markerLock:
			if current != nil {
				current.lockHeld = true
			}
		case markerAbandoned:
			if current != nil {
				current.lockAbandoned = true
			}
		case markerEnd:
			complete = true
		}
	}
	// Version blocks are deduplicated across dependencies by path, so hand
	// every probe the full table.
	for _, probe := range probes {
		probe.versions = versionsSeen
	}
	return probes, complete
}

// resolveObserved interprets one dependency's probe: managed beats toolkit
// beats PATH, duplicates by path collapse into the higher-precedence source.
func resolveObserved(dep catalog.Dependency, probe *rawProbe) Observed {
	obs := Observed{DepID: dep.ID}
	if probe == nil {
		obs.Err = "discovery produced no output for this dependency"
		return obs
	}
	obs.LockHeld = probe.lockHeld
	obs.LockAbandoned = probe.lockAbandoned
	primary := dep.Provides[0]
	var problems []string

	if probe.hasState {
		var state State
		if err := json.Unmarshal([]byte(probe.stateText), &state); err != nil {
			problems = append(problems, "state.json: "+err.Error())
		} else {
			obs.State = &state
			entrypoint := strings.TrimSpace(state.Entrypoints[primary])
			switch {
			case entrypoint == "":
				problems = append(problems, fmt.Sprintf("state.json lists no entrypoint for %q", primary))
			case !managedUsable(probe, primary, entrypoint):
				problems = append(problems, fmt.Sprintf("state.json entrypoint %s is not executable", entrypoint))
			default:
				version := extractVersion(probe.versions[entrypoint])
				if version == "" {
					version = strings.TrimSpace(state.Version)
				}
				obs.Candidates = append(obs.Candidates, Candidate{Source: SourceManaged, Path: entrypoint, Version: version})
			}
		}
	}
	if toolkitPath := probe.toolkit[primary]; toolkitPath != "" {
		obs.Candidates = appendCandidate(obs.Candidates, Candidate{Source: SourceToolkit, Path: toolkitPath, Version: extractVersion(probe.versions[toolkitPath])})
	}
	if pathCopy := probe.path[primary]; pathCopy != "" {
		obs.Candidates = appendCandidate(obs.Candidates, Candidate{Source: SourcePath, Path: pathCopy, Version: extractVersion(probe.versions[pathCopy])})
	}

	if len(obs.Candidates) > 0 {
		winner := obs.Candidates[0]
		obs.Present = true
		obs.Source = winner.Source
		obs.Command = winner.Path
		obs.Version = winner.Version
		obs.Entrypoints = entrypointsFor(winner.Source, dep, probe, obs.State)
	}
	obs.Err = strings.Join(problems, "; ")
	return obs
}

// managedUsable requires an executable check for the decoded state path.
func managedUsable(probe *rawProbe, primary, entrypoint string) bool {
	found, ok := probe.managed[primary]
	return ok && found.path == entrypoint && found.executable
}

// appendCandidate drops copies whose path is already claimed by a higher
// precedence source (the toolkit bin being on PATH is the common case).
func appendCandidate(candidates []Candidate, candidate Candidate) []Candidate {
	for _, existing := range candidates {
		if existing.Path == candidate.Path {
			return candidates
		}
	}
	return append(candidates, candidate)
}

func entrypointsFor(source Source, dep catalog.Dependency, probe *rawProbe, state *State) map[string]string {
	switch source {
	case SourceManaged:
		if state == nil {
			return nil
		}
		return cloneStringMap(state.Entrypoints)
	case SourceToolkit:
		return selectEntrypoints(dep.Provides, probe.toolkit)
	case SourcePath:
		return selectEntrypoints(dep.Provides, probe.path)
	default:
		return nil
	}
}

func selectEntrypoints(provides []string, found map[string]string) map[string]string {
	entrypoints := make(map[string]string, len(provides))
	for _, command := range provides {
		if resolved := found[command]; resolved != "" {
			entrypoints[command] = resolved
		}
	}
	return entrypoints
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

// extractVersion extracts a semantic version from command output.
func extractVersion(output string) string {
	return versionPattern.FindString(output)
}

type versionProbeResult struct {
	version string
	err     error
}

// Discovery never executes scripts.version from a newly fetched definition.
// A normal --version command has its own deadline and cannot abort discovery
// of other installed tools. Managed copies retain their recorded version.
func probeCandidateVersions(ctx context.Context, obs *Observed, cache map[string]versionProbeResult, probe func(context.Context, string) (string, error)) {
	var problems []string
	if obs.Err != "" {
		problems = append(problems, obs.Err)
	}
	for i := range obs.Candidates {
		candidate := &obs.Candidates[i]
		result, known := cache[candidate.Path]
		if !known {
			output, err := probe(ctx, candidate.Path)
			result = versionProbeResult{version: extractVersion(output), err: err}
			cache[candidate.Path] = result
		}
		if result.err != nil {
			problems = append(problems, fmt.Sprintf("version probe of %s: %v", candidate.Path, result.err))
		} else if result.version != "" {
			candidate.Version = result.version
		}
	}
	if len(obs.Candidates) > 0 {
		obs.Version = obs.Candidates[0].Version
	}
	obs.Err = strings.Join(problems, "; ")
}
