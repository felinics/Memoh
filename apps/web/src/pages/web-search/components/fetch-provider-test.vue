<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMutation } from '@pinia/colada'
import { CalloutBanner, Input, SettingsRow, SettingsSection } from '@felinic/ui'
import { postFetchProvidersByIdTest } from '@memohai/sdk'
import LoadingButton from '@/components/loading-button/index.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{ providerId?: string }>()
const { t } = useI18n()
const url = ref('https://example.com')
let controller: AbortController | undefined
onScopeDispose(() => controller?.abort())
const { mutateAsync: runTest, isLoading: loading } = useMutation({
  mutation: async ({ id, url, signal }: { id: string, url: string, signal: AbortSignal }) => {
    const { data } = await postFetchProvidersByIdTest({
      path: { id }, body: { url }, signal, throwOnError: true,
    })
    return data
  },
})
const result = ref<Record<string, unknown>>()
const error = ref('')
const content = computed(() => {
  if (!result.value) return ''
  return typeof result.value.content === 'string'
    ? result.value.content
    : JSON.stringify(result.value.data ?? result.value, null, 2)
})

watch(() => props.providerId, () => {
  controller?.abort()
  result.value = undefined
  error.value = ''
})

async function testProvider() {
  const id = props.providerId
  if (!id || loading.value) return
  controller = new AbortController()
  const { signal } = controller
  result.value = undefined
  error.value = ''
  try {
    const data = await runTest({ id, url: url.value.trim(), signal })
    if (!signal.aborted) result.value = data
  } catch (cause) {
    if (!signal.aborted) error.value = resolveApiErrorMessage(cause, t('webSearch.testFailed'))
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
    <SettingsRow
      v-if="error || result"
      aria-live="polite"
    >
      <template #content>
        <CalloutBanner
          v-if="error"
          tone="destructive"
          size="sm"
          :description="error"
        />
        <div
          v-else-if="result"
          class="space-y-3"
        >
          <p class="text-sm font-medium">
            {{ result.title || $t('webSearch.testSucceeded') }}
          </p>
          <pre class="max-h-80 overflow-auto whitespace-pre-wrap break-words text-xs">{{ content }}</pre>
        </div>
      </template>
    </SettingsRow>
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
