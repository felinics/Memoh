package runtime

import (
	"fmt"
	"strings"
	"testing"
)

func TestModuleNames(t *testing.T) {
	tests := []struct {
		name       string
		moduleName string
		option     fmt.Stringer
	}{
		{name: "runtime", moduleName: runtimeModuleName, option: Module()},
		{name: "embedded", moduleName: embeddedModuleName, option: EmbeddedModule()},
		{name: "lifecycle", moduleName: lifecycleModuleName, option: LifecycleModule()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := fmt.Sprintf("fx.Module(%q,", tt.moduleName)
			if got := tt.option.String(); !strings.HasPrefix(got, want) {
				t.Fatalf("module option = %q, want prefix %q", got, want)
			}
		})
	}
}
