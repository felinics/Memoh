package runtime

import (
	"testing"
)

func TestRuntimeConnectIsAnEmbeddedBackendPath(t *testing.T) {
	t.Parallel()
	if !isBackendPath("/runtimes/connect") {
		t.Fatal("Runtime WebSocket should be treated as a backend path")
	}
}
