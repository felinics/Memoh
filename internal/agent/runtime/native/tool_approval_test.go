package native

import (
	"testing"

	"github.com/felinics/memoh/internal/agent/toolexec"
)

func TestMarkApprovalToolsCoversWorkspaceTools(t *testing.T) {
	t.Parallel()

	tools := map[string]toolexec.Tool{}
	for _, tool := range markApprovalTools([]toolexec.Tool{
		{Name: "read"},
		{Name: "list"},
		{Name: "write"},
		{Name: "edit"},
		{Name: "apply_patch"},
		{Name: "exec"},
		{Name: "web_search"},
	}) {
		tools[tool.Name] = tool
	}

	for _, name := range []string{"read", "list", "write", "edit", "apply_patch", "exec"} {
		if !tools[name].RequireApproval {
			t.Fatalf("%s RequireApproval = false, want true", name)
		}
	}
	for _, name := range []string{"web_search"} {
		if tools[name].RequireApproval {
			t.Fatalf("%s RequireApproval = true, want false", name)
		}
	}
}
