<template>
  <div class="space-y-6">
    <!-- Connection: the form's primary concern. No section title — the back row
         already reads "MCP", so a "Connection" heading would echo the rung above. -->
    <SettingsSection>
      <div class="space-y-5 p-4">
        <!-- Name + the on/off intent share a row: what it's called, and whether
             the bot may use it. -->
        <div class="flex items-end gap-3">
          <Field class="min-w-0 flex-1">
            <FieldLabel>{{ $t('common.name') }}</FieldLabel>
            <FieldControl>
              <Input
                v-model="form.name"
                :placeholder="$t('mcp.placeholders.name')"
              />
            </FieldControl>
          </Field>
          <div class="flex h-9 shrink-0 items-center gap-3">
            <!-- Delete sits beside the on/off intent — a quiet ghost affordance,
                 the way every backend detail handles it, not a separate danger
                 block that ambushes you the moment a server is saved. Saved
                 servers only. -->
            <ConfirmPopover
              v-if="serverId"
              :message="$t('mcp.deleteConfirm')"
              :cancel-text="$t('common.cancel')"
              :confirm-text="$t('common.confirm')"
              :loading="deleting"
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
            <div class="flex items-center gap-2">
              <Label
                class="cursor-pointer text-muted-foreground"
                @click="form.active = !form.active"
              >
                {{ $t('common.enabled') }}
              </Label>
              <Switch v-model="form.active" />
            </div>
          </div>
        </div>

        <Field>
          <FieldLabel>{{ $t('mcp.transportType') }}</FieldLabel>
          <FieldControl>
            <SegmentedControl
              :model-value="connectionType"
              :items="transportItems"
              class="w-full sm:w-fit"
              @update:model-value="(v) => connectionType = v === 'remote' ? 'remote' : 'stdio'"
            />
          </FieldControl>
        </Field>

        <template v-if="connectionType === 'stdio'">
          <Field>
            <FieldLabel>{{ $t('mcp.command') }}</FieldLabel>
            <FieldControl>
              <Input
                v-model="form.command"
                class="font-mono"
                :placeholder="$t('mcp.commandPlaceholder')"
              />
            </FieldControl>
          </Field>
          <Field>
            <FieldLabel>{{ $t('mcp.arguments') }}</FieldLabel>
            <FieldControl>
              <TagsInput
                v-model="argsTags"
                :add-on-blur="true"
                :duplicate="true"
                class="font-mono"
              >
                <TagsInputItem
                  v-for="tag in argsTags"
                  :key="tag"
                  :value="tag"
                >
                  <TagsInputItemText />
                  <TagsInputItemDelete />
                </TagsInputItem>
                <TagsInputInput :placeholder="$t('mcp.argumentsPlaceholder')" />
              </TagsInput>
            </FieldControl>
          </Field>
          <Field>
            <FieldLabel
              optional
              :optional-text="$t('common.optional')"
            >
              {{ $t('mcp.cwd') }}
            </FieldLabel>
            <FieldControl>
              <Input
                v-model="form.cwd"
                class="font-mono"
                :placeholder="$t('mcp.cwdPlaceholder')"
              />
            </FieldControl>
          </Field>
        </template>

        <template v-else>
          <Field>
            <FieldLabel>{{ $t('mcp.endpointUrl') }}</FieldLabel>
            <FieldControl>
              <Input
                v-model="form.url"
                type="url"
                class="font-mono"
                :placeholder="$t('mcp.placeholders.url')"
              />
            </FieldControl>
          </Field>
          <Field>
            <FieldLabel>{{ $t('mcp.streamProtocol') }}</FieldLabel>
            <FieldControl>
              <Select v-model="form.transport">
                <SelectTrigger class="w-full sm:w-48">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="http">
                    {{ $t('mcp.protocol.http') }}
                  </SelectItem>
                  <SelectItem value="sse">
                    {{ $t('mcp.protocol.sse') }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </FieldControl>
          </Field>
        </template>

        <!-- Connection result: only meaningful once the server exists. A failed
             probe shows its raw handshake log inline, not behind a dialog. -->
        <div
          v-if="serverId"
          class="space-y-2 border-t border-border pt-4"
        >
          <div class="flex flex-wrap items-center gap-2">
            <Badge
              v-if="status === 'connected'"
              variant="outline"
              size="sm"
              class="border-success/30 text-success"
            >
              {{ $t('mcp.statusConnected') }}
            </Badge>
            <Badge
              v-else-if="status === 'error'"
              variant="outline"
              size="sm"
              class="border-destructive-border text-destructive"
            >
              {{ $t('mcp.statusError') }}
            </Badge>
            <Badge
              v-else
              variant="outline"
              size="sm"
              class="text-muted-foreground"
            >
              {{ $t('mcp.statusUnknown') }}
            </Badge>
            <span
              v-if="probing"
              class="text-xs text-muted-foreground"
            >
              {{ $t('mcp.probing') }}
            </span>
            <span
              v-else-if="lastProbedAt"
              class="text-xs text-muted-foreground"
            >
              {{ $t('mcp.testedAt', { time: relativeProbed }) }}
            </span>
          </div>
          <pre
            v-if="status === 'error' && statusMessage"
            class="max-h-40 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground"
          >{{ statusMessage }}</pre>
        </div>
      </div>
    </SettingsSection>

    <!-- Advanced: env vars / headers / auth behind a named ActionCard entry
         opening a focused dialog — the house replacement for the old in-card
         "Advanced" expand row. No section label above: the card's own
         title+description already carry the full naming (an h2 would just
         repeat it). -->
    <ActionCard
      :title="$t('mcp.advancedSettings')"
      :description="connectionType === 'stdio' ? $t('mcp.advancedHintStdio') : $t('mcp.advancedHintRemote')"
      @click="showAdvanced = true"
    >
      <template #icon>
        <SlidersHorizontal />
      </template>
    </ActionCard>

    <Dialog v-model:open="showAdvanced">
      <DialogPanel>
        <DialogHeader>
          <DialogTitle>{{ $t('mcp.advancedSettings') }}</DialogTitle>
          <DialogDescription>{{ connectionType === 'stdio' ? $t('mcp.advancedHintStdio') : $t('mcp.advancedHintRemote') }}</DialogDescription>
        </DialogHeader>
        <DialogBody class="space-y-5">
          <!-- stdio: process environment. remote: HTTP headers (+ env vars only when
               a migrated config already carries ${VAR} substitutions). -->
          <div
            v-if="connectionType === 'stdio'"
            class="space-y-2"
          >
            <Label>{{ $t('mcp.envVars') }}</Label>
            <KeyValueEditor
              v-model="envPairs"
              :key-placeholder="$t('mcp.placeholders.envKey')"
              :value-placeholder="$t('mcp.placeholders.envValue')"
            />
          </div>
          <template v-else>
            <div
              v-if="envPairs.length > 0"
              class="space-y-2"
            >
              <Label>{{ $t('mcp.envVars') }}</Label>
              <KeyValueEditor
                v-model="envPairs"
                :key-placeholder="$t('mcp.placeholders.envKey')"
                :value-placeholder="$t('mcp.placeholders.envValue')"
              />
            </div>
            <div class="space-y-2">
              <Label>{{ $t('mcp.httpHeaders') }}</Label>
              <KeyValueEditor
                v-model="headerPairs"
                :key-placeholder="$t('mcp.placeholders.headerKey')"
                :value-placeholder="$t('mcp.placeholders.headerValue')"
              />
            </div>

            <!-- Authentication (remote, saved servers only). Spotlit when a probe
                 reported it needs authorization and there's no token yet. -->
            <div
              v-if="serverId"
              class="space-y-3 border-t border-border pt-4"
            >
              <div class="flex items-center justify-between gap-2">
                <Label>{{ $t('mcp.oauth.title') }}</Label>
                <Badge
                  variant="outline"
                  size="sm"
                  :class="oauthStatus?.has_token ? 'border-success/30 text-success' : 'text-muted-foreground'"
                >
                  {{ oauthStatus?.has_token ? $t('mcp.oauth.authorized') : $t('mcp.oauth.notConfigured') }}
                </Badge>
              </div>

              <template v-if="oauthNeedsClientId && (!oauthStatus?.has_token || oauthStatus?.expired)">
                <p class="text-xs text-muted-foreground">
                  {{ $t('mcp.oauth.clientIdHint') }}
                </p>
                <Field>
                  <FieldLabel>{{ $t('mcp.oauth.clientId') }}</FieldLabel>
                  <FieldControl>
                    <Input
                      v-model="oauthClientId"
                      class="font-mono"
                      autocomplete="off"
                      :placeholder="$t('mcp.oauth.clientIdPlaceholder')"
                    />
                  </FieldControl>
                </Field>
                <Field>
                  <FieldLabel>{{ $t('mcp.oauth.clientSecret') }}</FieldLabel>
                  <FieldControl>
                    <Input
                      v-model="oauthClientSecret"
                      type="password"
                      class="font-mono"
                      autocomplete="new-password"
                      :placeholder="$t('mcp.oauth.clientSecretPlaceholder')"
                    />
                  </FieldControl>
                </Field>
              </template>

              <Button
                v-if="!oauthStatus?.has_token"
                :loading="oauthDiscovering || oauthAuthorizing"
                loading-mode="manual"
                @click="handleOAuthFlow"
              >
                <Loader2
                  v-if="oauthDiscovering || oauthAuthorizing"
                  class="size-4 animate-spin"
                />
                <KeyRound
                  v-else
                  class="size-4"
                />
                {{ oauthDiscovering ? $t('mcp.oauth.discovering') : $t('mcp.oauth.authorize') }}
              </Button>
              <Button
                v-else
                variant="outline"
                @click="handleOAuthRevoke"
              >
                {{ $t('mcp.oauth.revoke') }}
              </Button>
            </div>
          </template>
        </DialogBody>
      </DialogPanel>
    </Dialog>

    <!-- Discovered tools: a result, shown only once the server is connected.
         Read-only chips — names with the description on hover. -->
    <SettingsSection
      v-if="serverId && status === 'connected'"
      :title="$t('mcp.discoveredTools')"
    >
      <div class="p-4">
        <p
          v-if="tools.length === 0"
          class="text-sm text-muted-foreground"
        >
          {{ $t('mcp.noToolsExposed') }}
        </p>
        <div
          v-else
          class="flex flex-wrap gap-1.5"
        >
          <Badge
            v-for="tool in tools"
            :key="tool.name"
            variant="outline"
            size="sm"
            class="max-w-full truncate font-mono"
            :title="tool.description"
          >
            {{ tool.name }}
          </Badge>
        </div>
      </div>
    </SettingsSection>

    <!-- Primary actions, right-aligned at the foot of the form. Export/Test are
         secondary (outline); Save/Create is the charcoal primary. -->
    <div class="flex flex-wrap items-center justify-end gap-2">
      <Button
        v-if="serverId"
        variant="outline"
        @click="handleExport"
      >
        <Download class="size-4" />
        {{ $t('common.export') }}
      </Button>
      <Button
        v-if="serverId"
        variant="outline"
        :disabled="saving"
        @click="probing ? cancelProbe() : handleProbe(serverId)"
      >
        <Loader2
          v-if="probing"
          class="size-4 animate-spin"
        />
        <RefreshCw
          v-else
          class="size-4"
        />
        {{ probing ? $t('common.cancel') : $t('mcp.probe') }}
      </Button>
      <Button
        :disabled="probing"
        :loading="saving"
        @click="handleSave"
      >
        {{ serverId ? $t('common.save') : $t('mcp.createServer') }}
      </Button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ActionCard, Badge, Button, Dialog, DialogBody, DialogDescription, DialogHeader,
  DialogPanel, DialogTitle, Field, FieldControl, FieldLabel, Input, Label, Select, SelectContent,
  SelectItem, SelectTrigger, SelectValue, SegmentedControl, Switch, TagsInput,
  TagsInputInput, TagsInputItem, TagsInputItemDelete, TagsInputItemText, toast,
  type SegmentedItem,
} from '@felinic/ui'
import { Download, KeyRound, Loader2, RefreshCw, SlidersHorizontal, Trash2 } from 'lucide-vue-next'
import {
  postBotsByBotIdMcp, putBotsByBotIdMcpById, deleteBotsByBotIdMcpById,
  postBotsByBotIdMcpByIdProbe, getBotsByBotIdMcpByIdOauthStatus,
  postBotsByBotIdMcpByIdOauthDiscover, postBotsByBotIdMcpByIdOauthAuthorize,
  deleteBotsByBotIdMcpByIdOauthToken,
} from '@memohai/sdk'
import { client } from '@memohai/sdk/client'
import type { McpUpsertRequest, McpMcpServerEntry, McpToolDescriptor, McpOAuthStatus } from '@memohai/sdk'
import SettingsSection from '@/components/settings/section.vue'
import ConfirmPopover from '@/components/confirm-popover/index.vue'
import KeyValueEditor from '@/components/key-value-editor/index.vue'
import type { KeyValuePair } from '@/components/key-value-editor/index.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useClipboard } from '@/composables/useClipboard'
import { formatRelativeTime } from '@/utils/date-time'
import type { McpItem } from './mcp-types'

