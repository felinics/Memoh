<template>
  <SettingsShell width="narrow">
    <div class="space-y-6">
      <!-- Identity card: name on the left, delete on the right — same header
           shape as the provider / web search / voice details. -->
      <section class="flex items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card px-4 py-3">
        <span class="flex size-9 shrink-0 items-center justify-center rounded-full bg-muted">
          <EmailProviderIcon
            :provider="curProvider?.provider"
            class="size-5 text-muted-foreground"
          />
        </span>
        <div class="min-w-0 flex-1">
          <h2 class="truncate text-sm font-semibold">
            {{ curProvider?.name }}
          </h2>
        </div>
        <div class="ml-auto shrink-0">
          <ConfirmPopover
            v-if="curProvider?.id"
            :message="$t('email.deleteConfirm')"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('common.confirm')"
            :loading="deleteLoading"
            variant="destructive"
            @confirm="handleDelete"
          >
            <template #trigger>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                class="text-muted-foreground hover:text-destructive"
                :aria-label="$t('common.delete')"
              >
                <Trash2 class="size-4" />
              </Button>
            </template>
          </ConfirmPopover>
        </div>
      </section>

      <form @submit="handleSave">
        <!-- Configuration card -->
        <SettingsSection :title="$t('provider.configurationTitle')">
          <div>
            <FormField
              v-slot="{ componentField }"
              name="name"
            >
              <SettingsRow :label="$t('common.name')">
                <FieldStack
                  class="w-80"
                  for="email-provider-name"
                >
                  <FormControl>
                    <Input
                      id="email-provider-name"
                      type="text"
                      :placeholder="$t('common.namePlaceholder')"
                      v-bind="componentField"
                    />
                  </FormControl>
                </FieldStack>
              </SettingsRow>
            </FormField>

            <SettingsRow
              v-for="field in orderedFields"
              :key="field.key"
              :label="fieldLabel(field)"
              :description="field.description"
            >
              <div
                v-if="field.type === 'secret'"
                class="relative w-80"
              >
                <Input
                  :id="`email-field-${field.key}`"
                  v-model="configData[field.key!] as string"
                  :type="visibleSecrets[field.key!] ? 'text' : 'password'"
                  class="pr-9"
                  :placeholder="field.example ? String(field.example) : ''"
                />
                <button
                  type="button"
                  class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                  @click="visibleSecrets[field.key!] = !visibleSecrets[field.key!]"
                >
                  <component
                    :is="visibleSecrets[field.key!] ? EyeOff : Eye"
                    class="size-3.5"
                  />
                </button>
              </div>

              <Switch
                v-else-if="field.type === 'bool'"
                :model-value="!!configData[field.key!]"
                @update:model-value="(val) => configData[field.key!] = !!val"
              />

              <Input
                v-else-if="field.type === 'number'"
                :id="`email-field-${field.key}`"
                v-model.number="configData[field.key!] as string"
                type="number"
                class="w-40"
                :placeholder="field.example ? String(field.example) : ''"
              />

              <Select
                v-else-if="field.type === 'enum' && field.enum"
                :model-value="String(configData[field.key!] || '')"
                @update:model-value="(val) => configData[field.key!] = val"
              >
                <SelectTrigger class="w-80">
                  <SelectValue :placeholder="field.title || field.key" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem
                    v-for="opt in field.enum"
                    :key="opt"
                    :value="opt"
                  >
                    {{ opt }}
                  </SelectItem>
                </SelectContent>
              </Select>

              <Input
                v-else
                :id="`email-field-${field.key}`"
                v-model="configData[field.key !] as string"
                type="text"
                class="w-80"
                :placeholder="field.example ? String(field.example) : ''"
              />
            </SettingsRow>
          </div>

          <template #footer>
            <LoadingButton
              type="submit"
              size="sm"
              :loading="editLoading"
            >
              {{ $t('provider.saveChanges') }}
            </LoadingButton>
          </template>
        </SettingsSection>

        <!-- Gmail OAuth authorization -->
        <SettingsSection
          v-if="isOAuthProvider"
          :title="$t('email.oauth.title')"
          class="mt-6"
        >
          <div class="flex flex-wrap items-start justify-between gap-4 p-4">
            <div class="min-w-55 flex-1 space-y-1">
              <p class="text-xs text-muted-foreground">
                {{ $t('email.oauth.description') }}
              </p>
              <p
                class="text-xs"
                :class="oauthTokenExpired ? 'text-destructive' : 'text-muted-foreground'"
              >
                <template v-if="oauthStatusLoading">
                  {{ $t('email.oauth.status.checking') }}
                </template>
                <template v-else-if="oauthStatus && !oauthStatus.configured">
                  {{ $t('email.oauth.status.notConfigured') }}
                </template>
                <template v-else-if="oauthTokenExpired">
                  {{ $t('email.oauth.status.expired') }}
                </template>
                <template v-else-if="oauthStatus && oauthStatus.has_token">
                  {{ oauthStatus.email_address ? $t('email.oauth.status.authorized', { email: oauthStatus.email_address }) : $t('email.oauth.status.authorizedUnknown') }}
                </template>
                <template v-else>
                  {{ $t('email.oauth.status.missing') }}
                </template>
              </p>
            </div>
            <div class="flex items-center gap-2">
              <LoadingButton
                type="button"
                variant="outline"
                size="sm"
                :disabled="!canAuthorize"
                :loading="authorizeLoading"
                @click="handleAuthorize"
              >
                <KeyRound class="size-4" />
                {{ $t('email.oauth.authorize') }}
              </LoadingButton>
              <LoadingButton
                v-if="hasOAuthToken"
                type="button"
                variant="ghost"
                size="sm"
                :loading="revokeLoading"
                @click="handleRevoke"
              >
                {{ $t('email.oauth.logout') }}
              </LoadingButton>
            </div>
          </div>
        </SettingsSection>
      </form>
    </div>
  </SettingsShell>
