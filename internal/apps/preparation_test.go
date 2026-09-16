package apps

import (
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/workspacedeps"
)

func TestPreparationOnlyIncludesMissingDependenciesAndDoesNotInstall(t *testing.T) {
	h := newHarness()
	h.deps.present["node"] = presentDep("node", workspacedeps.SourceToolkit)
	pkg := release("memoh", "editor", "a", "1.0.0", []string{"editor"}, []string{"node", "codex"}, nil)
	h.publish(pkg)
	prepared, err := h.service.Prepare(t.Context(), testBotID, PrepareRequest{Action: "install", RegistryID: pkg.RegistryID, AppID: pkg.AppID, Revision: pkg.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Dependencies) != 1 || prepared.Dependencies[0].DependencyID != "codex" {
		t.Fatalf("unexpected executable dependencies: %+v", prepared.Dependencies)
	}
	installed, err := h.store.ListForBot(t.Context(), testBotID)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.deps.installed) != 0 || len(h.publisher.published) != 0 || len(installed) != 0 {
		t.Fatalf("prepare mutated workspace or installation: %v %v %v", h.deps.installed, h.publisher.published, installed)
	}
	target := prepared.Dependencies[0]
	if target.Version == "" || target.DefinitionRevision == "" || target.ManifestDigest == "" || target.SourceURL == "" {
		t.Fatalf("incomplete confirmation: %+v", target)
	}
}

func TestInstallRejectsUnconfirmedMissingDependenciesBeforeMutating(t *testing.T) {
	h := newHarness()
	pkg := release("memoh", "editor", "a", "1.0.0", nil, []string{"codex"}, nil)
	h.publish(pkg)
	_, err := h.service.Install(t.Context(), testBotID, InstallRequest{RegistryID: pkg.RegistryID, AppID: pkg.AppID, Revision: pkg.Revision}, nil)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unconfirmed install error = %v", err)
	}
	installed, _ := h.store.ListForBot(t.Context(), testBotID)
	if len(h.deps.installed) != 0 || len(h.publisher.published) != 0 || len(installed) != 0 {
		t.Fatal("unconfirmed dependency mutated the App or workspace")
	}
}

