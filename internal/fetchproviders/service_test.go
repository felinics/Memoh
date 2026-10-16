package fetchproviders

import (
	"context"
	"errors"
	"testing"
)

func TestCreateRejectsUnknownProvider(t *testing.T) {
	_, err := (&Service{}).Create(context.Background(), CreateRequest{Name: "x", Provider: "bogus"})
	if !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("errors.Is(%v, ErrInvalidProvider) = false", err)
	}
	if err.Error() != "invalid provider: bogus" {
		t.Fatalf("message changed: %q", err.Error())
	}
}
