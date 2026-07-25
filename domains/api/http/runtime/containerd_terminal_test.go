package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestDetectShellLaunchesBashWithShFallback(t *testing.T) {
	got := detectShell(context.Background(), nil)
	for _, want := range []string{
		"exec /bin/bash",
		"exec /usr/bin/bash",
		"exec bash",
		"exec /bin/sh",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("detectShell() = %q, want it to contain %q", got, want)
		}
	}
}
