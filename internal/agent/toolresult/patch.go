package toolresult

import "strings"

// ApplyPatch is the result of a multi-file patch.
type ApplyPatch struct {
	OK       bool             `json:"ok"`
	Summary  string           `json:"summary"`
	Added    []string         `json:"added"`
	Modified []string         `json:"modified"`
	Deleted  []string         `json:"deleted"`
	Files    []ApplyPatchFile `json:"files"`
}

// ApplyPatchFile is one touched file; Operation is add, modify or delete.
type ApplyPatchFile struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
}

// NewApplyPatch builds the result of a patch that touched the given paths. The
// summary lists added, then modified, then deleted paths; Files follows the
// same order.
func NewApplyPatch(added, modified, deleted []string) ApplyPatch {
	var summary strings.Builder
	summary.WriteString("Success. Updated the following files:")
	files := make([]ApplyPatchFile, 0, len(added)+len(modified)+len(deleted))
	for _, group := range []struct {
		mark, operation string
		paths           []string
	}{{"A", "add", added}, {"M", "modify", modified}, {"D", "delete", deleted}} {
		for _, path := range group.paths {
			summary.WriteString("\n" + group.mark + " " + path)
			files = append(files, ApplyPatchFile{Operation: group.operation, Path: path})
		}
	}
	return ApplyPatch{
		OK: true, Summary: summary.String(),
		Added: added, Modified: modified, Deleted: deleted, Files: files,
	}
}

func (r ApplyPatch) Map() map[string]any {
	if r.Files == nil {
		r.Files = []ApplyPatchFile{}
	}
	return toMap(r)
}
