package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	pathpkg "path"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	applyPatchBeginMarker  = "*** Begin Patch"
	applyPatchEndMarker    = "*** End Patch"
	applyPatchAddMarker    = "*** Add File: "
	applyPatchDeleteMarker = "*** Delete File: "
	applyPatchUpdateMarker = "*** Update File: "
	applyPatchMoveMarker   = "*** Move to: "
	applyPatchEOFMarker    = "*** End of File"
)

type applyPatchHunkKind string

const (
	applyPatchHunkAdd    applyPatchHunkKind = "add"
	applyPatchHunkDelete applyPatchHunkKind = "delete"
	applyPatchHunkUpdate applyPatchHunkKind = "update"
)

type applyPatchHunk struct {
	kind     applyPatchHunkKind
	path     string
	movePath string
	contents string
	chunks   []applyPatchChunk
}

type applyPatchChunk struct {
	changeContext *string
	oldLines      []string
	newLines      []string
	endOfFile     bool
}

type applyPatchParseError struct {
	line    int
	message string
}

func (e *applyPatchParseError) Error() string {
	if e.line > 0 {
		return fmt.Sprintf("invalid patch hunk on line %d: %s", e.line, e.message)
	}
	return "invalid patch: " + e.message
}

type applyPatchOperationKind string

const (
	applyPatchOperationWrite  applyPatchOperationKind = "write"
	applyPatchOperationDelete applyPatchOperationKind = "delete"
)

type applyPatchOperation struct {
	kind    applyPatchOperationKind
	path    string
	content string
}

type applyPatchPlan struct {
	operations []applyPatchOperation
	added      []string
	modified   []string
	deleted    []string
}

func (p applyPatchPlan) files() []map[string]any {
	files := make([]map[string]any, 0, len(p.added)+len(p.modified)+len(p.deleted))
	for _, path := range p.added {
		files = append(files, map[string]any{"operation": "add", "path": path})
	}
	for _, path := range p.modified {
		files = append(files, map[string]any{"operation": "modify", "path": path})
	}
	for _, path := range p.deleted {
		files = append(files, map[string]any{"operation": "delete", "path": path})
	}
	return files
}

func (p applyPatchPlan) summary() string {
	var b strings.Builder
	b.WriteString("Success. Updated the following files:")
	for _, path := range p.added {
		b.WriteString("\nA ")
		b.WriteString(path)
	}
	for _, path := range p.modified {
		b.WriteString("\nM ")
		b.WriteString(path)
	}
	for _, path := range p.deleted {
		b.WriteString("\nD ")
		b.WriteString(path)
	}
	return b.String()
}

type applyPatchFileInfo struct {
	exists bool
	isDir  bool
}

type applyPatchReadFS interface {
	Stat(ctx context.Context, path string) (applyPatchFileInfo, error)
	ReadText(ctx context.Context, path string) (string, error)
}

type bridgeApplyPatchFS struct {
	client *bridge.Client
}

func (fs bridgeApplyPatchFS) Stat(ctx context.Context, path string) (applyPatchFileInfo, error) {
	entry, err := fs.client.Stat(ctx, path)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return applyPatchFileInfo{exists: false}, nil
		}
		return applyPatchFileInfo{}, err
	}
	return applyPatchFileInfo{exists: true, isDir: entry.GetIsDir()}, nil
}

func (fs bridgeApplyPatchFS) ReadText(ctx context.Context, path string) (string, error) {
	reader, err := fs.client.ReadRaw(ctx, path)
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()
	raw, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	if strings.IndexByte(string(raw), 0) >= 0 {
		return "", fmt.Errorf("file appears to be binary: %s", path)
	}
	return string(raw), nil
}

type applyPatchVirtualFile struct {
	exists        bool
	isDir         bool
	content       string
	contentLoaded bool
}

