<script setup lang="ts">
import { computed, inject, onActivated, onDeactivated, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useEventListener } from '@vueuse/core'
import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Kbd,
  KbdGroup,
  toast,
} from '@felinic/ui'
import {
  displayKeyCombo,
  formatKeyCombo,
  isModifierKey,
  keyComboFromEvent,
  type ParsedKeyCombo,
} from '@/lib/keyboard-combo'
import { detectPlatform, keyboardBindings } from '@/lib/keyboard-bindings'
import {
  useKeyboardShortcutsStore,
  type ConflictResult,
} from '@/store/keyboard-shortcuts'
import type { AppKeyboardCommand } from '@/lib/keyboard-commands'
import { DesktopWindowKey } from '@/lib/desktop-shell'

const props = defineProps<{
  open: boolean
  command: AppKeyboardCommand | null
  i18nKey: string | null
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
}>()

const { t } = useI18n()
const store = useKeyboardShortcutsStore()
const platform = detectPlatform()
const isMac = platform === 'mac'

const captured = ref<ParsedKeyCombo | null>(null)
const active = ref(true)
const windowBridge = inject(DesktopWindowKey, undefined)

function setCapture(open: boolean) {
  void windowBridge?.setIgnoreMenuShortcuts?.(open)?.catch(() => {
    toast.error(t(open ? 'settings.keyboard.dialog.menuPauseFailed' : 'settings.keyboard.dialog.menuRestoreFailed'))
  })
}

watch(() => props.open, (isOpen) => {
  setCapture(isOpen && active.value)
  if (!isOpen) captured.value = null
}, { immediate: true })
onScopeDispose(() => setCapture(false))
onActivated(() => {
  active.value = true
  setCapture(props.open)
})
onDeactivated(() => {
  active.value = false
  setCapture(false)
  emit('update:open', false)
})

// Capture-phase listener so we run BEFORE the global dispatcher's bubble-phase
// listener registered in main.ts. stopImmediatePropagation cancels both that
// listener and reka-ui's Dialog Escape-to-close, so any key — including Escape —
// can be bound. Gated on props.open to stay inert when the dialog is closed.
// Enter/Space on a focused button are intentionally let through so keyboard
// users can still activate Save/Cancel/Reset; the trade-off is that binding
// Enter or Space requires focus to be off those buttons (e.g. the capture
// surface), which matches OS-level keybinding dialogs.
useEventListener(window, 'keydown', (event: KeyboardEvent) => {
  if (!props.open || !active.value) return
  if (isModifierKey(event.key)) return
  if ((event.key === 'Enter' || event.key === ' ') && event.target instanceof HTMLButtonElement) return
  event.preventDefault()
  event.stopImmediatePropagation()
  const combo = keyComboFromEvent(event, isMac)
  if (combo) captured.value = combo
}, { capture: true })

const conflict = computed<ConflictResult>(() => {
  if (!props.command || !captured.value) return { kind: 'none' }
  return store.detectConflict(props.command, captured.value)
})

const tokens = computed(() => captured.value ? displayKeyCombo(captured.value, platform) : [])

const collidedLabel = computed(() => {
  const command = conflict.value.collidesWith
  if (!command) return ''
  const binding = keyboardBindings.find(b => b.command === command)
  if (!binding) return ''
  return t(`settings.keyboard.commands.${binding.i18nKey}.label`)
})

const isBlockingConflict = computed(() => conflict.value.kind !== 'none' && conflict.value.kind !== 'cross-scope')

function handleSave() {
  if (!props.command || !captured.value) return
  const result = store.setBinding(props.command, formatKeyCombo(captured.value))
  if (result.kind !== 'none' && result.kind !== 'cross-scope') return
  emit('update:open', false)
}

function handleReset() {
  if (!props.command) return
  store.resetBinding(props.command)
  emit('update:open', false)
}

function handleCancel() {
  emit('update:open', false)
}
</script>

<template>
  <Dialog
    :open="open && active"
    @update:open="(v: boolean) => emit('update:open', v)"
  >
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{{ t('settings.keyboard.dialog.title') }}</DialogTitle>
        <DialogDescription v-if="i18nKey">
          {{ t(`settings.keyboard.commands.${i18nKey}.label`) }}
        </DialogDescription>
      </DialogHeader>

      <div class="flex min-h-[5rem] items-center justify-center rounded-md border border-dashed border-border bg-muted-soft">
        <div
          v-if="!captured"
          class="text-sm text-muted-foreground"
        >
          {{ t('settings.keyboard.dialog.prompt') }}
        </div>
        <KbdGroup v-else>
          <Kbd
            v-for="(token, i) in tokens"
            :key="i"
            class="h-6 min-w-6 px-1.5 text-xs"
          >
            {{ token }}
          </Kbd>
        </KbdGroup>
      </div>

      <div class="min-h-[1.25rem] text-xs">
        <div
          v-if="conflict.kind === 'reserved'"
          class="text-destructive"
        >
          {{ t('settings.keyboard.dialog.reservedError') }}
        </div>
        <div
          v-else-if="conflict.kind === 'editing'"
          class="text-destructive"
        >
          {{ t('settings.keyboard.dialog.editingError') }}
        </div>
        <div
          v-else-if="conflict.kind === 'no-modifier'"
          class="text-destructive"
        >
          {{ t('settings.keyboard.dialog.noModifierError') }}
        </div>
        <div
          v-else-if="conflict.kind === 'same-scope'"
          class="text-destructive"
        >
          {{ t('settings.keyboard.dialog.sameScopeError', { command: collidedLabel }) }}
        </div>
        <div
          v-else-if="conflict.kind === 'cross-scope'"
          class="text-warning"
        >
          {{ t('settings.keyboard.dialog.crossScopeWarn', { command: collidedLabel }) }}
        </div>
      </div>

      <DialogFooter class="sm:justify-between">
        <Button
          variant="ghost"
          size="sm"
          @click="handleReset"
        >
          {{ t('settings.keyboard.dialog.resetDefault') }}
        </Button>
        <div class="flex gap-2">
          <Button
            variant="outline"
            @click="handleCancel"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            :disabled="!captured || isBlockingConflict"
            @click="handleSave"
          >
            {{ t('common.save') }}
          </Button>
        </div>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
