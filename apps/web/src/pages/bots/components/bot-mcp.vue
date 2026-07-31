<template>
  <SwapTransition :direction="direction">
    <!-- List: one server per card, two-up. The card opens its setup; a dashed
         tile beside real servers is the secondary "add" affordance. -->
    <PageShell
      v-if="view === 'list'"
      variant="tab"
      :title="$t('bots.tabs.mcp')"
    >
      <template #actions>
        <div
          v-if="showSearch"
          class="w-40 sm:w-56"
        >
          <InputGroup class="w-full">
            <InputGroupAddon align="inline-start">
              <Search class="size-3.5 text-muted-foreground" />
            </InputGroupAddon>
            <InputGroupInput
              v-model="searchText"
              :placeholder="$t('mcp.searchServers')"
            />
          </InputGroup>
        </div>
        <TooltipProvider>
          <Tooltip :delay-duration="300">
            <TooltipTrigger as-child>
              <Button
                variant="outline"
                size="icon"
                :aria-label="$t('common.import')"
                @click="startImport"
              >
                <Upload class="size-4" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="bottom">
              {{ $t('common.import') }}
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
        <Button @click="openCreate">
          <Plus class="size-4" />
          {{ $t('mcp.addNew') }}
        </Button>
      </template>

      <div
        v-if="loading && items.length === 0"
        class="grid grid-cols-1 gap-3 sm:grid-cols-2"
      >
        <Skeleton
          v-for="n in 4"
          :key="n"
          class="h-[4.5rem] w-full rounded-[var(--radius-menu-shell)]"
        />
      </div>

      <div
        v-else-if="items.length > 0"
        class="grid grid-cols-1 gap-3 sm:grid-cols-2"
      >
        <BackendCard
          v-for="item in filteredItems"
          :key="item.id"
          :name="item.name || $t('mcp.unnamedServer')"
          :subtitle="cardSubtitle(item)"
          :enabled="item.is_active"
          @click="openServer(item)"
        >
          <template #leading>
            <span class="flex size-10 items-center justify-center rounded-full bg-muted text-muted-foreground">
              <component
                :is="item.type === 'stdio' ? Terminal : Globe"
                class="size-5"
              />
            </span>
          </template>
          <template #trailing>
            <div class="flex items-center gap-2">
              <Badge
                v-if="item.is_active && item.status === 'error'"
                variant="destructive"
                size="sm"
              >
                {{ $t('mcp.statusError') }}
              </Badge>
              <ChevronRight class="size-4 shrink-0 text-muted-foreground/60" />
            </div>
          </template>
        </BackendCard>

        <button
          type="button"
          class="flex min-h-[4.5rem] items-center justify-center gap-2 rounded-[var(--radius-menu-shell)] border border-dashed border-border bg-background text-sm text-muted-foreground transition-colors hover:border-foreground/30 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          @click="openCreate"
        >
          <Plus class="size-4" />
          {{ $t('mcp.addNew') }}
        </button>
      </div>

      <Empty
        v-else
        class="rounded-[var(--radius-menu-shell)] border border-border py-16"
      >
        <EmptyTitle>{{ $t('mcp.emptyTitle') }}</EmptyTitle>
        <EmptyDescription>{{ $t('mcp.emptyDescription') }}</EmptyDescription>
        <EmptyContent>
          <Button
            variant="outline"
            @click="openCreate"
          >
            <Plus class="size-4" />
            {{ $t('mcp.addNew') }}
          </Button>
        </EmptyContent>
      </Empty>
    </PageShell>

    <!-- Setup: the selected server only. Padding mirrors the list's PageShell tab
         variant so the back arrow lands at the list title's height. -->
    <section
      v-else
      class="mx-auto max-w-3xl pt-6 pb-8"
    >
      <Button
        variant="ghost"
        class="mb-6 text-foreground/85"
        @click="requestLeave(backToList)"
      >
        <ChevronLeft class="size-4" />
        {{ $t('bots.tabs.mcp') }}
      </Button>

      <!-- AutoHeight sits ABOVE the detail's remount boundary (detailKey), so
           the shape changes that remount or probe results produce — create form
           → saved header card, Discovered Tools appearing after the auto-probe —
           grow the surface smoothly instead of hard-cutting (the OAuth
           device-code card pattern). First paint still cuts, per the primitive. -->
      <AutoHeight>
        <McpServerDetail
          ref="detailRef"
          :key="detailKey"
          :bot-id="botId"
          :server="selectedServer"
          :auto-probe="probeOnCreate"
          @created="onDetailCreated"
          @changed="loadList"
          @deleted="onDetailDeleted"
        />
      </AutoHeight>
    </section>
  </SwapTransition>

  <!-- Import: paste a standard mcpServers JSON; same-name servers are updated.
       Same unified DialogPanel shell as the other focused dialogs (appearance /
       access) — no full-bleed border-b/border-t bars (dividers separate peers
       inside a body, they don't frame the shell). `grow` (fixed height, not
       max-h): Monaco has no intrinsic height, so the body row must be given
       one to fill. -->
  <Dialog v-model:open="importOpen">
    <DialogPanel
      grow
      footer
      width="3xl"
    >
      <DialogHeader>
        <DialogTitle>{{ $t('mcp.importSandbox') }}</DialogTitle>
        <DialogDescription>{{ $t('mcp.importHint') }}</DialogDescription>
      </DialogHeader>

      <div class="flex min-h-0 flex-col">
        <div class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-[var(--radius-menu-shell)] border border-border">
          <MonacoEditor
            v-model="importJson"
            language="json"
            :readonly="importSubmitting"
            class="min-h-0 flex-1"
            :options="{
              automaticLayout: true,
              fixedOverflowWidgets: true,
              minimap: { enabled: false },
              scrollBeyondLastLine: false,
            }"
          />
        </div>
        <p
          v-if="importError"
          class="mt-2 text-xs text-destructive"
        >
          {{ importError }}
        </p>
      </div>

      <DialogFooter class="items-center gap-2 sm:justify-between">
        <Button
          variant="outline"
          :disabled="importSubmitting"
          @click="formatImportJson"
        >
          {{ $t('common.format') }}
        </Button>
        <div class="flex items-center gap-2">
          <DialogClose as-child>
            <Button
              variant="ghost"
              :disabled="importSubmitting"
            >
              {{ $t('common.cancel') }}
            </Button>
          </DialogClose>
          <Button
            class="min-w-24"
            :disabled="!importJson.trim()"
            :loading="importSubmitting"
            @click="executeImport"
          >
            {{ $t('mcp.blindImport') }}
          </Button>
        </div>
      </DialogFooter>
    </DialogPanel>
  </Dialog>

  <!-- Unsaved-changes guard: leaving the detail (back / switch server / create
       new) with a dirty draft asks first. Known gap: browser-back and sidebar
       re-clicks flip the view via useViewSwap's route watcher and bypass this —
       accepted, since the draft lives in the detail component either way. -->
  <Dialog v-model:open="leaveConfirmOpen">
    <DialogPanel>
      <DialogHeader>
        <DialogTitle>{{ $t('common.unsaved') }}</DialogTitle>
        <DialogDescription>{{ $t('mcp.unsavedChangesDesc') }}</DialogDescription>
      </DialogHeader>
      <DialogFooter class="gap-2">
        <Button
          variant="ghost"
          @click="leaveConfirmOpen = false"
        >
          {{ $t('mcp.keepEditing') }}
        </Button>
        <Button
          variant="outline"
          @click="confirmDiscard"
        >
          {{ $t('mcp.discardAndSwitch') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  AutoHeight, Badge, Button, Dialog, DialogClose, DialogDescription, DialogFooter,
  DialogHeader, DialogPanel, DialogTitle, Empty, EmptyContent, EmptyDescription, EmptyTitle,
  InputGroup, InputGroupAddon, InputGroupInput, Skeleton, toast, Tooltip,
  TooltipContent, TooltipProvider, TooltipTrigger,
} from '@felinic/ui'
import { ChevronLeft, ChevronRight, Globe, Plus, Search, Terminal, Upload } from 'lucide-vue-next'
import { getBotsByBotIdMcp, putBotsByBotIdMcpImport } from '@memohai/sdk'
import type { McpImportRequest, McpToolDescriptor } from '@memohai/sdk'
import { BackendCard, PageShell, SwapTransition } from '@felinic/ui'
import McpServerDetail from './mcp-server-detail.vue'
import type { McpItem } from './mcp-types'
import MonacoEditor from '@/components/monaco-editor/index.vue'
import { useViewSwap } from '@/composables/useViewSwap'
import { useSyncedQueryParam } from '@/composables/useSyncedQueryParam'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { joinShellWords } from '@/utils/shell-words'

const props = defineProps<{ botId: string }>()
const { t } = useI18n()

const { view, direction, openDetail, backToList } = useViewSwap()
const loading = ref(false)
const items = ref<McpItem[]>([])
const searchText = ref('')
const selectedId = ref('')
const selectedMcpId = useSyncedQueryParam('mcpId', '')

// A stable key for the open detail session: it changes when the user opens a
// different server, starts a create, or a create redirects onto its new id —
// each of those is a fresh editing session with its own seeded draft.
const detailKey = ref('')
let keySeq = 0

const importOpen = ref(false)
const importJson = ref('{\n  "mcpServers": {\n    \n  }\n}')
const importSubmitting = ref(false)
const importError = ref('')

const showSearch = computed(() => items.value.length > 0)

const filteredItems = computed(() => {
  const kw = searchText.value.trim().toLowerCase()
  if (!kw) return items.value
  return items.value.filter(i => (i.name || '').toLowerCase().includes(kw))
})

const selectedServer = computed(() => items.value.find(i => i.id === selectedId.value) ?? null)

// The detail owns the draft; the list guards transitions that would discard it.
const detailRef = ref<{ hasChanges: boolean } | null>(null)
const leaveConfirmOpen = ref(false)
let pendingLeave: (() => void) | null = null

function requestLeave(action: () => void) {
  if (detailRef.value?.hasChanges) {
    pendingLeave = action
    leaveConfirmOpen.value = true
    return
  }
  action()
}

function confirmDiscard() {
  leaveConfirmOpen.value = false
  const action = pendingLeave
  pendingLeave = null
  action?.()
}

function cardSubtitle(item: McpItem): string {
  const cfg = item.config ?? {}
  if (item.type === 'stdio') {
    const command = typeof cfg.command === 'string' ? cfg.command : ''
    const args = Array.isArray(cfg.args) ? cfg.args.map(String) : []
    return joinShellWords(command, args)
  }
  const url = typeof cfg.url === 'string' ? cfg.url : ''
  try {
    return url ? new URL(url).host : ''
  } catch {
    return url
  }
}

// True only for the mount that a create redirects to: that mount auto-probes
// once, so the new server's feedback loop completes without a manual Test.
const probeOnCreate = ref(false)

function openServer(item: McpItem) {
  requestLeave(() => {
    probeOnCreate.value = false
    selectedId.value = item.id
    detailKey.value = `s${++keySeq}`
    openDetail()
  })
}

function openCreate() {
  requestLeave(() => {
    probeOnCreate.value = false
    selectedId.value = ''
    detailKey.value = `s${++keySeq}`
    openDetail()
  })
}

// Create redirects explicitly: reload the list (so the detail seeds from the
// server's canonical stored state, not from the draft that created it), then
// remount the detail on the new id. The fresh mount sees probeOnCreate and
// runs the first probe itself.
async function onDetailCreated(id: string) {
  probeOnCreate.value = true
  await loadList()
  selectedId.value = id
  detailKey.value = `s${++keySeq}`
}

function onDetailDeleted() {
  selectedId.value = ''
  backToList()
  void loadList()
}

async function loadList() {
  loading.value = true
  try {
    const { data } = await getBotsByBotIdMcp({ path: { bot_id: props.botId } as unknown as { bot_id: string }, throwOnError: true })
    items.value = (data.items ?? []).map((item: Record<string, unknown>) => ({
      ...(item as unknown as McpItem),
      status: (item.status as string) ?? 'unknown',
      tools_cache: (item.tools_cache as McpToolDescriptor[]) ?? [],
      last_probed_at: (item.last_probed_at as string) ?? null,
      status_message: (item.status_message as string) ?? '',
      auth_type: (item.auth_type as string) ?? 'none',
    }))

    // Deep link: open the server named in ?mcpId= on first load.
    if (view.value === 'list' && selectedMcpId.value) {
      const target = items.value.find(i => i.id === selectedMcpId.value)
      if (target) openServer(target)
    }
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('mcp.loadFailed')))
  } finally {
    loading.value = false
  }
}

