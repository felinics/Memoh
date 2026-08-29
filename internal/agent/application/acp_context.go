package application

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/prune"
)

const acpContextURI = "memoh://context/current-turn"

type acpContextRenderInput struct {
	Timezone                  string
	BotID                     string
	ChatID                    string
	SessionID                 string
	RunID                     string
	RouteID                   string
	AgentID                   string
	ProjectPath               string
	SourceChannelIdentityID   string
	DisplayName               string
	CurrentChannel            string
	ConversationType          string
	ConversationName          string
	ReplyTarget               string
	Attachments               []ChatAttachment
	Files                     []native.SystemFile
	SystemFilesMaxBytes       int
	PlatformIdentitiesSection string
}

func (s *Service) buildACPContextMarkdown(ctx context.Context, req ChatRequest, agentID, projectPath string) string {
	timezoneName, timezoneLocation := s.resolveTimezone(ctx, req.BotID, req.UserID)
	now := time.Now().UTC()
	if timezoneLocation != nil {
		now = now.In(timezoneLocation)
	}

	var files []native.SystemFile
	limits := native.DefaultLimits()
	if s != nil && s.agent != nil {
		limits = s.agent.Limits()
		nowFn := func() time.Time { return now }
		fs := native.NewFSClient(s.agent.BridgeProvider(), req.BotID, nowFn)
		files = fs.LoadSystemFiles(ctx)
	}

	platformIdentitiesSection := ""
	if s != nil && s.platformIdentities != nil {
		identities, err := s.platformIdentities.ListPlatformIdentities(ctx, req.BotID)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("load bot platform identities for ACP context failed",
					slog.String("bot_id", req.BotID),
					slog.Any("error", err),
				)
			}
		} else {
			platformIdentitiesSection = buildPlatformIdentitiesSection(identities)
		}
	}

	return renderACPContextMarkdown(acpContextRenderInput{
		Timezone:                  timezoneName,
		BotID:                     req.BotID,
		ChatID:                    req.ChatID,
		SessionID:                 req.ThreadID,
		RunID:                     req.RunID,
		RouteID:                   req.RouteID,
		AgentID:                   agentID,
		ProjectPath:               projectPath,
		SourceChannelIdentityID:   req.SourceChannelIdentityID,
		DisplayName:               req.DisplayName,
		CurrentChannel:            req.CurrentChannel,
		ConversationType:          req.ConversationType,
		ConversationName:          req.ConversationName,
		ReplyTarget:               req.ReplyTarget,
		Attachments:               req.Attachments,
		Files:                     files,
		SystemFilesMaxBytes:       limits.SystemFilesMaxBytes,
		PlatformIdentitiesSection: platformIdentitiesSection,
	})
}

func renderACPContextMarkdown(input acpContextRenderInput) string {
	timezoneName := strings.TrimSpace(input.Timezone)
	if timezoneName == "" {
		timezoneName = "UTC"
	}

	var sb strings.Builder
	sb.WriteString("# Memoh ACP Context\n\n")
	sb.WriteString("This virtual resource is already embedded in the current ACP prompt. It is not a workspace file and no file lookup is needed. Use it for identity, memory, user preferences, and session background. The user prompt outside this resource is the actual task.\n\n")

	writeACPContextSection(&sb, "Current Runtime", acpContextMetadataLines([][2]string{
		{"Timezone", timezoneName},
		{"Bot ID", input.BotID},
		{"Session ID", input.SessionID},
		{"Run ID", input.RunID},
		{"ACP agent", input.AgentID},
		{"Workspace", input.ProjectPath},
	}))

	writeACPContextSection(&sb, "Current Conversation", acpContextMetadataLines([][2]string{
		{"Sender", input.DisplayName},
		{"Channel identity ID", input.SourceChannelIdentityID},
		{"Channel", input.CurrentChannel},
		{"Conversation type", input.ConversationType},
		{"Conversation name", input.ConversationName},
		{"Chat ID", input.ChatID},
		{"Route ID", input.RouteID},
		{"Reply target", input.ReplyTarget},
	}))

	if attachments := formatACPContextAttachments(input.Attachments); attachments != "" {
		writeACPContextSection(&sb, "Attachments", "These attachments are part of the current user turn. Image content may also be included as ACP image blocks; inspect path or URL references for other files.\n\n"+attachments)
	}
	if section := strings.TrimSpace(input.PlatformIdentitiesSection); section != "" {
		sb.WriteString(section)
		sb.WriteString("\n\n")
	}

	files := acpContextSystemFiles(input.Files, input.SystemFilesMaxBytes)
	for _, file := range files {
		writeACPContextSection(&sb, file.Title, file.Content)
	}

	writeACPContextSection(&sb, "Memoh Runtime Notes", strings.TrimSpace(`
- This context is generated dynamically for the current ACP turn.
- Prefer the latest user prompt over stale memory when they conflict.
- Treat secrets, OAuth tokens, API keys, and private configuration as sensitive.
- Keep code changes scoped to the current task and preserve existing user changes.
`))

	return prune.PruneWithEdges(sb.String(), "ACP context", prune.Config{
		MaxBytes:  64 * 1024,
		MaxLines:  1600,
		HeadBytes: 48 * 1024,
		TailBytes: 12 * 1024,
		HeadLines: 1200,
		TailLines: 300,
	})
}