func execApplyPatchInput(input any) (string, error) {
	if text, ok := input.(string); ok {
		if strings.TrimSpace(text) == "" {
			return "", errors.New("patch is required")
		}
		return text, nil
	}
	args := inputAsMap(input)
	raw, ok := args["patch"]
	if !ok || raw == nil {
		return "", errors.New("patch is required")
	}
	text, ok := raw.(string)
	if !ok {
		return "", errors.New("patch must be a string")
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("patch is required")
	}
	return text, nil
}

func (p *ContainerProvider) execApplyPatch(ctx context.Context, session SessionContext, input any) (any, error) {
	patch, err := execApplyPatchInput(input)
	if err != nil {
		return nil, err
	}

	opCtx, opCancel := context.WithTimeout(ctx, containerOpTimeout)
	defer opCancel()

	target, err := p.resolveToolTarget(opCtx, session, inputAsMap(input))
	if err != nil {
		return nil, err
	}
	client := target.client
	hunks, err := parseApplyPatch(patch)
	if err != nil {
		return nil, err
	}
	if len(hunks) == 0 {
		return nil, errors.New("no files were modified")
	}

	plan, err := buildApplyPatchPlan(opCtx, bridgeApplyPatchFS{client: client}, target.workspace, hunks)
	if err != nil {
		return nil, err
	}
	if res, err := p.runWorkspaceToolHook(ctx, session, target.hookWorkspaceInfo(p.execWorkDir), hooks.EventBeforeFileWrite, map[string]any{
		"operation": "apply_patch",
		"summary":   plan.summary(),
		"files":     plan.files(),
	}); err != nil {
		if res.Decision == hooks.DecisionDeny {
			return nil, err
		}
		return nil, fmt.Errorf("before file write hook failed: %w", err)
	}
	if err := commitApplyPatchPlan(opCtx, client, plan); err != nil {
		return nil, err
	}
	if _, err := p.runWorkspaceToolHook(context.WithoutCancel(ctx), session, target.hookWorkspaceInfo(p.execWorkDir), hooks.EventAfterFileWrite, map[string]any{
		"operation": "apply_patch",
		"summary":   plan.summary(),
		"files":     plan.files(),
	}); err != nil {
		p.logWorkspaceToolHookError(hooks.EventAfterFileWrite, session.BotID, session.SessionID, err)
	}

	return map[string]any{
		"ok":       true,
		"summary":  plan.summary(),
		"added":    plan.added,
		"modified": plan.modified,
		"deleted":  plan.deleted,
		"files":    plan.files(),
	}, nil
}

func commitApplyPatchPlan(ctx context.Context, client *bridge.Client, plan *applyPatchPlan) error {
	for _, op := range plan.operations {
		switch op.kind {
		case applyPatchOperationWrite:
			data := []byte(op.content)
			if len(data) > largeFileThreshold {
				if _, err := client.WriteRaw(ctx, op.path, strings.NewReader(op.content)); err != nil {
					return fmt.Errorf("write %s: %w", op.path, err)
				}
				continue
			}
			if err := client.WriteFile(ctx, op.path, data); err != nil {
				return fmt.Errorf("write %s: %w", op.path, err)
			}
		case applyPatchOperationDelete:
			if err := client.DeleteFile(ctx, op.path, false); err != nil {
				return fmt.Errorf("delete %s: %w", op.path, err)
			}
		default:
			return fmt.Errorf("unknown apply_patch operation %q", op.kind)
		}
	}
	return nil
}