function startImport() {
  importJson.value = '{\n  "mcpServers": {\n    \n  }\n}'
  importError.value = ''
  importOpen.value = true
}

function formatImportJson() {
  try {
    importJson.value = JSON.stringify(JSON.parse(importJson.value), null, 2)
    importError.value = ''
  } catch {
    importError.value = t('mcp.importErrorJson')
  }
}

async function executeImport() {
  importSubmitting.value = true
  importError.value = ''
  try {
    let parsed: McpImportRequest = JSON.parse(importJson.value)
    if (!parsed.mcpServers && typeof parsed === 'object') {
      parsed = { mcpServers: parsed as McpImportRequest['mcpServers'] }
    }
    await putBotsByBotIdMcpImport({ path: { bot_id: props.botId } as unknown as { bot_id: string }, body: parsed, throwOnError: true })
    importOpen.value = false
    await loadList()
    toast.success(t('mcp.importSuccess'))
  } catch (error) {
    importError.value = error instanceof SyntaxError
      ? t('mcp.importErrorJson')
      : resolveApiErrorMessage(error, t('mcp.importErrorFormat'))
  } finally {
    importSubmitting.value = false
  }
}

watch(() => props.botId, () => { if (props.botId) void loadList() }, { immediate: true })

watch([view, selectedId], () => {
  selectedMcpId.value = view.value === 'detail' && selectedId.value ? selectedId.value : ''
})
</script>
