package contextview

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/prune"
)

const (
	acpSectionsCollectorName = "acp_sections"
	acpSectionsSource        = "acp_context"
)

// ACPSection is one structurally assembled block of the ACP context resource.
type ACPSection struct {
	ID         string
	Text       string
	Kind       contextfrag.Kind
	Trust      contextfrag.TrustLevel
	Priority   int
	CacheClass contextfrag.CacheClass
	Budget     contextfrag.BudgetPolicy
}

type ACPSectionsConfig struct {
	Sections []ACPSection
}

// ACPSectionsCollector maps structurally assembled ACP sections to fragments
// without reparsing their Markdown content.
type ACPSectionsCollector struct{}

func (*ACPSectionsCollector) Name() string {
	return acpSectionsCollectorName
}

func (*ACPSectionsCollector) Collect(_ context.Context, req CollectRequest) ([]contextfrag.ContextFrag, error) {
	cfg, err := collectorConfig[ACPSectionsConfig](req.Config, "acp_sections config must be ACPSectionsConfig")
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
			id = fmt.Sprintf("acp.section.%03d", i)
		}
		kind := section.Kind
		if kind == "" {
			kind = contextfrag.KindACPContext
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
			Source:     acpSectionsSource,
			SourceID:   id,
			Collector:  acpSectionsCollectorName,
			Index:      i,
			Render:     contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
		}))
	}
	return frags, nil
}

// FinalizeACPContextMarkdown joins section blocks with the legacy ACP document
// spacing and bounds the complete resource.
func FinalizeACPContextMarkdown(blocks []string) string {
	return FinalizeACPContextMarkdownFromJoined(JoinACPContextBlocks(blocks))
}

// JoinACPContextBlocks joins section blocks with the legacy ACP document
// spacing without bounding the result.
func JoinACPContextBlocks(blocks []string) string {
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

// finalizeACPContextMarkdownWithAudit bounds the joined document and reports
// the pre-prune byte size so renderers can audit a raw head/tail cut that
// happened after selection.
// FinalizeACPContextMarkdownFromJoined bounds an already-joined document.
func FinalizeACPContextMarkdownFromJoined(joined string) string {
	return prune.PruneWithEdges(joined, "ACP context", prune.Config{
		MaxBytes:  64 * 1024,
		MaxLines:  1600,
		HeadBytes: 48 * 1024,
		TailBytes: 12 * 1024,
		HeadLines: 1200,
		TailLines: 300,
	})
}