func parseApplyPatch(patch string) ([]applyPatchHunk, error) {
	lines := splitPatchLines(patch)
	lines, err := stripLenientHeredoc(lines)
	if err != nil {
		return nil, err
	}
	if len(lines) < 2 {
		return nil, &applyPatchParseError{message: "The first line of the patch must be '*** Begin Patch'"}
	}
	if strings.TrimSpace(lines[0]) != applyPatchBeginMarker {
		return nil, &applyPatchParseError{message: "The first line of the patch must be '*** Begin Patch'"}
	}
	if strings.TrimSpace(lines[len(lines)-1]) != applyPatchEndMarker {
		return nil, &applyPatchParseError{message: "The last line of the patch must be '*** End Patch'"}
	}

	var hunks []applyPatchHunk
	mode := ""
	currentUpdateLine := 0
	for i := 1; i < len(lines)-1; i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)

		updateContentLine := mode == "update" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-"))
		if !updateContentLine {
			header, ok, err := parseApplyPatchHeader(trimmed, lineNo, mode, currentUpdateLine, hunks)
			if ok || err != nil {
				if err != nil {
					return nil, err
				}
				switch header.kind {
				case applyPatchHunkAdd:
					hunks = append(hunks, applyPatchHunk{kind: applyPatchHunkAdd, path: header.path})
					mode = "add"
				case applyPatchHunkDelete:
					hunks = append(hunks, applyPatchHunk{kind: applyPatchHunkDelete, path: header.path})
					mode = "delete"
				case applyPatchHunkUpdate:
					hunks = append(hunks, applyPatchHunk{kind: applyPatchHunkUpdate, path: header.path})
					mode = "update"
					currentUpdateLine = lineNo
				}
				continue
			}
		}

		switch mode {
		case "":
			return nil, &applyPatchParseError{line: lineNo, message: fmt.Sprintf("'%s' is not a valid hunk header", trimmed)}
		case "add":
			if content, ok := strings.CutPrefix(line, "+"); ok {
				hunks[len(hunks)-1].contents += content + "\n"
				continue
			}
			return nil, &applyPatchParseError{line: lineNo, message: fmt.Sprintf("'%s' is not a valid hunk header", trimmed)}
		case "delete":
			return nil, &applyPatchParseError{line: lineNo, message: fmt.Sprintf("'%s' is not a valid hunk header", trimmed)}
		case "update":
			if err := parseApplyPatchUpdateLine(&hunks[len(hunks)-1], line, strings.TrimRight(line, " \t"), lineNo); err != nil {
				return nil, err
			}
		}
	}
	if err := ensureApplyPatchUpdateHunkComplete(mode, currentUpdateLine, len(lines), hunks); err != nil {
		return nil, err
	}
	return hunks, nil
}

func splitPatchLines(patch string) []string {
	patch = strings.TrimSpace(patch)
	if patch == "" {
		return nil
	}
	return strings.Split(patch, "\n")
}

func stripLenientHeredoc(lines []string) ([]string, error) {
	if len(lines) == 0 {
		return lines, nil
	}
	if strings.TrimSpace(lines[0]) == applyPatchBeginMarker {
		return lines, nil
	}
	if len(lines) < 4 {
		return lines, nil
	}
	first := strings.TrimSpace(lines[0])
	last := strings.TrimSpace(lines[len(lines)-1])
	if (first == "<<EOF" || first == "<<'EOF'" || first == "<<\"EOF\"") && strings.HasSuffix(last, "EOF") {
		return lines[1 : len(lines)-1], nil
	}
	return lines, nil
}

func parseApplyPatchHeader(trimmed string, lineNo int, mode string, hunkLine int, hunks []applyPatchHunk) (applyPatchHunk, bool, error) {
	var kind applyPatchHunkKind
	var path string
	switch {
	case strings.HasPrefix(trimmed, applyPatchAddMarker):
		kind = applyPatchHunkAdd
		path = strings.TrimSpace(strings.TrimPrefix(trimmed, applyPatchAddMarker))
	case strings.HasPrefix(trimmed, applyPatchDeleteMarker):
		kind = applyPatchHunkDelete
		path = strings.TrimSpace(strings.TrimPrefix(trimmed, applyPatchDeleteMarker))
	case strings.HasPrefix(trimmed, applyPatchUpdateMarker):
		kind = applyPatchHunkUpdate
		path = strings.TrimSpace(strings.TrimPrefix(trimmed, applyPatchUpdateMarker))
	default:
		return applyPatchHunk{}, false, nil
	}
	if path == "" {
		return applyPatchHunk{}, true, &applyPatchParseError{line: lineNo, message: "hunk path is required"}
	}
	if err := ensureApplyPatchUpdateHunkComplete(mode, hunkLine, lineNo, hunks); err != nil {
		return applyPatchHunk{}, true, err
	}
	return applyPatchHunk{kind: kind, path: path}, true, nil
}

