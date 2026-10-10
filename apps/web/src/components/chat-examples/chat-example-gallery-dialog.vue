<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import {
  Dialog,
  DialogBody,
  DialogDescription,
  DialogHeader,
  DialogPanel,
  DialogTitle,
} from '@felinic/ui'
import ChatExampleCard from './chat-example-card.vue'
import { useChatExampleAction, useChatExampleEntries, type ChatExampleEntry } from './use-chat-example-action'

/**
 * Every usage example in one list. Picking one closes the dialog
 * and hands off to the shared example action (settings link or prefill), so
 * it works from any page, including without a selected bot.
 */
const props = defineProps<{ botId: string }>()
const open = defineModel<boolean>('open', { default: false })

const { t } = useI18n()

// Probed even while closed (the result is shared with the welcome strip),
// so the list is already settled when the dialog opens.
const entries = useChatExampleEntries(() => props.botId, { surface: 'gallery' })
const { run } = useChatExampleAction(() => props.botId)

// The picked example runs only once the dialog has closed: while it is still
// animating out, its focus trap would pull focus back from the composer.
// closeAutoFocus marks that moment (and is prevented so focus does not return
// to the sidebar trigger); the timer covers a close that never animates.
let pendingPick: ChatExampleEntry | null = null
let keepFocusAfterClose = false
let fallbackTimer: ReturnType<typeof setTimeout> | undefined

function runPendingPick() {
  clearTimeout(fallbackTimer)
  const entry = pendingPick
  pendingPick = null
  if (entry) void run(entry.example, entry.missing)
}

function pick(entry: ChatExampleEntry) {
  pendingPick = entry
  keepFocusAfterClose = true
  open.value = false
  fallbackTimer = setTimeout(runPendingPick, 500)
}

function onCloseAutoFocus(event: Event) {
  if (!keepFocusAfterClose) return
  keepFocusAfterClose = false
  event.preventDefault()
  runPendingPick()
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogPanel
      width="2xl"
      @close-auto-focus="onCloseAutoFocus"
    >
      <DialogHeader class="pr-8">
        <DialogTitle>{{ t('chatExamples.galleryTitle') }}</DialogTitle>
        <DialogDescription>{{ t('chatExamples.galleryDescription') }}</DialogDescription>
      </DialogHeader>
      <DialogBody>
        <div class="@container">
          <div class="grid grid-cols-1 gap-2 @lg:grid-cols-2">
            <ChatExampleCard
              v-for="entry in entries ?? []"
              :key="entry.example.id"
              :example="entry.example"
              :missing="entry.missing"
              @select="pick(entry)"
            />
          </div>
        </div>
      </DialogBody>
    </DialogPanel>
  </Dialog>
</template>
