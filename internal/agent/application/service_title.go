package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	messageevent "github.com/felinics/memoh/internal/chat/event"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthctx"
	"github.com/felinics/memoh/internal/providers"
)

const (
	titleGenerateTimeout = 60 * time.Second
	// titleGenerateMaxTokens caps the title completion. Reasoning models burn
	// budget on hidden thinking before answering, so the cap must clear their
	// thinking plus a short title (provider defaults can be far smaller, which
	// truncates the response to an empty title). But it must also leave room
	// for the prompt on short-context chat models — provider templates still
	// catalog 4097-token ones — or the request is rejected outright. 2048
	// sits between the two failure modes.
	titleGenerateMaxTokens = 2048
)

// titleGenerationPrompt produces short sidebar titles: a noun phrase naming
// the topic, not a summary of the request. Tuned in an offline A/B
// experiment (2026-07, small reasoning models, real first user messages):
// bare rules left models parroting the user's request verbatim with a
// trailing question mark, while worked examples carry the target style.
// Keep the "rules + examples" shape when editing, and keep example inputs
// disjoint from real traffic so the model generalizes instead of copying.
//
// Deliberately no special case for meaningless input (a number, a sticker):
// the model still returns *something* topic-ish for it, and an empty result
// already falls back to the prompt-derived title — whereas a fixed
// placeholder string (the previous rule returned "新对话") collides with the
// UI's own untitled-session label and makes every such session
// indistinguishable in the sidebar.
const titleGenerationPrompt = `Generate a short title for this conversation, in the same language as the user's message.

Rules:
- A noun phrase naming the TOPIC, not a summary of the request.
- Chinese titles: 6-10 characters preferred; never shorter than 4 or longer than 12. English titles: 2-6 words.
- Keep concrete proper nouns (products, technologies, places).
- Prefer concrete specifics over abstract categories: name the actual thing (the damage, the tool, the symptom), not the class of problem.
- Vivid wording from the user's own message may be kept as-is; don't sand casual rants down into officialese.
- For two-entity topics in Chinese, use the "X与Y" form; in English, use "X and Y".
- For Chinese titles, allowed suffixes when one is needed: 分析/解析/介绍/建议/疑问/困惑/误解/修复/控制/设计/差异 — pick by topic nature, or use no suffix. English titles use plain noun phrases.
- No ending punctuation, no quotes.
- Return ONLY the title text.

Examples of the rules (do not copy their content):
- "烤箱预热要多久" → "烤箱预热时长" (concrete noun, no suffix)
- "为什么 Kubernetes 的滚动更新这么慢" → "K8s滚动更新缓慢分析" (technical, suffix 分析)
- "我到底是感冒还是过敏" → "感冒还是过敏性鼻炎" (keep the user's own contrast)
- "无糖可乐里的糖醇是不是更健康" → "无糖与糖醇区别" (two-entity X与Y form)
- "楼下的装修电钻吵了一整周" → "装修电钻噪声困扰" (casual complaint, vivid word kept)

User: `

// SessionService is the interface the application service uses for session metadata updates.
type SessionService interface {
	Get(ctx context.Context, sessionID string) (session.Thread, error)
	UpdateTitle(ctx context.Context, sessionID, title string) (session.Thread, error)
	UpdateMetadata(ctx context.Context, sessionID string, metadata map[string]any) (session.Thread, error)
}

// SetSessionService configures the session service used for auto title generation.
func (s *Service) SetSessionService(service SessionService) {
	s.sessionService = service
}

// SetEventPublisher configures the event publisher for broadcasting events
// such as session title updates.
func (s *Service) SetEventPublisher(p messageevent.Publisher) {
	s.eventPublisher = p
}

