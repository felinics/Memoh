// Package userenv is the one definition of the workspace user's environment:
// where their home is, where the things they install land, and the command
// search path that finds them. Every process Memoh starts in a bot's own
// workspace starts from it — the browser terminal, the bot's exec tool, MCP
// servers, agent CLIs and the commands they run — so what a user sets up in
// one of them (a sign-in, a config file, an installed command) is there in
// all the others.
//
// Home is the data volume, the only place in a workspace that survives a
// rebuild. Programs keep their state under HOME, so a home anywhere else
// loses it and splits it from what Memoh's own processes see.
//
// PATH is not replaced: a workspace image keeps the search path it set up.
// The bridge puts DepsBinDir and BinDir ahead of it for every command, and a
// command run on the user's behalf also gets what the user's rc files add
// (bridge.ExecOptions.UserShellPath), as the browser terminal does. Path is
// only the starting point of an environment built from nothing.
//
// This applies to the bot's own workspace only. A connected computer keeps
// its owner's environment and never receives these values.
package userenv

import "github.com/felinics/memoh/internal/workspace/vpath"

// Home is the workspace user's home directory.
const Home = vpath.DataMount

// Directories under Home, following the XDG base directory layout that
// most command-line programs default to.
const (
	ConfigDir = Home + "/.config"
	DataDir   = Home + "/.local/share"
	CacheDir  = Home + "/.cache"
	// BinDir is where user-installed commands go: pip --user, uv tool,
	// npm -g (through NPMPrefix) and most native installers put them here.
	BinDir = Home + "/.local/bin"
	// NPMPrefix makes `npm install -g` install under Home, so a globally
	// installed command persists and lands in BinDir.
	NPMPrefix = Home + "/.local"
)

// DepsBinDir holds the shims of Memoh-managed workspace dependencies. A
// managed overlay of an image runtime (node, python, uv) must win over the
// toolkit copy, so it comes first on Path.
const DepsBinDir = Home + "/.memoh/deps/bin"

// ToolkitBinDir is the workspace image's toolkit of base tools.
const ToolkitBinDir = "/opt/memoh/toolkit/bin"

// systemPath is the conventional system command search path.
const systemPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Path is the command search path of an environment built from nothing (an
// agent CLI started on a clean environment): managed dependency shims, then
// the user's own commands, then the image toolkit and the system
// directories. A user-installed command therefore shadows an image copy of
// the same name, but not a Memoh-managed one. The default workspace image
// declares the same PATH.
const Path = DepsBinDir + ":" + BinDir + ":" + ToolkitBinDir + ":" + systemPath

// Names lists the variables Env sets. A launcher that composes its own
// environment treats them as owned: an inherited value is dropped so the
// contract's value is the one in effect.
var Names = []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "NPM_CONFIG_PREFIX", "PATH"}

// Env returns the workspace user environment for an environment built from
// nothing, PATH included, as KEY=value entries. Callers append their own
// entries after it, so an explicit entry still wins.
func Env() []string {
	return append(HomeEnv(), "PATH="+Path)
}

// HomeEnv is Env without PATH: what every command in the workspace gets on
// top of the PATH it already has.
func HomeEnv() []string {
	return []string{
		"HOME=" + Home,
		"XDG_CONFIG_HOME=" + ConfigDir,
		"XDG_DATA_HOME=" + DataDir,
		"XDG_CACHE_HOME=" + CacheDir,
		"NPM_CONFIG_PREFIX=" + NPMPrefix,
	}
}

// With returns Env followed by extra, so entries in extra take precedence:
// the starting environment of an agent CLI.
func With(extra ...string) []string {
	return append(Env(), extra...)
}
