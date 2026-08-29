<template>
  <!-- A single item renders as a bare row (no group header). -->
  <ToolCallInline
    v-if="items.length === 1 && single && single.type === 'tool'"
    :block="(single as ToolCallBlockType)"
    :message-id="messageId"
  />
  <ThinkingBlock
    v-else-if="items.length === 1 && single && single.type === 'reasoning'"
    :block="(single as ThinkingBlockType)"
    :message-id="messageId"
    :streaming="active === true"
  />

  <!-- Multiple items collapse into one process block. -->
  <div
    v-else
    class="text-[0.90625rem] font-[400]"
  >
    <HeaderRow
      :open="open"
      @toggle="toggle"
    >
      <!-- Every connector this run touched, not just the first: the collapsed
           header is the only place a reader sees which external systems the
           whole segment reached. -->
      <div
        v-if="connectors.length"
        class="flex items-center gap-1 shrink-0"
      >
        <ConnectorLogo
          v-for="item in connectors"
          :key="item.alias"
          :connector="item"
        />
      </div>
      <span
        class="min-w-0 truncate tracking-[0.01em]"
        :class="running ? 'tool-shimmer-text' : ''"
      >{{ headerLabel }}</span>
      <ExpandChevron
        :open="open"
        class="ml-0.5"
      />
    </HeaderRow>

    <CollapseSection :open="open">
      <!-- Card body sets the in-card type scale (one notch below the root-level
           cop rows) + tighter leading so nested steps read as a distinct, denser
           layer; nested rows inherit this instead of re-asserting their own size.
           Deliberately NO inner scroll here: the process body must flow with the
           main chat scroll so the mouse wheel is never latched inside the
           capsule. Individual tool details (diffs, file content, exec output) keep
           their own small scroll bounds for truly large blobs, but the capsule
           itself never introduces a second scrollbar. -->
      <Capsule
        density="compact"
        class="mt-1 space-y-0.5 text-[0.84375rem] leading-snug"
      >
        <template
          v-for="(item, i) in items"
          :key="item.id"
        >
          <ToolCallInline
            v-if="item.type === 'tool'"
            :block="(item as ToolCallBlockType)"
            :message-id="messageId"
            in-group
          />
          <ThinkingBlock
            v-else-if="item.type === 'reasoning'"
            :block="(item as ThinkingBlockType)"
            :message-id="messageId"
            :streaming="active === true && i === items.length - 1"
            in-group
          />
        </template>
      </Capsule>
    </CollapseSection>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ContentBlock, ThinkingBlock as ThinkingBlockType, ToolCallBlock as ToolCallBlockType } from '@/store/chat-list'
import { SUMMARY_BUCKET_ORDER, getToolDisplay, isGuiTool, toolBucket } from './tool-call-registry'
import ToolCallInline from './tool-call-inline.vue'
import ThinkingBlock from './thinking-block.vue'
import CollapseSection from './collapse-section.vue'
import { getCollapseOpen, groupCollapseKey, setCollapseOpen } from './process-collapse'
import HeaderRow from './tool-detail/header-row.vue'
import ExpandChevron from './tool-detail/expand-chevron.vue'
import Capsule from './tool-detail/capsule.vue'
import ConnectorLogo from './tool-detail/connector-logo.vue'
import { useConnectorLogos, type ConnectorIdentity } from '../composables/useConnectorLogos'

const props = defineProps<{
  // Ordered run of tool + reasoning blocks belonging to one process segment.
  items: ContentBlock[]
  // Stable render identity for the assistant turn that owns these blocks.
  messageId: string
  // True when this segment is the last block of a still-streaming assistant turn.
  active?: boolean
}>()

const { t } = useI18n()

const single = computed(() => props.items[0])
const toolItems = computed(() => props.items.filter((b): b is ToolCallBlockType => b.type === 'tool'))

// Distinct connectors used by this run, in order of first call. Keyed by alias
// so two bindings of the same connector type stay two marks.
const connectorLookup = useConnectorLogos()
const connectors = computed(() => {
  const lookup = connectorLookup.value
  const seen = new Map<string, ConnectorIdentity>()
  for (const tool of toolItems.value) {
    const identity = lookup(tool.toolName)
    if (identity && !seen.has(identity.alias)) seen.set(identity.alias, identity)
  }
  return [...seen.values()]
})