// maybeGenerateSessionTitle checks whether the session needs an auto-generated
// title and, if so, calls the configured title model to produce one.
// It is fired asynchronously when a user message is received so the title
// appears as early as possible without blocking the chat flow.
func (s *Service) maybeGenerateSessionTitle(ctx context.Context, req ChatRequest, userQuery string) {
	if req.SkipTitleGeneration {
		return
	}
	sessionID := strings.TrimSpace(req.ThreadID)
	if sessionID == "" || s.sessionService == nil {
		return
	}

	userQuery = strings.TrimSpace(userQuery)
	if userQuery == "" {
		return
	}

	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		s.logger.Warn("title gen: failed to get session", slog.String("session_id", sessionID), slog.Any("error", err))
		return
	}
	// Only generate a title when the session doesn't have one yet. Reading the
	// DB title (rather than an in-memory cache) keeps this guard restart-safe:
	// a session that already has an LLM-generated title is never re-run, even
	// after a server restart.
	if !shouldGenerateSessionTitle(sess) {
		return
	}

	promptTitle := fallbackSessionTitle(userQuery)

	// Persist a prompt-derived title immediately so a brand-new session is never
	// left "Untitled" — before (or without) the LLM title model. The sidebar
	// shows the user's first prompt right away; if a title model is configured
	// and the LLM succeeds below, it upgrades this.
	if strings.TrimSpace(sess.Title) == "" && promptTitle != "" {
		s.applyFallbackTitle(ctx, req, sessionID, userQuery)
	}

	titleModelID, ownerUserID, err := s.resolveTitleModel(ctx, req.BotID)
	if err != nil {
		s.logger.Warn("title gen: failed to load owner profile", slog.String("bot_id", req.BotID), slog.Any("error", err))
		return
	}
	if titleModelID == "" {
		s.logger.Debug("title gen: no title model configured", slog.String("bot_id", req.BotID), slog.String("owner_user_id", ownerUserID))
		return
	}

	s.logger.Info("title gen: generating title", slog.String("session_id", sessionID), slog.String("title_model_id", titleModelID))

	titleModel, provider, err := s.fetchChatModel(ctx, titleModelID)
	if err != nil {
		s.logger.Warn("title gen: failed to resolve title model", slog.String("model_id", titleModelID), slog.Any("error", err))
		return
	}

	title := s.generateTitle(ctx, ownerUserID, titleModel, provider, userQuery)
	if title == "" {
		return
	}

	if _, err := s.sessionService.UpdateTitle(ctx, sessionID, title); err != nil {
		s.logger.Warn("title gen: failed to update session title", slog.String("session_id", sessionID), slog.Any("error", err))
	} else {
		s.logger.Info("title gen: session title updated", slog.String("session_id", sessionID), slog.String("title", title))
		s.publishSessionTitleUpdated(req.BotID, sessionID, title)
	}
}

func (s *Service) resolveTitleModel(ctx context.Context, botID string) (modelID, ownerUserID string, err error) {
	if s.queries == nil || s.accountService == nil {
		return "", "", errors.New("title model profile dependencies are not configured")
	}
	parsedBotID, err := db.ParseUUID(botID)
	if err != nil {
		return "", "", err
	}
	bot, err := s.queries.GetBotByID(ctx, parsedBotID)
	if err != nil {
		return "", "", err
	}
	if !bot.OwnerUserID.Valid {
		return "", "", errors.New("bot owner is not configured")
	}
	ownerUserID = bot.OwnerUserID.String()
	account, err := s.accountService.Get(ctx, ownerUserID)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(account.TitleModelID), ownerUserID, nil
}

func shouldGenerateSessionTitle(sess session.Thread) bool {
	title := strings.TrimSpace(sess.Title)
	if title == "" {
		return true
	}
	if !session.IsACPRuntime(sess) {
		return false
	}

	agentID := metadataString(sess.Metadata, "acp_agent_id")
	normalized := acpprofile.NormalizeAgentID(agentID)
	if normalized == "" {
		return false
	}

	if strings.EqualFold(title, normalized) {
		return true
	}
	if profile, ok := acpprofile.Lookup(normalized); ok && strings.EqualFold(title, strings.TrimSpace(profile.DisplayName)) {
		return true
	}
	return false
}