</template>

<script setup lang="ts">
import {
  Input,
  Button,
  FormControl,
  FormField,
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
  Switch,
} from '@felinic/ui'
import { Eye, EyeOff, KeyRound, Trash2 } from 'lucide-vue-next'
import LoadingButton from '@/components/loading-button/index.vue'
import EmailProviderIcon from '@/components/email-provider-icon/index.vue'
import { computed, inject, reactive, ref, watch } from 'vue'
import { ConfirmPopover, FieldStack, SettingsRow, SettingsSection, SettingsShell, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import { useForm } from 'vee-validate'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import {
  putEmailProvidersById,
  deleteEmailProvidersById,
  getEmailProvidersMeta,
  getEmailProvidersByIdOauthAuthorize,
  getEmailProvidersByIdOauthStatus,
  deleteEmailProvidersByIdOauthToken,
  postEmailProviders,
} from '@memohai/sdk'
import type {
  EmailCreateProviderRequest,
  EmailFieldSchema,
  EmailProviderMeta,
  EmailProviderResponse,
  HandlersEmailOAuthStatusResponse,
} from '@memohai/sdk'

const OAUTH_PROVIDERS = ['gmail']

const { t, te } = useI18n()
const curProvider = inject('curEmailProvider', ref<EmailProviderResponse>())
const emit = defineEmits<{
  materialized: [provider: EmailProviderResponse]
}>()
const curProviderId = computed(() => curProvider.value?.id)

const { data: metaList } = useQuery({
  key: () => ['email-providers-meta'],
  query: async () => {
    const { data } = await getEmailProvidersMeta({ throwOnError: true })
    return data
  },
})

const currentMeta = computed(() => {
  if (!metaList.value || !curProvider.value?.provider) return null
  return (metaList.value as EmailProviderMeta[]).find((m) => m.provider === curProvider.value?.provider)
})

const orderedFields = computed<EmailFieldSchema[]>(() => {
  const fields = currentMeta.value?.config_schema?.fields
  if (!Array.isArray(fields)) return []
  return [...fields].sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
})

// Field label: an i18n override when present, else the schema title/key, with a
// muted "(optional)" suffix for non-required fields.
function fieldLabel(field: EmailFieldSchema): string {
  const key = `email.fields.${field.key}`
  const base = te(key) ? t(key) : (field.title || field.key || '')
  return field.required ? base : `${base} (${t('common.optional')})`
}

const isOAuthProvider = computed(() =>
  OAUTH_PROVIDERS.includes(curProvider.value?.provider ?? ''),
)

const oauthStatus = ref<HandlersEmailOAuthStatusResponse | null>(null)
const oauthStatusLoading = ref(false)
const revokeLoading = ref(false)

const queryCache = useQueryCache()

const schema = toTypedSchema(z.object({
  name: z.string().min(1, t('provider.nameRequired')),
}))

const form = useForm({ validationSchema: schema })

const configData = reactive<Record<string, unknown>>({})
const visibleSecrets = reactive<Record<string, boolean>>({})

watch(curProvider, (provider) => {
  if (!provider) return
  form.setValues({ name: provider.name ?? '' })
  const cfg = provider.config ?? {}
  Object.keys(configData).forEach((k) => delete configData[k])
  Object.assign(configData, { ...cfg })
  if (isOAuthProvider.value && provider.id) {
    void fetchOAuthStatus()
  }
}, { immediate: true })

watch([isOAuthProvider, curProviderId], () => {
  if (!isOAuthProvider.value) {
    oauthStatus.value = null
    return
  }
  void fetchOAuthStatus()
})

const { mutateAsync: submitUpdate, isLoading: editLoading } = useMutation({
  mutation: async (data: { name: string; config: Record<string, unknown> }) => {
    if (!curProviderId.value) {
      const { data: created } = await postEmailProviders({
        body: {
          name: data.name,
          provider: curProvider.value?.provider as EmailCreateProviderRequest['provider'],
          config: data.config,
        },
        throwOnError: true,
      })
      if (created) {
        curProvider.value = created
        emit('materialized', created)
      }
      return created
    }
    const { data: result } = await putEmailProvidersById({
      path: { id: curProviderId.value },
      body: { name: data.name, config: data.config },
      throwOnError: true,
    })
    return result
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['email-providers'] }),
})

