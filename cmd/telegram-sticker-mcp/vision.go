package main

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	// zh-v2 is retained only to address descriptions already produced by the
	// legacy standalone recognizer. New descriptions use the profile supplied
	// by Memoh's first-party recognition API.
	stickerDescriptionPromptVersion = "zh-v2"
	stickerDescriptionBatchSize     = 1
	stickerDescriptionMaxAttempts   = 3
	maxStickerDescriptionRunes      = 50
)

type stickerVisionInput struct {
	ID       string
	Data     []byte
	MIMEType string
}

type stickerDescriber interface {
	Describe(context.Context, []stickerVisionInput) (map[string]string, error)
	Model() string
	PromptVersion() string
}

// cacheOnlyStickerDescriber preserves access to descriptions created by the
// legacy standalone vision path without making any model request. New and
// retried descriptions are supplied by Memoh's first-party management API.
type cacheOnlyStickerDescriber struct {
	model         string
	promptVersion string
}

func newCacheOnlyStickerDescriber(model string) *cacheOnlyStickerDescriber {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "legacy-unconfigured"
	}
	return &cacheOnlyStickerDescriber{model: model, promptVersion: stickerDescriptionPromptVersion}
}

func (*cacheOnlyStickerDescriber) Describe(context.Context, []stickerVisionInput) (map[string]string, error) {
	return nil, errors.New("standalone Sticker vision calls are disabled; use Memoh's first-party recognition API")
}

func (d *cacheOnlyStickerDescriber) Model() string         { return d.model }
func (d *cacheOnlyStickerDescriber) PromptVersion() string { return d.promptVersion }
func (*cacheOnlyStickerDescriber) CanDescribe() bool       { return false }

func normalizeStickerDescription(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'“”‘’`)
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= maxStickerDescriptionRunes {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:maxStickerDescriptionRunes]))
}
