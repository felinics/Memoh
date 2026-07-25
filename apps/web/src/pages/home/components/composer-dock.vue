<template>
  <section>
    <Transition
      enter-active-class="transition-all duration-150 ease-out"
      enter-from-class="opacity-0 translate-y-1"
      enter-to-class="opacity-100 translate-y-0"
      leave-active-class="transition-all duration-100 ease-in"
      leave-from-class="opacity-100 translate-y-0"
      leave-to-class="opacity-0 translate-y-1"
    >
      <ComposerPanel
        v-if="stackVisible"
        ref="panelEl"
        :approvals="approvals"
        :command-panel="commandPanel"
        :error-message="errorMessage"
        class="mb-2"
        @select-command-item="emit('selectCommandItem', $event)"
        @dismiss-command="emit('dismissCommand')"
      />
    </Transition>
    <Transition
      enter-active-class="transition-all duration-150 ease-out"
      enter-from-class="opacity-0 translate-y-1"
      enter-to-class="opacity-100 translate-y-0"
      leave-active-class="transition-all duration-100 ease-in"
      leave-from-class="opacity-100 translate-y-0"
      leave-to-class="opacity-0 translate-y-1"
    >
      <ChatUserInputForm
        v-if="pendingUserInput"
        ref="formEl"
        :class="composerVisible ? 'mb-2' : ''"
        :user-input="pendingUserInput"
        @reveal-composer="handleUserInputReveal"
      />
    </Transition>
    <div
      v-show="composerVisible"
      ref="boxEl"
    >
      <slot />
    </div>
  </section>
</template>

<script setup lang="ts">
// ComposerDock — the orchestrator of everything that lives at the bottom of a
// chat pane. The dock is ONE component with TWO tiers of members, and every
// member — the composer included — is something this component arranges:
//
// - BOX tier (the input slot, exactly ONE occupant at a time): the composer
//   (default, injected via the slot) or the ask_user capsule. ask_user
//   REPLACING the composer is a deliberate product decision, not an accident:
//   while the agent is asking, answering is the only input that matters, and
//   the capsule's own free-text field IS the substitute input. That mutex is
//   a public capability of this dock: selecting an answer never releases it;
//   the capsule hands the slot back only when the request resolves or the user
//   explicitly cancels.
// - STACK tier (ComposerPanel, hugs the box): approvals / command results /
//   errors — surfaces the user answers with a CLICK, so they must not take
//   the input slot away.
//
// The composer is the ONE special member: it owns its radius/height morph and
// its focus machinery, so it is injected as slot content and this component
// only ADAPTS around it — measuring the slot box for the backdrop mask and
// hiding it (v-show, never unmount) while ask_user owns the slot. Everything
// else (approvals, command results, errors, ask_user) is an ordinary member:
// same capsule shell, no special-casing beyond what ComposerPanel does.
//
// The pane that hosts this dock stays the business owner: it injects the
// composer, feeds the queues as props, and answers events (command selection,
// dismissal, and focusing the textarea when the slot is handed back). All
// geometry/visibility orchestration lives HERE and nowhere else.
import { computed, ref, watch } from 'vue'
import { useElementSize } from '@vueuse/core'
import ComposerPanel from './composer-panel.vue'
import ChatUserInputForm from './chat-user-input-form.vue'
import { COMPOSER_MASK_BELOW_PX } from '../composables/useComposerLayout'
import type { PendingApprovalItem } from '../composables/usePendingApprovals'
import type { CommandActionListItem, UIUserInput } from '@/composables/api/useChat'

// Same view-model shape ComposerPanel takes (kept structurally identical so
// the pane's computed needs no cast).
interface CommandPanelData {
  isError: boolean
  title: string
  text: string
  items: CommandActionListItem[]
}

const props = defineProps<{
  approvals: PendingApprovalItem[]
  commandPanel: CommandPanelData | null
  errorMessage: string
  pendingUserInput: UIUserInput | null
}>()

const emit = defineEmits<{
  (e: 'selectCommandItem', item: CommandActionListItem): void
  (e: 'dismissCommand'): void
  (e: 'revealComposer', opts: { focus?: boolean }): void
}>()

const stackVisible = computed(() => Boolean(
  props.errorMessage || props.commandPanel || props.approvals.length,
))

// Box-tier mutex: while an ask_user request is pending the capsule owns the
// slot and the composer hides (v-show so its textarea state survives); the
// capsule hands the slot back only once the request resolves or is canceled.
const userInputComposerRevealed = ref(false)
const composerVisible = computed(() => !props.pendingUserInput || userInputComposerRevealed.value)

watch(() => props.pendingUserInput?.user_input_id ?? null, () => {
  userInputComposerRevealed.value = false
})

function handleUserInputReveal(opts: { focus?: boolean }) {
  userInputComposerRevealed.value = true
  // The textarea belongs to the pane, so focusing is delegated back up.
  emit('revealComposer', opts)
}

// The backdrop mask (rendered by the pane, full-width) rises to the vertical
// centre of whichever box currently stands in the slot. WHY the centre: that
// is the box's widest point, so the mask's top edge hides behind the box's
// full-width middle — no visible seam. Above the line the box's rounded top
// simply floats over the messages; below it the fill covers the bottom-corner
// gaps and the strip beneath, so nothing bleeds out. Same rule for both box
// occupants, so measure the slot box and the capsule and pick by visibility.
const panelEl = ref<InstanceType<typeof ComposerPanel> | null>(null)
const formEl = ref<InstanceType<typeof ChatUserInputForm> | null>(null)
const boxEl = ref<HTMLElement | null>(null)
const { height: formHeight } = useElementSize(() => formEl.value?.$el ?? null)
const { height: slotBoxHeight } = useElementSize(boxEl)

const maskHeight = computed(() => {
  const boxHeight = composerVisible.value ? slotBoxHeight.value : formHeight.value
  return `${COMPOSER_MASK_BELOW_PX + boxHeight / 2}px`
})

// Forwarded so the pane's composer keydown can route arrows/Enter to the
// command list when its panel section is showing.
const commandBridge = computed(() => panelEl.value?.commandBridge ?? null)

defineExpose({ maskHeight, commandBridge })
</script>
