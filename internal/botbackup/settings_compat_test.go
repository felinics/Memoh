package botbackup

import (
	"testing"

	"github.com/felinics/memoh/internal/models"
)

func TestDecodeBackupSettingsCompactionRatioCompatibility(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		raw        string
		wantTarget *int
	}{
		{
			name:       "legacy manual ratio maps to keep share",
			raw:        `{"compaction_threshold":100000,"compaction_ratio":80}`,
			wantTarget: backupIntPointer(20),
		},
		{
			name:       "legacy minimum ratio maps to maximum target",
			raw:        `{"compaction_threshold":100000,"compaction_ratio":1}`,
			wantTarget: backupIntPointer(99),
		},
		{
			name: "zero config ignores its legacy dead ratio",
			raw:  `{"compaction_threshold":0,"compaction_ratio":80}`,
		},
		{
			name: "legacy ratio producing invalid target keeps default",
			raw:  `{"compaction_threshold":100000,"compaction_ratio":100}`,
		},
		{
			name:       "new target wins",
			raw:        `{"compaction_threshold":100000,"compaction_ratio":80,"compaction_target_percent":55}`,
			wantTarget: backupIntPointer(55),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodeBackupSettings([]byte(tc.raw))
			if err != nil {
				t.Fatalf("decodeBackupSettings() error = %v", err)
			}
			if !equalBackupInt(got.CompactionTargetPercent, tc.wantTarget) {
				t.Fatalf("CompactionTargetPercent = %v, want %v", got.CompactionTargetPercent, tc.wantTarget)
			}
		})
	}
}

func backupIntPointer(value int) *int {
	return &value
}

func equalBackupInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func TestDecodeBackupSettingsReasoningEnabledCompatibility(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "legacy off forces disable over the archived tier",
			raw:  `{"reasoning_enabled":false,"reasoning_effort":"high"}`,
			want: models.ReasoningEffortDisable,
		},
		{
			name: "legacy on keeps the archived tier",
			raw:  `{"reasoning_enabled":true,"reasoning_effort":"high"}`,
			want: models.ReasoningEffortHigh,
		},
		{
			name: "archive without the retired flag is untouched",
			raw:  `{"reasoning_effort":"low"}`,
			want: models.ReasoningEffortLow,
		},
		{
			name: "current archive can carry disable directly",
			raw:  `{"reasoning_effort":"disable"}`,
			want: models.ReasoningEffortDisable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodeBackupSettings([]byte(tc.raw))
			if err != nil {
				t.Fatalf("decodeBackupSettings() error = %v", err)
			}
			if got.ReasoningEffort != tc.want {
				t.Fatalf("ReasoningEffort = %q, want %q", got.ReasoningEffort, tc.want)
			}
		})
	}
}

func TestSettingsLabelsReasoningState(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "legacy off", raw: `{"reasoning_enabled":false,"reasoning_effort":"high"}`, want: "reasoning: off"},
		{name: "legacy on", raw: `{"reasoning_enabled":true,"reasoning_effort":"high"}`, want: "reasoning: on"},
		{name: "current disable", raw: `{"reasoning_effort":"disable"}`, want: "reasoning: off"},
		{name: "current tier", raw: `{"reasoning_effort":"medium"}`, want: "reasoning: on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			labels := settingsLabels([]byte(tc.raw))
			found := false
			for _, l := range labels {
				if l == tc.want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("labels %v missing %q", labels, tc.want)
			}
		})
	}
}
