<template>
  <form @submit.prevent="submitDraft">
    <SettingsSection
      v-if="!isManagedOAuthProvider"
      :title="$t('provider.configurationTitle')"
    >
      <!-- Field rows are grouped so the LAST one keeps its `last:border-b-0`
           (no trailing inset hairline) — the footer below owns the only divider,
           and it spans full width. -->
      <div>
        <SettingsRow :label="$t('common.name')">
          <div class="w-80">
            <!-- Free-typing draft committed on blur/Enter (appearance-page
                 idiom): autosave must fire once per edit, not per keystroke. -->
            <Input
              type="text"
              :model-value="nameDraft"
              :placeholder="$t('common.namePlaceholder')"
              :aria-label="$t('common.name')"
              :aria-invalid="!!draftErrors.name"
              @update:model-value="(value) => nameDraft = String(value ?? '')"
              @focus="nameFocused = true"
              @change="commitNameDraft"
              @blur="nameFocused = false; commitNameDraft()"
              @keydown.enter="commitNameDraft"
            />
            <p
              v-if="draftErrors.name"
              class="mt-1 text-body text-destructive"
            >
              {{ draftErrors.name }}
            </p>
          </div>
        </SettingsRow>

        <SettingsRow :label="$t('provider.clientType')">
          <div class="w-80">
            <Select
              :model-value="form.client_type"
              @update:model-value="(value) => updateClientType(String(value ?? ''))"
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
          </div>
        </SettingsRow>

        <SettingsRow
          v-if="form.client_type !== 'github-copilot'"
          :label="$t('provider.url')"
        >
          <div class="w-80">
            <Input
              type="text"
              :model-value="baseUrlDraft"
              :placeholder="$t('provider.urlPlaceholder')"
              :aria-label="$t('provider.url')"
              :aria-invalid="!!draftErrors.base_url"
              @update:model-value="(value) => baseUrlDraft = String(value ?? '')"
              @focus="baseUrlFocused = true"
              @change="commitBaseUrlDraft"
              @blur="baseUrlFocused = false; commitBaseUrlDraft()"
              @keydown.enter="commitBaseUrlDraft"
            />
            <p
              v-if="draftErrors.base_url"
              class="mt-1 text-body text-destructive"
            >
              {{ draftErrors.base_url }}
            </p>
          </div>
        </SettingsRow>

        <SettingsRow
          v-if="!isManagedOAuthClientType(form.client_type)"
          :label="$t('provider.apiKey')"
        >
          <div class="w-80">
            <!-- The key is write-only: the box starts empty, commits only a
                 non-empty value, and clears itself once stored. An empty
                 commit is a no-op so autosave can never wipe a secret. -->
            <Input
              type="password"
              :model-value="apiKeyDraft"
              :placeholder="getStoredSecret(props.provider?.config as Record<string, unknown> | undefined) || $t('provider.apiKeyPlaceholder')"
              :aria-label="$t('provider.apiKey')"
              :aria-invalid="!!draftErrors.api_key"
              @update:model-value="(value) => apiKeyDraft = String(value ?? '')"
              @focus="apiKeyFocused = true"
              @change="commitApiKeyDraft"
              @blur="apiKeyFocused = false; commitApiKeyDraft()"
              @keydown.enter="commitApiKeyDraft"
            />
            <p
              v-if="draftErrors.api_key"
              class="mt-1 text-body text-destructive"
            >
              {{ draftErrors.api_key }}
            </p>
          </div>
        </SettingsRow>

        <SettingsRow
          v-if="supportsPromptCache(form.client_type)"
          :label="$t('provider.promptCache.label')"
          :description="cacheDescription"
        >
          <Select
            :model-value="form.prompt_cache_ttl"
            @update:model-value="(value) => form.prompt_cache_ttl = normalizeCacheTtl(String(value ?? ''))"
          >
            <SelectTrigger
              size="sm"
              class="min-w-36"
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
        </SettingsRow>
      </div>

      <!-- Actions close the card via the section's footer band. An existing
           provider autosaves every field, so only the test button remains;
           the Save button exists solely to MATERIALIZE a template draft —
           creating a provider is a deliberate act, not an autosave. -->
      <template #footer>
        <HoverCard :open-delay="120">
          <HoverCardTrigger as-child>
            <Button
              type="button"
              variant="outline"
              size="sm"
              loading-mode="manual"
              :loading="testLoading"
              :disabled="!props.provider?.id"
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

        <LoadingButton
          v-if="isDraft"
          type="submit"
          size="sm"
          :loading="editLoading"
        >
          {{ $t('provider.saveChanges') }}
        </LoadingButton>
      </template>
    </SettingsSection>

    <!-- OAuth 账号:设备码授权。结构镜像 profile/connected-accounts-section(同一
         形状的已重构参考):一行账号状态 + 行内动作,等待输码时才在卡片内追加
         居中的验证码块(倒计时 + 复制并打开),轮询在后台静默完成授权。 -->
    <SettingsSection
      v-if="isManagedOAuthClientType(form.client_type)"
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
              :hint="$t(form.client_type === 'github-copilot' ? 'provider.oauth.githubDeviceHint' : 'provider.oauth.openaiDeviceHint')"
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
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
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
import { useAutosaveQueue, type AutosaveJob } from '@/composables/use-autosave-queue'

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
  // Promise-returning save (the parent's mutation): the autosave queue awaits
  // it so a failure can roll the field back; fire-and-forget emits can't.
  saveProvider: (payload: Record<string, unknown>) => Promise<unknown>
}>()

