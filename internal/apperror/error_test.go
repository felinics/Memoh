package apperror

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestKindOnlyErrorNeedsNoRegistration(t *testing.T) {
	err := Unavailable("workspace", errors.New("dial unix /run/memoh/bridge.sock: refused"))

	problem := ProblemOf(err, "req-1")
	if problem.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", problem.Status, http.StatusServiceUnavailable)
	}
	if problem.Type != "about:blank" {
		t.Fatalf("type = %q, want about:blank for an uncoded error", problem.Type)
	}
	if problem.Code != "" {
		t.Fatalf("code = %q, want empty", problem.Code)
	}
	if problem.Title == "" || problem.Detail == "" {
		t.Fatalf("uncoded error rendered without user-facing text: %#v", problem)
	}
	if problem.RequestID != "req-1" {
		t.Fatalf("request_id = %q", problem.RequestID)
	}
}

func TestCauseIsReachableInProcessAndAbsentOnTheWire(t *testing.T) {
	cause := errors.New("dial unix /run/memoh/bridge.sock: connection refused")
	err := fmt.Errorf("start workspace: %w", Wrap(CodeWorkspaceUnreachable, cause, nil))

	// v1 implements Unwrap on purpose: middle layers must be able to inspect
	// the cause. Leak protection is the renderer's job, not the type's.
	if !errors.Is(err, cause) {
		t.Fatal("cause is not reachable through errors.Is")
	}
	if got := CodeOf(err); got != CodeWorkspaceUnreachable {
		t.Fatalf("CodeOf() = %q, want %q", got, CodeWorkspaceUnreachable)
	}
	if got := CauseOf(err); !errors.Is(got, cause) {
		t.Fatalf("CauseOf() = %v, want original cause", got)
	}

	problem := ProblemOf(err, "req-2")
	if strings.Contains(fmt.Sprintf("%#v", problem), "bridge.sock") {
		t.Fatalf("cause reached the wire: %#v", problem)
	}
	public := PublicOf(err, "req-2")
	if strings.Contains(fmt.Sprintf("%#v", public), "bridge.sock") {
		t.Fatalf("cause reached the public projection: %#v", public)
	}
}

func TestKindOfClassifiesForeignErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want Kind
	}{
		{"plain error folds to the safest kind", errors.New("boom"), KindInternal},
		{"context cancellation", fmt.Errorf("read: %w", context.Canceled), KindCanceled},
		{"context deadline", fmt.Errorf("read: %w", context.DeadlineExceeded), KindDeadlineExceeded},
		{"contract error keeps its kind", NotFound("bot", nil), KindNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := KindOf(tc.err); got != tc.want {
				t.Fatalf("KindOf() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCodeOverridesKindStatusButNotKind(t *testing.T) {
	// acp.config_update_failed is a 502 in the catalog while its Kind folds to
	// KindUnavailable, whose default is 503. HTTP follows the catalog; gRPC and
	// JSON-RPC follow the Kind.
	err := New(CodeACPConfigUpdateFailed, nil)

	if got := ProblemOf(err, "").Status; got != http.StatusBadGateway {
		t.Fatalf("status = %d, want the catalog status %d", got, http.StatusBadGateway)
	}
	if got := KindOf(err); got != KindUnavailable {
		t.Fatalf("kind = %s, want %s", got, KindUnavailable)
	}
}

func TestWithCodePromotesAndIgnoresUnknownCodes(t *testing.T) {
	base := Conflict("create bot", errors.New("unique violation"))

	promoted := base.WithCode(CodeBotNameTaken, map[string]string{"field": "name"})
	if got := CodeOf(promoted); got != CodeBotNameTaken {
		t.Fatalf("CodeOf() = %q", got)
	}
	if got := ProblemOf(promoted, "").Args["field"]; got != "name" {
		t.Fatalf("args = %#v", ProblemOf(promoted, "").Args)
	}
	if CodeOf(base) != "" {
		t.Fatal("WithCode mutated the receiver instead of returning a copy")
	}

	// The catalog owns the Kind of a coded error: a handler that reaches for the
	// wrong base constructor must not be able to ship a 500 carrying a 409 code.
	mismatched := Internal("create bot", nil).WithCode(CodeBotNameTaken, nil)
	if got := KindOf(mismatched); got != KindConflict {
		t.Fatalf("kind = %s, want the catalog kind %s", got, KindConflict)
	}
	if got := ProblemOf(mismatched, "").Status; got != http.StatusConflict {
		t.Fatalf("status = %d, want %d", got, http.StatusConflict)
	}

	// A typo must degrade to the generic Kind rendering rather than produce a
	// contract with an empty identity.
	degraded := base.WithCode(Code("bot.nmae_taken"), nil)
	if CodeOf(degraded) != "" {
		t.Fatalf("unregistered code was accepted: %q", CodeOf(degraded))
	}
	if got := ProblemOf(degraded, "").Status; got != http.StatusConflict {
		t.Fatalf("degraded status = %d, want the Kind default %d", got, http.StatusConflict)
	}
}

func TestWithFieldsRendersValidationDetails(t *testing.T) {
	err := Invalid("update profile", nil).WithFields(
		FieldError{Pointer: "title_model", Code: FieldUnsupported},
	)

	problem := ProblemOf(err, "")
	if len(problem.Errors) != 1 {
		t.Fatalf("errors = %#v", problem.Errors)
	}
	// A bare field name is normalized into a JSON Pointer so that call sites
	// may pass either form.
	if problem.Errors[0].Pointer != "/title_model" {
		t.Fatalf("pointer = %q", problem.Errors[0].Pointer)
	}
}

// A rejected input is the most common client error in this codebase. It must
// stay localizable without minting a catalog code, which is what FieldCode is
// for.
func TestRequiredReplacesHandWrittenValidationSentences(t *testing.T) {
	err := Required("bot_id")

	problem := ProblemOf(err, "req")
	if problem.Status != http.StatusBadRequest {
		t.Fatalf("status = %d", problem.Status)
	}
	if problem.Code != "" {
		t.Fatalf("code = %q: a missing input must not need a catalog entry", problem.Code)
	}
	if len(problem.Errors) != 1 || problem.Errors[0].Pointer != "/bot_id" || problem.Errors[0].Code != FieldRequired {
		t.Fatalf("errors = %#v", problem.Errors)
	}
	if OpOf(err) != "bot_id" {
		t.Fatalf("OpOf() = %q", OpOf(err))
	}
}

func TestUnregisteredFieldCodeDegradesInsteadOfLeaking(t *testing.T) {
	err := Invalid("create bot", nil).WithFields(FieldError{Pointer: "/name", Code: FieldCode("wildly_specific")})

	fields := FieldsOf(err)
	if len(fields) != 1 || fields[0].Code != FieldInvalid {
		t.Fatalf("fields = %#v, want the unknown code folded to %q", fields, FieldInvalid)
	}
}

func TestErrorStringCarriesOpChainForLogs(t *testing.T) {
	err := Internal("archive directory", errors.New("no space left"))

	got := err.Error()
	if !strings.Contains(got, "archive directory") || !strings.Contains(got, "no space left") {
		t.Fatalf("Error() = %q, want the op chain and the cause", got)
	}
	if OpOf(err) != "archive directory" {
		t.Fatalf("OpOf() = %q", OpOf(err))
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
	if problem := ProblemOf(workspaceErr, "req-3"); len(problem.Args) != 0 {
		t.Fatalf("workspace args = %#v, want empty allowlisted metadata", problem.Args)
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

// TestNoConstructorLeaksItsCause is the package-level half of the leak gate:
// whatever a caller puts in a cause, no projection may echo it back.
func TestNoConstructorLeaksItsCause(t *testing.T) {
	const secret = "SECRET-c0ffee"
	constructors := map[string]func(string, error) *Error{
		"internal":            Internal,
		"invalid":             Invalid,
		"unauthenticated":     Unauthenticated,
		"forbidden":           Forbidden,
		"not_found":           NotFound,
		"conflict":            Conflict,
		"failed_precondition": FailedPrecondition,
		"exhausted":           Exhausted,
		"canceled":            Canceled,
		"deadline_exceeded":   DeadlineExceeded,
		"unavailable":         Unavailable,
		"unimplemented":       Unimplemented,
	}

	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			err := construct("op", errors.New(secret)).
				WithCode(CodeBotNameTaken, map[string]string{
					"field":             "name",
					"provider_response": secret,
				}).
				WithFields(FieldError{Pointer: "/name", Code: FieldTaken})

			if rendered := fmt.Sprintf("%#v", ProblemOf(err, "req")); strings.Contains(rendered, secret) {
				t.Fatalf("cause or unsanitized arg leaked: %s", rendered)
			}
		})
	}
}