const props = defineProps<{
  botId: string
  server: McpItem | null
}>()

const emit = defineEmits<{
  changed: []
  created: [id: string]
  deleted: []
}>()

const { t, locale } = useI18n()
const { copyText } = useClipboard()

const serverId = ref('')
const connectionType = ref<'stdio' | 'remote'>('stdio')
const form = ref({ name: '', command: '', url: '', cwd: '', transport: 'http' as 'http' | 'sse', active: true })
const argsTags = ref<string[]>([])
const envPairs = ref<KeyValuePair[]>([])
const headerPairs = ref<KeyValuePair[]>([])

// Live connection result — owned locally so a probe never waits on a list reload.
const status = ref('unknown')
const statusMessage = ref('')
const lastProbedAt = ref<string | null>(null)
const tools = ref<McpToolDescriptor[]>([])

const saving = ref(false)
const probing = ref(false)
const deleting = ref(false)
const showAdvanced = ref(false)
let probeAbortController: AbortController | null = null
// Teardown for an in-flight OAuth flow (poll timer + window message listener), so
// leaving the detail mid-authorization doesn't leak a 2s interval and a listener.
let oauthFlowCleanup: (() => void) | null = null

// OAuth (remote only).
const probeAuthRequired = ref(false)
const oauthDiscovering = ref(false)
const oauthAuthorizing = ref(false)
const oauthStatus = ref<McpOAuthStatus | null>(null)
const oauthClientId = ref('')
const oauthClientSecret = ref('')
const oauthNeedsClientId = ref(false)
const oauthDiscovered = ref(false)

