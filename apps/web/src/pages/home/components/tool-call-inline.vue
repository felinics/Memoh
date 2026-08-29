<template>
  <div
    class="font-[400]"
    :class="inGroup ? '' : 'text-[0.90625rem]'"
  >
    <HeaderRow
      v-if="expandable"
      :open="open"
      nested
      :tone="display.isError ? 'error' : 'cop'"
      @toggle="toggleOpen"
    >
      <ConnectorLogo
        v-if="connector"
        :connector="connector"
      />
      <span
        v-if="showActionLabel"
        class="shrink-0"
        :class="actionClass"
      >{{ renderedActionLabel }}</span>
      <button
        v-if="display.target && canOpenInFiles"
        class="truncate min-w-0 hover:underline cursor-pointer"
        :class="targetClass"
        :title="display.fullTarget || display.target"
        @click.stop="handleOpenInFiles"
      >
        {{ display.target }}
      </button>
      <span
        v-else-if="display.target"
        class="truncate min-w-0"
        :class="targetClass"
        :title="display.fullTarget || display.target"
      >{{ display.target }}</span>
      <span
        v-if="executionLocationLabel"
        class="shrink-0 text-muted-foreground"
        :title="t('chat.tools.executionLocation')"
      >· {{ executionLocationLabel }}</span>
      <span
        v-if="display.diffAdd"
        class="font-mono shrink-0 text-success-foreground"
      >+{{ display.diffAdd }}</span>
      <span
        v-if="display.diffRemove"
        class="font-mono shrink-0 text-destructive"
      >-{{ display.diffRemove }}</span>
      <span
        v-if="exitLabel"
        class="font-mono shrink-0"
      >{{ exitLabel }}</span>
      <span
        v-if="approvalLabel"
        class="font-mono shrink-0 text-xs text-warning-foreground"
      >{{ approvalLabel }}</span>
      <span
        v-if="userInputLabel"
        class="font-mono shrink-0 text-xs text-warning-foreground"
      >{{ userInputLabel }}</span>
      <ExpandChevron
        :open="open"
        class="ml-0.5"
      />
    </HeaderRow>

    <div
      v-else
      class="flex items-center gap-1.5 w-full py-px"
      :class="rowClass"
    >
      <ConnectorLogo
        v-if="connector"
        :connector="connector"
      />
      <span
        v-if="showActionLabel"
        class="shrink-0"
        :class="actionClass"
      >{{ renderedActionLabel }}</span>
      <button
        v-if="display.target && canOpenInFiles"
        class="truncate min-w-0 hover:underline cursor-pointer"
        :class="targetClass"
        :title="display.fullTarget || display.target"
        @click="handleOpenInFiles"
      >
        {{ display.target }}
      </button>
      <span
        v-else-if="display.target"
        class="truncate min-w-0"
        :class="targetClass"
        :title="display.fullTarget || display.target"
      >{{ display.target }}</span>
      <span
        v-if="executionLocationLabel"
        class="shrink-0 text-muted-foreground"
        :title="t('chat.tools.executionLocation')"
      >· {{ executionLocationLabel }}</span>
      <span
        v-if="display.diffAdd"
        class="font-mono shrink-0 text-success-foreground"
      >+{{ display.diffAdd }}</span>
      <span
        v-if="display.diffRemove"
        class="font-mono shrink-0 text-destructive"
      >-{{ display.diffRemove }}</span>
      <span
        v-if="exitLabel"
        class="font-mono shrink-0"
      >{{ exitLabel }}</span>
      <span
        v-if="approvalLabel"
        class="font-mono shrink-0 text-xs text-warning-foreground"
      >{{ approvalLabel }}</span>
      <span
        v-if="userInputLabel"
        class="font-mono shrink-0 text-xs text-warning-foreground"
      >{{ userInputLabel }}</span>
    </div>

    <CollapseSection
      v-if="expandable"
      :open="open && !isPending"
    >
      <!-- 'inline' detail (half-embedded key:value list) is not the capsule
           shape — just indentation, no filled surface. -->
      <div
        v-if="display.detailVariant === 'inline'"
        class="mt-1 pl-3 font-[400]"
      >
        <component
          :is="display.detail"
          v-if="display.detail"
          :block="block"
        />
        <ToolCallDetailGeneric
          v-else
          :block="block"
        />
      </div>
      <!-- inGroup: a card nested inside the group's own muted capsule needs a
           visibly different fill (bg-card, not bg-muted) so it reads as one
           layer up — a genuinely different surface, not a padding drift of
           the capsule shape below, so it stays hand-written. -->
      <div
        v-else-if="inGroup"
        class="mt-1.5 rounded-sm bg-card px-2.5 py-2 font-[400]"
      >
        <component
          :is="display.detail"
          v-if="display.detail"
          :block="block"
        />
        <ToolCallDetailGeneric
          v-else
          :block="block"
        />
      </div>
      <Capsule
        v-else
        density="detail"
        class="mt-1.5 font-[400]"
      >
        <component
          :is="display.detail"
          v-if="display.detail"
          :block="block"
        />
        <ToolCallDetailGeneric
          v-else
          :block="block"
        />
      </Capsule>
    </CollapseSection>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ToolCallBlock } from '@/store/chat-list'