const isDraft = computed(() => !props.provider?.id && !!props.provider?.provider_template_id)
const isManagedOAuthProvider = computed(() => isManagedOAuthClientType(props.provider?.client_type))

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
  form.prompt_cache_ttl === 'off'
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
  if (!props.provider?.id) return
  testLoading.value = true
  testResult.value = null
  testError.value = ''
  try {
    const { data } = await postProvidersByIdTest({
      path: { id: props.provider.id },
      throwOnError: true,
    })
    testResult.value = data ?? null
    if (testResult.value?.status !== 'ok') {
      testError.value = formatTestError(testResult.value?.message)
    }
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : ''
    testError.value = message ? formatTestError(message) : t('provider.testFailed')
  } finally {
    testLoading.value = false
  }
}

watch(() => props.provider?.id, () => {
  testResult.value = null
  testError.value = ''
})

const clientTypeOptions = computed(() =>
  MANUAL_LLM_CLIENT_TYPE_LIST.map((ct) => ({
    value: ct.value,
    label: ct.label,
  })),
)

// ---- Config fields (autosaved for an existing provider) ----
// Web skill §8: an existing provider's fields auto-save (the backend Update
// is pointer-partial and shallow-merges config, preserving masked secrets),
// silently on success, toast + rollback on failure. The one manual Save left
// is draft materialization — creating the provider at all.
// A type alias (not interface) so the record satisfies the queue's
// Record<string, unknown> constraint.
type ProviderFormRecord = {
  name: string
  client_type: string
  base_url: string
  // Write-only: never hydrated from the server; synced stays '' so a
  // committed key diffs exactly once and a rollback returns to ''.
  api_key: string
  prompt_cache_ttl: PromptCacheTtl
}

const form = reactive<ProviderFormRecord>({
  name: '',
  client_type: 'openai-completions',
  base_url: '',
  api_key: '',
  prompt_cache_ttl: '5m',
})

// Last-known-server snapshot; see bot-settings.vue for the full contract.
const synced = reactive<ProviderFormRecord>({ ...form })

