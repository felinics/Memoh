package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

type describedSticker struct {
	ID           string
	FileID       string
	FileUniqueID string
	Emoji        string
	Description  string
}

type stickerCatalog struct {
	cache     *stickerDescriptionCache
	describer stickerDescriber

	mu sync.Mutex
}

func (c *stickerCatalog) canDescribe() bool {
	capability, ok := c.describer.(interface{ CanDescribe() bool })
	return !ok || capability.CanDescribe()
}

func newStickerCatalog(
	cache *stickerDescriptionCache,
	describer stickerDescriber,
) (*stickerCatalog, error) {
	if cache == nil {
		return nil, errors.New("sticker description cache is required")
	}
	if describer == nil {
		return nil, errors.New("sticker describer is required")
	}
	return &stickerCatalog{cache: cache, describer: describer}, nil
}

func (c *stickerCatalog) DescribeSet(
	ctx context.Context,
	api telegramAPI,
	set telegramStickerSet,
) ([]describedSticker, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	type pendingSticker struct {
		sticker telegramSticker
	}
	descriptions := make(map[string]string, len(set.Stickers))
	pending := make([]pendingSticker, 0)
	for _, sticker := range set.Stickers {
		if strings.TrimSpace(sticker.FileID) == "" || strings.TrimSpace(sticker.FileUniqueID) == "" {
			continue
		}
		entry, found, err := c.cache.Get(
			ctx,
			sticker.FileUniqueID,
			c.describer.Model(),
			c.describer.PromptVersion(),
		)
		if err != nil {
			return nil, err
		}
		if !found {
			pending = append(pending, pendingSticker{sticker: sticker})
			continue
		}
		if entry.Status == descriptionStatusReady && entry.Description != "" {
			descriptions[sticker.FileUniqueID] = entry.Description
		}
	}

	for start := 0; start < len(pending); start += stickerDescriptionBatchSize {
		end := min(start+stickerDescriptionBatchSize, len(pending))
		batch := pending[start:end]
		visionInputs := make([]stickerVisionInput, 0, len(batch))
		byInputID := make(map[string]pendingSticker, len(batch))
		for batchIndex, item := range batch {
			inputID := fmt.Sprintf("S%02d", batchIndex+1)
			data, mimeType, attempts, err := downloadStickerMediaWithRetry(ctx, api, item.sticker)
			if err != nil {
				if cacheErr := c.cacheFailure(ctx, item.sticker.FileUniqueID, attempts); cacheErr != nil {
					return nil, cacheErr
				}
				log.Printf("skip sticker %s: media download failed: %v", candidateID(item.sticker.FileUniqueID), err)
				continue
			}
			visionInputs = append(visionInputs, stickerVisionInput{
				ID:       inputID,
				Data:     data,
				MIMEType: mimeType,
			})
			byInputID[inputID] = item
		}
		generated, attempts, describeErr := c.describeWithRetry(ctx, visionInputs)
		for inputID, item := range byInputID {
			description := normalizeStickerDescription(generated[inputID])
			status := descriptionStatusReady
			if describeErr != nil || description == "" {
				status = descriptionStatusFailed
				description = ""
			}
			if err := c.putCacheEntry(
				ctx,
				item.sticker.FileUniqueID,
				cachedStickerDescription{
					Description: description,
					Status:      status,
					Attempts:    attempts,
				},
			); err != nil {
				return nil, err
			}
			if status == descriptionStatusReady {
				descriptions[item.sticker.FileUniqueID] = description
			}
		}
		if describeErr != nil {
			log.Printf(
				"vision description batch for sticker set %s failed and was cached: %v",
				set.Name,
				describeErr,
			)
		}
	}

	result := make([]describedSticker, 0, len(descriptions))
	for _, sticker := range set.Stickers {
		description := descriptions[sticker.FileUniqueID]
		if description == "" {
			continue
		}
		result = append(result, describedSticker{
			ID:           candidateID(sticker.FileUniqueID),
			FileID:       sticker.FileID,
			FileUniqueID: sticker.FileUniqueID,
			Emoji:        strings.TrimSpace(sticker.Emoji),
			Description:  description,
		})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("sticker set %q has no successfully described stickers", set.Name)
	}
	return result, nil
}

