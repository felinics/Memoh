//go:build split

package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSplitDependencyClosureExcludesExternalChannelRuntime(t *testing.T) {
	command := exec.CommandContext(t.Context(), "go", "list", "-tags", "split", "-deps", ".")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list split dependency closure: %v", err)
	}
	dependencies := make(map[string]struct{})
	for dependency := range strings.Lines(string(output)) {
		dependencies[strings.TrimSpace(dependency)] = struct{}{}
	}

	forbidden := []string{
		"github.com/memohai/memoh/cmd/internal/channel/runtime",
		"github.com/memohai/memoh/domains/channel/adapter/catalog",
		"github.com/memohai/memoh/domains/channel/email/catalog",
		"github.com/memohai/memoh/domains/channel/http",
		"github.com/memohai/memoh/domains/channel/internal/adapter",
		"github.com/memohai/memoh/domains/channel/internal/email/generic",
		"github.com/memohai/memoh/domains/channel/internal/email/gmail",
		"github.com/memohai/memoh/domains/channel/internal/email/mailgun",
		"github.com/memohai/memoh/domains/channel/internal/webhook",
		"github.com/memohai/memoh/domains/channel/webhook/tunnel",
	}
	for _, packagePath := range forbidden {
		for dependency := range dependencies {
			if dependency == packagePath || strings.HasPrefix(dependency, packagePath+"/") {
				t.Errorf("split Server dependency closure contains external runtime package %s", dependency)
			}
		}
	}
}
