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
      <!-- Two-tone adaptive title: a muted verb names the phase ("Exploring"
           while streaming, "Explored" once settled) followed by the darker
           bare-count details. ONLY the verb carries the shimmer — shimmering
           the counts too turns the whole line into uniform noise and kills the
           contrast between "phase" and "progress". -->
      <template v-if="verbLabel">
        <span
          class="shrink-0 tracking-[0.01em]"
          :class="(active || anyToolRunning) ? 'tool-shimmer-text' : 'text-muted-foreground'"
        >{{ verbLabel }}</span>
        <span class="min-w-0 truncate tabular-nums">{{ detailsLabel }}</span>
      </template>
      <span
        v-else
        class="min-w-0 truncate tracking-[0.01em]"
        :class="anyToolRunning || active ? 'tool-shimmer-text' : ''"
      >{{ headerLabel }}</span>
      <!-- Segment-level diff totals, revealed only once the segment settles.
           Mid-stream the per-row diffs inside the capsule already carry that
           signal and a second growing total here would double-render it. -->
      <span
        v-if="!active && diffTotals.add"
        class="font-mono shrink-0 text-success-foreground"
      >+{{ diffTotals.add }}</span>
      <span
        v-if="!active && diffTotals.remove"
        class="font-mono shrink-0 text-destructive"
      >-{{ diffTotals.remove }}</span>
      <ExpandChevron
        :open="open"
        class="ml-0.5"
      />
    </HeaderRow>

    <!-- Second layer of the running header: the header names the phase and the
         live tally, this line rolls the one call happening right now. Only
         while collapsed — an open capsule's own last row already carries that
         signal, so the line steps aside instead of double-rendering it. -->
    <div
      v-if="active && !open"
      class="now-line relative h-[1lh] overflow-hidden mt-0.5 text-cop-title"
    >
      <Transition name="now-roll">
        <div
          :key="tickerLabel"
          class="absolute inset-0 truncate"
          v-text="tickerLabel"
        />
      </Transition>
    </div>

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
import {
  SUMMARY_BUCKET_ORDER,
  SUMMARY_FRAGMENT_ORDER,
  getToolDisplay,
  isGuiTool,
  toolBucket,
  toolFragmentKind,
  type SummaryFragment,
  type ToolBucket,
} from './tool-call-registry'
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
// (no auto-open while streaming, no auto-close on completion). The header
// tracks progress live while streaming, without forcing the body open.
const collapseKey = computed(() => groupCollapseKey(props.messageId, props.items))
const open = ref(getCollapseOpen(collapseKey.value) ?? false)
watch(collapseKey, (key) => {
  open.value = getCollapseOpen(key) ?? false
})
function toggle() {
  open.value = !open.value
  setCollapseOpen(collapseKey.value, open.value)
}

// Any tool still executing (a live foreground tool during streaming, or a
// background task outliving the turn). Drives the shimmer only; the header's
// text content is `active`-gated (see verbLabel/detailsLabel).
const anyToolRunning = computed(() => toolItems.value.some(tool => tool.running))

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

// Phase of the segment, by its dominant bucket. Ties break by
// SUMMARY_BUCKET_ORDER; 'other' never wins a tie because a generic "Working"
// adds nothing over the details alone. A GUI run is always "Browsing"
// regardless of mix (its observe+action steps are one browsing activity).
const phaseKey = computed<ToolBucket | 'gui' | null>(() => {
  const tools = toolItems.value
  if (tools.length === 0) return null
  if (tools.some(tool => isGuiTool(tool.toolName))) return 'gui'
  const counts = new Map<ToolBucket, number>()
  for (const tool of tools) {
    const bucket = toolBucket(tool.toolName)
    counts.set(bucket, (counts.get(bucket) ?? 0) + 1)
  }
  let best: ToolBucket = 'other'
  let bestCount = 0
  for (const bucket of SUMMARY_BUCKET_ORDER) {
    const count = counts.get(bucket) ?? 0
    if (count > bestCount) {
      best = bucket
      bestCount = count
    }
  }
  if ((counts.get('other') ?? 0) > bestCount) return 'other'
  return best
})