func (c *stickerCatalog) CachedSet(
	ctx context.Context,
	set telegramStickerSet,
	model string,
	promptVersion string,
) ([]describedSticker, error) {
	result := make([]describedSticker, 0, len(set.Stickers))
	for _, sticker := range set.Stickers {
		if strings.TrimSpace(sticker.FileID) == "" || strings.TrimSpace(sticker.FileUniqueID) == "" {
			continue
		}
		entry, found, err := c.cache.Get(
			ctx,
			sticker.FileUniqueID,
			model,
			promptVersion,
		)
		if err != nil {
			return nil, err
		}
		if !found || entry.Status != descriptionStatusReady || entry.Description == "" {
			continue
		}
		result = append(result, describedSticker{
			ID:           candidateID(sticker.FileUniqueID),
			FileID:       sticker.FileID,
			FileUniqueID: sticker.FileUniqueID,
			Emoji:        strings.TrimSpace(sticker.Emoji),
			Description:  entry.Description,
		})
	}
	return result, nil
}

func (c *stickerCatalog) describeWithRetry(
	ctx context.Context,
	inputs []stickerVisionInput,
) (map[string]string, int, error) {
	var lastErr error
	for attempt := 1; attempt <= stickerDescriptionMaxAttempts; attempt++ {
		generated, err := c.describer.Describe(ctx, inputs)
		if err == nil {
			for _, input := range inputs {
				if normalizeStickerDescription(generated[input.ID]) == "" {
					err = fmt.Errorf("vision response omitted description for %s", input.ID)
					break
				}
			}
		}
		if err == nil {
			return generated, attempt, nil
		}
		lastErr = err
		if ctx.Err() != nil || attempt == stickerDescriptionMaxAttempts {
			return nil, attempt, lastErr
		}
		if err := waitForStickerRetry(ctx, time.Duration(attempt)*500*time.Millisecond); err != nil {
			return nil, attempt, lastErr
		}
	}
	return nil, stickerDescriptionMaxAttempts, lastErr
}

func downloadStickerMediaWithRetry(
	ctx context.Context,
	api telegramAPI,
	sticker telegramSticker,
) ([]byte, string, int, error) {
	var lastErr error
	for attempt := 1; attempt <= stickerDescriptionMaxAttempts; attempt++ {
		data, mimeType, err := api.DownloadStickerMedia(ctx, sticker)
		if err == nil {
			return data, mimeType, attempt, nil
		}
		lastErr = err
		if ctx.Err() != nil || attempt == stickerDescriptionMaxAttempts {
			return nil, "", attempt, lastErr
		}
		if err := waitForStickerRetry(ctx, time.Duration(attempt)*500*time.Millisecond); err != nil {
			return nil, "", attempt, lastErr
		}
	}
	return nil, "", stickerDescriptionMaxAttempts, lastErr
}

func waitForStickerRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *stickerCatalog) cacheFailure(
	ctx context.Context,
	fileUniqueID string,
	attempts int,
) error {
	return c.putCacheEntry(
		ctx,
		fileUniqueID,
		cachedStickerDescription{
			Status:   descriptionStatusFailed,
			Attempts: attempts,
		},
	)
}

func (c *stickerCatalog) putCacheEntry(
	ctx context.Context,
	fileUniqueID string,
	entry cachedStickerDescription,
) error {
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return c.cache.Put(
		cacheCtx,
		fileUniqueID,
		c.describer.Model(),
		c.describer.PromptVersion(),
		entry,
	)
}

func candidateID(fileUniqueID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(fileUniqueID)))
	return "S" + strings.ToUpper(hex.EncodeToString(sum[:8]))
}