const transportItems = computed<SegmentedItem<string>[]>(() => [
  { value: 'stdio', label: t('mcp.types.stdio') },
  { value: 'remote', label: t('mcp.types.remote') },
])

// The minimum a server needs to connect: stdio → a command, remote → a URL.
const hasConnectionTarget = computed(() =>
  connectionType.value === 'stdio'
    ? form.value.command.trim() !== ''
    : form.value.url.trim() !== '',
)

const relativeProbed = computed(() => formatRelativeTime(lastProbedAt.value, { locale: locale.value }))

// Open the advanced dialog automatically when a probe says authorization is
// missing — the probe is a user-initiated action, so the dialog opening in
// response lands them on the auth section instead of a hidden one.
const oauthSpotlight = computed(() =>
  connectionType.value === 'remote' && probeAuthRequired.value && !oauthStatus.value?.has_token,
)
watch(oauthSpotlight, (on) => { if (on) showAdvanced.value = true })

function configValue(config: Record<string, unknown>, key: string): string {
  const val = config?.[key]
  return typeof val === 'string' ? val : ''
}
function configArray(config: Record<string, unknown>, key: string): string[] {
  const val = config?.[key]
  return Array.isArray(val) ? val.map(String) : []
}
function configMap(config: Record<string, unknown>, key: string): Record<string, string> {
  const val = config?.[key]
  if (val && typeof val === 'object' && !Array.isArray(val)) {
    const out: Record<string, string> = {}
    for (const [k, v] of Object.entries(val)) out[k] = String(v)
    return out
  }
  return {}
}
function recordToPairs(record: Record<string, string>): KeyValuePair[] {
  return Object.entries(record).map(([key, value]) => ({ key, value }))
}
function pairsToRecord(pairs: KeyValuePair[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const p of pairs) if (p.key.trim()) out[p.key.trim()] = p.value
  return out
}

