package assembly

import (
	"context"
	"testing"

	"github.com/memohai/memoh/domains/agent/chat/usage"
)

type assemblyUsageModelReader struct{}

func (assemblyUsageModelReader) GetModelProjections(context.Context, []string) (map[string]usage.ModelProjection, error) {
	return nil, nil
}

func TestNewUsageReader(t *testing.T) {
	t.Parallel()
	if got := NewUsageReader(nil, assemblyUsageModelReader{}); got == nil {
		t.Fatal("NewUsageReader() returned nil")
	}
}
