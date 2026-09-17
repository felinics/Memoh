<template>
  <form @submit="onSubmit">
    <SettingsSection
      v-if="!isManagedOAuthProvider"
      class="provider-configuration"
      :title="$t('provider.configurationTitle')"
    >
      <!-- Field rows are grouped so the LAST one keeps its `last:border-b-0`
           (no trailing inset hairline) — the footer below owns the only divider,
           and it spans full width. -->
      <div>
        <FormField
          v-slot="{ componentField, errorMessage }"
          name="name"
        >
          <SettingsRow
            stack="sm"
            :label="$t('common.name')"
          >
            <FormItem class="w-full sm:w-80">
              <FormControl>
                <Input
                  type="text"
                  :placeholder="$t('common.namePlaceholder')"
                  :aria-label="$t('common.name')"
                  :aria-invalid="!!errorMessage"
                  v-bind="componentField"
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          </SettingsRow>
        </FormField>

        <FormField
          v-slot="{ value, handleChange, errorMessage }"
          name="client_type"
        >
          <SettingsRow
            stack="sm"
            :label="$t('provider.clientType')"
          >
            <FormItem class="w-full sm:w-80">
              <FormControl>
                <Select
                  :model-value="value"
                  :aria-invalid="!!errorMessage"
                  @update:model-value="handleChange"
                >
                  <SelectTrigger class="w-full">
                    <SelectValue :placeholder="$t('models.clientTypePlaceholder')" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem
                      v-for="option in clientTypeOptions"
                      :key="option.value"
                      :value="option.value"
                    >
                      {{ option.label }}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </FormControl>
              <FormMessage />
            </FormItem>
          </SettingsRow>
        </FormField>

        <FormField
          v-if="form.values.client_type !== 'github-copilot'"
          v-slot="{ componentField, errorMessage }"
          name="base_url"
        >
          <SettingsRow
            stack="sm"
            :label="$t('provider.url')"
          >
            <FormItem class="w-full sm:w-80">
              <FormControl>
                <Input
                  type="text"
                  :placeholder="$t('provider.urlPlaceholder')"
                  :aria-label="$t('provider.url')"
                  :aria-invalid="!!errorMessage"
                  v-bind="componentField"
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          </SettingsRow>
        </FormField>

        <FormField
          v-if="!isManagedOAuthClientType(form.values.client_type)"
          v-slot="{ componentField, errorMessage }"
          name="api_key"
        >
          <SettingsRow
            stack="sm"
            :label="$t('provider.apiKey')"
          >
            <FormItem class="w-full sm:w-80">
              <FormControl>
                <!-- The key is write-only: the box starts empty and is only
                     sent when non-empty; the placeholder shows the stored
                     (masked) secret. -->
                <Input
                  type="password"
                  :placeholder="getStoredSecret(props.provider?.config as Record<string, unknown> | undefined) || $t('provider.apiKeyPlaceholder')"
                  :aria-label="$t('provider.apiKey')"
                  :aria-invalid="!!errorMessage"
                  v-bind="componentField"
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          </SettingsRow>
        </FormField>

        <FormField
          v-if="supportsPromptCache(form.values.client_type)"
          v-slot="{ value, handleChange, errorMessage }"
          name="prompt_cache_ttl"
        >
          <SettingsRow
            stack="sm"
            :label="$t('provider.promptCache.label')"
            :description="cacheDescription"
          >
            <FormItem>
              <FormControl>
                <Select
                  :model-value="value || '5m'"
                  :aria-invalid="!!errorMessage"
                  @update:model-value="handleChange"
                >
                  <SelectTrigger
                    size="sm"
                    class="w-full sm:w-auto sm:min-w-36"
                  >
                    <SelectValue :placeholder="$t('provider.promptCache.label')" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="5m">
                      {{ $t('provider.promptCache.option5m') }}
                    </SelectItem>
                    <SelectItem value="1h">
                      {{ $t('provider.promptCache.option1h') }}
                    </SelectItem>
                    <SelectItem value="off">
                      {{ $t('provider.promptCache.optionOff') }}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </FormControl>
              <FormMessage />
            </FormItem>
          </SettingsRow>
        </FormField>
      </div>

      <!-- Actions close the card via the section's footer band (its top hairline
           spans the card and its inset padding matches the field rows above).
           The probe runs against the SAVED configuration, so testing is gated
           on a clean form: testing while edits are pending would probe
           parameters the user is no longer looking at. -->
      <template #footer>
        <HoverCard :open-delay="120">
          <HoverCardTrigger as-child>
            <Button
              type="button"
              variant="outline"
              size="sm"
              loading-mode="manual"
              :loading="testLoading"
              :disabled="!props.provider?.id || hasChanges || editLoading"
              @click="runTest"
            >
              <Spinner
                v-if="testLoading"
                class="size-4"
              />
              <CheckDrawIcon
                v-else-if="testStatus === 'ok'"
                class="size-4 text-success"
              />
              <AlertCircle
                v-else-if="testStatus === 'unverified'"
                class="size-4 text-warning"
              />
              <AlertCircle
                v-else-if="testStatus === 'error'"
                class="size-4 text-destructive"
              />
              <RefreshCw
                v-else
                class="size-4"
              />
              {{ $t('provider.testConnection') }}
            </Button>
          </HoverCardTrigger>
          <HoverCardContent
            v-if="testError"
            class="w-80 text-xs whitespace-pre-wrap break-words"
            :class="testStatus === 'unverified' ? '' : 'text-destructive'"
          >
            <!-- unverified 不是失败:引导文案为主信息(正文色),上游细节
                 降为次要点色自成一行,不和文案挤在一句里。 -->
            <template v-if="testStatus === 'unverified'">
              <p>{{ $t('provider.testUnverifiedHint') }}</p>
              <p class="mt-1.5 text-muted-foreground">
                {{ testError }}
              </p>
            </template>
            <template v-else>
              {{ testError }}
            </template>
          </HoverCardContent>
        </HoverCard>

        <span
          v-if="props.provider?.id && hasChanges"
          class="text-body text-muted-foreground"
        >
          {{ $t('provider.saveBeforeTest') }}
        </span>
        <LoadingButton
          type="submit"
          size="sm"
          :loading="editLoading"
          :disabled="(!isDraft && !hasChanges) || !form.meta.value.valid"
        >
          {{ $t('provider.saveChanges') }}
        </LoadingButton>
      </template>
    </SettingsSection>

    <!-- OAuth 账号:设备码授权。结构镜像 profile/connected-accounts-section(同一
         形状的已重构参考):一行账号状态 + 行内动作,等待输码时才在卡片内追加
         居中的验证码块(倒计时 + 复制并打开),轮询在后台静默完成授权。 -->
    <SettingsSection
      v-if="isManagedOAuthClientType(form.values.client_type)"
      :title="$t('provider.oauth.sectionTitle')"
      :class="{ 'mt-6': !isManagedOAuthProvider }"
    >
      <!-- AutoHeight:状态切换(尤其设备码块出现/收起)让卡片平滑生长,不硬切。 -->
      <AutoHeight>
        <!-- 首次加载:借行高稳住卡片,状态到达时不跳动。
             ui-allow-shape: skeleton borrowing the row height, not a data row. -->
        <div
          v-if="oauthStatusLoading && !oauthStatus"
          class="mx-4 flex min-h-[3.75rem] items-center justify-center py-3"
        >
          <Spinner class="size-5 text-muted-foreground" />
        </div>

        <!-- 已连接:身份就是这一行的全部内容;撤销会切断在用的授权,须经确认。 -->
        <SettingsRow
          v-else-if="oauthConnected"
          :label="accountLabel"
          :description="connectedIdentity || $t('provider.oauth.status.authorizedCurrent')"
        >
          <ConfirmPopover
            :message="$t('provider.oauth.revokeConfirm')"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('provider.oauth.revoke')"
            :loading="revokeLoading"
            @confirm="handleRevoke"
          >
            <template #trigger>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                class="shrink-0 text-muted-foreground"
                :disabled="revokeLoading"
              >
                {{ $t('provider.oauth.revoke') }}
              </Button>
            </template>
          </ConfirmPopover>
        </SettingsRow>

        <!-- 后端未配置 OAuth:说明原因,没有可给的动作。 -->
        <SettingsRow
          v-else-if="oauthStatus && !oauthStatus.configured"
          :label="accountLabel"
          :description="$t('provider.oauth.status.notConfigured')"
        />

        <template v-else>
          <!-- 未连接 / 已过期:一行状态 + 长显的开关按钮。设备码流程进行中时它翻成
               "取消"(前端本地收起,服务端的码留给它自然过期);再点"连接"签发新码。 -->
          <SettingsRow
            :label="accountLabel"
            :description="oauthExpired ? $t('provider.oauth.status.expired') : connectDescription"
          >
            <Button
              type="button"
              variant="outline"
              size="sm"
              class="shrink-0"
              :disabled="authorizeLoading"
              :loading="authorizeLoading"
              loading-mode="manual"
              @click="devicePending ? cancelDeviceAuthorization() : handleAuthorize()"
            >
              <!-- 三态 label:Connect → Connecting…(按下变长,给等待一个视觉
                   挽留点) → Cancel(码到手收短)。宽度过渡由 LabelSwap 负责;
                   manual loading 只借 busy 铬层挡双击,spinner 在 connecting
                   slot 里占图标位,文字不被盖。 -->
              <LabelSwap :active="authorizeLoading ? 'connecting' : devicePending ? 'cancel' : 'connect'">
                <template #connect>
                  <KeyRound />
                  {{ $t('provider.oauth.connect') }}
                </template>
                <template #connecting>
                  <Spinner />
                  {{ $t('provider.oauth.connecting') }}
                </template>
                <template #cancel>
                  {{ $t('common.cancel') }}
                </template>
              </LabelSwap>
            </Button>
          </SettingsRow>

          <!-- 输码时刻交给 owner;这层 wrapper 只负责它在卡片里的定位。
               py-6 是有意偏离 connected-accounts link-code 块的 py-4:那是行内
               工具块(说明+输入行),这是居中英雄面板 —— 关系不同,留白档位不同;
               贴着分隔线的英雄内容需要更大的呼吸(人眼裁决 2026-07-13)。 -->
          <div
            v-if="devicePending"
            class="mx-4 border-b border-border py-6 last:border-b-0"
          >
            <DeviceCodePanel
              :code="oauthStatus?.device?.user_code ?? ''"
              :verification-uri="oauthStatus?.device?.verification_uri ?? ''"
              :expires-at="oauthStatus?.device?.expires_at ?? ''"
              :hint="$t(form.values.client_type === 'github-copilot' ? 'provider.oauth.githubDeviceHint' : 'provider.oauth.openaiDeviceHint')"
              :retry-loading="authorizeLoading"
              :copy-and-open-label="$t('deviceCode.copyAndOpen')"
              :retry-label="$t('deviceCode.retry')"
              :expired-label="$t('deviceCode.codeExpired')"
              :expires-in-label="(time: string) => $t('deviceCode.expiresIn', { time })"
              :copy-failed-message="$t('deviceCode.copyFailed')"
              @retry="handleAuthorize"
            />
          </div>
        </template>
      </AutoHeight>
    </SettingsSection>
  </form>