function seedFromServer(server: McpItem | null) {
  if (probeAbortController) {
    probeAbortController.abort()
    probeAbortController = null
  }
  probing.value = false
  showAdvanced.value = false
  probeAuthRequired.value = false
  oauthStatus.value = null
  oauthDiscovered.value = false
  oauthNeedsClientId.value = false
  oauthClientId.value = ''
  oauthClientSecret.value = ''

  if (!server) {
    serverId.value = ''
    connectionType.value = 'stdio'
    form.value = { name: '', command: '', url: '', cwd: '', transport: 'http', active: true }
    argsTags.value = []
    envPairs.value = []
    headerPairs.value = []
    status.value = 'unknown'
    statusMessage.value = ''
    lastProbedAt.value = null
    tools.value = []
    return
  }

  const cfg = server.config ?? {}
  serverId.value = server.id
  connectionType.value = server.type === 'stdio' ? 'stdio' : 'remote'
  form.value = {
    name: server.name,
    command: configValue(cfg, 'command'),
    url: configValue(cfg, 'url'),
    cwd: configValue(cfg, 'cwd'),
    transport: server.type === 'sse' ? 'sse' : 'http',
    active: !!server.is_active,
  }
  argsTags.value = configArray(cfg, 'args')
  envPairs.value = recordToPairs(configMap(cfg, 'env'))
  headerPairs.value = recordToPairs(configMap(cfg, 'headers'))
  // NOTE: unlike the old inline expand (which auto-opened when env/headers were
  // populated so configured values never sat hidden), the advanced surface is
  // now a DIALOG — auto-popping a modal on page load would be hostile. The
  // always-visible ActionCard entry is what keeps the facet discoverable.
  status.value = server.status || 'unknown'
  statusMessage.value = server.status_message || ''
  lastProbedAt.value = server.last_probed_at
  tools.value = server.tools_cache ?? []

  if (server.type !== 'stdio') void loadOAuthStatus()
}

