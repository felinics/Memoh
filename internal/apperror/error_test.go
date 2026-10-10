package apperror

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"
)

func TestErrorKeepsStableCodeAndPrivateCause(t *testing.T) {
	cause := errors.New("dial unix /run/memoh/bridge.sock: connection refused")
	err := fmt.Errorf("start workspace: %w", Wrap(CodeWorkspaceUnreachable, cause, nil))

	if got := CodeOf(err); got != CodeWorkspaceUnreachable {
		t.Fatalf("CodeOf() = %q, want %q", got, CodeWorkspaceUnreachable)
	}
	if got := CauseOf(err); !errors.Is(got, cause) {
		t.Fatalf("CauseOf() = %v, want original cause", got)
	}
	if errors.Is(err, cause) {
		t.Fatal("infrastructure cause leaked through errors.Is")
	}
	if strings.Contains(err.Error(), cause.Error()) {
		t.Fatal("infrastructure cause leaked through Error()")
	}
}

func TestArgsAreCopiedAtInputAndOutput(t *testing.T) {
	args := map[string]string{
		"field":             "name",
		"provider_response": "secret provider payload",
	}
	err := New(CodeBotNameTaken, args)
	args["field"] = "changed"

	got := ArgsOf(err)
	if got["field"] != "name" {
		t.Fatalf("stored field = %q", got["field"])
	}
	got["field"] = "changed again"
	if ArgsOf(err)["field"] != "name" {
		t.Fatal("ArgsOf returned mutable internal state")
	}
	if _, ok := got["provider_response"]; ok {
		t.Fatal("undeclared arg crossed the public error boundary")
	}

	workspaceErr := Wrap(CodeWorkspaceUnreachable, errors.New("private"), map[string]string{"path": "/secret"})
	if workspaceArgs := ArgsOf(workspaceErr); len(workspaceArgs) != 0 {
		t.Fatalf("workspace args = %#v, want empty allowlisted metadata", workspaceArgs)
	}
}

func TestLookupDoesNotExposeMutableCatalogState(t *testing.T) {
	definition, ok := Lookup(CodeBotNameTaken)
	if !ok {
		t.Fatal("bot.name_taken missing from catalog")
	}
	definition.AllowedArgs[0] = "changed"

	fresh, _ := Lookup(CodeBotNameTaken)
	if fresh.AllowedArgs[0] != "field" {
		t.Fatalf("catalog allowed args were mutated: %#v", fresh.AllowedArgs)
	}
}

func TestContextLifecycleErrorCatalog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code   Code
		status int
		detail string
	}{
		{CodeContextLifecycleRequestInvalid, http.StatusBadRequest, "The context lifecycle request is invalid."},
		{CodeContextLifecycleAuthenticationRequired, http.StatusUnauthorized, "Sign in to view context lifecycle diagnostics."},
		{CodeContextLifecycleAccessDenied, http.StatusForbidden, "You do not have access to context lifecycle diagnostics."},
		{CodeContextLifecycleNotFound, http.StatusNotFound, "The conversation was not found."},
		{CodeContextLifecycleLoadFailed, http.StatusInternalServerError, "Context lifecycle diagnostics could not be loaded. Please try again."},
	}
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			t.Parallel()

			definition, ok := Lookup(test.code)
			if !ok {
				t.Fatalf("Lookup(%q) did not find catalog definition", test.code)
			}
			if definition.HTTPStatus != test.status || definition.Detail != test.detail {
				t.Fatalf("Lookup(%q) = %#v, want status %d and detail %q", test.code, definition, test.status, test.detail)
			}
			if len(definition.AllowedArgs) != 0 {
				t.Fatalf("Lookup(%q).AllowedArgs = %#v, want none", test.code, definition.AllowedArgs)
			}
		})
	}
}