type acpContextFileSection struct {
	Title   string
	Content string
}

func acpContextSystemFiles(files []native.SystemFile, maxBytes int) []acpContextFileSection {
	if maxBytes <= 0 {
		maxBytes = native.DefaultSystemFilesMaxBytes
	}
	titles := map[string]string{
		"IDENTITY.md": "Bot Identity",
		"SOUL.md":     "Bot Soul",
		"MEMORY.md":   "Long-Term Memory",
		"PROFILES.md": "Profiles",
	}
	out := make([]acpContextFileSection, 0, len(files))
	used := 0
	for _, file := range files {
		name := strings.TrimSpace(file.Filename)
		content := strings.TrimSpace(file.Content)
		if content == "" {
			continue
		}
		title, ok := titles[name]
		if !ok {
			if strings.HasPrefix(name, "memory/") && strings.HasSuffix(name, ".md") {
				title = "Memory Concept - " + strings.TrimPrefix(name, "memory/")
			} else {
				continue
			}
		}
		remaining := maxBytes - used
		overhead := acpContextRenderedSectionOverhead(title)
		if remaining <= overhead {
			break
		}
		contentBudget := acpContextMinInt(14*1024, remaining-overhead)
		if contentBudget <= 0 {
			break
		}
		section := acpContextFileSection{
			Title: title,
			Content: formatACPContextFileExcerpt(name, prune.PruneWithEdges(content, name, prune.Config{
				MaxBytes:  contentBudget,
				MaxLines:  320,
				HeadBytes: contentBudget * 3 / 4,
				TailBytes: contentBudget / 4,
				HeadLines: 220,
				TailLines: 80,
			})),
		}
		sectionBytes := acpContextRenderedSectionBytes(section)
		if sectionBytes > remaining {
			contentBudget -= sectionBytes - remaining
			if contentBudget <= 0 {
				break
			}
			section.Content = prune.PruneWithEdges(section.Content, "ACP context file "+name, prune.Config{
				MaxBytes:  contentBudget,
				MaxLines:  320,
				HeadBytes: contentBudget * 3 / 4,
				TailBytes: contentBudget / 4,
				HeadLines: 220,
				TailLines: 80,
			})
			sectionBytes = acpContextRenderedSectionBytes(section)
		}
		if sectionBytes > remaining {
			break
		}
		out = append(out, section)
		used += sectionBytes
	}
	return out
}

func acpContextRenderedSectionOverhead(title string) int {
	return len("## ") + len(strings.TrimSpace(title)) + len("\n\n\n\n")
}

func acpContextRenderedSectionBytes(section acpContextFileSection) int {
	return acpContextRenderedSectionOverhead(section.Title) + len(strings.TrimSpace(section.Content))
}

func acpContextMinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func formatACPContextFileExcerpt(name, content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	fence := markdownFence(content)
	return fmt.Sprintf("Embedded excerpt from `/data/%s`. This content is already loaded; do not search for or read this file unless the user explicitly asks.\n\n%smarkdown\n%s\n%s", name, fence, content, fence)
}

func markdownFence(content string) string {
	maxRun := 3
	current := 0
	for _, r := range content {
		if r == '`' {
			current++
			if current >= maxRun {
				maxRun = current + 1
			}
			continue
		}
		current = 0
	}
	return strings.Repeat("`", maxRun)
}

func acpContextMetadataLines(pairs [][2]string) string {
	lines := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		key := strings.TrimSpace(pair[0])
		value := strings.TrimSpace(pair[1])
		if key == "" || value == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", key, value))
	}
	return strings.Join(lines, "\n")
}

func formatACPContextAttachments(attachments []ChatAttachment) string {
	if len(attachments) == 0 {
		return ""
	}
	lines := make([]string, 0, len(attachments))
	for i, attachment := range attachments {
		parts := []string{fmt.Sprintf("- Attachment %d", i+1)}
		if value := strings.TrimSpace(attachment.Name); value != "" {
			parts = append(parts, "name="+value)
		}
		if value := strings.TrimSpace(attachment.Type); value != "" {
			parts = append(parts, "type="+value)
		}
		if value := strings.TrimSpace(attachment.Mime); value != "" {
			parts = append(parts, "mime="+value)
		}
		if value := strings.TrimSpace(attachment.Path); value != "" {
			parts = append(parts, "path="+value)
		}
		if value := strings.TrimSpace(attachment.URL); value != "" {
			parts = append(parts, "url="+value)
		}
		if value := strings.TrimSpace(attachment.ContentHash); value != "" {
			parts = append(parts, "content_hash="+value)
		}
		if attachment.Size > 0 {
			parts = append(parts, fmt.Sprintf("size=%d", attachment.Size))
		}
		lines = append(lines, strings.Join(parts, ", "))
	}
	return strings.Join(lines, "\n")
}

func writeACPContextSection(sb *strings.Builder, title, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	sb.WriteString("## ")
	sb.WriteString(strings.TrimSpace(title))
	sb.WriteString("\n\n")
	sb.WriteString(content)
	sb.WriteString("\n\n")
}