</template>

<script setup lang="ts">
import {
  AutoHeight,
  Input,
  Button,
  FormControl,
  FormField,
  FormItem,
  FormMessage,
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
  LabelSwap,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Spinner,
} from '@felinic/ui'
import { AlertCircle, KeyRound, RefreshCw } from 'lucide-vue-next'
import CheckDrawIcon from '@/components/check-draw-icon/index.vue'
import LoadingButton from '@/components/loading-button/index.vue'
import {
  isManagedOAuthClientType,
  MANUAL_LLM_CLIENT_TYPE_LIST,
} from '@/constants/client-types'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import { useForm } from 'vee-validate'
import {
  deleteProvidersByIdOauthToken,
  getProvidersByIdOauthAuthorize,
  getProvidersByIdOauthStatus,
  postProvidersByIdOauthPoll,
  postProvidersByIdTest,
} from '@memohai/sdk'
import type {
  ProvidersGetResponse,
  ProvidersOAuthAuthorizeResponse,
  ProvidersOAuthStatus,
  ProvidersTestResponse,
} from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { ConfirmPopover, DeviceCodePanel, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { useProviderModelCatalog } from '@/composables/useProviderModelCatalog'
import { resolveApiErrorMessage } from '@/utils/api-error'

const { t } = useI18n()
const { syncProviderModelCatalog } = useProviderModelCatalog()

type ProviderWithAuth = Partial<ProvidersGetResponse>

function getStoredSecret(config: Record<string, unknown> | undefined) {
  if (!config) return ''
  const apiKey = config.api_key
  return typeof apiKey === 'string' ? apiKey : ''
}

type PromptCacheTtl = '5m' | '1h' | 'off'

function normalizeCacheTtl(value: string | undefined): PromptCacheTtl {
  return value === '1h' || value === 'off' ? value : '5m'
}

// Vendors that expose configurable prompt cache TTL. Currently only
// Anthropic Messages; expand this list as other providers gain support.
const PROMPT_CACHE_CLIENT_TYPES = new Set(['anthropic-messages'])

function supportsPromptCache(clientType: string | undefined): boolean {
  return !!clientType && PROMPT_CACHE_CLIENT_TYPES.has(clientType)
}

const props = defineProps<{
  provider: ProviderWithAuth | undefined
  editLoading: boolean
  ensureProvider: () => Promise<ProvidersGetResponse>
  // Promise-returning save (the parent's mutation): the form awaits it to
  // rebase its synced snapshot on success and keep the draft on failure.
  saveProvider: (payload: Record<string, unknown>) => Promise<unknown>
}>()

const isDraft = computed(() => !props.provider?.id && !!props.provider?.provider_template_id)
const isManagedOAuthProvider = computed(() => isManagedOAuthClientType(props.provider?.client_type))

let testGeneration = 0
const testLoading = ref(false)
const testResult = ref<ProvidersTestResponse | null>(null)
const testError = ref('')
const oauthStatus = ref<ProvidersOAuthStatus | null>(null)
const oauthStatusLoading = ref(false)
const authorizeLoading = ref(false)
const revokeLoading = ref(false)
const devicePollTimer = ref<number | null>(null)
let oauthStatusLoadGeneration = 0

const testStatus = computed(() => {
  if (testResult.value?.status === 'ok') return 'ok'
  // unverified(#1087)必须先于 error 判断:它不是失败,是"无法确认"。
  if (testResult.value?.status === 'unverified') return 'unverified'
  if (testError.value) return 'error'
  // Any non-ok probe result is an error state (the ok case returned above).
  if (testResult.value) return 'error'
  return 'idle'
})
const cacheDescription = computed(() =>
  form.values.prompt_cache_ttl === 'off'
    ? t('provider.promptCache.descriptionOff')
    : t('provider.promptCache.description'),
)

function truncateError(text: string): string {
  const max = 220
  return text.length > max ? `${text.slice(0, max).trimEnd()}…` : text
}

// The probe detail can embed the raw upstream response inside `[body: …]`. When
// a Base URL points at a website instead of an API the body is a full HTML page;
// its visible text is page prose ("Example Domain … Learn more"), never an
// actionable API error — and stripping tags leaves dead, unclickable text. Drop
// HTML bodies entirely and keep only the status head; non-HTML bodies (real
// JSON API errors) are still shown.
function formatTestError(raw: string | undefined): string {
  const text = (raw ?? '').trim()
  if (!text) return t('provider.unreachable')
  const bodyStart = text.indexOf('[body:')
  if (bodyStart === -1) return truncateError(text)
  const head = text.slice(0, bodyStart).trim()
  const body = text.slice(bodyStart + '[body:'.length).replace(/\]\s*$/, '').trim()
  if (/<!doctype|<\/?[a-z][^>]*>/i.test(body)) return truncateError(head)
  return truncateError(body ? `${head} · ${body}` : head)
}

