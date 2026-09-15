package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/messaging"
	"github.com/felinics/memoh/internal/sticker"
)

// stickerSightingLookback is how far back save_sticker will look for the
// sticker being described. Deep enough to survive a few exchanges between
// seeing a sticker and deciding to keep it, shallow enough that "this one"
// still means something.
const stickerSightingLookback = 20

// StickerLibrary is the slice of the sticker domain the tools need.
type StickerLibrary interface {
	Save(ctx context.Context, botID string, entry sticker.Entry) (sticker.Entry, error)
	Search(ctx context.Context, botID, query string, limit int) ([]sticker.Entry, error)
	FindByRef(ctx context.Context, botID, ref string) (sticker.Entry, bool, error)
}

// StickerSightingReader resolves recently seen stickers in a conversation.
type StickerSightingReader interface {
	RecentStickerSightings(ctx context.Context, sessionID string, limit int, before time.Time) ([]dbstore.StickerSighting, error)
}

// StickerProvider exposes the bot's sticker library: save_sticker records what
// a sticker depicts, search_stickers finds one to send again.
//
// The two halves are deliberately split from sending. A sticker is sent with
// the ordinary send tool, so stickers stay subject to the same target
// validation and delivery path as everything else the bot says.
type StickerProvider struct {
	library   StickerLibrary
	sightings StickerSightingReader
	logger    *slog.Logger
	now       func() time.Time
}

func NewStickerProvider(log *slog.Logger, library StickerLibrary, sightings StickerSightingReader) *StickerProvider {
	if log == nil {
		log = slog.Default()
	}
	return &StickerProvider{
		library:   library,
		sightings: sightings,
		logger:    log.With(slog.String("tool", "sticker")),
		now:       time.Now,
	}
}

// Usage ties the library to the send tool: a search result is only useful if
// the model knows the shape of the send call it feeds.
func (*StickerProvider) Usage(_ context.Context, _ SessionContext, available AvailableTools) string {
	searchRef, ok := available.Ref(ToolSearchStickers())
	if !ok {
		return ""
	}
	parts := []string{
		"Use " + searchRef + " to find a sticker this bot has saved, then send it with the returned `platform_key`: `attachments: [{\"type\": \"sticker\", \"platform_key\": \"<platform_key>\"}]`.",
	}
	if saveRef, saveOK := available.Ref(ToolSaveSticker()); saveOK {
		parts = append(parts,
			"Use "+saveRef+" after seeing a sticker worth reusing. Describe what it depicts and the feeling it carries — that description is the only thing a later search can match on.",
			"Stickers are Telegram-only. A saved sticker cannot be sent on another platform.",
		)
	}
	return usageSection("Stickers", parts)
}

