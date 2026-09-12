package message

import (
	"context"
	"encoding/json"
	"maps"
	"strings"

	"github.com/felinics/memoh/internal/markdownmedia"
)

func (s *DBService) SetMarkdownMediaResolver(resolve markdownmedia.Resolver) {
	s.resolveMarkdownMedia = resolve
}

func (s *DBService) prepareMarkdownMedia(ctx context.Context, input PersistInput) PersistInput {
	if s == nil || s.resolveMarkdownMedia == nil || input.Role != "assistant" || input.SessionMode != "chat" || input.Metadata[AgentStepInterruptedMetadataKey] == true {
		return input
	}
	if _, prepared := input.Metadata[markdownmedia.MetadataKey]; prepared {
		return input
	}
	source := strings.TrimSpace(markdownSource(input))
	bindings := s.resolveMarkdownMedia(ctx, input.BotID, source)
	if len(bindings) == 0 {
		return input
	}
	input.Metadata = maps.Clone(input.Metadata)
	if input.Metadata == nil {
		input.Metadata = map[string]any{}
	}
	input.Metadata[markdownmedia.MetadataKey] = bindings
	input.Metadata["markdown_media_source"] = source
	input.Metadata["markdown_media_run_id"] = input.RunID
	input.Assets = append([]AssetRef(nil), input.Assets...)
	seen := map[string]bool{}
	for _, existing := range input.Assets {
		seen[existing.ContentHash] = true
	}
	for _, binding := range bindings {
		asset := binding.Asset
		if binding.ErrorCode != "" || seen[asset.ContentHash] {
			continue
		}
		seen[asset.ContentHash] = true
		name := strings.TrimSpace(binding.Label)
		if name == "" {
			name = markdownmedia.Filename(binding.Target)
		}
		input.Assets = append(input.Assets, AssetRef{ContentHash: asset.ContentHash, Role: "markdown", Ordinal: len(input.Assets), Mime: asset.Mime, SizeBytes: asset.SizeBytes, StorageKey: asset.StorageKey, Name: name, Metadata: map[string]any{"source_path": binding.Target}})
	}
	return input
}

func markdownSource(input PersistInput) string {
	if input.DisplayText != "" {
		return input.DisplayText
	}
	return storedText(input.Content)
}

func storedText(raw json.RawMessage) string {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) == nil {
		var result strings.Builder
		for _, part := range parts {
			result.WriteString(storedText(part))
		}
		return result.String()
	}
	var node struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &node) != nil {
		return ""
	}
	if node.Type == "text" {
		return node.Text
	}
	if node.Type == "" && len(node.Content) > 0 {
		return storedText(node.Content)
	}
	return ""
}
