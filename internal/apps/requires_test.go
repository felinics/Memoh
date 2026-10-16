package apps

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/workspacedeps"
)

func requiringDep(id string, requires ...string) workspacedeps.Entry {
	entry := absentDep(id)
	entry.Dependency.Requires = requires
	return entry
}

func TestInstallRunsPrerequisitesAsStepsWithoutReferencingThem(t *testing.T) {
	h := newHarness()
	h.deps.present["python"] = presentDep("python", workspacedeps.SourceToolkit)
	h.deps.present["micromamba"] = requiringDep("micromamba", "python")
	h.deps.present["pandoc"] = requiringDep("pandoc", "python", "micromamba")
	h.deps.present["poppler"] = requiringDep("poppler", "python", "micromamba")
	pkg := release("memoh", "docx", "a", "1.0.0", []string{"docx"}, []string{"pandoc", "poppler"}, nil)
	h.publish(pkg)

	result, rec := h.install(t, pkg)
	if result.Installation.Status != StatusInstalled {
		t.Fatalf("status = %s: %+v", result.Installation.Status, result)
	}
	if got := strings.Join(h.deps.installed, ","); got != "micromamba,pandoc,poppler" {
		t.Fatalf("installed = %s, want the shared prerequisite once and first", got)
	}
	for _, want := range []string{"step_done:dependency:python=linked", "step_done:dependency:micromamba=installed"} {
		if !strings.Contains(rec.types(), want) {
			t.Fatalf("events = %s, want %s", rec.types(), want)
		}
	}
	refs, _ := h.store.ListDependencyRefs(context.Background(), result.Installation.ID)
	var ids []string
	for _, ref := range refs {
		ids = append(ids, ref.DependencyID)
	}
	slices.Sort(ids)
	if strings.Join(ids, ",") != "pandoc,poppler" {
		t.Fatalf("dependency refs = %v, want only the App's direct dependencies", ids)
	}
}

func TestInstallSkipsDependentsOfAFailedPrerequisite(t *testing.T) {
	h := newHarness()
	h.deps.present["micromamba"] = requiringDep("micromamba")
	h.deps.present["pandoc"] = requiringDep("pandoc", "micromamba")
	h.deps.present["node"] = requiringDep("node")
	h.deps.installErr["micromamba"] = errors.New("download failed")
	pkg := release("memoh", "docx", "a", "1.0.0", []string{"docx"}, []string{"pandoc", "node"}, nil)
	h.publish(pkg)

	result, rec := h.install(t, pkg)
	if result.Installation.Status != StatusPartial {
		t.Fatalf("status = %s, want partial", result.Installation.Status)
	}
	if got := strings.Join(h.deps.installed, ","); got != "node" {
		t.Fatalf("installed = %s, want only the independent branch", got)
	}
	if !strings.Contains(rec.types(), "step_done:dependency:pandoc=failed") {
		t.Fatalf("events = %s", rec.types())
	}
	if result.Installation.LastErrorCode != string(apperror.CodeAppOperationFailed) {
		t.Fatalf("last error code = %q", result.Installation.LastErrorCode)
	}
	var blocked *StepResult
	for i := range result.Steps {
		if result.Steps[i].ID == "pandoc" {
			blocked = &result.Steps[i]
		}
	}
	if blocked == nil || blocked.Code != string(apperror.CodeAppPrerequisiteFailed) || blocked.Error != "" {
		t.Fatalf("blocked step = %+v, want the prerequisite code and no text", blocked)
	}

	delete(h.deps.installErr, "micromamba")
	resumed, err := h.service.Resume(context.Background(), testBotID, result.Installation.ID, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Installation.Status != StatusInstalled {
		t.Fatalf("resumed status = %s", resumed.Installation.Status)
	}
	if got := strings.Join(h.deps.installed, ","); got != "node,micromamba,pandoc" {
		t.Fatalf("installed after resume = %s", got)
	}
}

func TestRemoveKeepsDependenciesOtherDependenciesRequire(t *testing.T) {
	h := newHarness()
	h.deps.present["micromamba"] = requiringDep("micromamba")
	h.deps.present["pandoc"] = requiringDep("pandoc", "micromamba")
	mamba := release("memoh", "mamba", "a", "1.0.0", nil, []string{"micromamba"}, nil)
	h.publish(mamba)
	installed, _ := h.install(t, mamba)
	// pandoc was installed on its own and needs micromamba.
	if _, err := h.deps.Install(context.Background(), testBotID, "pandoc", "", nil); err != nil {
		t.Fatal(err)
	}

	preview, err := h.service.RemovalPreview(context.Background(), testBotID, installed.Installation.ID)
	if err != nil {
		t.Fatalf("RemovalPreview: %v", err)
	}
	if len(preview.Dependencies) != 1 || preview.Dependencies[0].Action != RemovalActionKeep || preview.Dependencies[0].Reason != RemovalReasonRequired {
		t.Fatalf("dependency plan = %+v, want micromamba kept as required", preview.Dependencies)
	}
	if _, err := h.service.Remove(context.Background(), testBotID, installed.Installation.ID, RemoveOptions{}, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(h.deps.removed) != 0 {
		t.Fatalf("removed = %v, want micromamba kept", h.deps.removed)
	}
}