// Switching client type rewrites the base URL default in the same tick, so
// the pair saves as ONE job — never a codex type with a stale openai URL.
// Hydration applies the same defaults to `next` before writing form+synced,
// or this write would read as a user edit and trigger a phantom save.
function applyClientTypeDefaults(target: ProviderFormRecord) {
  if (target.client_type === 'openai-codex' && !target.base_url) {
    target.base_url = 'https://chatgpt.com/backend-api'
  }
  if (target.client_type === 'github-copilot') {
    target.base_url = ''
  }
}

function updateClientType(value: string) {
  form.client_type = value
  applyClientTypeDefaults(form)
}

watch(() => props.provider, (newVal) => {
  if (!newVal) return
  const cfg = newVal.config as Record<string, unknown> | undefined
  const next: ProviderFormRecord = {
    name: newVal.name ?? '',
    client_type: newVal.client_type || 'openai-completions',
    base_url: (cfg?.base_url as string) ?? '',
    api_key: '',
    prompt_cache_ttl: normalizeCacheTtl(cfg?.prompt_cache_ttl as string | undefined),
  }
  applyClientTypeDefaults(next)
  // Per-field guard: a refetch landing mid-edit must not clobber it.
  for (const key of Object.keys(next) as (keyof ProviderFormRecord)[]) {
    if (form[key] === synced[key]) form[key] = next[key] as never
    synced[key] = next[key] as never
  }
}, { immediate: true })

// Text drafts commit on blur/Enter; an invalid commit reverts to the last
// committed value (autosave replaces the old "Save stays disabled" gate).
const nameDraft = ref(form.name)
const nameFocused = ref(false)
const baseUrlDraft = ref(form.base_url)
const baseUrlFocused = ref(false)
const apiKeyDraft = ref('')
const apiKeyFocused = ref(false)

watch(() => form.name, (value) => {
  if (!nameFocused.value) nameDraft.value = value
})
watch(() => form.base_url, (value) => {
  if (!baseUrlFocused.value) baseUrlDraft.value = value
})

function commitNameDraft() {
  const value = nameDraft.value.trim()
  if (!value) {
    nameDraft.value = form.name
    return
  }
  form.name = value
  nameDraft.value = form.name
}

function commitBaseUrlDraft() {
  const value = baseUrlDraft.value.trim()
  if (!value && form.client_type !== 'github-copilot') {
    baseUrlDraft.value = form.base_url
    return
  }
  form.base_url = value
  baseUrlDraft.value = form.base_url
}

function commitApiKeyDraft() {
  const value = apiKeyDraft.value.trim()
  if (!value) {
    apiKeyDraft.value = ''
    return
  }
  form.api_key = value
  // Committing the key that's already stored produces no diff (no save, no
  // onSaved) — clear the box here or it would keep showing the typed secret.
  if (form.api_key === synced.api_key) apiKeyDraft.value = ''
}

// Draft-materialization validation errors (Save exists only in draft mode);
// they clear as the user edits.
const draftErrors = reactive({ name: '', base_url: '', api_key: '' })
watch(nameDraft, () => { draftErrors.name = '' })
watch(baseUrlDraft, () => { draftErrors.base_url = '' })
watch(apiKeyDraft, () => { draftErrors.api_key = '' })

function buildProviderPayload(values: ProviderFormRecord, keys: (keyof ProviderFormRecord)[]): Record<string, unknown> {
  const config: Record<string, unknown> = {}
  const payload: Record<string, unknown> = {}
  for (const key of keys) {
    if (key === 'name') payload.name = values.name
    else if (key === 'client_type') payload.client_type = values.client_type
    else if (key === 'base_url') config.base_url = values.base_url
    else if (key === 'api_key') {
      // Never ship an empty key: empty means "keep the stored secret"
      // (backend shallow-merges config and preserves masked secrets), and
      // autosave commits are already gated non-empty.
      if (values.api_key.trim()) config.api_key = values.api_key.trim()
    }
    else if (key === 'prompt_cache_ttl') config.prompt_cache_ttl = normalizeCacheTtl(values.prompt_cache_ttl)
  }
  if (Object.keys(config).length > 0) payload.config = config
  // Switching to github-copilot must scrub the stale managed-OAuth client id
  // left by a previous codex-type save (whole-replace metadata semantics).
  if (keys.includes('client_type') && values.client_type === 'github-copilot') {
    const metadata = {
      ...((props.provider?.metadata as Record<string, unknown> | undefined) ?? {}),
    }
    delete metadata.oauth_client_id
    payload.metadata = metadata
  }
  return payload
}