async function runTest() {
  if (!props.provider?.id || hasChanges.value) return
  const generation = ++testGeneration
  testLoading.value = true
  testResult.value = null
  testError.value = ''
  try {
    const { data } = await postProvidersByIdTest({
      path: { id: props.provider.id },
      throwOnError: true,
    })
    if (generation !== testGeneration) return
    testResult.value = data ?? null
    if (testResult.value?.status !== 'ok') {
      testError.value = formatTestError(testResult.value?.message)
    }
  } catch (err: unknown) {
    if (generation !== testGeneration) return
    const message = err instanceof Error ? err.message : ''
    testError.value = message ? formatTestError(message) : t('provider.testFailed')
  } finally {
    testLoading.value = false
  }
}

watch(() => props.provider?.id, () => {
  testGeneration++
  testResult.value = null
  testError.value = ''
})

const clientTypeOptions = computed(() =>
  MANUAL_LLM_CLIENT_TYPE_LIST.map((ct) => ({
    value: ct.value,
    label: ct.label,
  })),
)

// ---- Config fields (manual save for both drafts and existing providers) ----
// The configuration card saves as ONE deliberate submit: edits stay local
// until Save, and the connection probe only ever runs against saved state.
// Validation lives in the schema; the payload keeps the write-only api_key
// contract (empty = keep the stored secret; the backend shallow-merges config
// and preserves masked secrets).
const providerSchema = toTypedSchema(z.object({
  name: z.string().min(1, t('provider.nameRequired')),
  base_url: z.string().optional(),
  api_key: z.string().optional(),
  client_type: z.string().min(1, t('provider.clientTypeRequired')),
  prompt_cache_ttl: z.enum(['5m', '1h', 'off']).optional(),
}).superRefine((value, ctx) => {
  const existingSecret = getStoredSecret(
    props.provider?.config as Record<string, unknown> | undefined,
  )
  if (!isManagedOAuthClientType(value.client_type) && !value.api_key?.trim() && !existingSecret.trim()) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['api_key'],
      message: t('provider.apiKeyRequired'),
    })
  }
  if (value.client_type !== 'github-copilot' && !value.base_url?.trim()) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['base_url'],
      message: t('provider.urlRequired'),
    })
  }
}))