const (
	// titlePromptOverheadTokens reserves budget for the fixed prompt text plus
	// protocol framing when sizing the user message against the title model's
	// context window. The prompt is ~400 tokens; the slack covers tokenizer
	// variance.
	titlePromptOverheadTokens = 512
	// titleInputFallbackWindow bounds the user message when the model's
	// context window is unknown (config absent). 8k is below virtually every
	// chat model's real window, so the request stays accepted; without any
	// bound a huge pasted message could overflow an unknown-but-small window
	// and the provider would reject the whole title call, silently leaving
	// the first-line fallback title in place.
	titleInputFallbackWindow = 8192
	// titleInputFloorRunes is the minimum sample kept even when the window
	// cannot cover it (a window smaller than the output reservation). Sending
	// something risks a rejection there, but sending nothing guarantees the
	// fallback title wins.
	titleInputFloorRunes = 1000
)

// boundTitleInput sizes the first user message against the title model's
// context window so prompt + input + the titleGenerateMaxTokens reservation
// always fits. Small-context title models exist (provider templates catalog
// 4096/4097-token ones), and an oversized request is rejected outright —
// which would leave the fallback title in place, defeating long-message
// support exactly on the long messages it exists for.
//
// Messages within budget pass through whole (the common case). Over-budget
// messages keep a head/tail sample — 2/3 head, 1/3 tail — because a pasted
// log or document may state its subject at either end; a pure head cut was
// the old 500-char bug. The rune budget assumes the worst case of ~1 token
// per rune (CJK-heavy text); English over-reserves, which errs safe.
func boundTitleInput(msg string, contextWindowTokens int) string {
	if msg == "" {
		return ""
	}
	if contextWindowTokens <= 0 {
		contextWindowTokens = titleInputFallbackWindow
	}
	budget := contextWindowTokens - titleGenerateMaxTokens - titlePromptOverheadTokens
	if budget < titleInputFloorRunes {
		budget = titleInputFloorRunes
	}
	runes := []rune(msg)
	if len(runes) <= budget {
		return msg
	}
	head := budget * 2 / 3
	tail := budget - head
	return string(runes[:head]) + "\n…\n" + string(runes[len(runes)-tail:])
}

func (s *Service) generateTitle(ctx context.Context, userID string, model models.GetResponse, provider sqlc.Provider, userQuery string) string {
	userSnippet := boundTitleInput(strings.TrimSpace(userQuery), model.Config.ContextBudgetMaxTokens())
	if userSnippet == "" {
		return ""
	}

	prompt := titleGenerationPrompt + userSnippet

	authService := providers.NewService(nil, s.queries, "")
	authCtx := oauthctx.WithUserID(ctx, userID)
	creds, err := authService.ResolveModelCredentials(authCtx, provider)
	if err != nil {
		s.logger.Warn("title gen: failed to resolve provider credentials", slog.Any("error", err))
		return ""
	}

	modelCfg := models.SDKModelConfig{
		ModelID:               model.ModelID,
		ClientType:            provider.ClientType,
		APIKey:                creds.APIKey,
		CodexAccountID:        creds.CodexAccountID,
		BaseURL:               providers.ProviderConfigString(provider, "base_url"),
		ChatCompletionsCompat: providers.ProviderConfigString(provider, models.ChatCompletionsCompatConfigKey),
	}
	sdkModel := models.NewSDKChatModel(modelCfg)

	genCtx, cancel := context.WithTimeout(ctx, titleGenerateTimeout)
	defer cancel()

	cacheTTL := providers.ProviderConfigString(provider, "prompt_cache_ttl")
	system, messages, _ := models.ApplyPromptCache(
		sdkModel, cacheTTL, "", []sdk.Message{sdk.UserMessage(prompt)}, nil,
	)

	client := sdk.NewClient()
	text, err := client.GenerateText(genCtx,
		sdk.WithModel(sdkModel),
		sdk.WithSystem(system),
		sdk.WithMessages(messages),
		sdk.WithMaxTokens(titleGenerateMaxTokens),
	)
	if err != nil {
		s.logger.Warn("title gen: LLM call failed", slog.Any("error", err))
		return ""
	}

	title := strings.TrimSpace(text)
	title = strings.Trim(title, "\"'`")
	title = strings.TrimSpace(title)
	return title
}

func (s *Service) publishSessionTitleUpdated(botID, sessionID, title string) {
	if s.eventPublisher == nil {
		return
	}
	data, err := json.Marshal(map[string]string{
		"session_id": sessionID,
		"title":      title,
	})
	if err != nil {
		return
	}
	s.eventPublisher.Publish(messageevent.Event{
		Type:  messageevent.EventTypeSessionTitleUpdated,
		BotID: botID,
		Data:  data,
	})
}

