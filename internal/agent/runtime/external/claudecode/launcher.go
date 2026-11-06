package claudecode

// dependencyID is the workspace dependency catalog id that provisions the
// Claude Code CLI. No version is declared: the dependency manager installs
// what the user asks for, and a process that reports a CLI drifted from
// PinnedCLIVersion logs a warning when it starts.
const dependencyID = "claude-code"
