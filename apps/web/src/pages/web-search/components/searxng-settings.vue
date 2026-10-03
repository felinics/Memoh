<template>
  <SettingsRow
    :label="$t('common.baseUrl')"
    stack="sm"
  >
    <Input
      id="searxng-base-url"
      v-model="localConfig.base_url"
      class="w-full sm:w-80"
      :aria-label="$t('common.baseUrl')"
      placeholder="http://localhost:8080/search"
    />
  </SettingsRow>
  <SettingsRow
    :label="$t('webSearch.httpHeaders')"
    stack="always"
  >
    <div class="flex w-full flex-col gap-2">
      <div
        v-for="(header, index) in localConfig.headers"
        :key="index"
        class="grid gap-2 sm:grid-cols-[1fr_2fr_auto]"
      >
        <Input
          :model-value="header.name"
          :aria-label="$t('webSearch.headerName')"
          :placeholder="$t('webSearch.headerName')"
          autocomplete="off"
          @update:model-value="value => updateHeader(index, 'name', String(value))"
        />
        <Input
          :model-value="header.value"
          type="password"
          :aria-label="$t('webSearch.headerValue')"
          :placeholder="$t('webSearch.headerValue')"
          autocomplete="off"
          @update:model-value="value => updateHeader(index, 'value', String(value))"
        />
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          :aria-label="$t('common.delete')"
          @click="removeHeader(index)"
        >
          <X />
        </Button>
      </div>
      <Button
        type="button"
        variant="outline"
        size="sm"
        class="w-fit"
        @click="addHeader"
      >
        <Plus />
        {{ $t('webSearch.addHeader') }}
      </Button>
    </div>
  </SettingsRow>
  <SettingsRow
    :label="$t('settings.language')"
    stack="sm"
  >
    <Input
      id="searxng-language"
      v-model="localConfig.language"
      class="w-full sm:w-80"
      :aria-label="$t('settings.language')"
      placeholder="all"
    />
  </SettingsRow>
  <SettingsRow
    :label="$t('common.safeSearch')"
    stack="sm"
  >
    <Input
      id="searxng-safesearch"
      v-model="localConfig.safesearch"
      class="w-full sm:w-80"
      :aria-label="$t('common.safeSearch')"
      placeholder="0, 1, or 2"
    />
  </SettingsRow>
  <SettingsRow
    :label="$t('common.categories')"
    stack="sm"
  >
    <Input
      id="searxng-categories"
      v-model="localConfig.categories"
      class="w-full sm:w-80"
      :aria-label="$t('common.categories')"
      placeholder="general"
    />
  </SettingsRow>
  <SettingsRow
    :label="$t('common.timeoutSeconds')"
    stack="sm"
  >
    <NumberField
      id="searxng-timeout-seconds"
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
import { Button, Input, NumberField, SettingsRow } from '@felinic/ui'
import { Plus, X } from 'lucide-vue-next'

interface HeaderBinding {
  name: string
  value: string
}

const props = defineProps<{
  modelValue: Record<string, unknown>
}>()

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, unknown>]
}>()

const localConfig = reactive({
  base_url: '',
  headers: [] as HeaderBinding[],
  language: 'all',
  safesearch: '1',
  categories: 'general',
  // NumberField commits undefined for an emptied field; the key drops out of
  // the emitted config and hydration restores the default on the next load.
  timeout_seconds: 15 as number | undefined,
})

watch(
  () => props.modelValue,
  (val) => {
    localConfig.base_url = String(val?.base_url ?? '')
    localConfig.headers = val?.headers && typeof val.headers === 'object' && !Array.isArray(val.headers)
      ? Object.entries(val.headers as Record<string, unknown>).map(([name, value]) => ({ name, value: String(value) }))
      : [{ name: 'Authorization', value: '' }]
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
    headers: Object.fromEntries(localConfig.headers
      .filter(header => header.name.trim())
      .map(header => [header.name.trim(), header.value])),
    language: localConfig.language,
    safesearch: localConfig.safesearch,
    categories: localConfig.categories,
    timeout_seconds: localConfig.timeout_seconds,
  })
}, { deep: true })

function addHeader() {
  const name = localConfig.headers.length === 0
    ? 'Authorization'
    : `X-Custom-Header-${localConfig.headers.length + 1}`
  localConfig.headers.push({ name, value: '' })
}

function updateHeader(index: number, field: keyof HeaderBinding, value: string) {
  localConfig.headers[index] = { ...localConfig.headers[index], [field]: value }
}

function removeHeader(index: number) {
  localConfig.headers.splice(index, 1)
}
</script>
