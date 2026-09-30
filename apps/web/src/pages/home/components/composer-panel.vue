<template>
  <!-- Notices (usage, error, command result) stack as their own banners; the
       approval decision surface sits in a capsule below them. -->
  <div class="flex flex-col gap-2">
    <ComposerPanelUsage
      v-if="usageNotice"
      :exhausted="usageNotice.exhausted"
      :message="usageNotice.message"
      @dismiss="emit('dismissUsage')"
    />
    <ComposerPanelError
      v-if="errorMessage"
      :message="errorMessage"
      @dismiss="emit('dismissError')"
    />
    <ComposerPanelCommand
      v-if="commandPanel"
      ref="commandSection"
      :is-error="commandPanel.isError"
      :title="commandPanel.title"
      :text="commandPanel.text"
      :items="commandPanel.items"
      :data="commandPanel.data"
      @select="emit('selectCommandItem', $event)"
      @dismiss="emit('dismissCommand')"
    />
    <ComposerCapsule
      v-if="approvalHead"
      :label="$t('chat.panel.regionLabel')"
    >
      <AutoHeight>
        <div>
          <Transition
            mode="out-in"
            enter-active-class="transition-opacity duration-150 ease-out"
            enter-from-class="opacity-0"
            enter-to-class="opacity-100"
            leave-active-class="transition-opacity duration-100 ease-in"
            leave-from-class="opacity-100"
            leave-to-class="opacity-0"
          >
            <ComposerPanelApproval
              :key="approvalHead.id"
              :item="approvalHead"
              :queue-size="approvals.length"
            />
          </Transition>
        </div>
      </AutoHeight>
    </ComposerCapsule>
  </div>
</template>

<script setup lang="ts">
// ComposerPanel — the ONE home for "things that dock right above the composer
// box" (the stack tier of the composer dock): composer errors, slash-command
// results, tool approvals, and anything added later render HERE. Notices
// (account usage, error, command result) are page-level destructive/neutral banners that
// stack upward one per message, each its own solid surface — no shared wrapper
// around them. Decision surfaces (compaction status, approvals) share ONE
// capsule, separated by hairlines, so they read as one control block.
//
// The dock has TWO tiers, and the distinction is load-bearing:
// - BOX tier (the input slot): ONE box owns the composer's position at a
//   time — the composer itself by default, the ask_user capsule while the
//   agent is asking (it replaces the composer, never stacks above it). The
//   occupants of this tier are peers: mutually exclusive states of the same
//   slot. Today that mutex is hand-wired in chat-pane (composerVisible knows
//   only ask_user); a proper slot registry is the intended evolution.
// - STACK tier (this panel): decision surfaces that must NOT take the input
//   slot, because answering one is a click, not typing — the user keeps the
//   composer while deciding (e.g. to type "don't do that" instead). It
//   always hugs whichever box currently owns the slot.
//
// House rules for the stack tier:
// - Section order is fixed: account usage (ambient, lasts until the limit
//   window resets) → error (most transient) → command result → approvals
//   (hugging the box, the most actionable). All active sections
//   show at once; within approvals the queue is FIFO, ONE at a time — the
//   frame never jumps, resolving the head cross-fades the next one in place
//   while AutoHeight tweens any height difference.
// - The panel owns the shell (ComposerCapsule), section layout, the swap
//   animation and the approval queue; each section component owns its own
//   content and never builds a shell of its own. New kinds of dock content
//   get a section component + a branch in `sections` — they do not clone
//   this frame.
import { computed, ref } from 'vue'
import { AutoHeight } from '@felinic/ui'
import ComposerCapsule from './composer-capsule.vue'
import ComposerPanelApproval from './composer-panel-approval.vue'
import ComposerPanelCommand from './composer-panel-command.vue'
import ComposerPanelError from './composer-panel-error.vue'
import ComposerPanelUsage from './composer-panel-usage.vue'
import type { PendingApprovalItem } from '../composables/usePendingApprovals'
import type { CommandActionListItem } from '@/composables/api/useChat'

// The pane pre-digests the raw command event into this shape (it also drives
// the pane's keyboard arbitration); this component only renders it.
interface CommandPanelData {
  data?: unknown
  isError: boolean
  title: string
  text: string
  items: CommandActionListItem[]
}

interface UsageNotice {
  exhausted: boolean
  message: string
}

const props = defineProps<{
  approvals: PendingApprovalItem[]
  commandPanel: CommandPanelData | null
  errorMessage: string
  usageNotice?: UsageNotice | null
}>()

const emit = defineEmits<{
  (e: 'selectCommandItem', item: CommandActionListItem): void
  (e: 'dismissCommand'): void
  (e: 'dismissUsage'): void
  (e: 'dismissError'): void
}>()

const approvalHead = computed(() => props.approvals[0] ?? null)

// The command list's keyboard bridge, forwarded so the pane's composer
// keydown can route arrows/Enter here when the slash picker is closed.
const commandSection = ref<InstanceType<typeof ComposerPanelCommand> | null>(null)
const commandBridge = computed(() => commandSection.value?.bridge ?? null)
defineExpose({ commandBridge })
</script>
