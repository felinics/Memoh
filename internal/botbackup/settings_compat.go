package botbackup

import (
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
		CompactionRatio  *int  `json:"compaction_ratio"`
		ReasoningEnabled *bool `json:"reasoning_enabled"`
	}
	if err := unmarshalJSON(raw, &legacy); err != nil {
		return settings.Settings{}, err
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
