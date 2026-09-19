package tools

import (
	"context"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/apps"
	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

type (
	capabilityCatalogKey   struct{}
	capabilityTestRegistry struct {
		apps.RegistryClient
		current supermarket.AppDescriptor
	}
)

func (r *capabilityTestRegistry) FetchCurrentApp(context.Context, string, string) (supermarket.AppDescriptor, error) {
	return r.current, nil
}

func (*capabilityTestRegistry) FetchRelease(_ context.Context, registry, app, revision string) (supermarket.AppDescriptor, error) {
	return supermarket.AppDescriptor{AppSummary: supermarket.AppSummary{RegistryID: registry, AppID: app, AppMetadata: supermarket.AppMetadata{Dependencies: []string{"node"}}}, Revision: revision}, nil
}

type capabilityTestApps struct {
	CapabilityApps
	installed apps.InstallRequest
	snapshot  any
}

func (a *capabilityTestApps) Install(ctx context.Context, _ string, req apps.InstallRequest, sink apps.EventSink) (apps.OperationResult, error) {
	a.installed = req
	a.snapshot = ctx.Value(capabilityCatalogKey{})
	if sink != nil {
		sink.Send(apps.Event{Type: apps.EventStarted, Kind: apps.KindApp, ID: req.AppID})
		sink.Send(apps.Event{Type: apps.EventStep, Kind: apps.KindDependency, ID: "node"})
		sink.Send(apps.Event{Type: apps.EventStepDone, Kind: apps.KindDependency, ID: "node", Status: apps.StepInstalled})
		sink.Send(apps.Event{Type: apps.EventDone, Kind: apps.KindApp, ID: req.AppID, Status: string(apps.StatusInstalled)})
	}
	return apps.OperationResult{Installation: apps.Installation{ID: "installation"}}, nil
}

func (*capabilityTestApps) Get(context.Context, string, string) (apps.Item, error) {
	return apps.Item{Installation: &apps.Installation{ID: "installation", Status: apps.StatusInstalled}}, nil
}

func TestCapabilityInstallKeepsReleaseAndDependencySnapshotAcrossApproval(t *testing.T) {
	p, _, review, session := capabilityFixture(t)
	registry := &capabilityTestRegistry{current: supermarket.AppDescriptor{AppSummary: supermarket.AppSummary{RegistryID: "memoh", AppID: "node"}, Revision: "approved"}}
	service := &capabilityTestApps{}
	p.opts.Apps = service
	p.opts.Registry = registry
	p.opts.Catalog = &supermarket.Client{}
	currentRecipe := "recipe-approved"
	p.opts.FreezeDependencies = func(ctx context.Context) (context.Context, []catalog.Dependency, error) {
		return context.WithValue(ctx, capabilityCatalogKey{}, currentRecipe), []catalog.Dependency{{ID: "node", Revision: currentRecipe}}, nil
	}
	review.beforeDecision = func() { registry.current.Revision = "new-latest"; currentRecipe = "new-recipe" }
	available, err := p.Tools(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	var result any
	var progress []any
	for _, tool := range available {
		if tool.Name == ToolAppManage().String() {
			result, err = tool.Execute(&sdk.ToolExecContext{Context: t.Context(), SendProgress: func(value any) { progress = append(progress, value) }}, map[string]any{"action": "install", "registry_id": "memoh", "app_id": "node"})
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if service.installed.Revision != "approved" || service.snapshot != "recipe-approved" {
		t.Fatalf("installation changed across approval: release=%q snapshot=%v result=%v", service.installed.Revision, service.snapshot, result)
	}
	if review.input.ToolInput.(map[string]any)["revision"] != service.installed.Revision {
		t.Fatal("review did not show installed revision")
	}
	assertCapabilityMessage(t, result)
	if len(progress) != 4 {
		t.Fatalf("progress messages = %d, want 4", len(progress))
	}
	for _, event := range progress {
		assertCapabilityMessage(t, event)
	}
}

type changingCapabilityApps struct {
	capabilityTestApps
	item    apps.Item
	preview apps.RemovalPreview
	removed bool
	resumed bool
}

func (a *changingCapabilityApps) Get(context.Context, string, string) (apps.Item, error) {
	return a.item, nil
}

func (a *changingCapabilityApps) RemovalPreview(context.Context, string, string) (apps.RemovalPreview, error) {
	return a.preview, nil
}

func (a *changingCapabilityApps) Remove(context.Context, string, string, apps.RemoveOptions, apps.EventSink) (apps.OperationResult, error) {
	a.removed = true
	return apps.OperationResult{}, nil
}

func (a *changingCapabilityApps) ResumeApproved(context.Context, string, string, string, apps.EventSink) (apps.OperationResult, error) {
	a.resumed = true
	return apps.OperationResult{}, nil
}

func TestCapabilityAppDoesNotExecuteChangedPlanAfterApproval(t *testing.T) {
	for _, action := range []string{"uninstall", "resume"} {
		t.Run(action, func(t *testing.T) {
			p, _, review, session := capabilityFixture(t)
			release := supermarket.AppDescriptor{AppSummary: supermarket.AppSummary{RegistryID: "memoh", AppID: "node"}, Revision: "approved"}
			service := &changingCapabilityApps{item: apps.Item{Installation: &apps.Installation{ID: "installation", Revision: "approved", Status: apps.StatusFailed}, Release: &release}}
			p.opts.Apps = service
			p.opts.Registry = &capabilityTestRegistry{current: release}
			p.opts.FreezeDependencies = func(ctx context.Context) (context.Context, []catalog.Dependency, error) {
				return ctx, []catalog.Dependency{{ID: "node", Revision: "recipe"}}, nil
			}
			review.beforeDecision = func() {
				if action == "uninstall" {
					service.preview.Dependencies = []apps.RemovalPreviewDependency{{ID: "shared", Action: apps.RemovalActionRemove}}
				} else {
					service.item.Installation = &apps.Installation{ID: "installation", Revision: "changed", UpdatedAt: time.Now()}
				}
			}
			_, err := p.manageApp(&sdk.ToolExecContext{Context: t.Context()}, session, map[string]any{"action": action, "installation_id": "installation"})
			public, ok := apperror.PublicFrom(err, "")
			if !ok || public.Code != apperror.CodeCapabilityRequestInvalid || service.removed || service.resumed {
				t.Fatalf("changed plan executed: err=%v removed=%v resumed=%v", err, service.removed, service.resumed)
			}
		})
	}
}

func (*capabilityTestApps) List(context.Context, string, bool) (apps.ListResult, error) {
	return apps.ListResult{}, nil
}

func TestCapabilityAppRefreshInvalidatesConnectorDiscoveryAfterAuthorization(t *testing.T) {
	p, _, _, session := capabilityFixture(t)
	p.opts.Apps = &capabilityTestApps{}
	invalidated := ""
	p.opts.Invalidate = func(bot string) { invalidated = bot }
	if _, err := p.manageApp(&sdk.ToolExecContext{Context: t.Context()}, session, map[string]any{"action": "list", "refresh": true}); err != nil {
		t.Fatal(err)
	}
	if invalidated != session.BotID {
		t.Fatal("authorization refresh left connector discovery cached")
	}
}
