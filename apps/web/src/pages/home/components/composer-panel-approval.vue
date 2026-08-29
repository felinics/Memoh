<template>
  <div class="flex flex-col">
    <div class="px-3 py-1.5">
      <p
        data-slot="tool-approval-title"
        class="flex min-w-0 items-baseline gap-1.5 text-label font-medium text-foreground"
      >
        <span class="shrink-0">{{ approvalTitle }}</span>
        <span
          v-if="displayTarget"
          class="min-w-0 truncate font-normal"
          :title="display.fullTarget || display.target"
        >{{ displayTarget }}</span>
      </p>

      <Capsule
        v-if="codePreview || detailPreview"
        data-slot="tool-approval-preview"
        class="mt-2"
      >
        <div
          v-if="codePreview"
          class="max-h-48 overflow-auto"
        >
          <CodeBlock
            :code="codePreview.code"
            :lang="codePreview.lang"
            class="text-body leading-relaxed"
          />
        </div>
        <component
          :is="detailPreview"
          v-else
          :block="block"
        />
      </Capsule>

      <p
        class="text-body text-muted-foreground"
        :class="codePreview || detailPreview ? 'mt-2' : 'mt-0.5'"
      >
        <span v-if="approval.short_id">#{{ approval.short_id }} </span>{{ $t('chat.tools.pendingApproval') }}<span v-if="executionLocationLabel"> · {{ executionLocationLabel }}</span>
        <span
          v-if="queueSize > 1"
        > · {{ $t('chat.approval.moreInQueue', { count: queueSize - 1 }) }}</span>
      </p>
    </div>

    <ToolApprovalActions
      v-model:reason="rejectionReason"
      class="mt-2 px-3 pb-2"
      :options="approval.options"
      :responding="responding"
      :rejecting="rejecting"
      @approve="approve"
      @begin-reject="beginReject"
      @cancel-reject="cancelReject"
      @confirm-reject="confirmReject"
    />
  </div>
</template>

<script setup lang="ts">
// One tool-approval card, rendered inside ComposerPanel's shell (no chrome of
// its own). This is the dock port of the in-flow approval form (#840): it
// renders the tool's own display — title, target, a syntax-highlighted command
// preview (exec) or the tool's detail component (write/edit/patch) — through
// getToolDisplay(block), rather than a raw payload dump. The dock carries the
// block through the pending-approval projection for exactly this.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import ToolApprovalActions from '@/components/tool-approval-actions.vue'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from '../composables/useChatViewContext'
import CodeBlock from './code-block.vue'
import Capsule from './tool-detail/capsule.vue'
import { getToolDisplay } from './tool-call-registry'
import type { PendingApprovalItem } from '../composables/usePendingApprovals'

const props = defineProps<{
  item: PendingApprovalItem
  queueSize: number
}>()

const { t, te } = useI18n()
const chatStore = useChatStore()
const chatViewTarget = useChatViewTarget()

const block = computed(() => props.item.block)
const display = computed(() => getToolDisplay(block.value))
const approval = computed(() => props.item.approval)
const input = computed(() => (
  block.value.input && typeof block.value.input === 'object'
    ? block.value.input as Record<string, unknown>
    : {}
))
const actionLabel = computed(() => {
  const key = `chat.tools.${display.value.actionKey}`
  return t(key, display.value.actionParams ?? {})
})
// A "permission" approval is an agent question that maps to no concrete tool
// (network access, mode switch, an elicitation fallback). It carries the
// agent's own title and request text, so render those instead of a tool name.
const isPermissionRequest = computed(() => block.value.toolName === 'permission')
const permissionTitle = computed(() => {
  const title = input.value.title
  return typeof title === 'string' ? title.trim() : ''
})
const permissionRequest = computed(() => {
  const request = input.value.request
  return typeof request === 'string' ? request.trim() : ''
})
const permissionRequestLang = computed(() => {
  const lang = input.value.request_lang
  return typeof lang === 'string' && lang ? lang : 'text'
})
const approvalTitle = computed(() => {
  if (isPermissionRequest.value) {
    return permissionTitle.value || t('chat.approval.permissionRequest')
  }
  // An approval names the capability being granted ("Shell command"), not the
  // per-call verb, whenever the tool has such a name; the rest fall back to the
  // row label.
  const capabilityKey = `bots.toolApproval.toolNames.${block.value.toolName}`
  if (te(capabilityKey)) return t(capabilityKey)
  return actionLabel.value
})
const displayTarget = computed(() => {
  if (isPermissionRequest.value) return ''
  return block.value.toolName === 'exec' ? '' : display.value.target
})
const codePreview = computed(() => {
  if (isPermissionRequest.value) {
    return permissionRequest.value
      ? { code: permissionRequest.value, lang: permissionRequestLang.value }
      : null
  }
  if (block.value.toolName === 'exec') {
    const command = input.value.command
    return typeof command === 'string' && command
      ? { code: command, lang: 'bash' }
      : null
  }
  return null
})
const detailPreview = computed(() => {
  const toolName = block.value.toolName
  if (toolName === 'permission') return null
  if (toolName === 'write') {
    const content = input.value.content
    return typeof content === 'string' && content ? display.value.detail ?? null : null
  }
  if (toolName === 'edit') {
    const hasChanges = typeof input.value.old_text === 'string' || typeof input.value.new_text === 'string'
    return hasChanges ? display.value.detail ?? null : null
  }
  if (toolName === 'apply_patch') {
    const patch = input.value.patch
    return typeof patch === 'string' && patch ? display.value.detail ?? null : null
  }
  return null
})
const executionLocationLabel = computed(() => {
  const location = block.value.execution_location
  if (!location) return ''
  if (location.kind === 'native') return t('bots.remoteRuntime.nativeWorkspace')
  return location.name?.trim() || ''
})

// Resolve is optimistic (the store flips the status at once and the panel
// swaps to the next item), so no spinner — the flag only guards against a
// double-click landing two answers on the same request before the swap.
const responding = ref(false)
const rejecting = ref(false)
const rejectionReason = ref('')
const rejectOptionId = ref<string>()

function beginReject(optionId?: string) {
  rejectOptionId.value = optionId
  rejectionReason.value = ''
  rejecting.value = true
}

function cancelReject() {
  rejecting.value = false
  rejectionReason.value = ''
  rejectOptionId.value = undefined
}

async function confirmReject() {
  await respond('reject', rejectOptionId.value, rejectionReason.value)
}

async function approve(optionId?: string) {
  await respond('approve', optionId)
}

async function respond(decision: 'approve' | 'reject', optionId?: string, reason?: string) {
  if (responding.value) return
  responding.value = true
  const ok = await chatStore.respondToolApproval(approval.value, decision, chatViewTarget.value, optionId, reason)
  // On success the store already flipped the status and the panel swapped to
  // the next item, so this card unmounts. On failure (WebSocket disconnected,
  // send rejected) the approval stays pending and THIS card stays mounted —
  // re-enable the buttons so the user can retry after reconnecting, as the
  // connection-lost toast instructs.
  if (!ok) responding.value = false
}
</script>
