package tools

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// guiSnapshotMaxBytes bounds the text a single observation returns to the
	// model; the rest is reachable through the cursor.
	guiSnapshotMaxBytes = 64 * 1024
	// guiSnapshotWalkLimit is how many nodes the backends are asked for; the
	// model's limit only sizes the page it reads.
	guiSnapshotWalkLimit = a11ySnapshotMaxLimit
)

// guiNode is one observed element in backend-neutral form, used for diffing
// between two observations of the same target and for paging.
type guiNode struct {
	// Key identifies the underlying element across observations: the
	// backend DOM node id on the web, bus name + object path on the desktop.
	Key  string
	Ref  string
	Line string
	// Fingerprint captures everything the line shows (role, name, value,
	// states, geometry) so an "updated" node is one whose line changed.
	Fingerprint string
}

// guiSnapshotBaseline is what a session remembers about its latest
// observation of one target and scope: enough to reuse refs, compute a diff,
// and page through the same observation with a cursor.
type guiSnapshotBaseline struct {
	Target     string // tab id or app id ("" for the whole desktop)
	Scope      string // scope ref, "" for the whole target
	SnapshotID string
	Taken      time.Time
	Nodes      []guiNode
	// Page is the text the observation presented (full or incremental),
	// split into lines, for cursor paging.
	Page []string
	// RefsByKey lets the web backend keep refs stable across observations.
	RefsByKey map[string]string
	MaxRef    int
}

// guiSnapshotOptions are the observation parameters shared by both backends.
type guiSnapshotOptions struct {
	Limit          int
	ScopeRef       string
	Cursor         string
	DisableDiffing bool
}

func guiSnapshotOptionsFrom(args map[string]any) (guiSnapshotOptions, error) {
	opts := guiSnapshotOptions{Limit: a11ySnapshotDefaultLimit}
	if limit, ok, err := IntArg(args, "limit"); err != nil {
		return opts, err
	} else if ok && limit > 0 {
		opts.Limit = limit
	}
	opts.ScopeRef = normalizeBrowserRef(StringArg(args, "scope_ref"))
	opts.Cursor = StringArg(args, "cursor")
	if v, ok, err := BoolArg(args, "disable_diffing"); err != nil {
		return opts, err
	} else if ok {
		opts.DisableDiffing = v
	}
	if opts.Cursor != "" && (opts.ScopeRef != "" || opts.DisableDiffing) {
		return opts, errors.New("cursor continues the previous snapshot and cannot be combined with scope_ref or disable_diffing")
	}
	return opts, nil
}

// guiDiff describes how an observation differs from the session's baseline.
type guiDiff struct {
	Added     []guiNode
	Updated   []guiNode
	Removed   []string // refs
	Unchanged int
}

// diffNodes compares the new nodes against the baseline by element key.
func diffNodes(baseline *guiSnapshotBaseline, nodes []guiNode) guiDiff {
	var diff guiDiff
	previous := make(map[string]guiNode, len(baseline.Nodes))
	for _, n := range baseline.Nodes {
		previous[n.Key] = n
	}
	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		seen[n.Key] = true
		old, ok := previous[n.Key]
		switch {
		case !ok:
			diff.Added = append(diff.Added, n)
		case old.Fingerprint != n.Fingerprint || old.Ref != n.Ref:
			diff.Updated = append(diff.Updated, n)
		default:
			diff.Unchanged++
		}
	}
	for _, n := range baseline.Nodes {
		if !seen[n.Key] {
			diff.Removed = append(diff.Removed, n.Ref)
		}
	}
	sort.Strings(diff.Removed)
	return diff
}

// guiSnapshotPage is the paged, possibly incremental text of an observation
// plus the bookkeeping the model needs to continue or trust it.
type guiSnapshotPage struct {
	Lines           []string
	NextCursor      string
	Mode            string // "full" or "incremental"
	Added           int
	Updated         int
	RemovedRefs     []string
	Unchanged       int
	Truncated       bool
	TruncatedReason string
	BaselineReason  string
}

