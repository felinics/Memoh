package botbackup

import (
	"strings"

	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

func decodeBackupSettings(raw []byte) (settings.Settings, error) {
	var cfg settings.Settings
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := unmarshalJSON(raw, &cfg); err != nil {
		return settings.Settings{}, err
	}
	var legacy struct {
		CompactionRatio  *int   `json:"compaction_ratio"`
		ReasoningEnabled *bool  `json:"reasoning_enabled"`
		MemoryEnabled    *bool  `json:"memory_enabled"`
		MemoryProviderID string `json:"memory_provider_id"`
	}
	if err := unmarshalJSON(raw, &legacy); err != nil {
		return settings.Settings{}, err
	}
	// Archives written before the single memory switch carry the selected
	// provider instead. Keep its archive-local ID so resolveLegacyMemoryProvider
	// can tell a Built-in selection from a retired mem0/OpenViking one.
	if legacy.MemoryEnabled == nil {
		cfg.MemoryProviderID = strings.TrimSpace(legacy.MemoryProviderID)
		cfg.MemoryEnabled = cfg.MemoryProviderID != ""
	}
	// Archives written before bots dropped reasoning_enabled carry the on/off
	// state in a field Settings no longer decodes, so an archive with reasoning
	// off would otherwise import as "on" at whatever effort it stored. Only an
	// explicit false forces disable; a missing key leaves the archived tier alone.
	if legacy.ReasoningEnabled != nil && !*legacy.ReasoningEnabled {
		cfg.ReasoningEffort = models.ReasoningEffortDisable
	}
	if cfg.CompactionTargetPercent != nil || cfg.CompactionThreshold <= 0 {
		return cfg, nil
	}
	if legacy.CompactionRatio == nil {
		return cfg, nil
	}
	target := 100 - *legacy.CompactionRatio
	if target >= 1 && target <= 99 {
		cfg.CompactionTargetPercent = &target
	}
	return cfg, nil
}

// resolveLegacyMemoryProvider finishes the memory switch for pre-switch
// archives: a selection of a retired external provider (mem0/OpenViking) was
// already a no-op, so it imports as memory off. The archive-local provider ID
// never survives into the target bot's settings.
func resolveLegacyMemoryProvider(state *importState, cfg settings.Settings) settings.Settings {
	legacyID := cfg.MemoryProviderID
	cfg.MemoryProviderID = ""
	if legacyID == "" {
		return cfg
	}
	providers, _ := readEntry[[]struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
	}](state, "dependencies/memory_providers.json")
	for _, provider := range providers {
		if provider.ID == legacyID && provider.Provider != string(memprovider.ProviderBuiltin) {
			cfg.MemoryEnabled = false
		}
	}
	return cfg
}
