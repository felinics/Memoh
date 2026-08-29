package client

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// newSessionCandidateLease builds the minimal lease + fake bridge needed to
// exercise listSessionFileCandidates against the bounded ListDir contract.
func newSessionCandidateLease(t *testing.T, roots ...string) (*runtimeLease, *recordingBridgeServer) {
	t.Helper()
	bridgeClient, server := newRecordingBridgeClient(t)
	const root = "/data/acp/limit-test"
	server.mu.Lock()
	server.fs["/data/acp"] = recordingFSNode{isDir: true, modTime: time.Now()}
	server.fs[root] = recordingFSNode{isDir: true, modTime: time.Now()}
	for _, sessionRoot := range roots {
		current := root
		for part := range strings.SplitSeq(sessionRoot, "/") {
			current += "/" + part
			server.fs[current] = recordingFSNode{isDir: true, modTime: time.Now()}
		}
	}
	server.mu.Unlock()
	return &runtimeLease{client: bridgeClient, root: root, agentID: "codex", sessionRoots: roots}, server
}

func addSessionCandidateFiles(server *recordingBridgeServer, leaseRoot, sessionRoot string, count int) {
	server.mu.Lock()
	defer server.mu.Unlock()
	for index := range count {
		server.fs[fmt.Sprintf("%s/%s/f%05d.jsonl", leaseRoot, sessionRoot, index)] = recordingFSNode{
			content: []byte("{}\n"), modTime: time.Now(),
		}
	}
}

func TestListSessionFileCandidatesAccumulatesAcrossRoots(t *testing.T) {
	t.Parallel()
	lease, server := newSessionCandidateLease(t, "state/sessions", "state/extra")
	addSessionCandidateFiles(server, lease.root, "state/sessions", 3)
	addSessionCandidateFiles(server, lease.root, "state/extra", 2)

	candidates, err := lease.listSessionFileCandidates(context.Background())
	if err != nil {
		t.Fatalf("listSessionFileCandidates() error = %v", err)
	}
	if len(candidates) != 5 {
		t.Fatalf("candidates = %d, want 5 across both roots", len(candidates))
	}
	for _, candidate := range candidates {
		if !strings.HasPrefix(candidate.path, "state/") || candidate.size != 3 {
			t.Fatalf("candidate = %#v", candidate)
		}
	}
}

func TestListSessionFileCandidatesRejectsSingleRootOverEntryCap(t *testing.T) {
	t.Parallel()
	lease, server := newSessionCandidateLease(t, "state/sessions")
	// One more entry than the whole-session cap: the bridge returns exactly
	// cap+1 entries (the request asks for remaining+1), and the client side
	// converts that overshoot into a refusal.
	addSessionCandidateFiles(server, lease.root, "state/sessions", runtimeSessionMaxEntries+1)

	_, err := lease.listSessionFileCandidates(context.Background())
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("more than %d entries", runtimeSessionMaxEntries)) {
		t.Fatalf("over-cap listing error = %v, want the entry-cap refusal", err)
	}
}

func TestListSessionFileCandidatesEnforcesCumulativeBudgetAtTheBridge(t *testing.T) {
	t.Parallel()
	lease, server := newSessionCandidateLease(t, "state/sessions", "state/extra")
	// The first root consumes the entire budget; the second root's listing is
	// requested with a bound of one entry, so the bridge itself refuses with
	// ResourceExhausted instead of shipping the tree to the client.
	addSessionCandidateFiles(server, lease.root, "state/sessions", runtimeSessionMaxEntries)
	addSessionCandidateFiles(server, lease.root, "state/extra", 2)

	_, err := lease.listSessionFileCandidates(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ResourceExhausted") {
		t.Fatalf("cumulative over-budget listing error = %v, want the bridge's ResourceExhausted refusal", err)
	}
	if !strings.Contains(err.Error(), `"state/extra"`) {
		t.Fatalf("error %v does not name the root that exceeded the budget", err)
	}
}

func TestListSessionFileCandidatesExactlyAtEntryCapSucceeds(t *testing.T) {
	t.Parallel()
	lease, server := newSessionCandidateLease(t, "state/sessions")
	addSessionCandidateFiles(server, lease.root, "state/sessions", runtimeSessionMaxEntries)

	candidates, err := lease.listSessionFileCandidates(context.Background())
	if err != nil {
		t.Fatalf("listSessionFileCandidates() at the cap: %v", err)
	}
	if len(candidates) != runtimeSessionMaxEntries {
		t.Fatalf("candidates = %d, want the full cap %d", len(candidates), runtimeSessionMaxEntries)
	}
}
