// Command gen-codex-protocol regenerates the Go types for the codex
// app-server v2 protocol from the vendored JSON Schema snapshot in
// internal/agent/runtime/codex/protocolgen/schema.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocolgen"
)

func main() {
	out := flag.String("out", "internal/agent/runtime/codex/protocol", "output directory for generated files")
	flag.Parse()

	files, err := protocolgen.Generate()
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
