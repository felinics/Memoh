<template>
  <div class="space-y-6">
    <!-- Identity header: who this is, whether it works, its on/off and delete —
         everything about the SAVED entity lives here, so the form below is only
         ever about editing the draft. Mirrors the provider detail's header card.
         Rendered ONLY for a saved server: before creation there is no entity to
         badge, toggle, or delete, so the whole card (and its Switch) stays out. -->
    <section
      v-if="serverId"
      class="group/header flex items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card px-4 py-3"
    >
      <span class="flex size-9 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
        <component
          :is="connectionType === 'stdio' ? Terminal : Globe"
          class="size-4"
        />
      </span>

      <!-- Name: read-only text with a hover pencil (the profile-row pattern).
           Edits land in the form draft — the footer Save is what commits them. -->
      <div class="min-w-0 flex-1">
        <div class="flex h-8 items-center gap-1.5">
          <Input
            v-if="nameEditing"
            ref="nameInputRef"
            v-model="nameDraft"
            class="h-8 max-w-[20rem]"
            :aria-label="$t('common.name')"
            @keydown.enter="commitNameEdit"
            @keydown.esc="cancelNameEdit"
            @blur="commitNameEdit"
          />
          <template v-else>
            <span class="truncate text-sm font-medium">
              {{ form.name || $t('mcp.unnamedServer') }}
            </span>
            <Button
              variant="ghost"
              size="icon-sm"
              class="shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover/header:opacity-100 focus-visible:opacity-100"
              :aria-label="$t('common.edit')"
              @click="startNameEdit"
            >
              <Pencil class="size-3.5" />
            </Button>
          </template>
        </div>
      </div>

      <div class="ml-auto flex shrink-0 items-center gap-2">
        <!-- The badge reports the last KNOWN result and holds still while a new
             probe runs — progress already has a home (the test button's
             spinner), so the badge only flips when a real result lands.
             Semantic variants, never hand-injected colors. -->
        <Badge
          v-if="status === 'connected'"
          variant="success"
          size="sm"
        >
          {{ $t('mcp.statusConnected') }}
        </Badge>
        <Badge
          v-else-if="status === 'error'"
          variant="destructive"
          size="sm"
        >
          {{ $t('mcp.statusError') }}
        </Badge>
        <ConfirmPopover
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
        <Switch
          :model-value="form.active"
          :disabled="saving || activeSaving"
          :aria-label="$t('common.enabled')"
          @update:model-value="(v) => handleToggleActive(!!v)"
        />
      </div>
    </section>

    <!-- Connection: the form's only concern. No section title — on a saved
         server the header card above already names the entity, so a heading
         would echo it; on create the Name field below leads the card. -->
    <SettingsSection>
      <div class="space-y-5 p-4">
        <Field v-if="!serverId">
          <FieldLabel>{{ $t('common.name') }}</FieldLabel>
          <FieldControl>
            <Input
              v-model="form.name"
              :placeholder="$t('mcp.placeholders.name')"
            />
          </FieldControl>
        </Field>

        <Field>
          <FieldLabel>{{ $t('mcp.transportType') }}</FieldLabel>
          <FieldControl>
            <SegmentedControl
              :model-value="connectionType"
              :items="transportItems"
              class="w-full sm:w-fit"
              @update:model-value="handleTransportChange"
            />
          </FieldControl>
        </Field>

        <template v-if="connectionType === 'stdio'">
          <!-- The launch line is ONE field: docs hand out whole commands
               ("npx -y pkg"), so the form speaks that language. The official
               mcpServers schema splits command/args at rest — we parse on save
               and re-join on load (see shell-words.ts); what the user sees is
               always the line itself. -->
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
      </div>
      <!-- Test + Save close the form card. Test re-probes the SAVED server (its
           result lives on the button and the header badge — never a separate
           status line), so it is disabled while the draft diverges. -->
      <template #footer>
        <span
          v-if="serverId && hasChanges"
          class="mr-auto text-xs text-muted-foreground"
        >
          {{ $t('common.unsaved') }}
        </span>
        <HoverCard :open-delay="120">
          <HoverCardTrigger as-child>
            <span>
              <Button
                v-if="serverId"
                type="button"
                variant="outline"
                :disabled="hasChanges || saving || probing"
                @click="handleProbe(serverId)"
              >
                <Spinner
                  v-if="probing"
                  class="size-4"
                />
                <CheckDrawIcon
                  v-else-if="sessionProbeResult === 'ok'"
                  class="size-4 text-success"
                />
                <AlertCircle
                  v-else-if="sessionProbeResult === 'error'"
                  class="size-4 text-destructive"
                />
                <RefreshCw
                  v-else
                  class="size-4"
                />
                {{ $t('mcp.probe') }}
              </Button>
            </span>
          </HoverCardTrigger>
          <HoverCardContent
            v-if="sessionProbeResult === 'error' && statusMessage"
            class="max-h-40 w-80 overflow-auto text-xs text-destructive whitespace-pre-wrap break-words"
          >
            {{ statusMessage }}
          </HoverCardContent>
        </HoverCard>
        <Button
          :disabled="probing || activeSaving || (!!serverId && !hasChanges)"
          :loading="saving"
          @click="handleSave"
        >
          {{ serverId ? $t('common.save') : $t('mcp.createServer') }}
        </Button>
      </template>
    </SettingsSection>

    <!-- Advanced: env vars / headers behind a named ActionCard entry opening a
         focused dialog. The card's description names what's behind the door;
         the dialog itself carries no subtitle — its section label already does. -->
    <ActionCard
      :title="$t('mcp.advancedSettings')"
      :description="advancedHint"
      @click="showAdvanced = true"
    >
      <template #icon>
        <SlidersHorizontal />
      </template>
    </ActionCard>

    <!-- Authentication: a FIRST-CLASS section, not an Advanced knob — when a
         remote server needs sign-in, authorizing IS the main flow, so it sits
         on the page as one quiet row that grows in place (AutoHeight) when the
         flow needs more (credentials, the browser wait). Mirrors the provider
         detail's OAuth section. Saved remote servers only — OAuth state is
         stored against the connection row, so a draft has nothing to
         authorize yet. -->
    <SettingsSection
      v-if="connectionType === 'remote' && serverId"
      :title="$t('mcp.oauth.title')"
    >
      <AutoHeight>
        <!-- First load: borrow the row height so the card doesn't jump when
             the status lands. -->
        <div
          v-if="oauthStatusLoading && !oauthStatus"
          class="mx-4 flex min-h-[3.75rem] items-center justify-center py-3"
        >
          <Spinner class="size-5 text-muted-foreground" />
        </div>

        <!-- Authorized: the row IS the status. Revoke severs a live
             authorization the bot may be actively using, so it is
             confirm-gated. -->
        <SettingsRow
          v-else-if="oauthAuthorized"
          :label="$t('mcp.oauth.signInLabel')"
          :description="$t('mcp.oauth.authorized')"
        >
          <ConfirmPopover
            :message="$t('mcp.oauth.revokeConfirm')"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('mcp.oauth.revoke')"
            :loading="oauthRevoking"
            @confirm="handleOAuthRevoke"
          >
            <template #trigger>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                class="shrink-0 text-muted-foreground"
              >
                {{ $t('mcp.oauth.revoke') }}
              </Button>
            </template>
          </ConfirmPopover>
        </SettingsRow>

        <template v-else>
          <SettingsRow
            :label="$t('mcp.oauth.signInLabel')"
            :description="oauthRowDescription"
          >
            <Button
              type="button"
              variant="outline"
              size="sm"
              class="shrink-0"
              :loading="oauthDiscovering || oauthAuthorizing"
              :disabled="oauthDiscovering || oauthAuthorizing"
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
              {{ oauthDiscovering ? $t('mcp.oauth.discovering') : oauthAuthorizing ? $t('mcp.oauth.authorizing') : $t('mcp.oauth.authorize') }}
            </Button>
          </SettingsRow>

          <!-- No dynamic registration on the authorization server: the user
               must bring their own OAuth App credentials. Appears only after a
               discovery attempt found no registration endpoint. -->
          <div
            v-if="oauthNeedsClientId"
            class="mx-4 space-y-4 border-b border-border pb-4 last:border-b-0"
          >
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
          </div>
        </template>
      </AutoHeight>
    </SettingsSection>

    <Dialog v-model:open="showAdvanced">
      <DialogPanel>
        <DialogHeader>
          <DialogTitle>{{ $t('mcp.advancedSettings') }}</DialogTitle>
        </DialogHeader>
        <DialogBody class="space-y-5">
          <!-- stdio: process environment. remote: HTTP headers. -->
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
          <div
            v-else
            class="space-y-2"
          >
            <Label>{{ $t('mcp.httpHeaders') }}</Label>
            <KeyValueEditor
              v-model="headerPairs"
              :key-placeholder="$t('mcp.placeholders.headerKey')"
              :value-placeholder="$t('mcp.placeholders.headerValue')"
            />
          </div>
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
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch, type ComponentPublicInstance } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ActionCard, AutoHeight, Badge, Button, Dialog, DialogBody, DialogHeader,
  DialogPanel, DialogTitle, Field, FieldControl, FieldLabel, HoverCard, HoverCardContent,
  HoverCardTrigger, Input, Label, Select, SelectContent,
  SelectItem, SelectTrigger, SelectValue, SegmentedControl, Spinner, Switch, toast,
  type SegmentedItem,
} from '@felinic/ui'
import { AlertCircle, Globe, KeyRound, Loader2, Pencil, RefreshCw, SlidersHorizontal, Terminal, Trash2 } from 'lucide-vue-next'
import {
  postBotsByBotIdMcp, putBotsByBotIdMcpById, deleteBotsByBotIdMcpById,
  postBotsByBotIdMcpByIdProbe, getBotsByBotIdMcpByIdOauthStatus,
  postBotsByBotIdMcpByIdOauthDiscover, postBotsByBotIdMcpByIdOauthAuthorize,
  deleteBotsByBotIdMcpByIdOauthToken,
} from '@memohai/sdk'
import { client } from '@memohai/sdk/client'
import type { McpUpsertRequest, McpToolDescriptor, McpOAuthStatus } from '@memohai/sdk'
import { ConfirmPopover, SettingsRow, SettingsSection } from '@felinic/ui'
import CheckDrawIcon from '@/components/check-draw-icon/index.vue'
import KeyValueEditor from '@/components/key-value-editor/index.vue'
import type { KeyValuePair } from '@/components/key-value-editor/index.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { joinShellWords, splitShellWords } from '@/utils/shell-words'
import type { McpItem } from './mcp-types'