const form = useForm({
  validationSchema: providerSchema,
})

type ProviderFormValues = {
  name?: string
  client_type?: string
  base_url?: string
  api_key?: string
  prompt_cache_ttl?: PromptCacheTtl
}

// Last-known-server snapshot: a same-provider background refetch (window
// focus) must not clobber fields the user has diverged from it; see
// use-server-synced-form.ts for the contract this mirrors for vee-validate.
const synced = ref<ProviderFormValues>({})

// Switching client type rewrites the base URL default; hydration applies the
// same defaults so a default-URL provider does not read as a user edit.
function applyClientTypeDefaults(target: ProviderFormValues) {
  if (target.client_type === 'openai-codex' && !target.base_url) {
    target.base_url = 'https://chatgpt.com/backend-api'
  }
  if (target.client_type === 'github-copilot') {
    target.base_url = ''
  }
}

watch(() => props.provider, (provider, previous) => {
  if (!provider) return
  const cfg = provider.config as Record<string, unknown> | undefined
  const next: ProviderFormValues = {
    name: provider.name ?? '',
    client_type: provider.client_type || 'openai-completions',
    base_url: (cfg?.base_url as string) ?? '',
    api_key: '',
    prompt_cache_ttl: normalizeCacheTtl(cfg?.prompt_cache_ttl as string | undefined),
  }
  applyClientTypeDefaults(next)
  const switched = (provider.id ?? provider.provider_template_id)
    !== (previous?.id ?? previous?.provider_template_id)
  for (const key of Object.keys(next) as (keyof ProviderFormValues)[]) {
    // Per-field guard: a refetch landing mid-edit must not clobber it.
    if (switched || form.values[key] === synced.value[key]) {
      form.setFieldValue(key, next[key] as never)
    }
  }
  synced.value = next
}, { immediate: true })