func TestAgentResponseErrorCatalog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code   Code
		status int
		detail string
	}{
		{CodeAgentResponseTimeout, http.StatusGatewayTimeout, "The model did not respond in time. Please try again."},
		{CodeAgentResponseInterrupted, http.StatusBadGateway, "The model response was interrupted. Please try again."},
	}
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			t.Parallel()
			definition, ok := Lookup(test.code)
			if !ok {
				t.Fatalf("Lookup(%q) did not find catalog definition", test.code)
			}
			if definition.HTTPStatus != test.status || definition.Detail != test.detail {
				t.Fatalf("Lookup(%q) = %#v, want status %d and detail %q", test.code, definition, test.status, test.detail)
			}
			if len(definition.AllowedArgs) != 0 {
				t.Fatalf("Lookup(%q).AllowedArgs = %#v, want none", test.code, definition.AllowedArgs)
			}
		})
	}
}

func TestSkillLayoutErrorsUseConflictContract(t *testing.T) {
	for _, code := range []Code{
		CodeSkillBuiltinReadOnly,
	} {
		definition, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s missing from catalog", code)
		}
		if definition.HTTPStatus != http.StatusConflict {
			t.Fatalf("%s status = %d, want 409", code, definition.HTTPStatus)
		}
		if strings.TrimSpace(definition.Detail) == "" {
			t.Fatalf("%s has empty fallback detail", code)
		}
	}
}

func TestSkillSaveErrorsUseStableContracts(t *testing.T) {
	tests := []struct {
		code   Code
		status int
	}{
		{code: CodeSkillNameTaken, status: http.StatusConflict},
		{code: CodeSkillSaveFailed, status: http.StatusInternalServerError},
	}
	for _, test := range tests {
		definition, ok := Lookup(test.code)
		if !ok {
			t.Fatalf("%s missing from catalog", test.code)
		}
		if definition.HTTPStatus != test.status {
			t.Fatalf("%s status = %d, want %d", test.code, definition.HTTPStatus, test.status)
		}
		if strings.TrimSpace(definition.Detail) == "" {
			t.Fatalf("%s has empty fallback detail", test.code)
		}
	}
}

func TestRegistryUpstreamErrorsUseStableContracts(t *testing.T) {
	tests := []struct {
		code   Code
		status int
	}{
		{code: CodeRegistryUnavailable, status: http.StatusBadGateway},
		{code: CodeRegistryAppNotFound, status: http.StatusNotFound},
		{code: CodeRegistryAppInvalid, status: http.StatusBadGateway},
	}
	for _, test := range tests {
		definition, ok := Lookup(test.code)
		if !ok {
			t.Fatalf("%s missing from catalog", test.code)
		}
		if definition.HTTPStatus != test.status {
			t.Fatalf("%s status = %d, want %d", test.code, definition.HTTPStatus, test.status)
		}
		if strings.TrimSpace(definition.Detail) == "" {
			t.Fatalf("%s has empty fallback detail", test.code)
		}
	}
}

func TestRegistryAppInstallFailedUsesPrivateServerErrorContract(t *testing.T) {
	definition, ok := Lookup(CodeRegistryAppInstallFailed)
	if !ok {
		t.Fatal("registry.app_install_failed missing from catalog")
	}
	if definition.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("registry.app_install_failed status = %d, want 500", definition.HTTPStatus)
	}
	if strings.TrimSpace(definition.Detail) == "" {
		t.Fatal("registry.app_install_failed has empty fallback detail")
	}
}

func TestContextBudgetErrorsHaveStableCatalogContracts(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		code   Code
		detail string
	}{
		{CodeContextBudgetUnsatisfied, "The model context window is too small for this request. Run /compact to summarize older history, shorten the request, or switch to a model with a larger context window."},
		{CodeContextProtectedOverflow, "Required context exceeds the model context budget. Run /compact to summarize older history, or switch to a model with a larger context window."},
	} {
		definition, ok := Lookup(tt.code)
		if !ok {
			t.Fatalf("catalog missing %q", tt.code)
		}
		if definition.HTTPStatus != http.StatusUnprocessableEntity || definition.Detail != tt.detail {
			t.Fatalf("catalog[%q] = %#v", tt.code, definition)
		}
	}
}

