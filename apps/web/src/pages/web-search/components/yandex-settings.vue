<template>
  <SettingsRow
    :label="$t('provider.apiKey')"
    stack="sm"
  >
    <Input
      id="yandex-api-key"
      v-model="localConfig.api_key"
      type="password"
      class="w-full sm:w-80"
      :aria-label="$t('provider.apiKey')"
    />
  </SettingsRow>
  <SettingsRow
    :label="$t('common.searchType')"
    stack="sm"
  >
    <Input
      id="yandex-search-type"
      v-model="localConfig.search_type"
      class="w-full sm:w-80"
      :aria-label="$t('common.searchType')"
      placeholder="SEARCH_TYPE_RU"
    />
  </SettingsRow>
  <SettingsRow
    :label="$t('common.baseUrl')"
    stack="sm"
  >
    <Input
      id="yandex-base-url"
      v-model="localConfig.base_url"
      class="w-full sm:w-80"
      :aria-label="$t('common.baseUrl')"
    />
  </SettingsRow>
  <SettingsRow
    :label="$t('common.timeoutSeconds')"
    stack="sm"
  >
    <NumberField
      id="yandex-timeout-seconds"
      :model-value="localConfig.timeout_seconds"
      :min="1"
      class="w-full sm:w-80"
      :aria-label="$t('common.timeoutSeconds')"
      disable-wheel-change
      @update:model-value="(value) => localConfig.timeout_seconds = value"
    />
  </SettingsRow>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue'
import { Input, NumberField, SettingsRow } from '@felinic/ui'

const props = defineProps<{
  modelValue: Record<string, unknown>
}>()

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, unknown>]
}>()

const localConfig = reactive({
  api_key: '',
  search_type: 'SEARCH_TYPE_RU',
  base_url: 'https://searchapi.api.cloud.yandex.net/v2/web/search',
  // NumberField commits undefined for an emptied field; the key drops out of
  // the emitted config and hydration restores the default on the next load.
  timeout_seconds: 15 as number | undefined,
})

watch(
  () => props.modelValue,
  (val) => {
    localConfig.api_key = String(val?.api_key ?? '')
    localConfig.search_type = String(val?.search_type ?? 'SEARCH_TYPE_RU')
    localConfig.base_url = String(val?.base_url ?? 'https://searchapi.api.cloud.yandex.net/v2/web/search')
    const timeout = Number(val?.timeout_seconds ?? 15)
    localConfig.timeout_seconds = Number.isFinite(timeout) && timeout > 0 ? timeout : 15
  },
  { immediate: true, deep: true },
)

watch(localConfig, () => {
  emit('update:modelValue', {
    api_key: localConfig.api_key,
    search_type: localConfig.search_type,
    base_url: localConfig.base_url,
    timeout_seconds: localConfig.timeout_seconds,
  })
}, { deep: true })
</script>