seedFromServer(props.server)

// Re-seed only when a genuinely different server arrives (a new open session).
// The create→edit transition passes back the same id we just minted, so it is
// ignored here — local probe state survives.
watch(() => props.server?.id, (id) => {
  if (id && id !== serverId.value) seedFromServer(props.server)
})

onBeforeUnmount(() => {
  if (probeAbortController) probeAbortController.abort()
  oauthFlowCleanup?.()
})

function buildRequestBody(): McpUpsertRequest {
  const body: McpUpsertRequest = {
    name: form.value.name.trim() || t('mcp.unnamedServer'),
    is_active: form.value.active,
  }
  if (connectionType.value === 'stdio') {
    body.command = form.value.command.trim()
    if (argsTags.value.length > 0) body.args = argsTags.value
    const envRecord = pairsToRecord(envPairs.value)
    if (Object.keys(envRecord).length > 0) body.env = envRecord
    if (form.value.cwd.trim()) body.cwd = form.value.cwd.trim()
  } else {
    const templateVars = pairsToRecord(envPairs.value)
    body.url = substituteTemplateVars(form.value.url.trim(), templateVars)
    const headerRecord = pairsToRecord(headerPairs.value)
    const resolvedHeaders = Object.fromEntries(
      Object.entries(headerRecord).map(([key, value]) => [key, substituteTemplateVars(value, templateVars)]),
    )
    if (Object.keys(resolvedHeaders).length > 0) body.headers = resolvedHeaders
    if (form.value.transport === 'sse') body.transport = 'sse'
  }
  return body
}

function substituteTemplateVars(value: string, vars: Record<string, string>) {
  return value.replace(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g, (match, key: string) => vars[key] ?? match)
}

