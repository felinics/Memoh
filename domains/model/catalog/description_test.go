package catalog

import (
	"testing"

	modeldomain "github.com/memohai/memoh/domains/model"
)

func stringPointer(value string) *string { return &value }

func TestNormalizeModelConfigDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   *string
		want *string
	}{
		{name: "missing remains missing"},
		{name: "value is trimmed", in: stringPointer("  General purpose model.  "), want: stringPointer("General purpose model.")},
		{name: "explicit empty remains present", in: stringPointer("   "), want: stringPointer("")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := normalizeModelConfig(modeldomain.ModelConfig{Description: tt.in})
			if tt.want == nil {
				if cfg.Description != nil {
					t.Fatalf("description = %q, want nil", *cfg.Description)
				}
				return
			}
			if cfg.Description == nil || *cfg.Description != *tt.want {
				t.Fatalf("description = %v, want %q", cfg.Description, *tt.want)
			}
		})
	}
}
