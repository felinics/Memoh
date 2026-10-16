package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"

	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/errs"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/supermarket"
)

const (
	starterPromptCount           = 3
	starterPromptMemoryLimit     = 20
	starterPromptMemoryMaxRunes  = 200
	starterPromptTopicLimit      = 10
	starterPromptAppDescMaxRunes = 160
	starterPromptTitleMaxRunes   = 40
	starterPromptTextMaxRunes    = 500
	starterPromptDefaultLocale   = "en"
	starterPromptAvoidLimit      = 10
)

// starterPromptsPrompt asks the title model for the new-chat suggestions. The
// material JSON is appended after it, and the hash covers the whole prompt,
// so editing this text also retires every cached set.
const starterPromptsPrompt = `You write conversation starters for the "new chat" screen of a personal AI assistant.

Suggest exactly 3 messages this user would plausibly want to send the assistant next. Ground them in the material below: what the assistant remembers, what the user talked about recently, and which Apps the assistant has. When the material is thin, infer from the assistant's name what it is for.

Rules:
- Write in the language of the locale in the material.
- "title": a short label for a card. Chinese or Japanese 4-10 characters; English 2-5 words.
- "prompt": the full message the user would send, in the user's own voice, one or two sentences.
- Make the three clearly different. Build on recent topics instead of repeating them.
- Never invent personal facts that are not in the material.
- Return ONLY a JSON array: [{"title": "...", "prompt": "..."}]

Material:
`

var starterPromptLocalePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// StarterPrompt is one suggested message for the new-chat screen.
type StarterPrompt struct {
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
}

// StarterPromptsRequest identifies whose suggestions to produce. KnownHash is
// the hash of the set the client already holds, empty when it holds none.
// Refresh asks for a different set even when the held one is current; Avoid
// lists the prompts already on screen, which the new set must not repeat.
type StarterPromptsRequest struct {
	BotID     string
	UserID    string
	Locale    string
	KnownHash string
	Refresh   bool
	Avoid     []string
}

// StarterPromptsResult is either Unchanged (the client's set still matches
// the material) or a fresh set with its Hash. A set the model could not
// produce comes back with an empty Hash and no Items; the client shows its
// generic examples and asks again next time.
type StarterPromptsResult struct {
	Unchanged bool
	Hash      string
	Items     []StarterPrompt
}

type starterPromptMaterial struct {
	Locale       string             `json:"locale"`
	Name         string             `json:"assistant_name"`
	DisplayName  string             `json:"assistant_display_name,omitempty"`
	Memories     []string           `json:"memories,omitempty"`
	RecentTopics []string           `json:"recent_topics,omitempty"`
	Apps         []starterPromptApp `json:"apps,omitempty"`
}

type starterPromptApp struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// StarterPrompts returns the new-chat suggestions for one user of one bot.
//
// Suggestions are a function of their material: the bot's memory, the user's
// recent session titles, the bot's installed Apps, its name, and the locale.
// Every call reads the material and hashes the prompt built from it. When the
// hash equals req.KnownHash the client's set is still current; otherwise a new
// set is generated on the spot. A refresh always generates, steering away
// from req.Avoid; the hash still describes the material, so the refreshed set
// is as current as the one it replaces. Nothing is stored server-side: the
// client keeps the set and its hash. Concurrent calls for the same material
// share one generation, which outlives a caller that goes away.
//
// The caller must have authorized req.UserID to chat with the bot.
func (s *Service) StarterPrompts(ctx context.Context, req StarterPromptsRequest) (StarterPromptsResult, error) {
	material, ownerUserID, err := s.collectStarterPromptMaterial(ctx, req)
	if err != nil {
		return StarterPromptsResult{}, err
	}
	prompt, err := buildStarterPromptsPrompt(material)
	if err != nil {
		return StarterPromptsResult{}, err
	}
	hash := starterPromptsHash(prompt)
	if !req.Refresh && hash == strings.TrimSpace(req.KnownHash) {
		return StarterPromptsResult{Unchanged: true, Hash: hash, Items: []StarterPrompt{}}, nil
	}

	key := req.BotID + "\x00" + req.UserID + "\x00" + hash
	if req.Refresh {
		avoid := starterPromptAvoidList(req.Avoid)
		prompt += starterPromptsAvoidSection(avoid)
		key += "\x00refresh\x00" + strings.Join(avoid, "\x00")
	}
	value, _, _ := s.starterPromptFlights.Do(key, func() (any, error) {
		return s.generateStarterPrompts(context.WithoutCancel(ctx), req.BotID, ownerUserID, prompt), nil
	})
	items, _ := value.([]StarterPrompt)
	if len(items) == 0 {
		return StarterPromptsResult{Items: []StarterPrompt{}}, nil
	}
	return StarterPromptsResult{Hash: hash, Items: items}, nil
}