const props = defineProps<{
  botId: string
  server: McpItem | null
  // Set by the parent right after a create: this mount is the explicit redirect
  // target for the freshly-minted server, so it probes itself once on entry.
  autoProbe?: boolean
}>()

const emit = defineEmits<{
  changed: []
  created: [id: string]
  deleted: []
}>()

const { t } = useI18n()

// The canonical reference server from the MCP docs — the default a create
// falls back to when the command is left empty, so the one-click path builds
// a server that actually connects and shows discovered tools. Same text as
// mcp.commandPlaceholder (what the empty field suggests) — keep them in sync.
const DEFAULT_STDIO_COMMAND = 'npx -y @modelcontextprotocol/server-everything'

const serverId = ref('')
const connectionType = ref<'stdio' | 'remote'>('stdio')
const form = ref({ name: '', command: '', url: '', cwd: '', transport: 'http' as 'http' | 'sse', active: true })
const envPairs = ref<KeyValuePair[]>([])
const headerPairs = ref<KeyValuePair[]>([])

// Live connection result — owned locally so a probe never waits on a list reload.
const status = ref('unknown')
const statusMessage = ref('')
const tools = ref<McpToolDescriptor[]>([])
// Transient, this-visit-only feedback for the test button. The header badge
// owns the PERSISTED status; the button's check/alert only answers the probe
// the user actually watched happen, so it always starts bare on (re)entry.
const sessionProbeResult = ref<'ok' | 'error' | null>(null)

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
const oauthRevoking = ref(false)
const oauthStatusLoading = ref(false)
const oauthStatus = ref<McpOAuthStatus | null>(null)
const oauthClientId = ref('')
const oauthClientSecret = ref('')
const oauthNeedsClientId = ref(false)
const oauthDiscovered = ref(false)

