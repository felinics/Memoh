package apperror

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
)

// allKinds is the closed enum the contract freezes. A new Kind must be added
// here and to docs/architecture/error-contract-decision.md together.
var allKinds = []Kind{
	KindInternal,
	KindInvalid,
	KindUnauthenticated,
	KindForbidden,
	KindNotFound,
	KindConflict,
	KindFailedPrecondition,
	KindExhausted,
	KindCanceled,
	KindDeadlineExceeded,
	KindUnavailable,
	KindUnimplemented,
}

func TestEveryKindProjectsOntoEveryTransport(t *testing.T) {
	if len(kindProfiles) != len(allKinds) {
		t.Fatalf("kindProfiles has %d entries, want %d", len(kindProfiles), len(allKinds))
	}

	names := map[string]Kind{}
	statuses := map[int]Kind{}
	grpcCodes := map[codes.Code]Kind{}
	jsonRPCCodes := map[int]Kind{}

	for _, kind := range allKinds {
		if _, ok := kindProfiles[kind]; !ok {
			t.Fatalf("kind %d has no profile", kind)
		}
		if kind.Title() == "" || kind.Detail() == "" {
			t.Fatalf("kind %s has no user-facing text", kind)
		}
		for label, collision := range map[string]bool{
			"name":     assign(names, kind.String(), kind),
			"http":     assign(statuses, kind.HTTPStatus(), kind),
			"grpc":     assign(grpcCodes, kind.GRPCCode(), kind),
			"json_rpc": assign(jsonRPCCodes, kind.JSONRPCCode(), kind),
		} {
			if collision {
				t.Fatalf("kind %s collides with another kind on %s", kind, label)
			}
		}
	}
}

func assign[K comparable](seen map[K]Kind, key K, kind Kind) bool {
	if _, taken := seen[key]; taken {
		return true
	}
	seen[key] = kind
	return false
}

// TestKindNamesAreFrozen locks the one Kind projection that is not a number:
// the name reaches clients through errors.kind.<name> localization, so
// renaming a Kind silently degrades every localized message to English.
func TestKindNamesAreFrozen(t *testing.T) {
	frozen := map[Kind]string{
		KindInternal:           "internal",
		KindInvalid:            "invalid",
		KindUnauthenticated:    "unauthenticated",
		KindForbidden:          "forbidden",
		KindNotFound:           "not_found",
		KindConflict:           "conflict",
		KindFailedPrecondition: "failed_precondition",
		KindExhausted:          "exhausted",
		KindCanceled:           "canceled",
		KindDeadlineExceeded:   "deadline_exceeded",
		KindUnavailable:        "unavailable",
		KindUnimplemented:      "unimplemented",
	}
	for kind, want := range frozen {
		if got := kind.String(); got != want {
			t.Errorf("kind name changed: %q, want %q", got, want)
		}
	}
	if len(frozen) != len(allKinds) {
		t.Fatalf("frozen names cover %d kinds, want %d", len(frozen), len(allKinds))
	}
}

func TestUnknownKindDegradesToInternal(t *testing.T) {
	rogue := Kind(200)
	if rogue.HTTPStatus() != http.StatusInternalServerError {
		t.Fatalf("unknown kind status = %d", rogue.HTTPStatus())
	}
	if rogue.String() != KindInternal.String() {
		t.Fatalf("unknown kind name = %q", rogue.String())
	}
}

func TestKindFromHTTPStatusIsTotal(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   Kind
	}{
		{http.StatusBadRequest, KindInvalid},
		{http.StatusUnauthorized, KindUnauthenticated},
		{http.StatusForbidden, KindForbidden},
		{http.StatusNotFound, KindNotFound},
		{http.StatusConflict, KindConflict},
		{http.StatusUnprocessableEntity, KindFailedPrecondition},
		{http.StatusTooManyRequests, KindExhausted},
		{http.StatusInternalServerError, KindInternal},
		{http.StatusNotImplemented, KindUnimplemented},
		{http.StatusServiceUnavailable, KindUnavailable},
		{http.StatusGatewayTimeout, KindDeadlineExceeded},
		// 502 folds into unavailable on purpose; those endpoints answer 503.
		{http.StatusBadGateway, KindUnavailable},
		// Unmapped statuses fold by class, never upward into a client error.
		{http.StatusTeapot, KindInvalid},
		{http.StatusInsufficientStorage, KindInternal},
		{http.StatusOK, KindInternal},
	} {
		if got := KindFromHTTPStatus(tc.status); got != tc.want {
			t.Errorf("KindFromHTTPStatus(%d) = %s, want %s", tc.status, got, tc.want)
		}
	}
}

func TestKindFromGRPCCodeCoversEveryCanonicalCode(t *testing.T) {
	// codes.Unauthenticated is the highest canonical code; iterating up to it
	// fails the moment gRPC introduces one this project has not folded.
	for code := codes.OK; code <= codes.Unauthenticated; code++ {
		if code == codes.OK {
			continue
		}
		if _, ok := grpcCodeKinds[code]; !ok {
			t.Errorf("canonical gRPC code %s has no Kind", code)
		}
	}
	if got := KindFromGRPCCode(codes.Aborted); got != KindConflict {
		t.Errorf("ABORTED folded to %s, want %s", got, KindConflict)
	}
	if got := KindFromGRPCCode(codes.Code(999)); got != KindInternal {
		t.Errorf("unknown gRPC code folded to %s, want %s", got, KindInternal)
	}
}

// TestCatalogStatusesAreKnownToTheContract keeps the catalog from introducing
// a status the Kind table cannot classify, which would silently render the
// gRPC and JSON-RPC projections as internal errors.
func TestCatalogStatusesAreKnownToTheContract(t *testing.T) {
	for _, code := range Codes() {
		definition, _ := Lookup(code)
		if _, ok := httpStatusKinds[definition.HTTPStatus]; !ok {
			t.Errorf("catalog code %q uses status %d, which is not in the contract table", code, definition.HTTPStatus)
		}
	}
}