func ensureApplyPatchUpdateHunkComplete(mode string, hunkLine, lineNo int, hunks []applyPatchHunk) error {
	if mode != "update" || len(hunks) == 0 {
		return nil
	}
	last := hunks[len(hunks)-1]
	if len(last.chunks) == 0 {
		return &applyPatchParseError{line: hunkLine, message: fmt.Sprintf("Update file hunk for path '%s' is empty", last.path)}
	}
	chunk := last.chunks[len(last.chunks)-1]
	if len(chunk.oldLines) == 0 && len(chunk.newLines) == 0 {
		return &applyPatchParseError{line: lineNo, message: "Update hunk does not contain any lines"}
	}
	return nil
}

func parseApplyPatchUpdateLine(hunk *applyPatchHunk, line, updateLine string, lineNo int) error {
	if len(hunk.chunks) > 0 && hunk.chunks[len(hunk.chunks)-1].endOfFile {
		if updateLine == "" {
			return nil
		}
		if updateLine != "@@" && !strings.HasPrefix(updateLine, "@@ ") {
			return &applyPatchParseError{line: lineNo, message: fmt.Sprintf("Expected update hunk to start with a @@ context marker, got: '%s'", line)}
		}
	}

	if updateLine == applyPatchMoveMarker || strings.HasPrefix(updateLine, applyPatchMoveMarker) {
		if len(hunk.chunks) > 0 || hunk.movePath != "" {
			return &applyPatchParseError{line: lineNo, message: "move marker must appear before update lines"}
		}
		movePath := strings.TrimSpace(strings.TrimPrefix(updateLine, applyPatchMoveMarker))
		if movePath == "" {
			return &applyPatchParseError{line: lineNo, message: "move path is required"}
		}
		hunk.movePath = movePath
		return nil
	}

	if updateLine == "@@" || strings.HasPrefix(updateLine, "@@ ") {
		if len(hunk.chunks) > 0 {
			prev := hunk.chunks[len(hunk.chunks)-1]
			if len(prev.oldLines) == 0 && len(prev.newLines) == 0 {
				return &applyPatchParseError{line: lineNo, message: fmt.Sprintf("Unexpected line found in update hunk: '%s'. Every line should start with ' ' (context line), '+' (added line), or '-' (removed line)", line)}
			}
		}
		var ctx *string
		if strings.HasPrefix(updateLine, "@@ ") {
			value := strings.TrimPrefix(updateLine, "@@ ")
			ctx = &value
		}
		hunk.chunks = append(hunk.chunks, applyPatchChunk{changeContext: ctx})
		return nil
	}

	if updateLine == applyPatchEOFMarker {
		if len(hunk.chunks) == 0 {
			return &applyPatchParseError{line: lineNo, message: "Update hunk does not contain any lines"}
		}
		chunk := &hunk.chunks[len(hunk.chunks)-1]
		if len(chunk.oldLines) == 0 && len(chunk.newLines) == 0 {
			return &applyPatchParseError{line: lineNo, message: "Update hunk does not contain any lines"}
		}
		chunk.endOfFile = true
		return nil
	}

	if len(hunk.chunks) == 0 {
		hunk.chunks = append(hunk.chunks, applyPatchChunk{})
	}
	chunk := &hunk.chunks[len(hunk.chunks)-1]
	switch {
	case line == "":
		chunk.oldLines = append(chunk.oldLines, "")
		chunk.newLines = append(chunk.newLines, "")
	case strings.HasPrefix(line, " "):
		value := strings.TrimPrefix(line, " ")
		chunk.oldLines = append(chunk.oldLines, value)
		chunk.newLines = append(chunk.newLines, value)
	case strings.HasPrefix(line, "+"):
		chunk.newLines = append(chunk.newLines, strings.TrimPrefix(line, "+"))
	case strings.HasPrefix(line, "-"):
		chunk.oldLines = append(chunk.oldLines, strings.TrimPrefix(line, "-"))
	default:
		if len(chunk.oldLines) > 0 || len(chunk.newLines) > 0 {
			return &applyPatchParseError{line: lineNo, message: fmt.Sprintf("Expected update hunk to start with a @@ context marker, got: '%s'", line)}
		}
		return &applyPatchParseError{line: lineNo, message: fmt.Sprintf("Unexpected line found in update hunk: '%s'. Every line should start with ' ' (context line), '+' (added line), or '-' (removed line)", line)}
	}
	return nil
}

