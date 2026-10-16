package server

import (
	"errors"
	"net/http"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
)

func TestProblemFromUsesCatalogAndDoesNotExposeCause(t *testing.T) {
	err := apperror.Wrap(apperror.CodeWorkspaceUnreachable, errors.New("secret runtime detail"), nil)
	problem, ok := ProblemFrom(err, "req-1")
	if !ok {
		t.Fatal("ProblemFrom() did not recognize AppError")
	}
	if problem.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", problem.Status, http.StatusServiceUnavailable)
	}
	if problem.Type != "urn:memoh:error:workspace.unreachable" {
		t.Fatalf("type = %q", problem.Type)
	}
	if problem.Detail != "The workspace could not be reached." {
		t.Fatalf("detail = %q", problem.Detail)
	}
	if problem.RequestID != "req-1" {
		t.Fatalf("request_id = %q", problem.RequestID)
	}
}