// presentSnapshot turns nodes into the page the model reads. With a usable
// baseline for the same target and scope and diffing enabled it returns only
// added/updated lines (marked "+ " and "~ ") plus the removed refs; otherwise
// the full listing, saying why. It records the new baseline on the session
// state for target at scope, so a subtree observation diffs against the
// previous observation of that subtree and leaves the whole-target baseline
// untouched.
func presentSnapshot(state *guiSessionState, target, scope, snapshotID string, nodes []guiNode, refsByKey map[string]string, maxRef int, walkTruncated bool, opts guiSnapshotOptions) guiSnapshotPage {
	baseline := state.snapshotBaseline(target, scope)
	page := guiSnapshotPage{Mode: "full"}
	var lines []string
	switch {
	case opts.DisableDiffing:
		page.BaselineReason = "diffing disabled"
	case baseline == nil && scope != "":
		page.BaselineReason = "no previous snapshot of this subtree in this conversation"
	case baseline == nil:
		page.BaselineReason = "no previous snapshot of this target in this conversation"
	case baseline.Target != target || baseline.Scope != scope:
		page.BaselineReason = "target or scope changed since the previous snapshot"
	default:
		diff := diffNodes(baseline, nodes)
		page.Mode = "incremental"
		page.Added, page.Updated, page.Unchanged = len(diff.Added), len(diff.Updated), diff.Unchanged
		page.RemovedRefs = diff.Removed
		for _, n := range nodes {
			for _, a := range diff.Added {
				if a.Key == n.Key {
					lines = append(lines, "+ "+strings.TrimPrefix(n.Line, "- "))
				}
			}
			for _, u := range diff.Updated {
				if u.Key == n.Key {
					lines = append(lines, "~ "+strings.TrimPrefix(n.Line, "- "))
				}
			}
		}
	}
	if page.Mode == "full" {
		lines = make([]string, 0, len(nodes))
		for _, n := range nodes {
			lines = append(lines, n.Line)
		}
	}
	state.setSnapshotBaseline(target, scope, &guiSnapshotBaseline{
		Target:     target,
		Scope:      scope,
		SnapshotID: snapshotID,
		Taken:      time.Now(),
		Nodes:      nodes,
		Page:       lines,
		RefsByKey:  refsByKey,
		MaxRef:     maxRef,
	})
	page.Lines, page.NextCursor, page.Truncated, page.TruncatedReason = pageLines(lines, 0, opts.Limit)
	if walkTruncated {
		page.Truncated = true
		if page.TruncatedReason == "" {
			page.TruncatedReason = "the backend traversal budget was exhausted; use scope_ref to observe a subtree"
		} else {
			page.TruncatedReason += "; the backend traversal budget was also exhausted"
		}
	}
	return page
}

// continueSnapshot serves the next page of the session's latest observation
// of target (whatever scope it had) at cursor.
func continueSnapshot(state *guiSessionState, target, snapshotID, cursor string, limit int) (guiSnapshotPage, *guiSnapshotBaseline, error) {
	baseline := state.latestBaseline(target)
	if baseline == nil {
		return guiSnapshotPage{}, nil, fmt.Errorf("cursor %q does not belong to a snapshot in this conversation; take a new snapshot", cursor)
	}
	if snapshotID != "" && snapshotID != baseline.SnapshotID {
		return guiSnapshotPage{}, nil, fmt.Errorf("cursor %q belongs to snapshot %s, not %s; take a new snapshot", cursor, baseline.SnapshotID, snapshotID)
	}
	offset, err := strconv.Atoi(cursor)
	if err != nil || offset < 0 || offset > len(baseline.Page) {
		return guiSnapshotPage{}, nil, fmt.Errorf("cursor %q is not a position in snapshot %s", cursor, baseline.SnapshotID)
	}
	page := guiSnapshotPage{Mode: "continued"}
	page.Lines, page.NextCursor, page.Truncated, page.TruncatedReason = pageLines(baseline.Page, offset, limit)
	return page, baseline, nil
}

// pageLines slices lines from offset, honouring both the line limit and the
// byte budget, and returns the cursor for the remainder.
func pageLines(lines []string, offset, limit int) (out []string, nextCursor string, truncated bool, reason string) {
	if limit <= 0 {
		limit = a11ySnapshotDefaultLimit
	}
	bytes := 0
	i := offset
	for ; i < len(lines) && len(out) < limit; i++ {
		if bytes+len(lines[i])+1 > guiSnapshotMaxBytes && len(out) > 0 {
			reason = fmt.Sprintf("output byte budget (%d bytes) reached", guiSnapshotMaxBytes)
			break
		}
		out = append(out, lines[i])
		bytes += len(lines[i]) + 1
	}
	if i < len(lines) {
		nextCursor = strconv.Itoa(i)
		truncated = true
		if reason == "" {
			reason = fmt.Sprintf("limit %d reached; %d more lines remain", limit, len(lines)-i)
		}
	}
	return out, nextCursor, truncated, reason
}

// snapshotText joins page lines, or explains an empty page.
func (p guiSnapshotPage) text() string {
	if len(p.Lines) == 0 {
		switch p.Mode {
		case "incremental":
			if len(p.RemovedRefs) > 0 {
				return "(no added or updated elements; removed: " + strings.Join(p.RemovedRefs, ", ") + ")"
			}
			return "(no changes since the previous snapshot)"
		default:
			return "(no on-screen elements)"
		}
	}
	return strings.Join(p.Lines, "\n")
}

// result renders the page as the shared part of an observation result.
func (p guiSnapshotPage) result() map[string]any {
	out := map[string]any{
		"snapshot":  p.text(),
		"mode":      p.Mode,
		"truncated": p.Truncated,
	}
	if p.NextCursor != "" {
		out["next_cursor"] = p.NextCursor
	}
	if p.TruncatedReason != "" {
		out["truncated_reason"] = p.TruncatedReason
	}
	if p.BaselineReason != "" {
		out["full_because"] = p.BaselineReason
	}
	if p.Mode == "incremental" {
		out["added"] = p.Added
		out["updated"] = p.Updated
		out["unchanged"] = p.Unchanged
		out["removed_refs"] = p.RemovedRefs
	}
	return out
}
