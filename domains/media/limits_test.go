package media

import (
	"bytes"
	"errors"
	"testing"
)

func TestReadAllWithLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   []byte
		maxBytes  int64
		wantErr   bool
		errTooBig bool
	}{
		{
			name:     "within limit",
			payload:  []byte("hello"),
			maxBytes: 8,
		},
		{
			name:      "over limit",
			payload:   []byte("0123456789"),
			maxBytes:  5,
			wantErr:   true,
			errTooBig: true,
		},
		{
			name:     "exact limit",
			payload:  []byte("12345"),
			maxBytes: 5,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadAllWithLimit(bytes.NewReader(tt.payload), tt.maxBytes)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				if tt.errTooBig && !errors.Is(err, ErrAssetTooLarge) {
					t.Fatalf("expected ErrAssetTooLarge, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != string(tt.payload) {
				t.Fatalf("unexpected payload: %q", string(got))
			}
		})
	}
}