const transportItems = computed<SegmentedItem<string>[]>(() => [
  { value: 'stdio', label: t('mcp.types.stdio') },
  { value: 'remote', label: t('mcp.types.remote') },
])

// ---- Draft vs saved snapshot ----
// The page's single source of truth rule: the status/probe always describes the
// SAVED server, never the in-flight draft. The snapshot is what the server last
// looked like (seeded from props, refreshed after every successful save), and
// every "did anything change?" answer is a diff against it. `active` is
// deliberately excluded from the draft — its switch commits on its own.
interface DraftState {
  connectionType: 'stdio' | 'remote'
  name: string
  command: string
  url: string
  cwd: string
  transport: 'http' | 'sse'
  args: string[]
  env: Record<string, string>
  headers: Record<string, string>
}

const savedSnapshot = ref<DraftState | null>(null)
// Stashes keep the opposite transport's pairs alive across a round trip
// (stdio → remote → stdio) so toggling the segment never silently destroys
// typed rows; only cross-transport leakage into a SAVE is blocked.
const envStash = ref<KeyValuePair[]>([])
const headerStash = ref<KeyValuePair[]>([])

function captureDraft(): DraftState {
  // The command field is one line for the user; the draft keeps the parsed
  // shape so snapshot diffs are semantic (re-whitespacing the line is not a
  // change) and buildBody can emit the official command/args split.
  const [command, ...args] = splitShellWords(form.value.command)
  return {
    connectionType: connectionType.value,
    name: form.value.name,
    command: command ?? '',
    url: form.value.url,
    cwd: form.value.cwd,
    transport: form.value.transport,
    args,
    env: pairsToRecord(envPairs.value),
    headers: pairsToRecord(headerPairs.value),
  }
}

