package workspacedeps

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func previewEnv(t *testing.T, preview ScriptPreview, key string) ScriptEnvEntry {
	t.Helper()
	for _, entry := range preview.Env {
		if entry.Key == key {
			return entry
		}
	}
	t.Fatalf("env %s missing from %+v", key, preview.Env)
	return ScriptEnvEntry{}
}

func TestScriptPreviewDetailsMirrorsRunnerEnvironment(t *testing.T) {
	f := newServiceFixture(t)
	f.env = []string{"NPM_MIRROR=https://registry.example", "NPM_TOKEN=hunter2"}
	// Nothing probed yet: platform entries are placeholders.
	preview, err := f.svc.ScriptPreviewDetails(f.ctx(), testBot, testTarget, "agent-x", catalog.ActionInstall)
	if err != nil {
		t.Fatalf("ScriptPreviewDetails: %v", err)
	}
	agent := f.cat.MustGet("agent-x")
	if preview.DependencyID != "agent-x" || preview.Action != catalog.ActionInstall {
		t.Errorf("identity = %s/%s", preview.DependencyID, preview.Action)
	}
	if preview.Digest != agent.ManifestDigest || !strings.HasPrefix(preview.Digest, "sha256:") {
		t.Errorf("digest = %q, want manifest digest %q", preview.Digest, agent.ManifestDigest)
	}
	if preview.Exec != "exec sh -s" {
		t.Errorf("exec = %q", preview.Exec)
	}
	if preview.TimeoutSeconds != agent.Timeouts.For(catalog.ActionInstall) {
		t.Errorf("timeout = %d, want %d", preview.TimeoutSeconds, agent.Timeouts.For(catalog.ActionInstall))
	}
	if preview.Script != WrapScript("dep_log install agent\n") {
		t.Errorf("script = %q", preview.Script)
	}
	if got := previewEnv(t, preview, "MEMOH_DEP_HOME"); got.Value != Home(f.dataRoot, "agent-x") {
		t.Errorf("MEMOH_DEP_HOME = %q, want %q", got.Value, Home(f.dataRoot, "agent-x"))
	}
	if got := previewEnv(t, preview, "MEMOH_DEP_BIN"); got.Value != ShimDir(f.dataRoot) {
		t.Errorf("MEMOH_DEP_BIN = %q", got.Value)
	}
	if got := previewEnv(t, preview, "MEMOH_DEP_VERSION"); got.Value != "2.0.0" {
		t.Errorf("MEMOH_DEP_VERSION = %q, want the manifest pin", got.Value)
	}
	// An unpinned dependency installs the requested version or latest, which
	// only the request knows.
	toolPreview, err := f.svc.ScriptPreviewDetails(f.ctx(), testBot, testTarget, "tool-y", catalog.ActionUpdate)
	if err != nil {
		t.Fatalf("tool preview: %v", err)
	}
	if got := previewEnv(t, toolPreview, "MEMOH_DEP_VERSION"); got.Value != previewRequestedVersion {
		t.Errorf("unpinned MEMOH_DEP_VERSION = %q, want %q", got.Value, previewRequestedVersion)
	}
	if got := previewEnv(t, preview, "MEMOH_DEP_OS"); got.Value != previewProbedAtRunTime {
		t.Errorf("MEMOH_DEP_OS = %q, want placeholder", got.Value)
	}
	if got := previewEnv(t, preview, "MEMOH_DEP_RESULT"); !strings.Contains(got.Value, "memoh-dep-agent-x-") || !strings.Contains(got.Value, previewResultNonce) {
		t.Errorf("MEMOH_DEP_RESULT = %q", got.Value)
	}
	if got := previewEnv(t, preview, "NPM_MIRROR"); got.Secret || got.Value != "https://registry.example" {
		t.Errorf("NPM_MIRROR = %+v", got)
	}
	if got := previewEnv(t, preview, "NPM_TOKEN"); !got.Secret || got.Value != "" {
		t.Errorf("NPM_TOKEN = %+v, want secret with no value", got)
	}

	// Once the target has been probed the real platform is reported.
	if _, err := f.svc.List(f.ctx(), testBot, testTarget); err != nil {
		t.Fatalf("List: %v", err)
	}
	preview, err = f.svc.ScriptPreviewDetails(f.ctx(), testBot, testTarget, "agent-x", ActionRollback)
	if err != nil {
		t.Fatalf("rollback preview: %v", err)
	}
	if got := previewEnv(t, preview, "MEMOH_DEP_OS"); got.Value != "linux" {
		t.Errorf("MEMOH_DEP_OS after probe = %q", got.Value)
	}
	if got := previewEnv(t, preview, "MEMOH_DEP_VERSION"); got.Value != previewPreviousVersion {
		t.Errorf("rollback MEMOH_DEP_VERSION = %q", got.Value)
	}
	if preview.TimeoutSeconds != int(rollbackTimeout.Seconds()) {
		t.Errorf("rollback timeout = %d", preview.TimeoutSeconds)
	}
}

func TestScriptPreviewDetailsErrors(t *testing.T) {
	f := newServiceFixture(t)
	if _, err := f.svc.ScriptPreviewDetails(context.Background(), testBot, testTarget, "nope", catalog.ActionInstall); !errors.Is(err, ErrDependencyNotFound) {
		t.Errorf("unknown dependency error = %v", err)
	}
	if _, err := f.svc.ScriptPreviewDetails(context.Background(), testBot, testTarget, "img-z", catalog.ActionInstall); !errors.Is(err, ErrActionUnsupported) {
		t.Errorf("image dependency error = %v", err)
	}
	if _, err := f.svc.ScriptPreviewDetails(context.Background(), testBot, testTarget, "agent-x", catalog.ActionCheckUpdate); !errors.Is(err, ErrActionUnsupported) {
		t.Errorf("unscripted action error = %v", err)
	}
}
