package acp

import "github.com/memohai/memoh/internal/agent/runtime/acp/client"

// ApplyContext attaches one application's current-turn projection to an ACP
// prompt. ACP keeps its own prompt/resource/session wire format; callers only
// provide the selected values and do not assemble protocol fields themselves.
func (input *PromptInput) ApplyContext(
	prompt string,
	images []client.PromptImage,
	attachmentReferences []string,
	canFallbackImagesToFiles bool,
	contextURI string,
	contextMarkdown string,
) {
	if input == nil {
		return
	}
	input.Prompt = prompt
	input.Images = append([]client.PromptImage(nil), images...)
	input.AttachmentReferences = append([]string(nil), attachmentReferences...)
	input.CanFallbackImagesToFiles = canFallbackImagesToFiles
	input.ContextURI = contextURI
	input.ContextMarkdown = contextMarkdown
}
