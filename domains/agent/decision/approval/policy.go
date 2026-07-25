package approval

import (
	"encoding/json"
	"path"
	"strconv"
	"strings"
)

func policyDecision(cfg PolicyConfig, toolName string, input any) string {
	cfg = NormalizePolicyConfig(cfg)
	args := inputMap(input)
	operation, ok := OperationForTool(toolName)
	if !ok {
		return DecisionBypass
	}
	if !cfg.Enabled {
		return DecisionBypass
	}
	switch operation {
	case OperationRead:
		if decision, decided := explicitModeDecision(cfg.Read.Mode); decided {
			if decision != DecisionNeedsApproval {
				return decision
			}
		}
		targets := approvalPaths(args)
		if matchesAnyPathGlob(targets, cfg.Read.ForceReviewGlobs) {
			return DecisionNeedsApproval
		}
		if pathsBypass(targets, cfg.Read.BypassGlobs) {
			return DecisionBypass
		}
		if cfg.Read.Mode == PolicyModeAsk || cfg.Read.RequireApproval {
			return DecisionNeedsApproval
		}
		return DecisionBypass
	case OperationWrite:
		if decision, decided := explicitModeDecision(cfg.Write.Mode); decided {
			if decision != DecisionNeedsApproval {
				return decision
			}
		}
		if strings.EqualFold(strings.TrimSpace(toolName), "apply_patch") {
			targets := applyPatchPaths(readString(args, "patch"))
			if matchesAnyPathGlob(targets, cfg.Write.ForceReviewGlobs) {
				return DecisionNeedsApproval
			}
			if pathsBypass(targets, cfg.Write.BypassGlobs) {
				return DecisionBypass
			}
			if cfg.Write.Mode == PolicyModeAsk || cfg.Write.RequireApproval || len(cfg.Write.ForceReviewGlobs) > 0 {
				return DecisionNeedsApproval
			}
			return DecisionBypass
		}
		targets := approvalPaths(args)
		if matchesAnyPathGlob(targets, cfg.Write.ForceReviewGlobs) {
			return DecisionNeedsApproval
		}
		if pathsBypass(targets, cfg.Write.BypassGlobs) {
			return DecisionBypass
		}
		if cfg.Write.Mode == PolicyModeAsk || cfg.Write.RequireApproval {
			return DecisionNeedsApproval
		}
		return DecisionBypass
	case OperationExec:
		if decision, decided := explicitModeDecision(cfg.Exec.Mode); decided {
			if decision != DecisionNeedsApproval {
				return decision
			}
		}
		command := readString(args, "command")
		if matchesCommand(command, cfg.Exec.ForceReviewCommands) {
			return DecisionNeedsApproval
		}
		exe, ok := simpleExecutable(command)
		if !ok {
			if cfg.Exec.Mode == PolicyModeAsk || cfg.Exec.RequireApproval {
				return DecisionNeedsApproval
			}
			return DecisionBypass
		}
		if matchesCommandWithExecutable(command, exe, cfg.Exec.BypassCommands) {
			return DecisionBypass
		}
		if cfg.Exec.Mode == PolicyModeAsk || cfg.Exec.RequireApproval {
			return DecisionNeedsApproval
		}
		return DecisionBypass
	default:
		return DecisionBypass
	}
}

func explicitModeDecision(mode PolicyMode) (string, bool) {
	switch mode {
	case PolicyModeAllow:
		return DecisionBypass, true
	case PolicyModeAsk:
		return DecisionNeedsApproval, true
	case PolicyModeDeny:
		return DecisionDeny, true
	default:
		return "", false
	}
}

func OperationForTool(toolName string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "read", "list":
		return OperationRead, true
	case "write", "edit", "apply_patch":
		return OperationWrite, true
	case "exec":
		return OperationExec, true
	default:
		return "", false
	}
}

func inputMap(input any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	if m, ok := input.(map[string]any); ok {
		return m
	}
	data, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]any{}
	}
	return m
}

func readString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func approvalPaths(args map[string]any) []string {
	keys := []string{"path", "file_path", "filePath", "file", "filename", "old_path", "new_path"}
	seen := map[string]struct{}{}
	var out []string
	for _, key := range keys {
		addApprovalPath(&out, seen, args[key])
	}
	addApprovalPath(&out, seen, args["paths"])
	addApprovalPath(&out, seen, args["files"])
	return out
}

