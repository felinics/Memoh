package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// deadSpoolPID is far above any real pid space (Linux pid_max caps at 2^22,
// Darwin at 99999), so probing it always reports "no such process".
const deadSpoolPID = 1 << 30

func TestSpoolOwnerPIDParsing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		wantPID int
		wantOK  bool
	}{
		{runtimeSessionSpoolPrefix + "123-abcdef", 123, true},
		{runtimeSessionSpoolPrefix + "legacy", 0, false},
		{runtimeSessionSpoolPrefix + "-abcdef", 0, false},
		{runtimeSessionSpoolPrefix + "0-abcdef", 0, false},
		{runtimeSessionSpoolPrefix + "notapid-abcdef", 0, false},
		{runtimeSessionSpoolPrefix + "123", 0, false},
	}
	for _, tc := range cases {
		pid, ok := spoolOwnerPID(tc.name)
		if pid != tc.wantPID || ok != tc.wantOK {
			t.Errorf("spoolOwnerPID(%q) = (%d, %v), want (%d, %v)", tc.name, pid, ok, tc.wantPID, tc.wantOK)
		}
	}
}

func TestCleanupStaleSessionSpoolsOwnershipPrecedence(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	now := time.Now()
	old := now.Add(-runtimeSessionSpoolStaleAge - time.Hour)

	aliveFresh := filepath.Join(tempDir, fmt.Sprintf("%s%d-alive-fresh", runtimeSessionSpoolPrefix, os.Getpid()))
	aliveOld := filepath.Join(tempDir, fmt.Sprintf("%s%d-alive-old", runtimeSessionSpoolPrefix, os.Getpid()))
	deadFresh := filepath.Join(tempDir, fmt.Sprintf("%s%d-dead-fresh", runtimeSessionSpoolPrefix, deadSpoolPID))
	deadOld := filepath.Join(tempDir, fmt.Sprintf("%s%d-dead-old", runtimeSessionSpoolPrefix, deadSpoolPID))
	legacyFresh := filepath.Join(tempDir, runtimeSessionSpoolPrefix+"legacy-fresh")
	legacyOld := filepath.Join(tempDir, runtimeSessionSpoolPrefix+"legacy-old")
	for _, dir := range []string{aliveFresh, aliveOld, deadFresh, deadOld, legacyFresh, legacyOld} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{aliveOld, deadOld, legacyOld} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}

	cleanupStaleSessionSpools(tempDir, now)

	// A dead owner's spool is reclaimed immediately regardless of age; live
	// owners and legacy names are protected while fresh but yield to the age
	// backstop, which also covers recycled pids that probe as alive.
	for _, dir := range []string{aliveFresh, legacyFresh} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("cleanup removed a protected spool %q: %v", dir, err)
		}
	}
	for _, dir := range []string{aliveOld, deadFresh, deadOld, legacyOld} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("cleanup kept a reclaimable spool %q: %v", dir, err)
		}
	}
}

func TestSpoolProcessAliveProbes(t *testing.T) {
	t.Parallel()
	if !spoolProcessAlive(os.Getpid()) {
		t.Error("own process reported dead")
	}
	if spoolProcessAlive(deadSpoolPID) {
		t.Error("impossible pid reported alive")
	}
	// pid 1 exists but is not signalable by an unprivileged test (EPERM);
	// cleanup must treat it as alive rather than delete another owner's spool.
	if os.Getuid() != 0 && !spoolProcessAlive(1) {
		t.Error("unsignalable live process reported dead")
	}
}