watch(() => form.values.client_type, (clientType) => {
  if (clientType === 'openai-codex' && !form.values.base_url) {
    form.setFieldValue('base_url', 'https://chatgpt.com/backend-api')
  }
  if (clientType === 'github-copilot') {
    form.setFieldValue('base_url', '')
  }
})

const hasChanges = computed(() => {
  const values = form.values
  const baseChanged = JSON.stringify({
    name: values.name,
    client_type: values.client_type,
    base_url: values.base_url,
    prompt_cache_ttl: normalizeCacheTtl(values.prompt_cache_ttl),
  }) !== JSON.stringify({
    name: synced.value.name,
    client_type: synced.value.client_type,
    base_url: synced.value.base_url,
    prompt_cache_ttl: normalizeCacheTtl(synced.value.prompt_cache_ttl),
  })
  // The key box is write-only: any non-empty content IS a change to save.
  return baseChanged || Boolean(values.api_key?.trim())
})

// Editing any probed parameter invalidates the last result: the check mark
// would otherwise describe a configuration that is no longer on screen.
watch(
  () => [form.values.client_type, form.values.base_url, form.values.api_key],
  () => {
    testGeneration++
    testResult.value = null
    testError.value = ''
  },
)

const onSubmit = form.handleSubmit(async (value) => {
  const config: Record<string, unknown> = {}
  if (value.base_url && value.base_url.trim() !== '') {
    config.base_url = value.base_url.trim()
  }
  if (value.api_key && value.api_key.trim() !== '' && value.client_type !== 'github-copilot') {
    config.api_key = value.api_key.trim()
  }
  if (supportsPromptCache(value.client_type)) {
    config.prompt_cache_ttl = normalizeCacheTtl(value.prompt_cache_ttl)
  }
  const payload: Record<string, unknown> = {
    name: value.name.trim(),
    client_type: value.client_type,
    config,
  }
  // Switching to github-copilot must scrub the stale managed-OAuth client id
  // left by a previous codex-type save (whole-replace metadata semantics).
  if (value.client_type === 'github-copilot') {
    const metadata = {
      ...((props.provider?.metadata as Record<string, unknown> | undefined) ?? {}),
    }
    delete metadata.oauth_client_id
    payload.metadata = metadata
  }
  if (isDraft.value) payload.enable = props.provider?.enable ?? true

  try {
    await props.saveProvider(payload)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
    return
  }
  // The saved values are the new server baseline until the refetch lands;
  // the key box returns to its write-only empty state.
  const saved: ProviderFormValues = {
    name: value.name.trim(),
    client_type: value.client_type,
    base_url: (config.base_url as string) ?? synced.value.base_url ?? '',
    api_key: '',
    prompt_cache_ttl: normalizeCacheTtl(value.prompt_cache_ttl),
  }
  synced.value = saved
  form.resetForm({ values: { ...saved } })
  toast.success(t('provider.saveSuccess'))
})

