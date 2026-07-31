<template>
  <SettingsRow :label="$t('common.baseUrl')">
    <Input
      id="searxng-base-url"
      v-model="localConfig.base_url"
      class="w-80"
      :aria-label="$t('common.baseUrl')"
      placeholder="http://localhost:8080/search"
    />
  </SettingsRow>
  <SettingsRow :label="$t('settings.language')">
    <Input
      id="searxng-language"
      v-model="localConfig.language"
      class="w-80"
      :aria-label="$t('settings.language')"
      placeholder="all"
    />
  </SettingsRow>
  <SettingsRow :label="$t('common.safeSearch')">
    <Input
      id="searxng-safesearch"
      v-model="localConfig.safesearch"
      class="w-80"
      :aria-label="$t('common.safeSearch')"
      placeholder="0, 1, or 2"
    />
  </SettingsRow>
  <SettingsRow :label="$t('common.categories')">
    <Input
      id="searxng-categories"
      v-model="localConfig.categories"
      class="w-80"
      :aria-label="$t('common.categories')"
      placeholder="general"
    />
  </SettingsRow>
  <SettingsRow :label="$t('common.timeoutSeconds')">
    <Input
      id="searxng-timeout-seconds"
      v-model.number="localConfig.timeout_seconds"
      type="number"
      :min="1"
      class="w-80"
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
  base_url: '',
  language: 'all',
  safesearch: '1',
  categories: 'general',
  timeout_seconds: 15,
})

watch(
  () => props.modelValue,
  (val) => {
    localConfig.base_url = String(val?.base_url ?? '')
    localConfig.language = String(val?.language ?? 'all')
    localConfig.safesearch = String(val?.safesearch ?? '1')
    localConfig.categories = String(val?.categories ?? 'general')
    const timeout = Number(val?.timeout_seconds ?? 15)
    localConfig.timeout_seconds = Number.isFinite(timeout) && timeout > 0 ? timeout : 15
  },
  { immediate: true, deep: true },
)

watch(localConfig, () => {
  emit('update:modelValue', {
    base_url: localConfig.base_url,
    language: localConfig.language,
    safesearch: localConfig.safesearch,
    categories: localConfig.categories,
    timeout_seconds: localConfig.timeout_seconds,
  })
}, { deep: true })
</script>
