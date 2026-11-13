package userenv

import (
	_ "embed"
	"strings"
)

// LegacyHomeScript adopts what the workspace user kept under a previous home
// (the image's /root before Home became the passwd home, or a sandbox user's
// own home) into Home, leaving symlinks behind so old absolute paths still
// resolve. Run it with `sh -s` from the workspace itself, before it serves
// commands; see the script for the rules. It is idempotent and never writes
// over anything already in Home.
//
//go:embed legacy_home.sh
var LegacyHomeScript string

// LegacyHomeActions are the decisions LegacyHomeScript reports, one
// "<action> <path>" line each, in log order.
var LegacyHomeActions = []string{"adopted", "linked", "copied", "kept", "skipped_no_space", "failed"}

// LegacyHomeDecisions groups LegacyHomeScript output lines by action.
func LegacyHomeDecisions(output string) map[string][]string {
	decisions := map[string][]string{}
	for _, line := range strings.Split(output, "\n") {
		action, rel, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || action == "" || rel == "" {
			continue
		}
		decisions[action] = append(decisions[action], rel)
	}
	return decisions
}
