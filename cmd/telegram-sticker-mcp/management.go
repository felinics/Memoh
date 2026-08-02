package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	descriptionStatusPending  = "pending"
	defaultStickerSearchLimit = 8
	maxStickerSearchLimit     = 20
)

type stickerCatalogEntry struct {
	ID          string `json:"id"`
	Emoji       string `json:"emoji,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts,omitempty"`
}

type stickerCatalogView struct {
	Name         string                  `json:"name"`
	TotalCount   int                     `json:"total_count"`
	ReadyCount   int                     `json:"ready_count"`
	FailedCount  int                     `json:"failed_count"`
	PendingCount int                     `json:"pending_count"`
	Stickers     []stickerCatalogEntry   `json:"stickers"`
	Sets         []stickerCatalogSetView `json:"sets,omitempty"`
}

type stickerCatalogSetView struct {
	Name         string                `json:"name"`
	TotalCount   int                   `json:"total_count"`
	ReadyCount   int                   `json:"ready_count"`
	FailedCount  int                   `json:"failed_count"`
	PendingCount int                   `json:"pending_count"`
	Stickers     []stickerCatalogEntry `json:"stickers"`
}

type stickerSearchResult struct {
	ID          string `json:"id"`
	Emoji       string `json:"emoji,omitempty"`
	Description string `json:"description"`
	Score       int    `json:"-"`
}

func (s *stickerService) Catalog(ctx context.Context) (stickerCatalogView, error) {
	if len(s.sets) > 0 {
		view := stickerCatalogView{Name: s.setName, Sets: make([]stickerCatalogSetView, 0, len(s.sets))}
		for _, set := range s.sets {
			setView, err := set.Catalog(ctx)
			if err != nil {
				return stickerCatalogView{}, err
			}
			for index := range setView.Stickers {
				setView.Stickers[index].ID = collectionStickerID(set.setName, setView.Stickers[index].ID)
			}
			view.TotalCount += setView.TotalCount
			view.ReadyCount += setView.ReadyCount
			view.FailedCount += setView.FailedCount
			view.PendingCount += setView.PendingCount
			view.Stickers = append(view.Stickers, setView.Stickers...)
			view.Sets = append(view.Sets, stickerCatalogSetView{
				Name: setView.Name, TotalCount: setView.TotalCount,
				ReadyCount: setView.ReadyCount, FailedCount: setView.FailedCount,
				PendingCount: setView.PendingCount, Stickers: setView.Stickers,
			})
		}
		sort.Slice(view.Stickers, func(i, j int) bool { return view.Stickers[i].ID < view.Stickers[j].ID })
		return view, nil
	}
	set, err := s.getStickerSet(ctx)
	if err != nil {
		return stickerCatalogView{}, err
	}
	view := stickerCatalogView{
		Name:       set.Name,
		TotalCount: len(set.Stickers),
		Stickers:   make([]stickerCatalogEntry, 0, len(set.Stickers)),
	}
	profile, _, err := s.descriptionProfile(ctx)
	if err != nil {
		return stickerCatalogView{}, err
	}
	for index, sticker := range set.Stickers {
		entry := stickerCatalogEntry{
			ID:     candidateID(index),
			Emoji:  strings.TrimSpace(sticker.Emoji),
			Status: descriptionStatusPending,
		}
		cached, found, cacheErr := s.catalog.cache.Get(
			ctx,
			sticker.FileUniqueID,
			profile.Model,
			profile.PromptVersion,
		)
		if cacheErr != nil {
			return stickerCatalogView{}, cacheErr
		}
		if found {
			entry.Status = cached.Status
			entry.Description = cached.Description
			entry.Attempts = cached.Attempts
		}
		switch entry.Status {
		case descriptionStatusReady:
			view.ReadyCount++
		case descriptionStatusFailed:
			view.FailedCount++
		default:
			view.PendingCount++
		}
		view.Stickers = append(view.Stickers, entry)
	}
	if view.PendingCount > 0 && s.canWarmDescriptions(ctx) {
		s.WarmDescriptions()
	}
	return view, nil
}