func TestWorkspaceDependencyErrorCatalog(t *testing.T) {
	cases := map[Code]int{
		CodeWorkspaceDependencyNotFound:             http.StatusNotFound,
		CodeWorkspaceDependencyRequestInvalid:       http.StatusBadRequest,
		CodeWorkspaceDependencyActionUnsupported:    http.StatusUnprocessableEntity,
		CodeWorkspaceDependencyPlatformUnsupported:  http.StatusUnprocessableEntity,
		CodeWorkspaceDependencyBusy:                 http.StatusConflict,
		CodeWorkspaceDependencyRequired:             http.StatusConflict,
		CodeWorkspaceDependencyPrerequisitesChanged: http.StatusConflict,
		CodeWorkspaceDependencyWorkspaceNotRunning:  http.StatusConflict,
		CodeWorkspaceDependencyWorkspaceMissing:     http.StatusConflict,

		CodeWorkspaceDependencyRollbackUnavailable: http.StatusConflict,
		CodeWorkspaceDependencyOperationFailed:     http.StatusInternalServerError,
	}
	for code, status := range cases {
		definition, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s is not in the catalog", code)
		}
		if definition.HTTPStatus != status {
			t.Errorf("%s status = %d, want %d", code, definition.HTTPStatus, status)
		}
		if definition.Detail == "" {
			t.Errorf("%s has no detail", code)
		}
	}
}

func TestChannelVerificationErrorCatalog(t *testing.T) {
	t.Parallel()

	definition, ok := Lookup(CodeChannelVerificationFailed)
	if !ok {
		t.Fatal("channel.verification_failed missing from catalog")
	}
	if definition.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", definition.HTTPStatus, http.StatusBadGateway)
	}
	if definition.Detail != "The channel configuration could not be verified. Check the credentials, then try again." {
		t.Fatalf("detail = %q", definition.Detail)
	}
}

func TestErrorCauseIsNotUnwrap(t *testing.T) {
	cause := errors.New("private cause")
	err := Wrap(CodeInternal, cause, nil)

	if got := err.Cause(); got != cause { //nolint:errorlint // identity of the retained cause
		t.Fatalf("Cause() = %v, want the wrapped cause", got)
	}
	if errors.Unwrap(err) != nil || errors.Is(err, cause) {
		t.Fatal("the private cause must not be reachable through Unwrap")
	}
	if got := New(CodeInternal, nil).Cause(); got != nil {
		t.Fatalf("Cause() without a cause = %v, want nil", got)
	}
	var nilErr *Error
	if got := nilErr.Cause(); got != nil {
		t.Fatalf("nil Error Cause() = %v, want nil", got)
	}
}

func TestParseFault(t *testing.T) {
	t.Parallel()
	for _, f := range []Fault{FaultClient, FaultServer, FaultDependency, FaultCanceled} {
		if got, ok := ParseFault(string(f)); !ok || got != f {
			t.Errorf("ParseFault(%q) = %q, %v", f, got, ok)
		}
	}
	for _, s := range []string{"", "Client", "unknown"} {
		if got, ok := ParseFault(s); ok || got != "" {
			t.Errorf("ParseFault(%q) = %q, %v; want rejected", s, got, ok)
		}
	}
}

// A field error carries only the field to the client; the cause stays private.
func TestFieldErrorsCarryOnlyTheField(t *testing.T) {
	cause := errors.New("strconv.Atoi: parsing \"three\": invalid syntax")
	invalid := FieldInvalid("count", cause)
	if CodeOf(invalid) != CodeRequestFieldInvalid || !maps.Equal(ArgsOf(invalid), map[string]string{"field": "count"}) {
		t.Fatalf("FieldInvalid = %s %v", CodeOf(invalid), ArgsOf(invalid))
	}
	if !errors.Is(CauseOf(invalid), cause) {
		t.Fatalf("cause = %v, want the private cause", CauseOf(invalid))
	}
	required := FieldRequired("session_id")
	if CodeOf(required) != CodeRequestFieldRequired || !maps.Equal(ArgsOf(required), map[string]string{"field": "session_id"}) {
		t.Fatalf("FieldRequired = %s %v", CodeOf(required), ArgsOf(required))
	}
}