const oauthExpired = computed(() => Boolean(oauthStatus.value?.has_token && oauthStatus.value?.expired))
const oauthConnected = computed(() => Boolean(oauthStatus.value?.has_token) && !oauthExpired.value)

// 行标签按账号体系命名(用户的 outcome),而不是"设备授权"这类流程名。
const accountLabel = computed(() =>
  t(form.values.client_type === 'github-copilot' ? 'provider.oauth.githubAccount' : 'provider.oauth.chatgptAccount'),
)

const connectDescription = computed(() =>
  t(form.values.client_type === 'github-copilot' ? 'provider.oauth.githubConnectHint' : 'provider.oauth.openaiConnectHint'),
)

// 连接后的身份行:优先邮箱/显示名,附 @login;两者皆空时由模板回退到"已连接"。
const connectedIdentity = computed(() => {
  const account = oauthStatus.value?.account
  if (!account) return ''
  const login = account.login?.trim()
  return [
    account.email?.trim() || account.label?.trim() || account.name?.trim() || '',
    login ? `@${login}` : '',
  ].filter(Boolean).join(' · ')
})

const devicePending = computed(() => Boolean(
  oauthStatus.value?.mode === 'device'
  && oauthStatus.value.device?.pending
  && !oauthStatus.value.has_token
  && oauthStatus.value.device.user_code
  && oauthStatus.value.device.verification_uri,
))

function clearDevicePollTimer() {
  if (devicePollTimer.value !== null) {
    window.clearTimeout(devicePollTimer.value)
    devicePollTimer.value = null
  }
}

async function fetchOAuthStatus(): Promise<ProvidersOAuthStatus | null> {
  if (!props.provider?.id) return null
  const generation = ++oauthStatusLoadGeneration
  oauthStatusLoading.value = true
  try {
    const { data } = await getProvidersByIdOauthStatus({
      path: { id: props.provider.id },
      throwOnError: true,
    })
    const nextStatus = data ?? null
    if (generation !== oauthStatusLoadGeneration) return null
    oauthStatus.value = nextStatus
    return nextStatus
  } catch (error) {
    if (generation !== oauthStatusLoadGeneration) return null
    oauthStatus.value = null
    console.error('failed to load provider oauth status', error)
    return null
  } finally {
    if (generation === oauthStatusLoadGeneration) {
      oauthStatusLoading.value = false
    }
  }
}

