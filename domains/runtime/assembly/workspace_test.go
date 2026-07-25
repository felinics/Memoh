package assembly

import "testing"

func TestNewWorkspaceRequiresContainerAndPostgres(t *testing.T) {
	if _, _, err := NewWorkspace(WorkspaceDeps{}); err == nil {
		t.Fatal("expected container required error")
	}
}