// env/headers compare key-by-key: re-adding an identical pair in a different
// order is not a change, and JSON.stringify would call it one (phantom dirty).
function recordEqual(a: Record<string, string>, b: Record<string, string>): boolean {
  const aKeys = Object.keys(a)
  if (aKeys.length !== Object.keys(b).length) return false
  return aKeys.every((k) => a[k] === b[k])
}

function draftEqual(a: DraftState, b: DraftState): boolean {
  return a.connectionType === b.connectionType
    && a.name === b.name
    && a.command === b.command
    && a.url === b.url
    && a.cwd === b.cwd
    && a.transport === b.transport
    && JSON.stringify(a.args) === JSON.stringify(b.args)
    && recordEqual(a.env, b.env)
    && recordEqual(a.headers, b.headers)
}

// A never-saved server counts as dirty once it carries any content worth
// losing — flipping the transport alone is not content.
function newDraftHasContent(d: DraftState): boolean {
  return d.name.trim() !== ''
    || d.command.trim() !== ''
    || d.url.trim() !== ''
    || d.cwd.trim() !== ''
    || d.args.length > 0
    || Object.keys(d.env).length > 0
    || Object.keys(d.headers).length > 0
}

// Stashed rows are user input too: switching transport parks the opposite
// side's pairs in the stash, so a new draft whose only content sits in the
// stash (type env → switch to remote → leave) must still trip the guard.
function stashHasContent(): boolean {
  const has = (pairs: KeyValuePair[]) => pairs.some((p) => p.key.trim() !== '' || p.value.trim() !== '')
  return has(envStash.value) || has(headerStash.value)
}

const hasChanges = computed(() => {
  const snap = savedSnapshot.value
  const cur = captureDraft()
  return snap ? !draftEqual(snap, cur) : newDraftHasContent(cur) || stashHasContent()
})

// The fields a probe depends on — a rename-only save must not re-spawn the
// process or re-hit the endpoint.
function connectionFieldsChanged(a: DraftState, b: DraftState): boolean {
  return a.connectionType !== b.connectionType
    || a.command !== b.command
    || a.url !== b.url
    || a.cwd !== b.cwd
    || a.transport !== b.transport
    || JSON.stringify(a.args) !== JSON.stringify(b.args)
    || !recordEqual(a.env, b.env)
    || !recordEqual(a.headers, b.headers)
}

function handleTransportChange(v: string) {
  const next = v === 'remote' ? 'remote' : 'stdio'
  if (next === connectionType.value) return
  if (next === 'remote') {
    // stdio env means process env — meaningless on HTTP (the backend never
    // stores env for remote configs, so there is no remote env editor).
    envStash.value = envPairs.value
    envPairs.value = []
    if (headerPairs.value.length === 0 && headerStash.value.length > 0) {
      headerPairs.value = headerStash.value
    }
  } else {
    headerStash.value = headerPairs.value
    headerPairs.value = []
    if (envPairs.value.length === 0 && envStash.value.length > 0) {
      envPairs.value = envStash.value
    }
  }
  connectionType.value = next
}

