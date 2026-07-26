package channel

import (
	"fmt"
	"strings"
	"testing"

	"go.uber.org/fx"
)

func TestModuleNames(t *testing.T) {
	tests := []struct {
		name       string
		moduleName string
		option     fx.Option
	}{
		{name: "foundation", moduleName: foundationModuleName, option: FoundationModule()},
		{name: "local foundation", moduleName: foundationModuleName, option: LocalFoundationModule()},
		{name: "server local", moduleName: serverLocalModuleName, option: ServerLocalModule()},
		{name: "gateway", moduleName: gatewayModuleName, option: GatewayModule()},
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
