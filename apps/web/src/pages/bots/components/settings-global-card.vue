<!-- eslint-disable vue/no-mutating-props -->
<template>
  <SettingsSection :title="$t('bots.settings.blocks.global')">
    <SettingsRow :label="$t('bots.settings.chatLanguage')">
      <div class="w-52">
        <SearchableSelectPopover
          :model-value="form.language || 'auto'"
          :options="languageOptions"
          :placeholder="$t('bots.settings.chatLanguagePlaceholder')"
          :search-placeholder="$t('common.search')"
          :empty-text="$t('common.noData')"
          :show-group-headers="false"
          popover-class="min-w-[var(--reka-popover-trigger-width)] w-80"
          popover-align="end"
          @update:model-value="(v) => form.language = (v === 'auto' ? '' : (v as string))"
        />
      </div>
    </SettingsRow>

    <SettingsRow :label="$t('bots.timezone')">
      <div class="w-52">
        <TimezoneSelect
          :model-value="form.timezone || emptyTimezoneValue"
          :placeholder="$t('bots.timezonePlaceholder')"
          allow-empty
          :empty-label="$t('bots.timezoneInherited')"
          popover-class="min-w-[var(--reka-popover-trigger-width)] w-72"
          popover-align="end"
          @update:model-value="(val: string) => form.timezone = val === emptyTimezoneValue ? '' : val"
        />
      </div>
    </SettingsRow>
  </SettingsSection>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { SettingsRow, SettingsSection } from '@felinic/ui'
import TimezoneSelect from '@/components/timezone-select/index.vue'
import SearchableSelectPopover from '@/components/searchable-select-popover/index.vue'
import type { SearchableSelectOption } from '@/components/searchable-select-popover/index.vue'
import { emptyTimezoneValue } from '@/utils/timezones'
import { ISO639_LANGUAGES } from '@/utils/languages'
import type { SettingsSettings } from '@memohai/sdk'

const { t } = useI18n()

defineProps<{
  form: SettingsSettings
}>()

const languageOptions = computed<SearchableSelectOption[]>(() => {
  const autoOption: SearchableSelectOption = {
    value: 'auto',
    label: t('languages.auto') || t('bots.settings.chatLanguagePlaceholder'),
    keywords: ['auto', t('languages.auto'), t('bots.settings.chatLanguagePlaceholder')],
  }
  const codeOptions: SearchableSelectOption[] = ISO639_LANGUAGES.map((lang) => ({
    value: lang.code,
    label: `${lang.code} (${lang.name} / ${lang.nativeName})`,
    keywords: [lang.code, lang.name, lang.nativeName],
  }))
  return [autoOption, ...codeOptions]
})
</script>
