package core

import (
	"fmt"
	"strings"
	"testing"

	"go.uber.org/fx"
)

func TestModuleNames(t *testing.T) {
	tests := []struct {
		name   string
		option fx.Option
	}{
		{name: foundationModuleName, option: FoundationModule()},
		{name: serverModuleName, option: ServerModule()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := fmt.Sprintf("fx.Module(%q,", tt.name)
			if got := tt.option.String(); !strings.HasPrefix(got, want) {
				t.Fatalf("module option = %q, want prefix %q", got, want)
			}
		})
	}
}
