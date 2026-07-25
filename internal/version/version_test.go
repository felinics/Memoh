package version

import "testing"

func TestGetInfoIncludesBuildProfile(t *testing.T) {
	originalVersion := Version
	originalCommitHash := CommitHash
	originalBuildTime := BuildTime
	t.Cleanup(func() {
		Version = originalVersion
		CommitHash = originalCommitHash
		BuildTime = originalBuildTime
	})

	Version = "1.2.3"
	CommitHash = "1234567890"
	BuildTime = "2026-07-24T00:00:00Z"

	if got, want := GetInfo("split"), "1.2.3 (1234567) profile=split"; got != want {
		t.Fatalf("GetInfo() = %q, want %q", got, want)
	}
}