func TestInstallDoesNotAuthorizeAnExistingDependencyThroughReference(t *testing.T) {
	h := newHarness()
	h.deps.present["codex"] = presentDep("codex", workspacedeps.SourceManaged)
	pkg := release("memoh", "editor", "a", "1.0.0", nil, []string{"codex"}, nil)
	h.publish(pkg)
	result, err := h.service.Install(t.Context(), testBotID, InstallRequest{RegistryID: pkg.RegistryID, AppID: pkg.AppID, Revision: pkg.Revision}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Installation.Status != StatusInstalled || len(h.deps.prepared) != 0 || len(h.deps.installed) != 0 {
		t.Fatalf("link must not prepare/install/grant authority: %+v %v %v", result, h.deps.prepared, h.deps.installed)
	}
	refs, _ := h.store.ListDependencyRefs(t.Context(), result.Installation.ID)
	if len(refs) != 1 {
		t.Fatalf("link must still be recorded: %v", refs)
	}
}

func TestDependencyLostAfterPrepareRequiresNewConfirmation(t *testing.T) {
	h := newHarness()
	h.deps.present["node"] = presentDep("node", workspacedeps.SourceToolkit)
	pkg := release("memoh", "editor", "a", "1.0.0", nil, []string{"node", "codex"}, nil)
	h.publish(pkg)
	prepared, err := h.service.Prepare(t.Context(), testBotID, PrepareRequest{Action: "install", RegistryID: pkg.RegistryID, AppID: pkg.AppID, Revision: pkg.Revision})
	if err != nil {
		t.Fatal(err)
	}
	delete(h.deps.present, "node")
	_, err = h.service.Install(t.Context(), testBotID, InstallRequest{RegistryID: pkg.RegistryID, AppID: pkg.AppID, Revision: pkg.Revision, DependencyConfirmations: prepared.Dependencies}, nil)
	if !errors.Is(err, ErrInvalidRequest) || len(h.deps.installed) != 0 || len(h.publisher.published) != 0 {
		t.Fatalf("lost dependency must not expand authorization: %v %+v", err, h.deps.installed)
	}
}

func TestInstallUsesConfirmedVersionWithoutResolvingLatestAgain(t *testing.T) {
	h := newHarness()
	pkg := release("memoh", "editor", "a", "1.0.0", nil, []string{"codex"}, nil)
	h.publish(pkg)
	prepared, err := h.service.Prepare(t.Context(), testBotID, PrepareRequest{Action: "install", RegistryID: pkg.RegistryID, AppID: pkg.AppID, Revision: pkg.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.service.Install(t.Context(), testBotID, InstallRequest{RegistryID: pkg.RegistryID, AppID: pkg.AppID, Revision: pkg.Revision, DependencyConfirmations: prepared.Dependencies}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.deps.installVersions) != 1 || h.deps.installVersions[0] != prepared.Dependencies[0].Version {
		t.Fatalf("version changed at execution: %v", h.deps.installVersions)
	}
	// The first prepare resolves latest. Admission only validates that same
	// exact version; a blank request here would resolve mutable metadata again.
	if len(h.deps.prepareVersions) != 2 || h.deps.prepareVersions[0] != "" || h.deps.prepareVersions[1] != prepared.Dependencies[0].Version {
		t.Fatalf("unexpected metadata lookups: %v", h.deps.prepareVersions)
	}
}

func TestInstallRejectsTamperedConfirmationBeforeAnyDependencyRuns(t *testing.T) {
	for _, field := range []string{"version", "revision", "source", "unreferenced", "duplicate"} {
		t.Run(field, func(t *testing.T) {
			h := newHarness()
			pkg := release("memoh", "editor", "a", "1.0.0", nil, []string{"node", "codex"}, nil)
			h.publish(pkg)
			prepared, err := h.service.Prepare(t.Context(), testBotID, PrepareRequest{Action: "install", RegistryID: pkg.RegistryID, AppID: pkg.AppID, Revision: pkg.Revision})
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "version":
				prepared.Dependencies[1].Version = "latest"
			case "revision":
				prepared.Dependencies[1].DefinitionRevision = strings.Repeat("c", 64)
			case "source":
				prepared.Dependencies[1].SourceURL = "https://unconfirmed.example"
			case "unreferenced":
				prepared.Dependencies[1].DependencyID = "python"
			case "duplicate":
				prepared.Dependencies = append(prepared.Dependencies, prepared.Dependencies[0])
			}
			_, err = h.service.Install(t.Context(), testBotID, InstallRequest{RegistryID: pkg.RegistryID, AppID: pkg.AppID, Revision: pkg.Revision, DependencyConfirmations: prepared.Dependencies}, nil)
			if !errors.Is(err, ErrInvalidRequest) || len(h.deps.installed) != 0 {
				t.Fatalf("tampered confirmation ran dependency: %v %v", err, h.deps.installed)
			}
		})
	}
}

func TestUpdateUsesPreparedAppReleaseAfterRegistryMoves(t *testing.T) {
	h := newHarness()
	v1 := release("memoh", "editor", "a", "1.0.0", []string{"editor"}, nil, nil)
	h.publish(v1)
	h.install(t, v1)
	v2 := release("memoh", "editor", "b", "2.0.0", []string{"editor"}, []string{"codex"}, nil)
	h.publish(v2)
	req := h.confirmUpdate(t, UpdateRequest{RegistryID: "memoh", AppID: "editor", Release: true})
	v3 := release("memoh", "editor", "c", "3.0.0", []string{"editor"}, []string{"python"}, nil)
	h.publish(v3)
	result, err := h.service.UpdateSelection(t.Context(), testBotID, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Installation.Revision != v2.Revision || strings.Join(h.deps.installed, ",") != "codex" {
		t.Fatalf("unconfirmed release/dependencies ran: %+v %v", result, h.deps.installed)
	}
}

func TestResumeRejectsChangedAppRelease(t *testing.T) {
	h := newHarness()
	v1 := release("memoh", "editor", "a", "1.0.0", nil, nil, nil)
	h.publish(v1)
	result, _ := h.install(t, v1)
	prepared, err := h.service.Prepare(t.Context(), testBotID, PrepareRequest{Action: "resume", InstallationID: result.Installation.ID})
	if err != nil {
		t.Fatal(err)
	}
	v2 := release("memoh", "editor", "b", "2.0.0", nil, []string{"codex"}, nil)
	h.publish(v2)
	h.install(t, v2)
	published := len(h.publisher.published)
	_, err = h.service.Resume(t.Context(), testBotID, result.Installation.ID, ResumeRequest{Revision: prepared.Revision, DependencyConfirmations: prepared.Dependencies}, nil)
	if !errors.Is(err, ErrInvalidRequest) || len(h.publisher.published) != published {
		t.Fatalf("stale resume must not execute: %v", err)
	}
}
