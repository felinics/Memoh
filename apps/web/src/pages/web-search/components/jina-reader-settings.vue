<template>
  <SettingsRow :label="$t('provider.apiKey')">
    <Input
      id="jina-reader-api-key"
      v-model="localConfig.api_key"
      type="password"
      class="w-80"
      :aria-label="$t('provider.apiKey')"
    />
  </SettingsRow>
  <SettingsRow :label="$t('common.baseUrl')">
    <Input
      id="jina-reader-base-url"
      v-model="localConfig.base_url"
      class="w-80"
      :aria-label="$t('common.baseUrl')"
    />
  </SettingsRow>
  <SettingsRow :label="$t('common.timeoutSeconds')">
    <Input
      id="jina-reader-timeout-seconds"
      v-model.number="localConfig.timeout_seconds"
      type="number"
      class="w-40"
      :min="1"
      :aria-label="$t('common.timeoutSeconds')"
    />
  </SettingsRow>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue'
import { Input, SettingsRow } from '@felinic/ui'

const props = defineProps<{
  modelValue: Record<string, unknown>
}>()

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, unknown>]
}>()

const localConfig = reactive({
  api_key: '',
  base_url: 'https://r.jina.ai/',
  timeout_seconds: 30,
})

watch(
  () => props.modelValue,
  (val) => {
    localConfig.api_key = String(val?.api_key ?? '')
    localConfig.base_url = String(val?.base_url ?? 'https://r.jina.ai/')
    const timeout = Number(val?.timeout_seconds ?? 30)
    localConfig.timeout_seconds = Number.isFinite(timeout) && timeout > 0 ? timeout : 30
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
