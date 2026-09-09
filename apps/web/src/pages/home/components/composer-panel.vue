<template>
  <ComposerCapsule :label="$t('chat.panel.regionLabel')">
    <AutoHeight>
      <div
        v-for="(section, index) in sections"
        :key="section.kind"
        :class="index > 0 ? 'mt-2 border-t border-border-soft pt-2' : ''"
      >
        <ComposerPanelError
          v-if="section.kind === 'error'"
          :message="section.message"
        />
        <ComposerPanelCommand
          v-else-if="section.kind === 'command'"
          ref="commandSection"
          :is-error="section.panel.isError"
          :title="section.panel.title"
          :text="section.panel.text"
          :items="section.panel.items"
          @select="emit('selectCommandItem', $event)"
          @dismiss="emit('dismissCommand')"
        />
        <ComposerPanelCompaction v-else-if="section.kind === 'compaction'" />
        <Transition
          v-else
          mode="out-in"
          enter-active-class="transition-opacity duration-150 ease-out"
          enter-from-class="opacity-0"
          enter-to-class="opacity-100"
          leave-active-class="transition-opacity duration-100 ease-in"
          leave-from-class="opacity-100"
          leave-to-class="opacity-0"
        >
          <ComposerPanelApproval
            v-if="approvalHead"
            :key="approvalHead.id"
            :item="approvalHead"
            :queue-size="approvals.length"
          />
        </Transition>
      </div>
    </AutoHeight>
  </ComposerCapsule>
</template>

<script setup lang="ts">
// ComposerPanel — the ONE home for "things that dock right above the composer
// box" (the stack tier of the composer dock). Everything of that kind — tool
// approvals, slash-command results, composer errors, and anything added later
// — renders HERE as sections of ONE capsule, separated by hairlines, never as
// N independent cards adrift with message text bleeding through the gaps.
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
// - Section order is fixed: error (most transient) → command result →
//   approvals (hugging the box, the most actionable). All active sections
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
import ComposerPanelCompaction from './composer-panel-compaction.vue'
import type { PendingApprovalItem } from '../composables/usePendingApprovals'
import type { CommandActionListItem } from '@/composables/api/useChat'

// The pane pre-digests the raw command event into this shape (it also drives
// the pane's keyboard arbitration); this component only renders it.
interface CommandPanelData {
  isError: boolean
  title: string
  text: string
  items: CommandActionListItem[]
}

type PanelSection =
  | { kind: 'error', message: string }
  | { kind: 'command', panel: CommandPanelData }
  | { kind: 'approval' }
  | { kind: 'compaction' }

const props = defineProps<{
  approvals: PendingApprovalItem[]
  commandPanel: CommandPanelData | null
  errorMessage: string
  compacting?: boolean
}>()

const emit = defineEmits<{
  (e: 'selectCommandItem', item: CommandActionListItem): void
  (e: 'dismissCommand'): void
}>()

const approvalHead = computed(() => props.approvals[0] ?? null)

const sections = computed<PanelSection[]>(() => {
  const list: PanelSection[] = []
  if (props.errorMessage) list.push({ kind: 'error', message: props.errorMessage })
  if (props.commandPanel) list.push({ kind: 'command', panel: props.commandPanel })
  if (props.compacting) list.push({ kind: 'compaction' })
  if (approvalHead.value) list.push({ kind: 'approval' })
  return list
})

// The command list's keyboard bridge, forwarded so the pane's composer
// keydown can route arrows/Enter here when the slash picker is closed. The
// ref sits inside the section v-for, so Vue collects it as an array even
// though at most one command section ever renders — read the first element.
const commandSection = ref<InstanceType<typeof ComposerPanelCommand>[] | null>(null)
const commandBridge = computed(() => commandSection.value?.[0]?.bridge ?? null)
defineExpose({ commandBridge })
</script>