// The advanced card's description names its whole content: per-transport extras
// only (env vars / headers) — authorization has its own entry above.
const advancedHint = computed(() =>
  connectionType.value === 'stdio' ? t('mcp.advancedHintStdio') : t('mcp.advancedHintRemote'),
)

// ---- Name inline edit (header) ----
// Commits to the DRAFT (form.name), never to the server — Save owns persistence.
const nameEditing = ref(false)
const nameDraft = ref('')
const nameInputRef = ref<ComponentPublicInstance | null>(null)

function startNameEdit() {
  nameDraft.value = form.value.name
  nameEditing.value = true
  void nextTick(() => {
    const el = nameInputRef.value?.$el as HTMLElement | undefined
    ;(el instanceof HTMLInputElement ? el : el?.querySelector('input'))?.focus()
  })
}

function commitNameEdit() {
  if (!nameEditing.value) return
  form.value.name = nameDraft.value.trim()
  nameEditing.value = false
}

function cancelNameEdit() {
  nameEditing.value = false
}

// The row's description is the one place auth state speaks: status when there
// is something to report (authorized / expired / the probe asked for sign-in /
// discovery needs manual credentials), and a calm "only if asked" note the
// rest of the time — most remote servers never need this row touched.
const oauthAuthorized = computed(() => !!oauthStatus.value?.has_token && !oauthStatus.value?.expired)
const oauthRowDescription = computed(() => {
  const s = oauthStatus.value
  if (s?.has_token && s.expired) return t('mcp.oauth.expired')
  if (probeAuthRequired.value && !s?.has_token) return t('mcp.oauth.signInRequired')
  if (oauthNeedsClientId.value) return t('mcp.oauth.clientIdHint')
  return t('mcp.oauth.optionalHint')
})

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

// The snapshot mirrors what is STORED (raw cfg), not what the line renders to.
// For a legacy config whose command token holds a whole line, the stored shape
// ("npx -y pkg" as one token) differs from the parsed line ("npx" + args) —
// so the repair surfaces as unsaved changes with Save enabled, and the next
// Save writes the clean split.
function snapshotFromServer(server: McpItem, draftConnectionType: 'stdio' | 'remote'): DraftState {
  const cfg = server.config ?? {}
  return {
    connectionType: draftConnectionType,
    name: server.name,
    command: configValue(cfg, 'command').trim(),
    url: configValue(cfg, 'url'),
    cwd: configValue(cfg, 'cwd'),
    transport: server.type === 'sse' ? 'sse' : 'http',
    args: configArray(cfg, 'args'),
    env: configMap(cfg, 'env'),
    headers: configMap(cfg, 'headers'),
  }
}

function seedFromServer(server: McpItem | null) {
  if (probeAbortController) {
    probeAbortController.abort()
    probeAbortController = null
  }
  probing.value = false
  showAdvanced.value = false
  nameEditing.value = false
  sessionProbeResult.value = null
  probeAuthRequired.value = false
  oauthStatus.value = null
  oauthDiscovered.value = false
  oauthNeedsClientId.value = false
  oauthClientId.value = ''
  oauthClientSecret.value = ''
  envStash.value = []
  headerStash.value = []

  if (!server) {
    serverId.value = ''
    connectionType.value = 'stdio'
    form.value = { name: '', command: '', url: '', cwd: '', transport: 'http', active: true }
    envPairs.value = []
    headerPairs.value = []
    status.value = 'unknown'
    statusMessage.value = ''
    tools.value = []
    savedSnapshot.value = null
    return
  }

  const cfg = server.config ?? {}
  serverId.value = server.id
  connectionType.value = server.type === 'stdio' ? 'stdio' : 'remote'
  form.value = {
    name: server.name,
    command: joinShellWords(configValue(cfg, 'command'), configArray(cfg, 'args')),
    url: configValue(cfg, 'url'),
    cwd: configValue(cfg, 'cwd'),
    transport: server.type === 'sse' ? 'sse' : 'http',
    active: !!server.is_active,
  }
  envPairs.value = recordToPairs(configMap(cfg, 'env'))
  headerPairs.value = recordToPairs(configMap(cfg, 'headers'))
  status.value = server.status || 'unknown'
  statusMessage.value = server.status_message || ''
  tools.value = server.tools_cache ?? []
  savedSnapshot.value = snapshotFromServer(server, connectionType.value)

  if (server.type !== 'stdio') void loadOAuthStatus()
}