// generateStarterPrompts runs the title model on prompt. Every failure, from
// a missing title model to unparsable output, yields nil: the caller treats
// that as "no suggestions this time".
func (s *Service) generateStarterPrompts(ctx context.Context, botID, ownerUserID, prompt string) []StarterPrompt {
	logger := s.logger.With(slog.String("bot_id", botID))
	account, err := s.accountService.Get(ctx, ownerUserID)
	if err != nil {
		logger.WarnContext(ctx, "starter prompts: load owner account failed", slog.Any("error", err))
		return nil
	}
	modelID := strings.TrimSpace(account.TitleModelID)
	if modelID == "" {
		logger.DebugContext(ctx, "starter prompts: no title model configured")
		return nil
	}
	model, provider, err := s.fetchChatModel(ctx, modelID)
	if err != nil {
		logger.WarnContext(ctx, "starter prompts: resolve title model failed", slog.Any("error", err))
		return nil
	}
	return s.completeStarterPrompts(ctx, logger, ownerUserID, model, provider, prompt)
}

func (s *Service) completeStarterPrompts(ctx context.Context, logger *slog.Logger, ownerUserID string, model models.GetResponse, provider sqlc.Provider, prompt string) []StarterPrompt {
	text, err := s.generateWithTitleModel(ctx, "agent.starter_prompts", ownerUserID, model, provider, prompt)
	if err != nil {
		logger.WarnContext(ctx, "starter prompts: generation failed", slog.Any("error", err))
		return nil
	}
	items := parseStarterPrompts(text)
	if len(items) == 0 {
		logger.WarnContext(ctx, "starter prompts: model output had no usable items", slog.Int("output_len", len(text)))
	}
	return items
}

// collectStarterPromptMaterial reads everything the suggestions depend on.
// Memory is best-effort: a memory read that fails is treated as no memory,
// which changes the hash, so the set is regenerated once memory reads again.
func (s *Service) collectStarterPromptMaterial(ctx context.Context, req StarterPromptsRequest) (starterPromptMaterial, string, error) {
	botUUID, err := db.ParseUUID(req.BotID)
	if err != nil {
		return starterPromptMaterial{}, "", errs.Wrap(err, "parse bot id")
	}
	userUUID, err := db.ParseUUID(req.UserID)
	if err != nil {
		return starterPromptMaterial{}, "", errs.Wrap(err, "parse user id")
	}
	bot, err := s.queries.GetBotByID(ctx, botUUID)
	if err != nil {
		return starterPromptMaterial{}, "", errs.Wrap(err, "get bot")
	}
	if !bot.OwnerUserID.Valid {
		return starterPromptMaterial{}, "", errs.New("bot owner is not configured")
	}

	material := starterPromptMaterial{
		Locale:      normalizeStarterPromptLocale(req.Locale),
		Name:        bot.Name,
		DisplayName: strings.TrimSpace(bot.DisplayName.String),
		Memories:    s.starterPromptMemories(ctx, req.BotID),
	}

	sessions, err := s.queries.ListSessionsByBotAndCreatedByUserPaged(ctx, sqlc.ListSessionsByBotAndCreatedByUserPagedParams{
		BotID:           botUUID,
		CreatedByUserID: userUUID,
		Types:           session.AllSessionTypes(),
		UseVisibility:   true,
		Visibility:      string(session.VisibilityUser),
		LimitCount:      starterPromptTopicLimit,
	})
	if err != nil {
		return starterPromptMaterial{}, "", errs.Wrap(err, "list recent sessions")
	}
	for _, row := range sessions {
		if title := strings.TrimSpace(row.Title); title != "" {
			material.RecentTopics = append(material.RecentTopics, title)
		}
	}

	installations, err := s.queries.ListBotAppInstallations(ctx, botUUID)
	if err != nil {
		return starterPromptMaterial{}, "", errs.Wrap(err, "list app installations")
	}
	for _, inst := range installations {
		if app, ok := starterPromptAppFrom(inst); ok {
			material.Apps = append(material.Apps, app)
		}
	}
	return material, bot.OwnerUserID.String(), nil
}