const { mutateAsync: doDelete, isLoading: deleteLoading } = useMutation({
  mutation: async () => {
    if (!curProviderId.value) return
    await deleteEmailProvidersById({ path: { id: curProviderId.value }, throwOnError: true })
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['email-providers'] }),
})

const handleSave = form.handleSubmit(async (values) => {
  try {
    const providerId = await persistProvider(values)
    if (!providerId) return
    toast.success(t('provider.saveChanges'))
    if (isOAuthProvider.value) {
      await fetchOAuthStatus(providerId)
    }
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : t('common.saveFailed'))
  }
})

async function persistProvider(values: { name: string }): Promise<string | null> {
  const missing = orderedFields.value.find((field) => {
    if (!field.required || !field.key) return false
    const value = configData[field.key]
    if (field.type === 'bool') return value === undefined || value === null
    return !String(value ?? '').trim()
  })
  if (missing) {
    toast.error(t('provider.requiredField', { field: fieldLabel(missing) }))
    return null
  }

  const provider = await submitUpdate({ name: values.name, config: { ...configData } })
  const providerId = provider?.id ?? curProviderId.value
  if (!providerId) throw new Error(t('common.saveFailed'))
  return providerId
}

const persistProviderForOAuth = form.handleSubmit(values => persistProvider(values))

async function handleDelete() {
  if (!curProviderId.value) return
  try {
    await doDelete()
    toast.success(t('common.deleteSuccess'))
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : t('common.saveFailed'))
  }
}

const authorizeLoading = ref(false)
const hasOAuthToken = computed(() => Boolean(oauthStatus.value?.has_token))
const oauthTokenExpired = computed(() => Boolean(oauthStatus.value?.has_token && oauthStatus.value?.expired))
const canAuthorize = computed(() => {
  if (!isOAuthProvider.value) return false
  if (oauthStatusLoading.value) return false
  if (oauthStatus.value && !oauthStatus.value.configured) return false
  return true
})

async function handleAuthorize() {
  const popup = window.open('', 'email-oauth', 'width=600,height=720')
  if (!popup) {
    toast.error(t('email.oauth.authorizeFailed'))
    return
  }

  authorizeLoading.value = true
  try {
    const providerId = await persistProviderForOAuth()
    if (!providerId) {
      popup.close()
      return
    }

    const { data, error } = await getEmailProvidersByIdOauthAuthorize({
      path: { id: providerId },
    })
    if (error || !data?.auth_url) {
      throw new Error(t('email.oauth.authorizeFailed'))
    }

    popup.location.href = data.auth_url
    popup.focus()

    await new Promise<void>((resolve, reject) => {
      const cleanup = () => {
        window.removeEventListener('message', onMessage)
      }

      const onMessage = async (event: MessageEvent) => {
        if (event.data?.type !== 'memoh-email-oauth-callback') return
        if (event.data?.providerId && event.data.providerId !== providerId) return

        cleanup()

        if (event.data?.status === 'success') {
          await fetchOAuthStatus(providerId)
          toast.success(t('email.oauth.authorizeOpened'))
          resolve()
          return
        }

        reject(new Error(typeof event.data?.error === 'string' && event.data.error ? event.data.error : t('email.oauth.authorizeFailed')))
      }

      window.addEventListener('message', onMessage)
    })
  } catch (e: unknown) {
    popup.close()
    toast.error(e instanceof Error ? e.message : t('email.oauth.authorizeFailed'))
  } finally {
    authorizeLoading.value = false
  }
}

async function fetchOAuthStatus(providerId = curProviderId.value) {
  if (!isOAuthProvider.value || !providerId) {
    oauthStatus.value = null
    return
  }
  oauthStatusLoading.value = true
  try {
    const { data, error } = await getEmailProvidersByIdOauthStatus({
      path: { id: providerId },
    })
    if (error) {
      throw error
    }
    oauthStatus.value = data ?? null
  } catch (error: unknown) {
    oauthStatus.value = null
    console.error('failed to fetch email oauth status', error)
  } finally {
    oauthStatusLoading.value = false
  }
}

async function handleRevoke() {
  if (!curProviderId.value) return
  revokeLoading.value = true
  try {
    const { error } = await deleteEmailProvidersByIdOauthToken({
      path: { id: curProviderId.value },
    })
    if (error) throw error
    toast.success(t('email.oauth.logoutSuccess'))
    await fetchOAuthStatus()
  } catch (error: unknown) {
    toast.error(error instanceof Error ? error.message : t('email.oauth.logoutFailed'))
  } finally {
    revokeLoading.value = false
  }
}
</script>