seedFromServer(props.server)
if (props.autoProbe && serverId.value) void handleProbe(serverId.value)

// Re-seed only when a genuinely different server arrives (a new open session).
// The create→edit transition is an explicit redirect: the parent remounts this
// component (fresh key) seeded from the reloaded list, so no id is special here.
watch(() => props.server?.id, (id) => {
  if (id && id !== serverId.value) seedFromServer(props.server)
})

onBeforeUnmount(() => {
  if (probeAbortController) probeAbortController.abort()
  oauthFlowCleanup?.()
})

function buildBody(draft: DraftState, active: boolean): McpUpsertRequest {
  const body: McpUpsertRequest = {
    name: draft.name.trim() || t('mcp.unnamedServer'),
    is_active: active,
  }
  if (draft.connectionType === 'stdio') {
    body.command = draft.command.trim()
    if (draft.args.length > 0) body.args = draft.args
    if (Object.keys(draft.env).length > 0) body.env = draft.env
    if (draft.cwd.trim()) body.cwd = draft.cwd.trim()
  } else {
    // Remote url/headers are stored verbatim — env is a stdio-only concept
    // (process environment), so there is no ${VAR} resolution for remote.
    body.url = draft.url.trim()
    if (Object.keys(draft.headers).length > 0) {
      body.headers = { ...draft.headers }
    }
    if (draft.transport === 'sse') body.transport = 'sse'
  }
  return body
}

async function handleSave() {
  // A save must not overlap the switch's own commit: the toggle PUT carries the
  // pre-edit snapshot, and the save PUT carries the toggle's optimistic active —
  // letting both fly lets whichever lands last silently revert the other. The
  // UI disables the pair during either flight; this is the belt-and-suspenders.
  if (activeSaving.value) return
  // Validate at submit rather than disabling the button: a stdio server needs a
  // command, a remote one needs an endpoint — anything else can't connect.
  const wasNew = !serverId.value
  let draft = captureDraft()
  if (wasNew && draft.connectionType === 'stdio' && !draft.command.trim()) {
    // Create with the command left empty accepts the factory default (the same
    // example the placeholder shows), so the one-click path actually connects.
    // Edit mode never takes this branch: clearing a real server's command is a
    // deliberate act and must keep erroring.
    const [cmd, ...rest] = splitShellWords(DEFAULT_STDIO_COMMAND)
    draft = { ...draft, command: cmd ?? '', args: rest }
  }
  const hasTarget = draft.connectionType === 'stdio' ? draft.command.trim() !== '' : draft.url.trim() !== ''
  if (!hasTarget) {
    toast.error(connectionType.value === 'stdio' ? t('mcp.commandRequired') : t('mcp.urlRequired'))
    return
  }
  saving.value = true
  try {
    // Probing is expensive (a container process spawn or a network handshake),
    // so it only follows a save that could change the answer. For a create the
    // probe is not fired here: the parent remounts this detail at the new
    // server's canonical state (autoProbe), and that mount runs it.
    const needsProbe = !wasNew
      && savedSnapshot.value !== null && connectionFieldsChanged(savedSnapshot.value, draft)
    const body = buildBody(draft, form.value.active)
    if (wasNew) {
      const { data } = await postBotsByBotIdMcp({ path: { bot_id: props.botId } as unknown as { bot_id: string }, body, throwOnError: true })
      const id = data?.id ?? ''
      toast.success(t('mcp.createSuccess'))
      // Clean the draft for the brief window before the parent's redirect
      // remount, so the leave-guard can't misfire on a just-created server.
      savedSnapshot.value = draft
      if (id) emit('created', id)
      emit('changed')
    } else {
      await putBotsByBotIdMcpById({ path: { bot_id: props.botId, id: serverId.value } as unknown as { bot_id: string, id: string }, body, throwOnError: true })
      toast.success(t('mcp.updateSuccess'))
      savedSnapshot.value = draft
      emit('changed')
      if (needsProbe) void handleProbe(serverId.value)
    }
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('mcp.invalidConfig')))
  } finally {
    saving.value = false
  }
}