async function pollOAuthAuthorization(notifyOnSuccess = false) {
  if (!props.provider?.id || oauthStatus.value?.mode !== 'device') return
  try {
    const { data } = await postProvidersByIdOauthPoll({
      path: { id: props.provider.id },
      throwOnError: true,
    })
    if (!data) throw new Error(t('provider.oauth.authorizeFailed'))
    const nextStatus = data
    const becameAuthorized = !oauthStatus.value?.has_token && Boolean(nextStatus.has_token)
    oauthStatus.value = nextStatus
    if (notifyOnSuccess && becameAuthorized) {
      toast.success(t('provider.oauth.authorizeSuccess'))
      // Both managed OAuth providers need an account-scoped model catalog.
      // Sync immediately after the token is stored so the provider is usable
      // without a second manual action; failure leaves Refresh available.
      try {
        await syncProviderModelCatalog(props.provider.id)
      } catch {
        toast.error(t('models.refreshFailed'))
      }
    }
  } catch (error) {
    clearDevicePollTimer()
    toast.error(error instanceof Error ? error.message : t('provider.oauth.authorizeFailed'))
  }
}

watch(oauthStatus, (status) => {
  clearDevicePollTimer()
  if (status?.mode !== 'device' || !status.device?.pending || status.has_token) {
    return
  }
  const intervalSeconds = Math.max(status.device.interval_seconds ?? 5, 1)
  devicePollTimer.value = window.setTimeout(() => {
    void pollOAuthAuthorization(true)
  }, intervalSeconds * 1000)
})

onBeforeUnmount(() => {
  clearDevicePollTimer()
})

// 前端本地取消:providers 侧没有 cancel API(ACP 有),只能清掉本地 device 状态、
// 停掉轮询,服务端签发的码留给它自然过期。代价:刷新后 status 若仍带 pending 会
// 重新展开 —— 已报备,待后端补 cancel endpoint 后在此接上。
function cancelDeviceAuthorization() {
  clearDevicePollTimer()
  if (!oauthStatus.value) return
  oauthStatus.value = { ...oauthStatus.value, device: undefined }
}

async function handleAuthorize() {
  authorizeLoading.value = true
  try {
    let providerId = props.provider?.id
    if (!providerId) {
      const provider = await props.ensureProvider()
      providerId = provider.id
    }
    if (!providerId) throw new Error(t('provider.oauth.authorizeFailed'))

    oauthStatusLoadGeneration += 1
    oauthStatusLoading.value = false
    const { data } = await getProvidersByIdOauthAuthorize({
      path: { id: providerId },
      throwOnError: true,
    })
    if (!data) throw new Error(t('provider.oauth.authorizeFailed'))
    const result = data as ProvidersOAuthAuthorizeResponse
    if (result.mode !== 'device' || !result.device) {
      throw new Error(t('provider.oauth.authorizeFailed'))
    }
    oauthStatus.value = {
      configured: true,
      mode: 'device',
      has_token: false,
      expired: false,
      callback_url: '',
      device: result.device,
    }
  } catch (error) {
    toast.error(error instanceof Error ? error.message : t('provider.oauth.authorizeFailed'))
  } finally {
    authorizeLoading.value = false
  }
}

async function handleRevoke() {
  if (!props.provider?.id) return
  clearDevicePollTimer()
  revokeLoading.value = true
  try {
    await deleteProvidersByIdOauthToken({
      path: { id: props.provider.id },
      throwOnError: true,
    })
    toast.success(t('provider.oauth.revokeSuccess'))
    await fetchOAuthStatus()
  } catch (error) {
    toast.error(error instanceof Error ? error.message : t('provider.oauth.revokeFailed'))
  } finally {
    revokeLoading.value = false
  }
}

watch(() => form.values.client_type, (clientType) => {
  if (!isManagedOAuthClientType(clientType)) {
    oauthStatusLoadGeneration += 1
    oauthStatusLoading.value = false
    oauthStatus.value = null
  }
})

watch(() => [props.provider?.id, form.values.client_type] as const, async ([id, clientType]) => {
  if (!id || !isManagedOAuthClientType(clientType)) {
    oauthStatusLoadGeneration += 1
    oauthStatusLoading.value = false
    oauthStatus.value = null
    return
  }
  await fetchOAuthStatus()
}, { immediate: true })
</script>