function buildJobs(changed: (keyof ProviderFormRecord)[]): AutosaveJob<ProviderFormRecord>[] {
  // Drafts (and nothing-selected) never autosave: the only way out of a
  // template draft is the deliberate Save (materialize) or OAuth connect.
  if (!props.provider?.id) return []
  const sent: Partial<ProviderFormRecord> = {}
  for (const key of changed) sent[key] = form[key] as never
  const payload = buildProviderPayload(form, changed)
  const includesApiKey = changed.includes('api_key')
  return [{
    payload: sent,
    save: async () => {
      await props.saveProvider(payload)
    },
    onSaved: () => {
      // The key now lives server-side; the box returns to its write-only
      // empty state (placeholder shows the stored secret).
      if (includesApiKey) apiKeyDraft.value = ''
    },
    onError: (error) => toast.error(resolveApiErrorMessage(error, t('common.saveFailed'))),
  }]
}

useAutosaveQueue<ProviderFormRecord>({
  form,
  synced,
  buildJobs,
})

// Manual Save = materialize a template draft. Validation lives here (not in a
// schema library): name required, base URL required off-copilot, API key
// required unless the type is managed-OAuth.
async function submitDraft() {
  if (!isDraft.value) return
  commitNameDraft()
  commitBaseUrlDraft()
  commitApiKeyDraft()
  let valid = true
  if (!form.name.trim()) {
    draftErrors.name = 'Name is required'
    valid = false
  }
  if (form.client_type !== 'github-copilot' && !form.base_url.trim()) {
    draftErrors.base_url = 'Base URL is required'
    valid = false
  }
  if (!isManagedOAuthClientType(form.client_type) && !form.api_key.trim()) {
    draftErrors.api_key = 'API key is required'
    valid = false
  }
  if (!valid) return

  const payload = buildProviderPayload(form, ['name', 'client_type', 'base_url', 'api_key', 'prompt_cache_ttl'])
  payload.enable = props.provider?.enable ?? true
  try {
    await props.saveProvider(payload)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  }
}

const oauthExpired = computed(() => Boolean(oauthStatus.value?.has_token && oauthStatus.value?.expired))
const oauthConnected = computed(() => Boolean(oauthStatus.value?.has_token) && !oauthExpired.value)

// 行标签按账号体系命名(用户的 outcome),而不是"设备授权"这类流程名。
const accountLabel = computed(() =>
  t(form.client_type === 'github-copilot' ? 'provider.oauth.githubAccount' : 'provider.oauth.chatgptAccount'),
)

const connectDescription = computed(() =>
  t(form.client_type === 'github-copilot' ? 'provider.oauth.githubConnectHint' : 'provider.oauth.openaiConnectHint'),
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

watch(() => form.client_type, (clientType) => {
  if (!isManagedOAuthClientType(clientType)) {
    oauthStatusLoadGeneration += 1
    oauthStatusLoading.value = false
    oauthStatus.value = null
  }
})

watch(() => [props.provider?.id, form.client_type] as const, async ([id, clientType]) => {
  if (!id || !isManagedOAuthClientType(clientType)) {
    oauthStatusLoadGeneration += 1
    oauthStatusLoading.value = false
    oauthStatus.value = null
    return
  }
  await fetchOAuthStatus()
}, { immediate: true })
</script>