async function handleSave() {
  // Validate at submit rather than disabling the button: a stdio server needs a
  // command, a remote one needs an endpoint — anything else can't connect.
  if (!hasConnectionTarget.value) {
    toast.error(connectionType.value === 'stdio' ? t('mcp.commandRequired') : t('mcp.urlRequired'))
    return
  }
  saving.value = true
  try {
    const body = buildRequestBody()
    if (!serverId.value) {
      const { data } = await postBotsByBotIdMcp({ path: { bot_id: props.botId } as unknown as { bot_id: string }, body, throwOnError: true })
      const id = data?.id ?? ''
      serverId.value = id
      toast.success(t('mcp.createSuccess'))
      if (id) emit('created', id)
      emit('changed')
      if (id) void handleProbe(id)
    } else {
      await putBotsByBotIdMcpById({ path: { bot_id: props.botId, id: serverId.value } as unknown as { bot_id: string, id: string }, body, throwOnError: true })
      toast.success(t('mcp.updateSuccess'))
      emit('changed')
      void handleProbe(serverId.value)
    }
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('mcp.invalidConfig')))
  } finally {
    saving.value = false
  }
}

async function handleProbe(id: string) {
  if (!id) return
  probing.value = true
  probeAuthRequired.value = false
  if (probeAbortController) probeAbortController.abort()
  probeAbortController = new AbortController()
  try {
    const { data } = await postBotsByBotIdMcpByIdProbe({ path: { bot_id: props.botId, id } as unknown as { bot_id: string, id: string }, throwOnError: true })
    if (data) {
      status.value = data.status ?? status.value
      tools.value = data.tools ?? []
      statusMessage.value = data.error ?? ''
      lastProbedAt.value = new Date().toISOString()
      probeAuthRequired.value = !!data.auth_required
    }
    emit('changed')
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') return
    status.value = 'error'
    statusMessage.value = resolveApiErrorMessage(error, t('mcp.probeFailedNetwork'))
    lastProbedAt.value = new Date().toISOString()
    emit('changed')
  } finally {
    probing.value = false
    probeAbortController = null
  }
}

function cancelProbe() {
  if (probeAbortController) {
    probeAbortController.abort()
    probeAbortController = null
  }
  probing.value = false
  toast.info(t('mcp.probeAborted'))
}