// The Enabled switch is NOT part of the draft: on a saved server it commits
// immediately (optimistic, rolling back on failure) — a switch reads as
// instant, so making it wait for Save was a silent-data-loss trap. The request
// carries the SNAPSHOT's other fields so in-flight draft edits are not
// accidentally committed along with the toggle.
const activeSaving = ref(false)
async function handleToggleActive(value: boolean) {
  if (!serverId.value) {
    form.value.active = value
    return
  }
  if (activeSaving.value || saving.value) return
  const snap = savedSnapshot.value
  if (!snap) return
  const prev = form.value.active
  form.value.active = value
  activeSaving.value = true
  try {
    await putBotsByBotIdMcpById({
      path: { bot_id: props.botId, id: serverId.value } as unknown as { bot_id: string, id: string },
      body: buildBody(snap, value),
      throwOnError: true,
    })
    emit('changed')
  } catch (error) {
    form.value.active = prev
    toast.error(resolveApiErrorMessage(error, t('mcp.saveFailed')))
  } finally {
    activeSaving.value = false
  }
}

async function handleProbe(id: string) {
  if (!id) return
  probing.value = true
  probeAuthRequired.value = false
  probeAbortController?.abort()
  // Each probe tracks its own controller: when probes overlap (autoProbe in
  // flight + OAuth completion re-probe), the older one's finally must not
  // blank the newer one's spinner/controller — only the latest clears.
  const controller = new AbortController()
  probeAbortController = controller
  try {
    const { data } = await postBotsByBotIdMcpByIdProbe({ path: { bot_id: props.botId, id } as unknown as { bot_id: string, id: string }, signal: controller.signal, throwOnError: true })
    if (data) {
      status.value = data.status ?? status.value
      tools.value = data.tools ?? []
      statusMessage.value = data.error ?? ''
      probeAuthRequired.value = !!data.auth_required
      sessionProbeResult.value = status.value === 'connected' ? 'ok' : 'error'
    }
    emit('changed')
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') return
    status.value = 'error'
    statusMessage.value = resolveApiErrorMessage(error, t('mcp.probeFailedNetwork'))
    sessionProbeResult.value = 'error'
    emit('changed')
  } finally {
    if (probeAbortController === controller) {
      probing.value = false
      probeAbortController = null
    }
  }
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
  oauthStatusLoading.value = true
  try {
    const { data } = await getBotsByBotIdMcpByIdOauthStatus({ path: { bot_id: props.botId, id: serverId.value } as unknown as { bot_id: string, id: string }, throwOnError: true })
    oauthStatus.value = data ?? null
  } catch {
    oauthStatus.value = null
  } finally {
    oauthStatusLoading.value = false
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
  // One flow at a time: restarting re-navigates the same named popup, which
  // aborts any in-flight callback — and a mid-exchange abort used to burn the
  // single-use auth code. The button is disabled too; this is the guard.
  if (oauthDiscovering.value || oauthAuthorizing.value) return
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
    if (!popup && !window.api?.desktop?.openExternalUrl) {
      // window.open returned null: the browser blocked the popup. Fail fast
      // with a signpost — with no window handle, the poll below has no close
      // signal and would spin silently for 120s before a generic failure.
      // (Desktop returns null by design: the URL went to the system browser
      // and the token poll is the only completion signal.)
      toast.error(t('mcp.oauth.popupBlocked'))
      oauthAuthorizing.value = false
      return
    }
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
        // The flow is done — close the authorization popup if it is still
        // around (poll-based completion can win the race against the
        // callback page's own postMessage + self-close). The section row
        // flips to Authorized in place; nothing else needs to close.
        popup?.close()
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
          // Completion means the TOKEN LANDED — `configured` is already true
          // once authorization starts (the endpoint is stored up front), so
          // polling it fires a false success seconds after the user clicks
          // Authorize, before they have even consented.
          if (next?.has_token) {
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
  if (!serverId.value || oauthRevoking.value) return
  oauthRevoking.value = true
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
  } finally {
    oauthRevoking.value = false
  }
}

// The list host reads this to guard list↔detail transitions against unsaved edits.
defineExpose({ hasChanges })
</script>