func (s *Service) starterPromptMemories(ctx context.Context, botID string) []string {
	provider := s.resolveMemoryProvider(ctx, botID)
	if provider == nil {
		return nil
	}
	resp, err := provider.GetAll(ctx, memprovider.GetAllRequest{
		BotID: botID,
		Limit: starterPromptMemoryLimit,
		Filters: map[string]any{
			"namespace": "bot",
			"scopeId":   botID,
			"bot_id":    botID,
		},
		NoStats: true,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "starter prompts: read memory failed", slog.String("bot_id", botID), slog.Any("error", err))
		return nil
	}
	memories := make([]string, 0, len(resp.Results))
	for _, item := range resp.Results {
		if text := strings.TrimSpace(item.Memory); text != "" {
			memories = append(memories, truncate(text, starterPromptMemoryMaxRunes))
		}
	}
	return memories
}

// starterPromptAppFrom describes a usable installed App by its cached release
// document, falling back to the App id when the release cannot be read.
func starterPromptAppFrom(inst sqlc.BotAppInstallation) (starterPromptApp, bool) {
	if inst.Status != "installed" && inst.Status != "updating" {
		return starterPromptApp{}, false
	}
	app := starterPromptApp{Name: inst.AppID}
	var release supermarket.AppDescriptor
	if len(inst.Release) > 0 && json.Unmarshal(inst.Release, &release) == nil {
		if name := strings.TrimSpace(release.Name); name != "" {
			app.Name = name
		}
		app.Description = truncate(strings.TrimSpace(release.Description), starterPromptAppDescMaxRunes)
	}
	return app, true
}

func normalizeStarterPromptLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if !starterPromptLocalePattern.MatchString(locale) {
		return starterPromptDefaultLocale
	}
	return locale
}

func buildStarterPromptsPrompt(material starterPromptMaterial) (string, error) {
	encoded, err := json.MarshalIndent(material, "", "  ")
	if err != nil {
		return "", errs.Wrap(err, "encode starter prompt material")
	}
	return starterPromptsPrompt + string(encoded), nil
}

// starterPromptAvoidList keeps the non-empty prompts of an avoid list,
// trimmed to what a suggestion can hold and capped in number.
func starterPromptAvoidList(prompts []string) []string {
	avoid := make([]string, 0, min(len(prompts), starterPromptAvoidLimit))
	for _, prompt := range prompts {
		if prompt = truncate(strings.TrimSpace(prompt), starterPromptTextMaxRunes); prompt != "" {
			avoid = append(avoid, prompt)
		}
		if len(avoid) == starterPromptAvoidLimit {
			break
		}
	}
	return avoid
}

// starterPromptsAvoidSection is appended to the prompt of a refresh. It is not
// part of the hash: it changes what is written, not what the set is about.
func starterPromptsAvoidSection(avoid []string) string {
	if len(avoid) == 0 {
		return ""
	}
	encoded, err := json.Marshal(avoid)
	if err != nil {
		return ""
	}
	return "\n\nThe user asked for different suggestions. These are already on screen; do not repeat them or their topics:\n" + string(encoded)
}

func starterPromptsHash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:8])
}

// parseStarterPrompts reads the model's JSON array, tolerating a code fence
// or prose around it. Items missing a title or prompt are dropped, and at
// most starterPromptCount are kept.
func parseStarterPrompts(text string) []StarterPrompt {
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end <= start {
		return nil
	}
	var raw []StarterPrompt
	if err := json.Unmarshal([]byte(text[start:end+1]), &raw); err != nil {
		return nil
	}
	items := make([]StarterPrompt, 0, starterPromptCount)
	for _, item := range raw {
		title := truncate(strings.TrimSpace(item.Title), starterPromptTitleMaxRunes)
		prompt := truncate(strings.TrimSpace(item.Prompt), starterPromptTextMaxRunes)
		if title == "" || prompt == "" {
			continue
		}
		items = append(items, StarterPrompt{Title: title, Prompt: prompt})
		if len(items) == starterPromptCount {
			break
		}
	}
	return items
}