func addApprovalPath(out *[]string, seen map[string]struct{}, value any) {
	switch v := value.(type) {
	case string:
		p := strings.TrimSpace(v)
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		*out = append(*out, p)
	case []string:
		for _, item := range v {
			addApprovalPath(out, seen, item)
		}
	case []any:
		for _, item := range v {
			addApprovalPath(out, seen, item)
		}
	}
}

func applyPatchPaths(patch string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, line := range strings.Split(patch, "\n") {
		for _, prefix := range []string{
			"*** Add File: ",
			"*** Delete File: ",
			"*** Update File: ",
			"*** Move to: ",
		} {
			if path, ok := strings.CutPrefix(line, prefix); ok {
				addApprovalPath(&out, seen, path)
				break
			}
		}
	}
	return out
}

func normalizeContainerPath(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "/data" || p == "/tmp" {
		return p
	}
	p = strings.TrimPrefix(p, "./")
	if p == "" {
		return "."
	}
	return path.Clean(p)
}

func matchesAnyPathGlob(targets []string, patterns []string) bool {
	for _, target := range targets {
		if matchesAnyGlob(target, patterns) {
			return true
		}
	}
	return false
}

func pathsBypass(targets []string, patterns []string) bool {
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if !matchesAnyGlob(target, patterns) {
			return false
		}
	}
	return true
}

func matchesAnyGlob(target string, patterns []string) bool {
	target = normalizeContainerPath(target)
	for _, raw := range patterns {
		pattern := normalizeContainerPath(raw)
		if pattern == "." || pattern == "" {
			continue
		}
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			relativePrefix := strings.TrimLeft(prefix, "/")
			if prefix == "/data" && !strings.HasPrefix(target, "/") {
				return true
			}
			if target == prefix || strings.HasPrefix(target, prefix+"/") ||
				target == relativePrefix || strings.HasPrefix(target, relativePrefix+"/") {
				return true
			}
			continue
		}
		if ok, _ := path.Match(pattern, target); ok {
			return true
		}
		if !strings.Contains(pattern, "/") {
			if ok, _ := path.Match(pattern, path.Base(target)); ok {
				return true
			}
		}
	}
	return false
}

func simpleExecutable(command string) (string, bool) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return "", false
	}
	if strings.Contains(cmd, "&&") || strings.Contains(cmd, "||") ||
		strings.ContainsAny(cmd, ";|`") || strings.Contains(cmd, "$(") {
		return "", false
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return "", false
	}
	exe := strings.Trim(fields[0], `"'`)
	if exe == "" {
		return "", false
	}
	if unquoted, err := strconv.Unquote(exe); err == nil {
		exe = unquoted
	}
	return path.Base(exe), true
}

func matchesCommand(command string, patterns []string) bool {
	exe, _ := simpleExecutable(command)
	return matchesCommandWithExecutable(command, exe, patterns)
}

func matchesCommandWithExecutable(command, exe string, patterns []string) bool {
	command = normalizeCommand(command)
	exe = strings.ToLower(strings.TrimSpace(exe))
	for _, raw := range patterns {
		pattern := normalizeCommand(raw)
		if pattern == "" {
			continue
		}
		if isExecutablePattern(pattern) {
			if exe != "" && exe == strings.ToLower(pattern) {
				return true
			}
			continue
		}
		if commandMatchesPattern(command, pattern) {
			return true
		}
	}
	return false
}

func normalizeCommand(raw string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
}

func isExecutablePattern(pattern string) bool {
	return !strings.ContainsAny(pattern, " \t\r\n*?")
}

func commandMatchesPattern(command, pattern string) bool {
	command = strings.ToLower(command)
	pattern = strings.ToLower(pattern)
	if !strings.ContainsAny(pattern, "*?") {
		return command == pattern
	}
	return wildcardMatch(pattern, command)
}

func wildcardMatch(pattern, value string) bool {
	p, v := 0, 0
	star := -1
	match := 0
	for v < len(value) {
		if p < len(pattern) && (pattern[p] == '?' || pattern[p] == value[v]) {
			p++
			v++
			continue
		}
		if p < len(pattern) && pattern[p] == '*' {
			star = p
			match = v
			p++
			continue
		}
		if star != -1 {
			p = star + 1
			match++
			v = match
			continue
		}
		return false
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