// Open state is purely user-driven and persisted across the post-turn refetch:
// a process is collapsed until the user opens it, then stays as they left it
// (no auto-open while streaming, no auto-close on completion). The header still
// acts as a live ticker via `running`/`headerLabel`, so the user can follow
// progress without the body being forced open.
const collapseKey = computed(() => groupCollapseKey(props.messageId, props.items))
const open = ref(getCollapseOpen(collapseKey.value) ?? false)
watch(collapseKey, (key) => {
  open.value = getCollapseOpen(key) ?? false
})
function toggle() {
  open.value = !open.value
  setCollapseOpen(collapseKey.value, open.value)
}

const anyToolRunning = computed(() => toolItems.value.some(tool => tool.running))
const running = computed(() => props.active === true || anyToolRunning.value)

function basename(path: string): string {
  if (!path) return ''
  const parts = path.split('/').filter(Boolean)
  return parts[parts.length - 1] ?? path
}

const FILE_PATH_TOOLS = new Set(['read', 'write', 'edit', 'list'])

// Subject of a single tool call: a short, human target (filename / query /
// command) rather than a bare count — "Read chat-pane.vue", not "Read 1".
function subjectOf(tool: ToolCallBlockType): string {
  const display = getToolDisplay(tool)
  if (FILE_PATH_TOOLS.has(tool.toolName)) return basename(display.target) || display.target
  return display.target
}

function verbOf(tool: ToolCallBlockType): string {
  const display = getToolDisplay(tool)
  return t(`chat.tools.${display.actionKey}`, display.actionParams ?? {})
}

function labelFor(tool: ToolCallBlockType): string {
  const subject = subjectOf(tool)
  const verb = verbOf(tool)
  return subject ? `${verb} ${subject}` : verb
}

// Collapsed summary: a single tool keeps its subject; multiple tools fall back
// to category counts ("Read 3 files · Edited 2 files"). The buckets live in the
// registry so this header and the per-row display share one tool catalog.

// Where a browser navigation went, by host — the one piece of a browsing run
// worth surfacing in the collapsed header ("Browsed example.com").
function navigateHost(tool: ToolCallBlockType): string {
  if (tool.toolName !== 'browser_action') return ''
  const input = tool.input && typeof tool.input === 'object' ? tool.input as Record<string, unknown> : {}
  if (input.action !== 'navigate') return ''
  const url = typeof input.url === 'string' ? input.url : ''
  if (!url) return ''
  try {
    return new URL(url).hostname.replace(/^www\./, '')
  } catch {
    return (url.replace(/^[a-z]+:\/\//i, '').split('/')[0] ?? '').replace(/^www\./, '')
  }
}

const aggregateLabel = computed(() => {
  const tools = toolItems.value
  if (tools.length === 0) return t('chat.process.thought')
  if (tools.length === 1) return labelFor(tools[0]!)
  // A browsing/desktop run is summarized by where it went, not by the screenshot
  // reads it folded in — name the destination, fall back to a step count.
  if (tools.some(tool => isGuiTool(tool.toolName))) {
    const hosts = [...new Set(tools.map(navigateHost).filter(Boolean))]
    if (hosts.length === 1) return t('chat.process.browsed', { target: hosts[0] })
    if (hosts.length > 1) return t('chat.process.browsedSites', { count: hosts.length })
    return t('chat.process.steps', { count: tools.length })
  }
  const acc = new Map<string, number>()
  for (const tool of tools) {
    const b = toolBucket(tool.toolName)
    if (b !== 'other') acc.set(b, (acc.get(b) ?? 0) + 1)
  }
  const segments = SUMMARY_BUCKET_ORDER
    .filter(b => acc.has(b))
    .map(b => t(`chat.process.${b}`, { count: acc.get(b)! }))
  return segments.length ? segments.join(' · ') : t('chat.process.steps', { count: tools.length })
})

// Streaming header acts as a ticker for the current (last) item.
const tickerLabel = computed(() => {
  const current = props.items[props.items.length - 1]
  if (!current) return ''
  if (current.type === 'reasoning') return t('chat.thinkingInProgress')
  if (current.type === 'tool') return labelFor(current as ToolCallBlockType)
  return aggregateLabel.value
})

const headerLabel = computed(() => (running.value ? tickerLabel.value : aggregateLabel.value))
</script>
