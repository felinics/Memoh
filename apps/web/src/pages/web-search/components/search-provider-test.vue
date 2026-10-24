<script setup lang="ts">
import { onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useForm } from 'vee-validate'
import { toTypedSchema } from '@vee-validate/zod'
import { z } from 'zod'
import { useMutation } from '@pinia/colada'
import { postSearchProvidersByIdTest } from '@memohai/sdk'
import { CalloutBanner, FieldStack, FormControl, FormField, Input, SettingsRow, SettingsSection } from '@felinic/ui'
import LoadingButton from '@/components/loading-button/index.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{ providerId?: string }>()
const { t } = useI18n()
const result = ref<Record<string, unknown>>()
const failure = ref('')
const form = useForm({
  validationSchema: toTypedSchema(z.object({
    query: z.string().trim().min(1, t('webSearch.test.queryRequired'))
      .refine(value => Array.from(value).length <= 128, t('webSearch.test.queryTooLong')),
  })),
  initialValues: { query: '' },
})
let controller: AbortController | undefined
onScopeDispose(() => controller?.abort())
const { mutateAsync: runTest, isLoading } = useMutation({
  mutation: async ({ id, query, signal }: { id: string, query: string, signal: AbortSignal }) => {
    const { data } = await postSearchProvidersByIdTest({
      path: { id }, body: { query }, signal, throwOnError: true,
    })
    return data
  },
})
const submit = form.handleSubmit(async ({ query }) => {
  if (!props.providerId || isLoading.value) return
  const id = props.providerId
  controller = new AbortController()
  const { signal } = controller
  result.value = undefined
  failure.value = ''
  try {
    const data = await runTest({ id, query, signal })
    if (!signal.aborted) result.value = data
  } catch (error) {
    if (!signal.aborted) failure.value = resolveApiErrorMessage(error, t('webSearch.test.failed'))
  }
})
watch(() => props.providerId, () => {
  controller?.abort()
  result.value = undefined
  failure.value = ''
  form.resetForm()
})
</script>

<template>
  <form @submit="submit">
    <SettingsSection :title="t('webSearch.test.title')">
      <FormField
        v-slot="{ componentField }"
        name="query"
      >
        <SettingsRow
          :label="t('webSearch.test.query')"
          :description="t('webSearch.test.savedConfig')"
          stack="sm"
        >
          <FieldStack class="w-full sm:w-80">
            <FormControl>
              <Input
                v-bind="componentField"
                :aria-label="t('webSearch.test.query')"
                :placeholder="t('webSearch.test.queryPlaceholder')"
                :disabled="isLoading"
              />
            </FormControl>
          </FieldStack>
        </SettingsRow>
      </FormField>
      <SettingsRow
        v-if="!providerId"
        :description="t('webSearch.test.saveFirst')"
      />
      <SettingsRow v-if="failure">
        <CalloutBanner
          tone="destructive"
          :description="failure"
          bare
        />
      </SettingsRow>
      <SettingsRow
        v-if="result"
        :label="t('webSearch.test.results', { count: Array.isArray(result.results) ? result.results.length : 0 })"
        stack="always"
      >
        <pre class="w-full whitespace-pre-wrap break-words text-xs">{{ JSON.stringify(result, null, 2) }}</pre>
      </SettingsRow>
      <template #footer>
        <LoadingButton
          type="submit"
          :loading="isLoading"
          :disabled="!providerId"
        >
          {{ t('webSearch.test.run') }}
        </LoadingButton>
      </template>
    </SettingsSection>
  </form>
</template>