func (s *stickerService) ActivateDescriptionProfile(ctx context.Context, model, promptVersion string) error {
	if len(s.sets) > 0 {
		for _, set := range s.sets {
			if err := set.ActivateDescriptionProfile(ctx, model, promptVersion); err != nil {
				return err
			}
		}
		return nil
	}
	return s.catalog.cache.PutProfile(ctx, s.setMetadataKey, stickerDescriptionProfile{
		Model: model, PromptVersion: promptVersion,
	})
}

func (s *stickerService) StoreDescription(
	ctx context.Context,
	stickerID, model, promptVersion, description string,
) (stickerCatalogEntry, error) {
	if len(s.sets) > 0 {
		set, localID, err := s.collectionSticker(stickerID)
		if err != nil {
			return stickerCatalogEntry{}, err
		}
		entry, err := set.StoreDescription(ctx, localID, model, promptVersion, description)
		entry.ID = collectionStickerID(set.setName, entry.ID)
		return entry, err
	}
	_, sticker, err := s.stickerByID(ctx, stickerID)
	if err != nil {
		return stickerCatalogEntry{}, err
	}
	model = strings.TrimSpace(model)
	promptVersion = strings.TrimSpace(promptVersion)
	description = normalizeStickerDescription(description)
	if model == "" || promptVersion == "" || description == "" {
		return stickerCatalogEntry{}, errors.New("model, prompt_version and description are required")
	}
	if err := s.catalog.cache.Put(ctx, sticker.FileUniqueID, model, promptVersion, cachedStickerDescription{
		Description: description,
		Status:      descriptionStatusReady,
		Attempts:    1,
	}); err != nil {
		return stickerCatalogEntry{}, err
	}
	if err := s.ActivateDescriptionProfile(ctx, model, promptVersion); err != nil {
		return stickerCatalogEntry{}, err
	}
	return stickerCatalogEntry{
		ID: normalizeStickerID(stickerID), Emoji: strings.TrimSpace(sticker.Emoji),
		Description: description, Status: descriptionStatusReady, Attempts: 1,
	}, nil
}

func (s *stickerService) StickerMedia(ctx context.Context, stickerID string) ([]byte, string, error) {
	if len(s.sets) > 0 {
		set, localID, err := s.collectionSticker(stickerID)
		if err != nil {
			return nil, "", err
		}
		return set.StickerMedia(ctx, localID)
	}
	_, sticker, err := s.stickerByID(ctx, stickerID)
	if err != nil {
		return nil, "", err
	}
	return s.api.DownloadStickerMedia(ctx, sticker)
}

func (s *stickerService) RetryDescription(ctx context.Context, stickerID string) (stickerCatalogEntry, error) {
	if len(s.sets) > 0 {
		set, localID, err := s.collectionSticker(stickerID)
		if err != nil {
			return stickerCatalogEntry{}, err
		}
		entry, err := set.RetryDescription(ctx, localID)
		entry.ID = collectionStickerID(set.setName, entry.ID)
		return entry, err
	}
	set, sticker, err := s.stickerByID(ctx, stickerID)
	if err != nil {
		return stickerCatalogEntry{}, err
	}
	if err := s.catalog.cache.Delete(
		ctx,
		sticker.FileUniqueID,
		s.catalog.describer.Model(),
		s.catalog.describer.PromptVersion(),
	); err != nil {
		return stickerCatalogEntry{}, err
	}
	if _, err := s.catalog.DescribeSet(ctx, s.api, set); err != nil {
		return stickerCatalogEntry{}, err
	}
	cached, found, err := s.catalog.cache.Get(
		ctx,
		sticker.FileUniqueID,
		s.catalog.describer.Model(),
		s.catalog.describer.PromptVersion(),
	)
	if err != nil {
		return stickerCatalogEntry{}, err
	}
	if !found || cached.Status != descriptionStatusReady || strings.TrimSpace(cached.Description) == "" {
		return stickerCatalogEntry{}, errors.New("sticker visual recognition failed")
	}
	return stickerCatalogEntry{
		ID:          normalizeStickerID(stickerID),
		Emoji:       strings.TrimSpace(sticker.Emoji),
		Description: cached.Description,
		Status:      cached.Status,
		Attempts:    cached.Attempts,
	}, nil
}

