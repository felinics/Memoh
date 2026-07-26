package assembly

import "testing"

func TestNewDiscussDriverReturnsInboundContract(t *testing.T) {
	driver := NewDiscussDriver(nil, nil, nil, nil, nil, nil)
	if driver == nil {
		t.Fatal("NewDiscussDriver returned nil")
	}
	if err := driver.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}
