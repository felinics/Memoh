package tools

import (
	"strings"
	"testing"
)

func TestEditContextDiffShowsContextAroundChange(t *testing.T) {
	before := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\n"
	after := "line 1\nline 2\nline 3\nline 4\nline five\nline 6\nline 7\nline 8\nline 9\n"

	diff, err := editContextDiff("dir/file.txt", before, after)
	if err != nil {
		t.Fatalf("editContextDiff returned error: %v", err)
	}
	if diff == "" {
		t.Fatal("expected a diff, got empty string")
	}
	for _, want := range []string{
		"--- a/dir/file.txt",
		"+++ b/dir/file.txt",
		"@@ -2,7 +2,7 @@",
		" line 2",
		" line 3",
		" line 4",
		"-line 5",
		"+line five",
		" line 6",
		" line 7",
		" line 8",
	} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
	// Far-away unchanged lines must not appear: only the 3 context lines on
	// each side of the change belong in the hunk.
	for _, unwanted := range []string{"line 1", "line 9"} {
		if strings.Contains(diff, unwanted) {
			t.Errorf("diff should not contain far-away context %q:\n%s", unwanted, diff)
		}
	}
}

func TestEditContextDiffNormalizesLineEndingsAndBOM(t *testing.T) {
	before := "\uFEFFfirst\r\nsecond\r\nthird\r\n"
	after := "\uFEFFfirst\r\nchanged\r\nthird\r\n"

	diff, err := editContextDiff("file.txt", before, after)
	if err != nil {
		t.Fatalf("editContextDiff returned error: %v", err)
	}
	if strings.Contains(diff, "\r") {
		t.Errorf("diff should be LF-only:\n%q", diff)
	}
	if strings.ContainsRune(diff, '\uFEFF') {
		t.Errorf("diff should not contain the BOM:\n%q", diff)
	}
	if !strings.Contains(diff, "-second") || !strings.Contains(diff, "+changed") {
		t.Errorf("diff missing the actual change:\n%s", diff)
	}
	if !strings.Contains(diff, " first") || !strings.Contains(diff, " third") {
		t.Errorf("diff missing context lines:\n%s", diff)
	}
}

func TestEditContextDiffEmptyForIdenticalContent(t *testing.T) {
	diff, err := editContextDiff("file.txt", "same\n", "same\n")
	if err != nil {
		t.Fatalf("editContextDiff returned error: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff for identical content, got:\n%s", diff)
	}
}

func TestEditContextDiffSkippedForLargeFiles(t *testing.T) {
	before := strings.Repeat("a", largeFileThreshold+1)
	after := before + "\nchanged"

	diff, err := editContextDiff("big.txt", before, after)
	if err != nil {
		t.Fatalf("editContextDiff returned error: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff above the large-file threshold, got %d bytes", len(diff))
	}
}

func TestEditContextDiffNewFileIsAllAdditions(t *testing.T) {
	diff, err := editContextDiff("dir/new.txt", "", "alpha\nbeta\n")
	if err != nil {
		t.Fatalf("editContextDiff returned error: %v", err)
	}
	if diff == "" {
		t.Fatal("expected a diff for a new file, got empty string")
	}
	for _, want := range []string{"@@ -0,0 +1,2 @@", "+alpha", "+beta"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "\n-") {
		t.Errorf("new-file diff must not contain removals:\n%s", diff)
	}
}

func TestEditContextDiffNoopWriteYieldsNoDiff(t *testing.T) {
	diff, err := editContextDiff("dir/same.txt", "alpha\n", "alpha\n")
	if err != nil {
		t.Fatalf("editContextDiff returned error: %v", err)
	}
	if diff != "" {
		t.Errorf("identical before/after must yield no diff, got:\n%s", diff)
	}
}
