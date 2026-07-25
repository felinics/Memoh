package formation

import (
	_ "embed"
)

//go:embed prompts/memory_extract.md
var memoryExtractPrompt string

//go:embed prompts/memory_update.md
var memoryUpdatePrompt string
