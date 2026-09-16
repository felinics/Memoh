package workspacedeps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestRecoveryCannotElevateWorkspaceReceiptAuthority(t *testing.T) {
	cases := map[string]func(*OperationReceipt){
		"kernel_control_protocol": func(r *OperationReceipt) { r.ControlProtocol = "" },
		"management_actor":        func(r *OperationReceipt) { r.AuthorizedByActor = "forged-manager" },
		"repair_mode":             func(r *OperationReceipt) { r.Repair = !r.Repair; r.DesiredRevision = "forged-revision" },
		"exact_version":           func(r *OperationReceipt) { r.RequestedVersion = "9.0.0" },
		"frozen_definition":       func(r *OperationReceipt) { r.DefinitionRevision = "forged-publication" },
		"removal_action":          func(r *OperationReceipt) { r.Action = "remove" },
		"payload_identity":        func(r *OperationReceipt) { r.StoreRoot = "/var/lib/other"; r.RestorePayloadPath = "/var/lib/other/foo" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := isolatedFixture(t)
			if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
				t.Fatal(err)
			}
			before := f.readState(t, "foo")
			originalRun := f.svc.run
			f.svc.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
				result, err := originalRun(ctx, client, spec, sink)
				if err == nil && spec.Receipt != nil {
					return result, ErrOperationUncertain
				}
				return result, err
			}
			ctx := WithRepairActor(f.ctx(), "original-manager")
			if _, err := f.svc.Reinstall(ctx, testBot, "foo", "1.0.0", nil); !errors.Is(err, ErrOperationUncertain) {
				t.Fatal(err)
			}
			receipt, err := ReadOperationReceipt(f.ctx(), f.client, f.home("foo"), "foo")
			if err != nil || receipt == nil || !receipt.Completed {
				t.Fatalf("receipt %+v %v", receipt, err)
			}
			mutate(receipt)
			metadata, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(receipt.Directory, "metadata.json")
			if err := os.WriteFile(file, metadata, 0o600); err != nil {
				t.Fatal(err)
			}
			f.svc.run = originalRun
			if _, err := f.svc.Refresh(f.ctx(), testBot); err != nil {
				t.Fatal(err)
			}
			rec, err := f.store.Get(f.ctx(), f.key("foo"))
			if err != nil {
				t.Fatal(err)
			}
			if rec.Status != StatusFailed || rec.OperationID != "" || rec.OperationIntent != nil {
				t.Fatalf("tampered receipt retained/committed operation: %+v", rec)
			}
			if got := f.readState(t, "foo"); got.InstallationID != before.InstallationID {
				t.Fatal("tampered receipt replaced current payload")
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatal("rejected receipt evidence removed", err)
			}
		})
	}
}

func TestRecoveryWithoutTrustedIntentRequiresNewManagementAction(t *testing.T) {
	f := isolatedFixture(t)
	originalRun := f.svc.run
	f.svc.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
		result, err := originalRun(ctx, client, spec, sink)
		if err == nil {
			return result, ErrOperationUncertain
		}
		return result, err
	}
	if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); !errors.Is(err, ErrOperationUncertain) {
		t.Fatal(err)
	}
	f.store.mu.Lock()
	rec := f.store.records[f.key("foo")]
	rec.OperationIntent = nil
	f.store.records[f.key("foo")] = rec
	f.store.mu.Unlock()
	f.svc.run = originalRun
	if _, err := f.svc.Refresh(f.ctx(), testBot); err != nil {
		t.Fatal(err)
	}
	rec, err := f.store.Get(f.ctx(), f.key("foo"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" {
		if !rec.Status.InProgress() || rec.OperationIntent == nil || rec.OperationIntent.ControlMigrationEpoch == "" {
			t.Fatalf("legacy lock location was treated as unowned: %+v", rec)
		}
		// The database is trusted. Model an enrollment from an earlier complete
		// workspace lifetime; the live Linux probe supplies the new lifetime.
		f.store.mu.Lock()
		rec.OperationIntent.ControlMigrationEpoch = "prior-workspace-lifetime"
		f.store.records[f.key("foo")] = rec
		f.store.mu.Unlock()
		if _, err := f.svc.Refresh(f.ctx(), testBot); err != nil {
			t.Fatal(err)
		}
		rec, err = f.store.Get(f.ctx(), f.key("foo"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if rec.Status != StatusFailed || rec.OperationID != "" {
		t.Fatalf("untrusted legacy operation granted install: %+v", rec)
	}
	if _, err := os.Stat(StatePath(f.home("foo"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("untrusted legacy payload published")
	}
}