func (p *StickerProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	if p.library == nil {
		return nil, nil
	}
	sess := session
	// "The most recent sticker" has to mean the most recent one this turn could
	// actually see. Resolving it when the tool runs would read the newest row
	// at that instant instead — and in a group chat, messages from other people
	// are persisted whether or not they wake the bot, so a sticker arriving
	// while the model is still writing its description would steal it.
	visibleUntil := p.now()
	tools := []sdk.Tool{
		{
			Name: ToolSearchStickers().String(),
			Description: "Search this bot's saved stickers by description, emoji, or pack name. " +
				"Returns each match's `platform_key`, which sends the sticker via the send tool's attachments.",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Words to match against description, emoji, tags, and pack name. All words must match. Omit to list the library.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum results (default 10, max 50).",
					},
				},
				"required": []string{},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				args := inputAsMap(input)
				botID := strings.TrimSpace(sess.BotID)
				if botID == "" {
					return nil, errors.New("bot_id is required")
				}
				limit, _, err := IntArg(args, "limit")
				if err != nil {
					return nil, err
				}
				query := strings.TrimSpace(FirstStringArg(args, "query"))
				entries, err := p.library.Search(ctx.Context, botID, query, limit)
				if err != nil {
					return nil, err
				}
				items := make([]map[string]any, 0, len(entries))
				for _, entry := range entries {
					items = append(items, stickerResult(entry))
				}
				return map[string]any{
					"ok":       true,
					"count":    len(items),
					"stickers": items,
				}, nil
			},
		},
	}
	if p.sightings == nil {
		return tools, nil
	}
	return append(tools, sdk.Tool{
		Name: ToolSaveSticker().String(),
		Description: "Save a sticker this conversation has shown, with a description of what it depicts, so it can be found and sent later. " +
			"Describing the same sticker again replaces its description.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"description"},
			"properties": map[string]any{
				"description": map[string]any{
					"type":        "string",
					"description": "What the sticker depicts and what it is used to express. This is the only text a later search matches on.",
				},
				"platform_key": map[string]any{
					"type":        "string",
					"description": "Which sticker to save, from a search_stickers result. Omit to save the most recent sticker in this conversation.",
				},
				"tags": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional extra search terms, e.g. the mood or the character's name.",
				},
			},
		},
		Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
			args := inputAsMap(input)
			botID := strings.TrimSpace(sess.BotID)
			if botID == "" {
				return nil, errors.New("bot_id is required")
			}
			description := strings.TrimSpace(FirstStringArg(args, "description"))
			if description == "" {
				return nil, errors.New("description is required")
			}
			sessionID := strings.TrimSpace(sess.SessionID)
			if sessionID == "" {
				return nil, errors.New("session_id is required")
			}
			sightings, err := p.sightings.RecentStickerSightings(ctx.Context, sessionID, stickerSightingLookback, visibleUntil)
			if err != nil {
				return nil, err
			}
			requested := strings.TrimSpace(FirstStringArg(args, "platform_key"))
			target, err := p.resolveStickerTarget(ctx.Context, botID, sightings, requested)
			if err != nil {
				return nil, err
			}
			target.Tags = stringSliceArg(args, "tags")
			target.Description = description
			entry, err := p.library.Save(ctx.Context, botID, target)
			if err != nil {
				return nil, err
			}
			result := stickerResult(entry)
			result["ok"] = true
			result["saved_to"] = entry.Path
			return result, nil
		},
	}), nil
}

// resolveStickerTarget decides which sticker a save is about.
//
// A named key is looked up in the conversation first and in the library
// second — rewording a sticker saved weeks ago is an ordinary edit, while an
// invented key is not: the platform reference is what gets sent later, so
// storing one the bot never received would fail on first use.
func (p *StickerProvider) resolveStickerTarget(ctx context.Context, botID string, sightings []dbstore.StickerSighting, requested string) (sticker.Entry, error) {
	if requested == "" {
		if len(sightings) == 0 {
			return sticker.Entry{}, errors.New("no sticker has been seen in this conversation yet")
		}
		return entryFromSighting(sightings[0]), nil
	}
	for _, sighting := range sightings {
		if sighting.Ref == requested || (sighting.UniqueID != "" && sighting.UniqueID == requested) {
			return entryFromSighting(sighting), nil
		}
	}
	stored, found, err := p.library.FindByRef(ctx, botID, requested)
	if err != nil {
		return sticker.Entry{}, err
	}
	if found {
		return stored, nil
	}
	return sticker.Entry{}, fmt.Errorf("sticker %q is neither saved nor seen in this conversation; omit platform_key to save the most recent one", requested)
}

func entryFromSighting(sighting dbstore.StickerSighting) sticker.Entry {
	return sticker.Entry{
		Platform:    sticker.PlatformTelegram,
		Ref:         sighting.Ref,
		UniqueID:    sighting.UniqueID,
		Pack:        sighting.Pack,
		Emoji:       sighting.Emoji,
		Kind:        sighting.Kind,
		ContentHash: sighting.ContentHash,
	}
}

func stickerResult(entry sticker.Entry) map[string]any {
	platform := strings.TrimSpace(entry.Platform)
	if platform == "" {
		platform = sticker.PlatformTelegram
	}
	// source_platform travels with the key, and is not decoration: an
	// attachment that arrives without one is normalized to the platform it is
	// being sent on, which would present a Telegram handle to another platform
	// as one of its own.
	result := map[string]any{
		"platform_key":    entry.Ref,
		"source_platform": platform,
		"type":            string(messaging.AttachmentSticker),
		"description":     entry.Description,
	}
	if entry.Emoji != "" {
		result["emoji"] = entry.Emoji
	}
	if entry.Pack != "" {
		result["pack"] = entry.Pack
	}
	if entry.Kind != "" {
		result["kind"] = entry.Kind
	}
	if len(entry.Tags) > 0 {
		result["tags"] = entry.Tags
	}
	return result
}
