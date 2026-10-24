<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { CalloutBanner, Input, SettingsRow, SettingsSection } from '@felinic/ui'
import { postFetchProvidersByIdTest } from '@memohai/sdk'
import LoadingButton from '@/components/loading-button/index.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{ providerId?: string }>()
const { t } = useI18n()
const url = ref('https://example.com')
const loading = ref(false)
const result = ref<Record<string, unknown>>()
const error = ref('')
const content = computed(() => {
  if (!result.value) return ''
  return typeof result.value.content === 'string'
    ? result.value.content
    : JSON.stringify(result.value.data ?? result.value, null, 2)
})

watch(() => props.providerId, () => {
  result.value = undefined
  error.value = ''
})

async function testProvider() {
  const id = props.providerId
  if (!id || loading.value) return
  loading.value = true
  result.value = undefined
  error.value = ''
  try {
    const { data } = await postFetchProvidersByIdTest({
      path: { id },
      body: { url: url.value.trim() },
      throwOnError: true,
    })
    if (props.providerId === id) result.value = data
  } catch (cause) {
    if (props.providerId === id) error.value = resolveApiErrorMessage(cause, t('webSearch.testFailed'))
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <SettingsSection :title="$t('webSearch.testFetchTitle')">
    <form
      id="fetch-provider-test-form"
      @submit.prevent="testProvider"
    >
      <SettingsRow
        :label="$t('webSearch.testUrl')"
        :description="providerId ? $t('webSearch.testSavedConfig') : $t('webSearch.testSaveFirst')"
        stack="sm"
      >
        <Input
          v-model="url"
          type="url"
          required
          :maxlength="8192"
          class="w-full sm:w-80"
          :aria-label="$t('webSearch.testUrl')"
          :disabled="loading"
        />
      </SettingsRow>
    </form>
    <div
      v-if="error || result"
      class="space-y-3 px-4 py-3"
      aria-live="polite"
    >
      <CalloutBanner
        v-if="error"
        tone="destructive"
        size="sm"
        :description="error"
      />
      <template v-else-if="result">
        <p class="text-sm font-medium">
          {{ result.title || $t('webSearch.testSucceeded') }}
        </p>
        <pre class="max-h-80 overflow-auto whitespace-pre-wrap break-words text-xs">{{ content }}</pre>
      </template>
    </div>
    <template #footer>
      <LoadingButton
        type="submit"
        form="fetch-provider-test-form"
        variant="outline"
        size="sm"
        :loading="loading"
        :disabled="!providerId || loading"
      >
        {{ $t('webSearch.testFetch') }}
      </LoadingButton>
    </template>
  </SettingsSection>
</template>
