// Package templates holds the built-in files seeded into every agent
// workspace on first boot: the AGENTS.md persona scaffold, memory notes, and
// the default .memoh directory (hooks, built-in skills).
//
// The canonical copy lives in the workspace/ subdirectory. The Server embeds
// this package and applies it through the provider-neutral workspace bootstrap
// filesystem. Container workspaces currently use a bridge-backed adapter;
// providers such as E2B can use their native filesystem APIs instead.
package templates

import (
	"embed"
	"io/fs"
)

// The all: prefix is load-bearing: without it the hidden .memoh directory
// (hooks.json and built-in skills) is silently excluded from the embed.
//
//go:embed all:workspace
var workspaceFS embed.FS

// WorkspaceFS returns the workspace bootstrap template tree, rooted at the
// template contents (AGENTS.md, .memoh/, ...). Callers seeding a workspace
// should skip .gitkeep placeholder files. User-owned files are create-only;
// built-in files under .memoh/skills are managed and may be refreshed.
func WorkspaceFS() fs.FS {
	sub, err := fs.Sub(workspaceFS, "workspace")
	if err != nil {
		panic("templates: workspace subtree missing from embed: " + err.Error())
	}
	return sub
}