func buildApplyPatchPlan(ctx context.Context, fs applyPatchReadFS, workspace toolWorkspace, hunks []applyPatchHunk) (*applyPatchPlan, error) {
	files := make(map[string]applyPatchVirtualFile)
	plan := &applyPatchPlan{}

	for _, hunk := range hunks {
		path, err := normalizeApplyPatchPath(hunk.path, workspace)
		if err != nil {
			return nil, err
		}
		switch hunk.kind {
		case applyPatchHunkAdd:
			info, err := statApplyPatchVirtualFile(ctx, fs, files, path)
			if err != nil {
				return nil, err
			}
			if info.exists {
				if info.isDir {
					return nil, fmt.Errorf("cannot add file over directory: %s", path)
				}
				return nil, fmt.Errorf("cannot add existing file: %s", path)
			}
			files[path] = applyPatchVirtualFile{exists: true, content: hunk.contents, contentLoaded: true}
			plan.operations = append(plan.operations, applyPatchOperation{kind: applyPatchOperationWrite, path: path, content: hunk.contents})
			plan.added = append(plan.added, path)
		case applyPatchHunkDelete:
			info, err := statApplyPatchVirtualFile(ctx, fs, files, path)
			if err != nil {
				return nil, err
			}
			if !info.exists {
				return nil, fmt.Errorf("cannot delete missing file: %s", path)
			}
			if info.isDir {
				return nil, fmt.Errorf("cannot delete directory with apply_patch: %s", path)
			}
			files[path] = applyPatchVirtualFile{exists: false, contentLoaded: true}
			plan.operations = append(plan.operations, applyPatchOperation{kind: applyPatchOperationDelete, path: path})
			plan.deleted = append(plan.deleted, path)
		case applyPatchHunkUpdate:
			original, err := readApplyPatchVirtualFile(ctx, fs, files, path)
			if err != nil {
				return nil, err
			}
			newContent, err := deriveApplyPatchContent(path, original, hunk.chunks)
			if err != nil {
				return nil, err
			}
			if hunk.movePath != "" {
				movePath, err := normalizeApplyPatchPath(hunk.movePath, workspace)
				if err != nil {
					return nil, err
				}
				if movePath == path {
					files[path] = applyPatchVirtualFile{exists: true, content: newContent, contentLoaded: true}
					plan.operations = append(plan.operations, applyPatchOperation{kind: applyPatchOperationWrite, path: path, content: newContent})
				} else {
					info, err := statApplyPatchVirtualFile(ctx, fs, files, movePath)
					if err != nil {
						return nil, err
					}
					if info.exists && info.isDir {
						return nil, fmt.Errorf("cannot move file over directory: %s", movePath)
					}
					files[path] = applyPatchVirtualFile{exists: false, contentLoaded: true}
					files[movePath] = applyPatchVirtualFile{exists: true, content: newContent, contentLoaded: true}
					plan.operations = append(plan.operations,
						applyPatchOperation{kind: applyPatchOperationWrite, path: movePath, content: newContent},
						applyPatchOperation{kind: applyPatchOperationDelete, path: path},
					)
				}
			} else {
				files[path] = applyPatchVirtualFile{exists: true, content: newContent, contentLoaded: true}
				plan.operations = append(plan.operations, applyPatchOperation{kind: applyPatchOperationWrite, path: path, content: newContent})
			}
			plan.modified = append(plan.modified, path)
		default:
			return nil, fmt.Errorf("unknown apply_patch hunk kind %q", hunk.kind)
		}
	}

	return plan, nil
}

