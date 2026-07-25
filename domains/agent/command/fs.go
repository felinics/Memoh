package command

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func (h *Handler) buildFSGroup() *CommandGroup {
	g := newCommandGroup("fs", "Browse workspace files")
	g.DefaultAction = "list" // bare /fs lands on the workspace root listing
	g.Register(SubCommand{
		Name:  "list",
		Usage: "list [path] - List files in the workspace",
		Handler: func(cc CommandContext) (string, error) {
			if h.containerFS == nil {
				return cc.T("cmd.fs.unavailable"), nil
			}
			dir := "/"
			if len(cc.Args) > 0 {
				dir = cc.Args[0]
			}
			entries, err := h.containerFS.ListDir(cc.Ctx, cc.BotID, dir)
			if err != nil {
				return "", err
			}
			if len(entries) == 0 {
				return cc.T("cmd.fs.emptyDir", map[string]any{"path": fmt.Sprintf("%q", dir)}), nil
			}
			// Wrap in a code fence so on markdown-capable channels the proportional
			// font doesn't collapse alignment. Inside the fence, entries are
			// bulleted (not 2-space indented) so that on plain-text channels
			// (Weixin / WeChat OA / Local-Web) — where the fence is stripped —
			// each entry still owns its own line instead of soft-wrapping into
			// the parent header.
			var b strings.Builder
			fmt.Fprintf(&b, "```\n%s:\n", dir)
			for _, e := range entries {
				if e.IsDir {
					fmt.Fprintf(&b, "- %s/\n", e.Name)
				} else {
					fmt.Fprintf(&b, "- %s (%s)\n", e.Name, humanizeBytes(e.Size))
				}
			}
			b.WriteString("```")
			return b.String(), nil
		},
	})
	g.Register(SubCommand{
		Name:  "read",
		Usage: "read <path> - Read a file from the workspace",
		Handler: func(cc CommandContext) (string, error) {
			if h.containerFS == nil {
				return cc.T("cmd.fs.unavailable"), nil
			}
			if len(cc.Args) < 1 {
				return cc.T("cmd.fs.readUsage"), nil
			}
			content, err := h.containerFS.ReadFile(cc.Ctx, cc.BotID, cc.Args[0])
			if err != nil {
				return "", err
			}
			const maxRunes = 2000
			truncated := false
			if utf8.RuneCountInString(content) > maxRunes {
				content = string([]rune(content)[:maxRunes])
				truncated = true
			}
			if strings.TrimSpace(content) == "" {
				return cc.T("cmd.fs.emptyFile"), nil
			}
			out := fmt.Sprintf("```\n%s\n```", content)
			if truncated {
				// The marker is a system note, not file content — keep it outside
				// the fence so it doesn't read as part of the file.
				out += "\n_" + cc.T("cmd.fs.truncated") + "_"
			}
			return out, nil
		},
	})
	return g
}