import { openInFileManagerKey } from '../composables/useFileManagerProvider'
import { useConnectorLogos } from '../composables/useConnectorLogos'
import {
  getToolDisplay,
  isDirPathTool,
  isFilePathTool,
} from './tool-call-registry'
import ConnectorLogo from './tool-detail/connector-logo.vue'
import ToolCallDetailGeneric from './tool-call-detail-generic.vue'
import CollapseSection from './collapse-section.vue'
import { getCollapseOpen, setCollapseOpen, toolCollapseKey } from './process-collapse'
import HeaderRow from './tool-detail/header-row.vue'
import ExpandChevron from './tool-detail/expand-chevron.vue'
import Capsule from './tool-detail/capsule.vue'

const props = defineProps<{ block: ToolCallBlock, messageId: string, inGroup?: boolean }>()
const { t } = useI18n()

const openInFileManager = inject(openInFileManagerKey, undefined)

const display = computed(() => getToolDisplay(props.block))

// A Connect-It tool carries its binding's alias in the tool name; when that
// alias resolves to one of the bot's connectors the row leads with its logo.
const connectorLookup = useConnectorLogos()
const connector = computed(() => connectorLookup.value(props.block.toolName))
const executionLocationLabel = computed(() => {
  const location = props.block.execution_location
  if (!location) return ''
  if (location.kind === 'native') return t('bots.remoteRuntime.nativeWorkspace')
  return location.name?.trim() || ''
})

// Persisted, user-driven toggle (survives the post-turn refetch/remount).
const collapseKey = computed(() => toolCollapseKey(props.messageId, props.block))
const open = ref(getCollapseOpen(collapseKey.value) ?? (display.value.defaultOpen === true))
watch(collapseKey, (key) => {
  open.value = getCollapseOpen(key) ?? (display.value.defaultOpen === true)
})

const expandable = computed(
  () => Boolean(display.value.detail) || display.value.expandable === true,
)

// A failed command carries its exit status on the collapsed row; every other
// failure detail stays in the expanded output.
const exitLabel = computed(() => (
  display.value.exitCode ? t('chat.tools.exitCode', { code: display.value.exitCode }) : ''
))

const actionLabel = computed(() => {
  const key = `chat.tools.${display.value.actionKey}`
  return t(key, display.value.actionParams ?? {})
})

// A tool is "pending" while it is running and its input arguments have not
// streamed in yet (tool_call_input_start fires before the full call). In that
// window tools like write/edit hide their action label and have no target, so
// only a bare icon would show. We surface a placeholder label instead.
const isPending = computed(() => {
  if (props.block.done) return false
  const input = props.block.input
  return !(
    input
    && typeof input === 'object'
    && Object.keys(input as Record<string, unknown>).length > 0
  )
})

const showsBareIconWhenPending = computed(
  () => display.value.hideAction === true && !display.value.target,
)