func normalizeApplyPatchPath(rawPath string, workspace toolWorkspace) (string, error) {
	value := strings.TrimSpace(rawPath)
	if value == "" {
		return "", errors.New("patch path is required")
	}
	if strings.Contains(value, "\x00") {
		return "", fmt.Errorf("patch path contains null byte: %q", rawPath)
	}
	value = workspace.resolveToolPath(value)
	if workspace.windows {
		return normalizeWindowsApplyPatchPath(value, rawPath)
	}
	clean := pathpkg.Clean(value)
	if clean == "." || clean == "/" {
		return "", fmt.Errorf("patch path must refer to a file: %s", rawPath)
	}
	if !strings.HasPrefix(clean, "/") && (clean == ".." || strings.HasPrefix(clean, "../")) {
		return "", fmt.Errorf("patch path escapes workspace: %s", rawPath)
	}
	return clean, nil
}

func normalizeWindowsApplyPatchPath(value, rawPath string) (string, error) {
	forward := strings.ReplaceAll(value, `\`, "/")
	if len(forward) >= 2 && forward[1] == ':' && (len(forward) == 2 || forward[2] != '/') {
		return "", fmt.Errorf("drive-relative patch path is not supported: %s", rawPath)
	}
	unc := strings.HasPrefix(forward, "//")
	clean := pathpkg.Clean(forward)
	if clean == "." || clean == "/" || (len(clean) == 2 && clean[1] == ':') {
		return "", fmt.Errorf("patch path must refer to a file: %s", rawPath)
	}
	isDriveAbsolute := len(clean) >= 3 && clean[1] == ':' && clean[2] == '/'
	isRootAbsolute := strings.HasPrefix(clean, "/")
	if !isDriveAbsolute && !isRootAbsolute && (clean == ".." || strings.HasPrefix(clean, "../")) {
		return "", fmt.Errorf("patch path escapes workspace: %s", rawPath)
	}
	if unc && strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	return strings.ReplaceAll(clean, "/", `\`), nil
}

func statApplyPatchVirtualFile(ctx context.Context, fs applyPatchReadFS, files map[string]applyPatchVirtualFile, path string) (applyPatchVirtualFile, error) {
	if file, ok := files[path]; ok {
		return file, nil
	}
	info, err := fs.Stat(ctx, path)
	if err != nil {
		return applyPatchVirtualFile{}, fmt.Errorf("stat %s: %w", path, err)
	}
	file := applyPatchVirtualFile{exists: info.exists, isDir: info.isDir}
	files[path] = file
	return file, nil
}

func readApplyPatchVirtualFile(ctx context.Context, fs applyPatchReadFS, files map[string]applyPatchVirtualFile, path string) (string, error) {
	file, err := statApplyPatchVirtualFile(ctx, fs, files, path)
	if err != nil {
		return "", err
	}
	if !file.exists {
		return "", fmt.Errorf("cannot update missing file: %s", path)
	}
	if file.isDir {
		return "", fmt.Errorf("cannot update directory with apply_patch: %s", path)
	}
	if file.contentLoaded {
		return file.content, nil
	}
	content, err := fs.ReadText(ctx, path)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return "", fmt.Errorf("cannot update missing file: %s", path)
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	file.content = content
	file.contentLoaded = true
	files[path] = file
	return content, nil
}

func deriveApplyPatchContent(path, original string, chunks []applyPatchChunk) (string, error) {
	originalLines := strings.Split(original, "\n")
	if len(originalLines) > 0 && originalLines[len(originalLines)-1] == "" {
		originalLines = originalLines[:len(originalLines)-1]
	}
	replacements, err := computeApplyPatchReplacements(originalLines, path, chunks)
	if err != nil {
		return "", err
	}
	updatedLines := applyPatchReplacements(originalLines, replacements)
	if len(updatedLines) == 0 || updatedLines[len(updatedLines)-1] != "" {
		updatedLines = append(updatedLines, "")
	}
	return strings.Join(updatedLines, "\n"), nil
}

type applyPatchReplacement struct {
	start    int
	oldLen   int
	newLines []string
}

func computeApplyPatchReplacements(originalLines []string, path string, chunks []applyPatchChunk) ([]applyPatchReplacement, error) {
	replacements := make([]applyPatchReplacement, 0, len(chunks))
	lineIndex := 0
	for _, chunk := range chunks {
		hasChangeContext := chunk.changeContext != nil
		if chunk.changeContext != nil {
			idx, ok := seekApplyPatchSequence(originalLines, []string{*chunk.changeContext}, lineIndex, false)
			if !ok {
				return nil, fmt.Errorf("failed to find context %q in %s", *chunk.changeContext, path)
			}
			lineIndex = idx + 1
		}
		if len(chunk.oldLines) == 0 {
			insertionIdx := len(originalLines)
			if hasChangeContext {
				insertionIdx = lineIndex
			}
			replacements = append(replacements, applyPatchReplacement{start: insertionIdx, newLines: append([]string(nil), chunk.newLines...)})
			lineIndex = insertionIdx
			continue
		}

		pattern := chunk.oldLines
		newLines := chunk.newLines
		start, ok := seekApplyPatchSequence(originalLines, pattern, lineIndex, chunk.endOfFile)
		if !ok && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			pattern = pattern[:len(pattern)-1]
			if len(newLines) > 0 && newLines[len(newLines)-1] == "" {
				newLines = newLines[:len(newLines)-1]
			}
			start, ok = seekApplyPatchSequence(originalLines, pattern, lineIndex, chunk.endOfFile)
		}
		if !ok {
			return nil, fmt.Errorf("failed to find expected lines in %s:\n%s", path, strings.Join(chunk.oldLines, "\n"))
		}
		replacements = append(replacements, applyPatchReplacement{
			start:    start,
			oldLen:   len(pattern),
			newLines: append([]string(nil), newLines...),
		})
		lineIndex = start + len(pattern)
	}
	sort.SliceStable(replacements, func(i, j int) bool {
		return replacements[i].start < replacements[j].start
	})
	return replacements, nil
}

func applyPatchReplacements(lines []string, replacements []applyPatchReplacement) []string {
	out := append([]string(nil), lines...)
	for i := len(replacements) - 1; i >= 0; i-- {
		repl := replacements[i]
		if repl.start > len(out) {
			continue
		}
		end := repl.start + repl.oldLen
		if end > len(out) {
			end = len(out)
		}
		next := make([]string, 0, len(out)-(end-repl.start)+len(repl.newLines))
		next = append(next, out[:repl.start]...)
		next = append(next, repl.newLines...)
		next = append(next, out[end:]...)
		out = next
	}
	return out
}

func seekApplyPatchSequence(lines, pattern []string, start int, eof bool) (int, bool) {
	if len(pattern) == 0 {
		return start, true
	}
	if len(pattern) > len(lines) {
		return 0, false
	}
	searchStart := start
	if eof && len(lines) >= len(pattern) {
		searchStart = len(lines) - len(pattern)
	}
	if searchStart < 0 {
		searchStart = 0
	}
	if searchStart > len(lines)-len(pattern) {
		return 0, false
	}
	for _, match := range []func(string, string) bool{
		func(a, b string) bool { return a == b },
		func(a, b string) bool { return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t") },
		func(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) },
	} {
		for i := searchStart; i <= len(lines)-len(pattern); i++ {
			ok := true
			for j := range pattern {
				if !match(lines[i+j], pattern[j]) {
					ok = false
					break
				}
			}
			if ok {
				return i, true
			}
		}
	}
	return 0, false
}
