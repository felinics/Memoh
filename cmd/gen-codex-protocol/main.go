// Command gen-codex-protocol regenerates the Go types for the codex
// app-server v2 protocol from the JSON Schema the local codex CLI exports.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external/codex/protocolgen"
)

func main() {
	out := flag.String("out", "internal/agent/runtime/external/codex/protocol", "output directory for generated files")
	flag.Parse()

	files, err := generate(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-codex-protocol:", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(*out, 0o750); err != nil {
		fmt.Fprintln(os.Stderr, "gen-codex-protocol:", err)
		os.Exit(1)
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(*out, name)
		if err := os.WriteFile(path, files[name], 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "gen-codex-protocol:", err)
			os.Exit(1)
		}
		fmt.Println("wrote", path)
	}

	// Remove stale generated files no longer produced.
	entries, err := os.ReadDir(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-codex-protocol:", err)
		os.Exit(1)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".gen.go") {
			continue
		}
		if _, ok := files[name]; !ok {
			path := filepath.Join(*out, name)
			if err := os.Remove(path); err != nil {
				fmt.Fprintln(os.Stderr, "gen-codex-protocol:", err)
				os.Exit(1)
			}
			fmt.Println("removed stale", path)
		}
	}
}

func generate(ctx context.Context) (map[string][]byte, error) {
	versionOutput, err := exec.CommandContext(ctx, "codex", "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("codex --version: %w", err)
	}
	fields := strings.Fields(string(versionOutput))
	if len(fields) != 2 {
		return nil, fmt.Errorf("unexpected codex --version output %q", versionOutput)
	}

	dir, err := os.MkdirTemp("", "codex-schema-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	export := exec.CommandContext(ctx, "codex", "app-server", "generate-json-schema", "--experimental", "--out", dir) //nolint:gosec // dir comes from MkdirTemp
	export.Stderr = os.Stderr
	if err := export.Run(); err != nil {
		return nil, fmt.Errorf("export schema: %w", err)
	}
	return protocolgen.Generate(os.DirFS(dir), fields[1])
}
