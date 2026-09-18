<template>
  <SettingsRow
    label="API Key"
    stack="sm"
  >
    <Input
      id="firecrawl-api-key"
      v-model="localConfig.api_key"
      type="password"
      class="w-full sm:w-80"
      aria-label="API Key"
    />
  </SettingsRow>
  <SettingsRow
    label="Base URL"
    stack="sm"
  >
    <Input
      id="firecrawl-base-url"
      v-model="localConfig.base_url"
      class="w-full sm:w-80"
      aria-label="Base URL"
    />
  </SettingsRow>
  <SettingsRow
    label="Timeout (seconds)"
    stack="sm"
  >
    <NumberField
      id="firecrawl-timeout-seconds"
      :model-value="localConfig.timeout_seconds"
      :min="1"
      :max="mode === 'scrape' ? 300 : undefined"
      class="w-full sm:w-80"
      aria-label="Timeout (seconds)"
      disable-wheel-change
      @update:model-value="(value) => localConfig.timeout_seconds = value"
    />
  </SettingsRow>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue'
import { Input, NumberField, SettingsRow } from '@felinic/ui'

const props = withDefaults(defineProps<{
  mode?: 'search' | 'scrape'
  modelValue: Record<string, unknown>
}>(), {
  mode: 'search',
})

const mode = props.mode
const defaultBaseURL = props.mode === 'scrape'
  ? 'https://api.firecrawl.dev/v2/scrape'
  : 'https://api.firecrawl.dev/v2/search'
const defaultTimeout = props.mode === 'scrape' ? 30 : 15

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, unknown>]
}>()

const localConfig = reactive({
  api_key: '',
  base_url: defaultBaseURL,
  timeout_seconds: defaultTimeout as number | undefined,
})

watch(
  () => props.modelValue,
  (val) => {
    localConfig.api_key = String(val?.api_key ?? '')
    localConfig.base_url = String(val?.base_url ?? defaultBaseURL)
    const timeout = Number(val?.timeout_seconds ?? defaultTimeout)
    localConfig.timeout_seconds = Number.isFinite(timeout) && timeout > 0 ? timeout : defaultTimeout
  },
  { immediate: true, deep: true },
)

watch(localConfig, () => {
  emit('update:modelValue', {
    api_key: localConfig.api_key,
    base_url: localConfig.base_url,
    timeout_seconds: localConfig.timeout_seconds,
  })
}, { deep: true })
</script>
