package reasoning

import "testing"

func TestNormalizeSelection(t *testing.T) {
	t.Parallel()

	canDisable := Options{
		Supported:     true,
		CanDisable:    true,
		Efforts:       []string{EffortLow, EffortHigh},
		DefaultEffort: EffortLow,
	}
	cannotDisable := canDisable
	cannotDisable.CanDisable = false

	tests := []struct {
		name      string
		selection string
		opts      Options
		want      string
		wantOK    bool
	}{
		{name: "active tier", selection: " HIGH ", opts: canDisable, want: EffortHigh, wantOK: true},
		{name: "off label", selection: "off", opts: canDisable, want: EffortDisable, wantOK: true},
		{name: "canonical off", selection: EffortDisable, opts: canDisable, want: EffortDisable, wantOK: true},
		{name: "legacy off", selection: EffortNone, opts: canDisable, want: EffortDisable, wantOK: true},
		{name: "off unavailable", selection: "off", opts: cannotDisable},
		{name: "unadvertised tier", selection: EffortXHigh, opts: canDisable},
		{name: "unsupported model", selection: EffortLow, opts: Options{}},
		{name: "blank", selection: "  ", opts: canDisable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := NormalizeSelection(tt.selection, tt.opts)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("NormalizeSelection(%q) = (%q, %t), want (%q, %t)", tt.selection, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
