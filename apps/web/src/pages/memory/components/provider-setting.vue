<template>
  <SettingsShell
    v-if="curProvider"
    width="narrow"
    class="space-y-6"
  >
    <!-- Header card: identity + the single destructive action, mirroring the
         provider-detail header language (icon · name/type · confirm-gated
         delete) instead of a bare title above a hairline. -->
    <section class="flex items-center gap-3 rounded-menu-shell border border-border bg-card px-4 py-3">
      <span class="flex size-9 shrink-0 items-center justify-center rounded-full bg-muted">
        <Brain class="size-5 text-muted-foreground" />
      </span>
      <div class="min-w-0 flex-1">
        <h3 class="truncate text-control font-semibold text-foreground">
          {{ curProvider.name }}
        </h3>
        <p class="mt-0.5 truncate text-body text-muted-foreground">
          {{ $t(`memory.providerNames.${curProvider.provider}`, curProvider.provider ?? '') }}
        </p>
      </div>
      <ConfirmPopover
        v-if="curProvider.id"
        :message="$t('memory.deleteConfirm')"
        :cancel-text="$t('common.cancel')"
        :confirm-text="$t('common.confirm')"
        :loading="deleteLoading"
        @confirm="handleDelete"
      >
        <template #trigger>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            class="text-muted-foreground"
            :aria-label="$t('common.delete')"
          >
            <Trash2 class="size-4" />
          </Button>
        </template>
      </ConfirmPopover>
    </section>

    <!-- Config card: the name plus the backend's dynamic schema fields, laid out
         as a form block inside the card and committed via the footer's Save —
         not a button floating below the card. Titleless: the header card already
         names the backend, so a "Configuration" title would just add a rung. -->
    <SettingsSection>
      <div class="p-4">
        <FormStack>
          <FieldStack :label="$t('memory.name')">
            <Input
              v-model="form.name"
              :placeholder="$t('memory.namePlaceholder')"
            />
          </FieldStack>

          <div
            v-if="providerSchema"
            class="grid gap-4 md:grid-cols-2"
          >
            <FieldStack
              v-for="(fieldSchema, fieldKey) in providerSchema.fields"
              :key="fieldKey"
              :help="fieldSchema.description"
              :class="isWideField(fieldKey, fieldSchema) ? 'md:col-span-2' : ''"
            >
              <template #label>
                <Label>
                  {{ fieldSchema.title || fieldKey }}
                  <span
                    v-if="fieldSchema.required"
                    class="text-destructive"
                  >*</span>
                </Label>
              </template>
              <Input
                v-model="configForm[fieldKey]"
                :type="fieldSchema.secret ? 'password' : 'text'"
                :placeholder="fieldSchema.example ? String(fieldSchema.example) : ''"
              />
            </FieldStack>
          </div>
        </FormStack>
      </div>

      <template #footer>
        <LoadingButton
          :loading="saveLoading"
          @click="handleSave"
        >
          {{ $t('common.save') }}
        </LoadingButton>
      </template>
    </SettingsSection>
  </SettingsShell>
</template>

<script setup lang="ts">
import { inject, reactive, watch, computed, ref, type Ref } from 'vue'
import {
  Button,
  Input,
  Label,
} from '@felinic/ui'
import { Brain, Trash2 } from 'lucide-vue-next'
import { useQuery, useQueryCache } from '@pinia/colada'
import { deleteMemoryProvidersById, getMemoryProvidersMeta, postMemoryProviders, putMemoryProvidersById } from '@memohai/sdk'
import type {
  AdaptersProviderCreateRequest,
  AdaptersProviderGetResponse,
  AdaptersProviderMeta,
} from '@memohai/sdk'
import { ConfirmPopover, FieldStack, FormStack, SettingsSection, SettingsShell, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import LoadingButton from '@/components/loading-button/index.vue'

const { t } = useI18n()
const queryCache = useQueryCache()

const curProvider = inject<Ref<AdaptersProviderGetResponse | null>>('curMemoryProvider')
const emit = defineEmits<{
  materialized: [provider: AdaptersProviderGetResponse]
}>()

const form = reactive({ name: '' })
const configForm = reactive<Record<string, string>>({})

const saveLoading = ref(false)
const deleteLoading = ref(false)

const { data: metaData } = useQuery({
  key: ['memory-providers-meta'],
  query: async () => {
    const { data } = await getMemoryProvidersMeta({ throwOnError: true })
    return data
  },
})

const providerSchema = computed(() => {
  if (!curProvider?.value || !metaData.value) return null
  const meta = (metaData.value as AdaptersProviderMeta[])?.find(
    (m) => m.provider === curProvider.value?.provider,
  )
  return meta?.config_schema ?? null
})

// Heuristic: URL / endpoint / api-key / secret / long descriptions get full width.
function isWideField(key: string | number, schema: { secret?: boolean; type?: string; description?: string }) {
  const keyStr = String(key).toLowerCase()
  if (schema.secret) return true
  if (keyStr.includes('url') || keyStr.includes('endpoint') || keyStr.includes('key') || keyStr.includes('token') || keyStr.includes('path') || keyStr.includes('uri')) return true
  if ((schema.description ?? '').length > 80) return true
  return false
}

watch(curProvider!, (val) => {
  if (val) {
    form.name = val.name ?? ''
    Object.keys(configForm).forEach((k) => delete configForm[k])
    if (val.config) {
      Object.entries(val.config).forEach(([k, v]) => {
        configForm[k] = (v as string) ?? ''
      })
    }
  }
}, { immediate: true })

async function handleSave() {
  if (!curProvider?.value) return
  const missing = providerSchema.value
    ? Object.entries(providerSchema.value.fields ?? {}).find(([key, field]) => field.required && !String(configForm[key] ?? '').trim())
    : undefined
  if (missing) {
    toast.error(t('provider.requiredField', { field: missing[1].title || missing[0] }))
    return
  }
  saveLoading.value = true
  try {
    const config: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(configForm)) {
      if (v) config[k] = v
    }
    let data: AdaptersProviderGetResponse | undefined
    if (curProvider.value.id) {
      const response = await putMemoryProvidersById({
        path: { id: curProvider.value.id },
        body: { name: form.name.trim(), config },
        throwOnError: true,
      })
      data = response.data
    } else {
      const response = await postMemoryProviders({
        body: {
          name: form.name.trim(),
          provider: curProvider.value.provider as AdaptersProviderCreateRequest['provider'],
          config,
        },
        throwOnError: true,
      })
      data = response.data
      if (data) emit('materialized', data)
    }
    if (curProvider?.value && data) {
      Object.assign(curProvider.value, data)
    }
    toast.success(t('memory.saveSuccess'))
    queryCache.invalidateQueries({ key: ['memory-providers'] })
  } catch (error) {
    console.error('Failed to save:', error)
    toast.error(t('common.saveFailed'))
  } finally {
    saveLoading.value = false
  }
}

async function handleDelete() {
  if (!curProvider?.value?.id) return
  deleteLoading.value = true
  try {
    await deleteMemoryProvidersById({
      path: { id: curProvider.value.id },
      throwOnError: true,
    })
    toast.success(t('memory.deleteSuccess'))
    queryCache.invalidateQueries({ key: ['memory-providers'] })
  } catch (error) {
    console.error('Failed to delete:', error)
    toast.error(t('memory.deleteFailed'))
  } finally {
    deleteLoading.value = false
  }
}
</script>