// The verb names the phase; its tense is the segment's own lifecycle — present
// while THIS segment is the streaming tail, past once anything supersedes it.
// Only genuine multi-tool segments get the verb+details split: a lone tool's
// own label ("Run ls /tmp") already IS the specific, and prefixing its bucket
// verb would stutter ("Running Run ls /tmp") while duplicating the now line.
const verbLabel = computed(() => {
  const tools = toolItems.value
  if (tools.length <= 1) return ''
  const key = phaseKey.value
  if (!key) return ''
  return t(`chat.process.phase.${key}.${props.active ? 'ing' : 'done'}`)
})

// The details half: bare counts ("12 files, 4 searches, ran 3 commands") that
// grow live while streaming and settle with the segment. GUI runs name the
// destination instead.
const detailsLabel = computed(() => {
  const tools = toolItems.value
  if (tools.length === 0) return props.active ? t('chat.thinkingInProgress') : t('chat.process.thought')
  if (phaseKey.value === 'gui') {
    const hosts = [...new Set(tools.map(navigateHost).filter(Boolean))]
    if (hosts.length === 1) return hosts[0]!
    if (hosts.length > 1) return t('chat.process.frag.sites', { count: hosts.length })
    return t('chat.process.steps', { count: tools.length })
  }
  const counts = new Map<SummaryFragment, number>()
  for (const tool of tools) {
    const kind = toolFragmentKind(tool.toolName)
    counts.set(kind, (counts.get(kind) ?? 0) + 1)
  }
  const parts = SUMMARY_FRAGMENT_ORDER
    .filter(kind => counts.has(kind))
    .map(kind => t(`chat.process.frag.${kind}`, { count: counts.get(kind)! }))
  return parts.length ? parts.join(t('chat.process.fragmentSeparator')) : t('chat.process.steps', { count: tools.length })
})

// Single-span fallback for the two cases without a verb: a thought-only
// segment ("Thinking…" / "Thought") and a lone settled tool (its own label).
const headerLabel = computed(() => {
  const tools = toolItems.value
  if (tools.length === 0) return props.active ? t('chat.thinkingInProgress') : t('chat.process.thought')
  return labelFor(tools[0]!)
})

// Segment-level diff totals for the settled header (+284 −96), summed from
// each row's own display math so the header can never disagree with the body.
const diffTotals = computed(() => {
  let add = 0
  let remove = 0
  for (const tool of toolItems.value) {
    const display = getToolDisplay(tool)
    add += display.diffAdd ?? 0
    remove += display.diffRemove ?? 0
  }
  return { add, remove }
})

// The now line rolls the current (last) item. A reasoning tail reads as
// "Thinking…"; a tool whose input hasn't streamed yet gets the pending label
// (same wording as its in-capsule row) instead of a target-less bare verb.
const tickerLabel = computed(() => {
  const current = props.items[props.items.length - 1]
  if (!current) return ''
  if (current.type === 'reasoning') return t('chat.thinkingInProgress')
  if (current.type === 'tool') {
    const tool = current as ToolCallBlockType
    const input = tool.input
    const inputReady = input && typeof input === 'object' && Object.keys(input as Record<string, unknown>).length > 0
    if (tool.running && !inputReady) {
      const display = getToolDisplay(tool)
      return t(`chat.tools.pending.${display.actionKey}`, t('chat.tools.pending.generic'))
    }
    return labelFor(tool)
  }
  return headerLabel.value
})
</script>

<style scoped>
/* Roll animation for the now line: incoming row rises from below, outgoing
   exits upward, both absolutely positioned so they overlap mid-swap. 300ms
   matches the house motion palette's gentle end. */
.now-roll-enter-active,
.now-roll-leave-active {
  transition: transform 300ms cubic-bezier(0.215, 0.61, 0.355, 1);
}

.now-roll-enter-from {
  transform: translateY(calc(100% + 1px));
}

.now-roll-leave-to {
  transform: translateY(calc(-100% - 1px));
}

@media (prefers-reduced-motion: reduce) {
  .now-roll-enter-active,
  .now-roll-leave-active {
    transition: none;
  }
}
</style>
