package contextview

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/prune"
)

const (
	runtimeSectionsCollectorName = "runtime_sections"
	runtimeSectionsSource        = "runtime_context"
)

// RuntimeSection is one structurally assembled block of the runtime context resource.
type RuntimeSection struct {
	ID         string
	Text       string
	Kind       contextfrag.Kind
	Trust      contextfrag.TrustLevel
	Priority   int
	CacheClass contextfrag.CacheClass
	Budget     contextfrag.BudgetPolicy
}

type RuntimeSectionsConfig struct {
	Sections []RuntimeSection
}

// RuntimeSectionsCollector maps structurally assembled runtime sections to fragments
// without reparsing their Markdown content.
type RuntimeSectionsCollector struct{}

func (*RuntimeSectionsCollector) Name() string {
	return runtimeSectionsCollectorName
}

func (*RuntimeSectionsCollector) Collect(_ context.Context, req CollectRequest) ([]contextfrag.ContextFrag, error) {
	cfg, err := collectorConfig[RuntimeSectionsConfig](req.Config, "runtime_sections config must be RuntimeSectionsConfig")
	if err != nil {
		return nil, err
	}
	frags := make([]contextfrag.ContextFrag, 0, len(cfg.Sections))
	for i, section := range cfg.Sections {
		text := strings.TrimSpace(section.Text)
		if text == "" {
			continue
		}
		id := strings.TrimSpace(section.ID)
		if id == "" {
			id = fmt.Sprintf("runtime.section.%03d", i)
		}
		kind := section.Kind
		if kind == "" {
			kind = contextfrag.KindRuntimeContext
		}
		trust := section.Trust
		if trust == "" {
			trust = contextfrag.TrustSystem
		}
		priority := section.Priority
		if priority == 0 {
			priority = 35
		}
		cacheClass := section.CacheClass
		if cacheClass == "" {
			cacheClass = contextfrag.CacheDynamic
		}
		frags = append(frags, contextfrag.TextFrag(contextfrag.TextFragInput{
			ID:         id,
			Kind:       kind,
			Role:       sdk.MessageRoleSystem,
			Slot:       contextfrag.SlotSystem,
			Text:       text,
			Priority:   priority,
			CacheClass: cacheClass,
			Trust:      trust,
			Budget:     section.Budget,
			Scope:      req.Scope,
			Source:     runtimeSectionsSource,
			SourceID:   id,
			Collector:  runtimeSectionsCollectorName,
			Index:      i,
			Render:     contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
		}))
	}
	return frags, nil
}

// FinalizeRuntimeContextMarkdown joins section blocks with the runtime context document
// spacing and bounds the complete resource.
func FinalizeRuntimeContextMarkdown(blocks []string) string {
	return FinalizeRuntimeContextMarkdownFromJoined(JoinRuntimeContextBlocks(blocks))
}

// JoinRuntimeContextBlocks joins section blocks with the runtime context document
// spacing without bounding the result.
func JoinRuntimeContextBlocks(blocks []string) string {
	var result strings.Builder
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		result.WriteString(block)
		result.WriteString("\n\n")
	}
	return result.String()
}

// finalizeRuntimeContextMarkdownWithAudit bounds the joined document and reports
// the pre-prune byte size so renderers can audit a raw head/tail cut that
// happened after selection.
// FinalizeRuntimeContextMarkdownFromJoined bounds an already-joined document.
func FinalizeRuntimeContextMarkdownFromJoined(joined string) string {
	return prune.PruneWithEdges(joined, "Runtime context", prune.Config{
		MaxBytes:  64 * 1024,
		MaxLines:  1600,
		HeadBytes: 48 * 1024,
		TailBytes: 12 * 1024,
		HeadLines: 1200,
		TailLines: 300,
	})
}
