package application

import "testing"

func TestParseLoopDetectionEnabledFromMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
		expected bool
	}{
		{
			name:     "empty payload defaults to false",
			metadata: nil,
			expected: false,
		},
		{
			name:     "missing nested path defaults to false",
			metadata: map[string]any{"features": map[string]any{}},
			expected: false,
		},
		{
			name:     "explicit false",
			metadata: map[string]any{"features": map[string]any{"loop_detection": map[string]any{"enabled": false}}},
			expected: false,
		},
		{
			name:     "explicit true",
			metadata: map[string]any{"features": map[string]any{"loop_detection": map[string]any{"enabled": true}}},
			expected: true,
		},
		{
			name:     "non-boolean value defaults to false",
			metadata: map[string]any{"features": map[string]any{"loop_detection": map[string]any{"enabled": "true"}}},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loopDetectionEnabled(tt.metadata)
			if got != tt.expected {
				t.Fatalf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}
