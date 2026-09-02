package workspacedeps

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// ScriptEnvEntry is one environment variable the runner injects into a
// script (design §5.4).
type ScriptEnvEntry struct {
	Key string
	// Value is the real value when it is known before the run and a
	// placeholder in angle brackets otherwise (result path, values that are
	// only known once the workspace has been probed or read). It is empty
	// when Secret is set.
	Value string
	// Secret marks operator-supplied entries whose value must not leave the
	// Server, such as registry tokens passed through ScriptEnv.
	Secret bool
}

// ScriptPreview is the complete "what will run" view of one action
// (WD-API-001): the exact stdin text, how it is executed, its time budget,
// and the environment it sees.
type ScriptPreview struct {
	DependencyID string
	Action       catalog.Action
	// Digest is the manifest digest over dependency.yaml and every script
	// file the manifest references; it is what state.json records after a
	// successful install.
	Digest string
	// Exec is the command the runner starts; the script arrives on its stdin.
	Exec           string
	TimeoutSeconds int
	Env            []ScriptEnvEntry
	Script         string
}

// Placeholders for environment values the preview cannot know.
const (
	previewInstalledVersion = "<installed version>"
	previewPreviousVersion  = "<previous version>"
	previewProbedAtRunTime  = "<probed at run time>"
	previewResultNonce      = "<nonce>"
)

// secretEnvMarkers flags operator-supplied environment keys whose values are
// credentials rather than configuration.
var secretEnvMarkers = []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "API_KEY", "APIKEY", "AUTH"}

// ScriptPreviewDetails is ScriptPreview with the execution details the UI
// shows next to the script text. The environment mirrors buildEnv for the
// given (bot, target): the dependency home follows the target's data root
// and the platform entries come from the last probe when there is one.
// Nothing is executed and the workspace is never started.
func (s *Service) ScriptPreviewDetails(ctx context.Context, botID, targetID, depID string, action catalog.Action) (ScriptPreview, error) {
	dep, err := s.dependency(depID)
	if err != nil {
		return ScriptPreview{}, err
	}
	script, err := s.ScriptPreview(dep.ID, action)
	if err != nil {
		return ScriptPreview{}, err
	}
	targetID = normalizeTargetID(targetID)

	dataRoot := config.DefaultDataMount
	if root, err := s.workspace.DataRoot(ctx, botID, targetID); err == nil && strings.TrimSpace(root) != "" {
		dataRoot = root
	}
	platform := Platform{OS: previewProbedAtRunTime, Arch: previewProbedAtRunTime, Libc: previewProbedAtRunTime}
	if snap, ok := s.cache.Get(botID, targetID); ok {
		platform = snap.Platform
	}
	tmpDir := strings.TrimSpace(platform.TmpDir)
	if tmpDir == "" {
		tmpDir = defaultTmpDir
	}
	timeout := previewTimeout(dep, action)

	spec := RunSpec{
		DepID:          dep.ID,
		Action:         action,
		Home:           Home(dataRoot, dep.ID),
		ShimDir:        ShimDir(dataRoot),
		Version:        previewVersion(dep, action),
		CurrentVersion: previewCurrentVersion(action),
		Platform:       platform,
		Timeout:        timeout,
	}
	resultPath := path.Join(tmpDir, "memoh-dep-"+dep.ID+"-"+previewResultNonce+".json")
	env := make([]ScriptEnvEntry, 0, 16)
	for _, kv := range buildEnv(spec, resultPath, timeout) {
		env = append(env, previewEnvEntry(kv, false))
	}
	if s.scriptEnv != nil {
		for _, kv := range s.scriptEnv(ctx) {
			env = append(env, previewEnvEntry(kv, true))
		}
	}
	return ScriptPreview{
		DependencyID:   dep.ID,
		Action:         action,
		Digest:         dep.ManifestDigest,
		Exec:           scriptExecCommand,
		TimeoutSeconds: int(timeout / time.Second),
		Env:            env,
		Script:         script,
	}, nil
}

// previewTimeout mirrors the budget runScript gives each action.
func previewTimeout(dep catalog.Dependency, action catalog.Action) time.Duration {
	if action == ActionRollback {
		return rollbackTimeout
	}
	return dep.Timeouts.Duration(action)
}

// previewVersion mirrors the MEMOH_DEP_VERSION each service method passes.
func previewVersion(dep catalog.Dependency, action catalog.Action) string {
	switch action {
	case catalog.ActionInstall, catalog.ActionUpdate, catalog.ActionReinstall:
		return dep.Version.Pin
	case ActionRollback:
		return previewPreviousVersion
	default:
		return ""
	}
}

// previewCurrentVersion mirrors the MEMOH_DEP_CURRENT_VERSION each service
// method passes: the version recorded in state.json, when there is one.
func previewCurrentVersion(action catalog.Action) string {
	switch action {
	case catalog.ActionInstall, catalog.ActionUpdate, catalog.ActionReinstall:
		return previewInstalledVersion + " (if any)"
	case catalog.ActionRemove, ActionRollback, catalog.ActionCheckUpdate:
		return previewInstalledVersion
	default:
		return ""
	}
}

// previewEnvEntry splits a KEY=VALUE entry. Operator-supplied entries whose
// key looks like a credential are reported without their value.
func previewEnvEntry(kv string, operatorSupplied bool) ScriptEnvEntry {
	key, value, _ := strings.Cut(kv, "=")
	entry := ScriptEnvEntry{Key: key, Value: value}
	if operatorSupplied && isSecretEnvKey(key) {
		entry.Secret = true
		entry.Value = ""
	}
	return entry
}

func isSecretEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range secretEnvMarkers {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}
