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

// Path is the command search path every workspace process starts from:
// managed dependency shims, then the user's own commands, then the image
// toolkit and the system directories. A user-installed command therefore
// shadows an image copy of the same name, but not a Memoh-managed one.
const Path = DepsBinDir + ":" + BinDir + ":" + ToolkitBinDir + ":" + systemPath

// Names lists the variables Env sets. A launcher that composes its own
// environment treats them as owned: an inherited value is dropped so the
// contract's value is the one in effect.
var Names = []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "NPM_CONFIG_PREFIX", "PATH"}

// Env returns the workspace user environment as KEY=value entries. Callers
// append their own entries after it, so an explicit entry still wins.
func Env() []string {
	return append(HomeEnv(), "PATH="+Path)
}

// HomeEnv is Env without PATH, for a launcher that derives its own search
// path from Path (for example by asking the user's shell).
func HomeEnv() []string {
	return []string{
		"HOME=" + Home,
		"XDG_CONFIG_HOME=" + ConfigDir,
		"XDG_DATA_HOME=" + DataDir,
		"XDG_CACHE_HOME=" + CacheDir,
		"NPM_CONFIG_PREFIX=" + NPMPrefix,
	}
}

// With returns Env followed by extra, so entries in extra take precedence.
func With(extra ...string) []string {
	return append(Env(), extra...)
}