func (s *stickerService) Search(ctx context.Context, query string, limit int) ([]stickerSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	if limit <= 0 {
		limit = defaultStickerSearchLimit
	}
	limit = min(limit, maxStickerSearchLimit)
	if len(s.sets) > 0 {
		results := make([]stickerSearchResult, 0)
		for _, set := range s.sets {
			setResults, err := set.Search(ctx, query, maxStickerSearchLimit)
			if err != nil {
				continue
			}
			for index := range setResults {
				setResults[index].ID = collectionStickerID(set.setName, setResults[index].ID)
			}
			results = append(results, setResults...)
		}
		sort.SliceStable(results, func(i, j int) bool {
			if results[i].Score == results[j].Score {
				return results[i].ID < results[j].ID
			}
			return results[i].Score > results[j].Score
		})
		if len(results) > limit {
			results = results[:limit]
		}
		if len(results) == 0 {
			return nil, errors.New("sticker descriptions are not ready yet")
		}
		return results, nil
	}
	set, err := s.getStickerSet(ctx)
	if err != nil {
		return nil, err
	}
	profile, _, err := s.descriptionProfile(ctx)
	if err != nil {
		return nil, err
	}
	stickers, err := s.catalog.CachedSet(ctx, set, profile.Model, profile.PromptVersion)
	if err != nil {
		return nil, err
	}
	if len(stickers) == 0 {
		s.WarmDescriptions()
		return nil, errors.New("sticker descriptions are not ready yet")
	}
	results := make([]stickerSearchResult, 0, len(stickers))
	for _, sticker := range stickers {
		results = append(results, stickerSearchResult{
			ID:          sticker.ID,
			Emoji:       sticker.Emoji,
			Description: sticker.Description,
			Score:       scoreSticker(query, sticker.Description, sticker.Emoji),
		})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].ID < results[j].ID
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *stickerService) stickerByID(ctx context.Context, stickerID string) (telegramStickerSet, telegramSticker, error) {
	stickerID = normalizeStickerID(stickerID)
	if stickerID == "" {
		return telegramStickerSet{}, telegramSticker{}, errors.New("sticker_id is required")
	}
	set, err := s.getStickerSet(ctx)
	if err != nil {
		return telegramStickerSet{}, telegramSticker{}, err
	}
	for index, sticker := range set.Stickers {
		if candidateID(index) == stickerID {
			return set, sticker, nil
		}
	}
	return telegramStickerSet{}, telegramSticker{}, fmt.Errorf("sticker_id %q was not found in set %q", stickerID, set.Name)
}

func normalizeStickerID(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func scoreSticker(query, description, emoji string) int {
	query = normalizeStickerSearchText(query)
	candidate := normalizeStickerSearchText(description + " " + emoji)
	if query == "" || candidate == "" {
		return 0
	}
	score := 0
	if strings.Contains(candidate, query) {
		score += 1000 + len([]rune(query))*20
	}
	for _, token := range strings.Fields(query) {
		if strings.Contains(candidate, token) {
			score += 200 + len([]rune(token))*10
		}
	}
	queryRunes := []rune(strings.ReplaceAll(query, " ", ""))
	candidateRunes := []rune(strings.ReplaceAll(candidate, " ", ""))
	candidateSet := make(map[rune]struct{}, len(candidateRunes))
	for _, r := range candidateRunes {
		candidateSet[r] = struct{}{}
	}
	for _, r := range queryRunes {
		if _, ok := candidateSet[r]; ok {
			score += 8
		}
	}
	candidateBigrams := runeNgrams(candidateRunes, 2)
	for bigram := range runeNgrams(queryRunes, 2) {
		if _, ok := candidateBigrams[bigram]; ok {
			score += 30
		}
	}
	return score
}

func normalizeStickerSearchText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	}), " ")
}

func runeNgrams(runes []rune, size int) map[string]struct{} {
	result := map[string]struct{}{}
	if size <= 0 || len(runes) < size {
		return result
	}
	for i := 0; i+size <= len(runes); i++ {
		result[string(runes[i:i+size])] = struct{}{}
	}
	return result
}
