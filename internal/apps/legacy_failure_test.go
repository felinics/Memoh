package apps

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/supermarket"
)

func TestConnectorAuthorizationPreservesDependencyFailure(t *testing.T) {
	methods := []struct {
		name    string
		connect func(context.Context, *Service, string) error
	}{
		{"oauth", func(ctx context.Context, service *Service, id string) error {
			_, err := service.BeginConnectorOAuth(ctx, testBotID, id, "github", "oauth")
			return err
		}},
		{"api_key", func(ctx context.Context, service *Service, id string) error {
			_, err := service.CreateConnectorCredential(ctx, testBotID, id, "github", "api_key", map[string]string{"token": "test"})
			return err
		}},
	}
	cases := []struct {
		name          string
		lastError     string
		lastErrorCode string
		wantStatus    Status
	}{
		{"legacy", "dependency installation failed", "", StatusPartial},
		{"catalog", "", string(apperror.CodeAppOperationFailed), StatusPartial},
		{"both", "dependency installation failed", string(apperror.CodeAppOperationFailed), StatusPartial},
		{"connector_only", "", "", StatusInstalled},
	}
	for _, method := range methods {
		for _, tc := range cases {
			t.Run(method.name+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				h := newHarness()
				h.deps.present["codex"] = absentDep("codex")
				if tc.wantStatus == StatusPartial {
					h.deps.installErr["codex"] = errors.New("dependency installation failed")
				}
				pkg := release("memoh", "review", "a", "1.0.0", []string{"review"}, []string{"codex"}, []supermarket.AppConnectorReference{{Type: "github", Required: true}})
				h.publish(pkg)
				result, _ := h.install(t, pkg)
				if result.Installation.Status != StatusPartial {
					t.Fatalf("status before authorization = %s, want partial", result.Installation.Status)
				}
				// Migration 0163 leaves old last_error text in place and adds an
				// empty last_error_code. Exercise that stored shape as well as new rows.
				h.store.mu.Lock()
				inst := h.store.installations[result.Installation.ID]
				inst.LastError, inst.LastErrorCode = tc.lastError, tc.lastErrorCode
				h.store.installations[inst.ID] = inst
				h.store.mu.Unlock()

				if err := method.connect(ctx, h.service, inst.ID); err != nil {
					t.Fatal(err)
				}
				refs, err := h.store.ListConnectorRefs(ctx, inst.ID)
				if err != nil || len(refs) != 1 || refs[0].ConnectionID == "" {
					t.Fatalf("connector was not linked: refs=%+v err=%v", refs, err)
				}
				got, err := h.store.GetByID(ctx, testBotID, inst.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != tc.wantStatus || got.LastError != tc.lastError || got.LastErrorCode != tc.lastErrorCode {
					t.Fatalf("after authorization: status=%s last_error=%q last_error_code=%q; want %s, %q, %q",
						got.Status, got.LastError, got.LastErrorCode, tc.wantStatus, tc.lastError, tc.lastErrorCode)
				}
			})
		}
	}
}

func TestResumeReplacesLegacyFailureAndClearsItOnSuccess(t *testing.T) {
	ctx := context.Background()
	h := newHarness()
	h.deps.present["codex"] = absentDep("codex")
	h.deps.installErr["codex"] = errors.New("dependency installation failed")
	pkg := release("memoh", "codex", "a", "1.0.0", []string{"review"}, []string{"codex"}, nil)
	h.publish(pkg)
	result, _ := h.install(t, pkg)
	h.store.mu.Lock()
	legacy := h.store.installations[result.Installation.ID]
	legacy.LastError, legacy.LastErrorCode = "old dependency failure", ""
	h.store.installations[legacy.ID] = legacy
	h.store.mu.Unlock()

	// An explicit retry replaces the old text with the current failure code.
	failed, err := h.service.Resume(ctx, testBotID, legacy.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Installation.Status != StatusPartial || failed.Installation.LastError != "" ||
		failed.Installation.LastErrorCode != string(apperror.CodeAppOperationFailed) {
		t.Fatalf("failed retry = %+v", failed.Installation)
	}

	delete(h.deps.installErr, "codex")
	recovered, err := h.service.Resume(ctx, testBotID, legacy.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Installation.Status != StatusInstalled || recovered.Installation.LastError != "" || recovered.Installation.LastErrorCode != "" {
		t.Fatalf("successful retry = %+v", recovered.Installation)
	}
}
