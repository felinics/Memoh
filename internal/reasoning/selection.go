package reasoning

import (
	"slices"
	"strings"
)

// NormalizeSelection validates a user-facing reasoning choice against the
// resolved options for one model and returns the canonical stored value.
// "off" and the legacy wire spelling "none" both store as EffortDisable.
func NormalizeSelection(selection string, opts Options) (string, bool) {
	selection = strings.ToLower(strings.TrimSpace(selection))
	if !opts.Supported || selection == "" {
		return "", false
	}
	if selection == "off" || IsDisabled(selection) {
		if opts.CanDisable {
			return EffortDisable, true
		}
		return "", false
	}
	if slices.Contains(opts.Efforts, selection) {
		return selection, true
	}
	return "", false
}
