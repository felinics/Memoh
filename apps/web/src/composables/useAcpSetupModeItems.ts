import { computed, toValue, type MaybeRefOrGetter } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SegmentedItem } from '@felinic/ui'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import { acpSetupModeLabel, acpSetupModes } from '@/utils/acp/setup-fields'

export function useAcpSetupModeItems(profile: MaybeRefOrGetter<AcpprofilePublicProfile>) {
  const { t } = useI18n()

  const setupModeItems = computed<SegmentedItem<string>[]>(() =>
    acpSetupModes(toValue(profile)).map(mode => ({
      value: mode,
      label: acpSetupModeLabel(toValue(profile), mode, t),
    })),
  )

  return { setupModeItems, setupModes: () => acpSetupModes(toValue(profile)) }
}
