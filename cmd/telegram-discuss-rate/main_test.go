package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunReportsDeprecation(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	if code := run(&stderr); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	message := stderr.String()
	for _, want := range []string{"已弃用", "Web", "Telegram"} {
		if !strings.Contains(message, want) {
			t.Fatalf("deprecation message %q does not contain %q", message, want)
		}
	}
}