async function handleDelete() {
  if (!serverId.value) return
  deleting.value = true
  try {
    await deleteBotsByBotIdMcpById({ path: { bot_id: props.botId, id: serverId.value } as unknown as { bot_id: string, id: string }, throwOnError: true })
    toast.success(t('mcp.deleteSuccess'))
    emit('deleted')
  } catch {
    toast.error(t('mcp.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

function handleExport() {
  if (!serverId.value) return
  const isStdio = connectionType.value === 'stdio'
  const args = argsTags.value
  const env = pairsToRecord(envPairs.value)
  const headers = pairsToRecord(headerPairs.value)
  const entry: McpMcpServerEntry = {
    command: isStdio ? form.value.command.trim() || undefined : undefined,
    args: isStdio && args.length ? args : undefined,
    env: isStdio && Object.keys(env).length ? env : undefined,
    cwd: isStdio && form.value.cwd.trim() ? form.value.cwd.trim() : undefined,
    url: !isStdio ? form.value.url.trim() || undefined : undefined,
    headers: !isStdio && Object.keys(headers).length ? headers : undefined,
    transport: !isStdio && form.value.transport === 'sse' ? 'sse' : undefined,
  }
  copyText(JSON.stringify({ mcpServers: { [form.value.name.trim() || t('mcp.unnamedServer')]: entry } }, null, 2))
  toast.success(t('mcp.copySuccess'))
}

// ---- OAuth (remote) ----
function mcpOAuthCallbackUrl() {
  const rawBase = String(client.getConfig().baseUrl || '/api')
  const base = new URL(rawBase, window.location.origin)
  base.pathname = `${base.pathname.replace(/\/+$/, '')}/oauth/mcp/callback`
  base.search = ''
  base.hash = ''
  return base.toString()
}

async function loadOAuthStatus() {
  if (!serverId.value || connectionType.value === 'stdio') {
    oauthStatus.value = null
    return
  }
  try {
    const { data } = await getBotsByBotIdMcpByIdOauthStatus({ path: { bot_id: props.botId, id: serverId.value } as unknown as { bot_id: string, id: string }, throwOnError: true })
    oauthStatus.value = data ?? null
  } catch {
    oauthStatus.value = null
  }
}

async function handleOAuthDiscover(): Promise<boolean> {
  if (!serverId.value) return false
  oauthDiscovering.value = true
  oauthNeedsClientId.value = false
  try {
    const { data } = await postBotsByBotIdMcpByIdOauthDiscover({ path: { bot_id: props.botId, id: serverId.value } as unknown as { bot_id: string, id: string }, throwOnError: true })
    if (!data?.registration_endpoint) oauthNeedsClientId.value = true
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('mcp.oauth.discoverFailed')))
    oauthDiscovering.value = false
    return false
  }
  oauthDiscovering.value = false
  return true
}

async function handleOAuthFlow() {
  if (!serverId.value) return
  if (!oauthDiscovered.value) {
    const discovered = await handleOAuthDiscover()
    if (!discovered) return
    oauthDiscovered.value = true
    if (oauthNeedsClientId.value && !oauthClientId.value.trim()) return
  }

  oauthAuthorizing.value = true
  try {
    const { data } = await postBotsByBotIdMcpByIdOauthAuthorize({
      path: { bot_id: props.botId, id: serverId.value } as unknown as { bot_id: string, id: string },
      body: {
        client_id: oauthClientId.value.trim() || undefined,
        client_secret: oauthClientSecret.value.trim() || undefined,
        callback_url: mcpOAuthCallbackUrl(),
      },
      throwOnError: true,
    })
    if (!data?.authorization_url) throw new Error(t('mcp.oauth.flowInitFailed'))

    const popup = await openOAuthURL(data.authorization_url)
    let completed = false
    let pollTimer: ReturnType<typeof setInterval> | undefined
    const finishOAuth = async (result: 'success' | 'error', error?: string) => {
      if (completed) return
      completed = true
      if (pollTimer) clearInterval(pollTimer)
      window.removeEventListener('message', onMessage)
      oauthFlowCleanup = null
      oauthAuthorizing.value = false
      await loadOAuthStatus()
      if (result === 'success') {
        toast.success(t('mcp.oauth.authSuccess'))
        void handleProbe(serverId.value)
      } else {
        toast.error(error || t('mcp.oauth.authFailed'))
      }
    }
    const onMessage = async (event: MessageEvent) => {
      if (event.data?.type === 'mcp-oauth-callback') {
        await finishOAuth(event.data.status === 'success' ? 'success' : 'error', event.data.error)
      }
    }
    window.addEventListener('message', onMessage)
    oauthFlowCleanup = () => {
      completed = true
      if (pollTimer) clearInterval(pollTimer)
      window.removeEventListener('message', onMessage)
      oauthFlowCleanup = null
    }

    const startedAt = Date.now()
    pollTimer = setInterval(() => {
      if (completed || !serverId.value) return
      getBotsByBotIdMcpByIdOauthStatus({ path: { bot_id: props.botId, id: serverId.value } as unknown as { bot_id: string, id: string }, throwOnError: true })
        .then(async ({ data: next }) => {
          if (completed) return
          if (next?.configured) {
            oauthStatus.value = next
            await finishOAuth('success')
            return
          }
          if (popup?.closed || Date.now() - startedAt > 120_000) await finishOAuth('error')
        })
        .catch(() => {
          if (!completed && (popup?.closed || Date.now() - startedAt > 120_000)) void finishOAuth('error')
        })
    }, 2000)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('mcp.oauth.flowInitFailed')))
    oauthAuthorizing.value = false
  }
}

async function openOAuthURL(url: string): Promise<Window | null> {
  const desktopOpenExternal = window.api?.desktop?.openExternalUrl
  if (desktopOpenExternal) {
    await desktopOpenExternal(url)
    return null
  }
  return window.open(url, 'mcp-oauth', 'width=600,height=700')
}

async function handleOAuthRevoke() {
  if (!serverId.value) return
  try {
    await deleteBotsByBotIdMcpByIdOauthToken({ path: { bot_id: props.botId, id: serverId.value } as unknown as { bot_id: string, id: string }, throwOnError: true })
    toast.success(t('mcp.oauth.revokeSuccess'))
    oauthDiscovered.value = false
    oauthNeedsClientId.value = false
    oauthClientId.value = ''
    oauthClientSecret.value = ''
    await loadOAuthStatus()
  } catch {
    toast.error(t('mcp.oauth.revokeFailed'))
  }
}
</script>