const showPendingLabel = computed(
  () => isPending.value && showsBareIconWhenPending.value,
)

const pendingLabel = computed(
  () => t(`chat.tools.pending.${display.value.actionKey}`, t('chat.tools.pending.generic')),
)

const showActionLabel = computed(
  () => showPendingLabel.value || !display.value.hideAction,
)

const renderedActionLabel = computed(
  () => (showPendingLabel.value ? pendingLabel.value : actionLabel.value),
)

// Every row is gray at rest and animates to near-black (foreground) on hover:
// one neutral material, with color expressing interaction. Rest ink matches the
// process/thinking headers (--cop-title) so a lone tool row and a collapsed
// group read at the same weight.
const rowClass = computed(() => {
  if (display.value.isError) return 'text-destructive transition-colors duration-75'
  return 'text-cop-title hover:text-foreground transition-colors duration-75'
})

// Brief tools (e.g. send/memory) finish in <100ms. Showing the running
// shimmer for them flickers, so we only display it after a short delay.
const showRunning = ref(false)
let runningTimer: ReturnType<typeof setTimeout> | null = null
const RUNNING_SHIMMER_DELAY_MS = 250

function clearRunningTimer() {
  if (runningTimer !== null) {
    clearTimeout(runningTimer)
    runningTimer = null
  }
}

watch(
  () => props.block.done,
  (done) => {
    clearRunningTimer()
    if (done) {
      showRunning.value = false
      return
    }
    runningTimer = setTimeout(() => {
      showRunning.value = true
      runningTimer = null
    }, RUNNING_SHIMMER_DELAY_MS)
  },
  { immediate: true },
)

onBeforeUnmount(clearRunningTimer)

const targetClass = computed(() => {
  if (showRunning.value) return 'tool-shimmer-text'
  if (display.value.isError) return 'text-destructive'
  return '' // inherit the row's gray→black hover color
})

const actionClass = computed(() => {
  if (showPendingLabel.value) return 'tool-shimmer-text'
  if (showRunning.value && !display.value.target) return 'tool-shimmer-text'
  return ''
})

// Pending approvals are answered from the composer-dock panel (see
// composer-panel.vue), never here — this row keeps only the read-only status
// label so history still shows which call needed one and how it ended. The
// old inline Allow/Reject also carried raw color classes that bypassed the
// Button variants, so nothing of it is worth keeping.
const approvalLabel = computed(() => {
  const approval = props.block.approval
  if (!approval?.approval_id) return ''
  const id = approval.short_id ? `#${approval.short_id}` : ''
  if (approval.status === 'pending') return `${id} ${t('chat.tools.pendingApproval', 'pending approval')}`.trim()
  return `${id} ${approval.status}`.trim()
})

const userInputLabel = computed(() => {
  const userInput = props.block.userInput
  if (!userInput?.user_input_id) return ''
  if (userInput.status === 'pending') return ''
  return userInputStatusLabel(userInput.status)
})

function userInputStatusLabel(status: string) {
  const normalized = status.trim().toLowerCase()
  switch (normalized) {
    case 'submitted':
      return t('chat.tools.userInputSubmitted', 'answered')
    case 'canceled':
      return t('chat.tools.userInputCanceled', 'canceled')
    case 'failed':
      return t('chat.tools.userInputFailed', 'failed')
    case 'expired':
      return t('chat.tools.userInputExpired', 'expired')
    default:
      return status
  }
}

const filePath = computed(() => {
  if (!isFilePathTool(props.block.toolName)) return ''
  const input = props.block.input as Record<string, unknown> | undefined
  return (input?.path as string) ?? ''
})

const canOpenInFiles = computed(
  () => Boolean(filePath.value) && Boolean(openInFileManager),
)

function toggleOpen() {
  open.value = !open.value
  setCollapseOpen(collapseKey.value, open.value)
}

function handleOpenInFiles() {
  if (!filePath.value || !openInFileManager) return
  openInFileManager(filePath.value, isDirPathTool(props.block.toolName))
}
</script>
