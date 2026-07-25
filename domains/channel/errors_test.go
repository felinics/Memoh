package channel

import (
	"errors"
	"testing"
)

func TestDomainError(t *testing.T) {
	err := NewDomainError(ErrConfigNotFound, ErrorDetail{Reason: ErrorReasonConfigNotFound, ResourceID: "cfg-1"})
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("error = %v", err)
	}
	var domain *DomainError
	if !errors.As(err, &domain) || domain.Detail.ResourceID != "cfg-1" {
		t.Fatalf("domain error = %#v", domain)
	}
}

func TestDomainErrorDefaultsToUnknown(t *testing.T) {
	err := NewDomainError(nil, ErrorDetail{})
	if !errors.Is(err, ErrUnknown) || err.Error() != ErrUnknown.Error() {
		t.Fatalf("error = %v", err)
	}
}