func truncate(s string, maxChars int) string {
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	return string(runes[:maxChars])
}

// fallbackTitleMaxRunes caps the prompt-derived fallback title. Mirrors the
// frontend provisionalSessionTitle so the value the client shows optimistically
// matches what the server persists (no flicker when the SSE lands).
const fallbackTitleMaxRunes = 50

// stripMarkdown removes structural markdown so a prompt-derived title reads as
// clean prose: fenced/inline code, images, links, heading/list/blockquote
// markers, emphasis, and table pipes. Mirrors the frontend stripMarkdown used by
// provisionalSessionTitle — keep them in lockstep so the persisted fallback
// equals the optimistic client display.
var (
	fallbackReFencedCode = regexp.MustCompile("(?s)```.*?```")
	fallbackReInlineCode = regexp.MustCompile("`([^`]+)`")
	fallbackReImage      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	fallbackReLink       = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	fallbackReHeading    = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	fallbackReBlockquote = regexp.MustCompile(`(?m)^\s*>+\s?`)
	fallbackReListUl     = regexp.MustCompile(`(?m)^\s*[-*+]\s+`)
	fallbackReListOl     = regexp.MustCompile(`(?m)^\s*\d+\.\s+`)
	fallbackReEmphasis   = regexp.MustCompile(`(\*\*|\*|__|~~)`)
	fallbackReTablePipe  = regexp.MustCompile(`\|`)
	fallbackReWhitespace = regexp.MustCompile(`\s+`)
)

func stripMarkdown(md string) string {
	md = fallbackReFencedCode.ReplaceAllString(md, " ")
	md = fallbackReInlineCode.ReplaceAllString(md, "$1")
	md = fallbackReImage.ReplaceAllString(md, "$1")
	md = fallbackReLink.ReplaceAllString(md, "$1")
	md = fallbackReHeading.ReplaceAllString(md, "")
	md = fallbackReBlockquote.ReplaceAllString(md, "")
	md = fallbackReListUl.ReplaceAllString(md, "")
	md = fallbackReListOl.ReplaceAllString(md, "")
	md = fallbackReEmphasis.ReplaceAllString(md, "")
	md = fallbackReTablePipe.ReplaceAllString(md, " ")
	return md
}

// fallbackSessionTitle derives a one-line sidebar title from a user's first
// message: strip markdown across the whole prompt (so a complete code fence is
// dropped, not just its opening line), take the first remaining line, collapse
// whitespace, and cap at fallbackTitleMaxRunes with an ellipsis. Returns "" when
// there is no usable text.
func fallbackSessionTitle(userQuery string) string {
	stripped := strings.TrimSpace(stripMarkdown(userQuery))
	if stripped == "" {
		return ""
	}
	firstLine := strings.SplitN(stripped, "\n", 2)[0]
	firstLine = strings.TrimSpace(fallbackReWhitespace.ReplaceAllString(firstLine, " "))
	if firstLine == "" {
		return ""
	}
	if truncated := truncate(firstLine, fallbackTitleMaxRunes); truncated != firstLine {
		return truncated + "…"
	}
	return firstLine
}

// applyFallbackTitle persists a prompt-derived title for a session that would
// otherwise stay untitled, then publishes it so clients replace the
// "Untitled" placeholder immediately. No-op when the prompt yields no usable
// text.
func (s *Service) applyFallbackTitle(ctx context.Context, req ChatRequest, sessionID, userQuery string) {
	title := fallbackSessionTitle(userQuery)
	if title == "" {
		return
	}
	if _, err := s.sessionService.UpdateTitle(ctx, sessionID, title); err != nil {
		s.logger.Warn("title gen: failed to apply fallback title", slog.String("session_id", sessionID), slog.Any("error", err))
		return
	}
	s.logger.Info("title gen: applied fallback title", slog.String("session_id", sessionID), slog.String("title", title))
	s.publishSessionTitleUpdated(req.BotID, sessionID, title)
}
