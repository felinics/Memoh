<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, FieldError, Kbd, KbdGroup, SettingsRow } from '@felinic/ui'
import { RotateCcw } from 'lucide-vue-next'
import { comboFromBinding, displayKeyCombo, parseKeyCombo, type ParsedKeyCombo } from '@/lib/keyboard-combo'
import { detectPlatform, keyboardBindings, type KeyboardBinding } from '@/lib/keyboard-bindings'
import type { AppKeyboardCommand } from '@/lib/keyboard-commands'
import { useKeyboardShortcutsStore, type ConflictKind } from '@/store/keyboard-shortcuts'

const props = defineProps<{
  binding: KeyboardBinding
}>()

const emit = defineEmits<{
  edit: []
}>()

const { t } = useI18n()
const store = useKeyboardShortcutsStore()
const platform = detectPlatform()

const tokens = computed(() => displayKeyCombo(comboFromBinding(props.binding), platform))
const overridden = computed(() => store.isOverridden(props.binding.command))

const reasons: Partial<Record<ConflictKind, string>> = {
  reserved: 'settings.keyboard.dialog.reservedError',
  editing: 'settings.keyboard.dialog.editingError',
  'no-modifier': 'settings.keyboard.dialog.noModifierError',
  typing: 'settings.keyboard.dialog.typingError',
}
const label = (command: AppKeyboardCommand) => {
  const binding = keyboardBindings.find(b => b.command === command)
  return binding ? t(`settings.keyboard.commands.${binding.i18nKey}.label`) : command
}
const format = (combo: ParsedKeyCombo) => displayKeyCombo(combo, platform).join(platform === 'mac' ? '' : '+')

const notice = computed(() => {
  const command = props.binding.command
  const shadowedBy = store.shadowedDefaults[command]
  if (shadowedBy) return t('settings.keyboard.row.shadowed', { combo: format(comboFromBinding(props.binding)), command: label(shadowedBy) })
  const ignored = store.ignoredOverrides[command]
  if (!ignored) return ''
  const saved = store.overrides[command] ?? ''
  const parsed = parseKeyCombo(saved)
  const reason = ignored.collidesWith
    ? t('settings.keyboard.dialog.sameScopeError', { command: label(ignored.collidesWith) })
    : reasons[ignored.kind] && t(reasons[ignored.kind]!)
  return [t('settings.keyboard.row.ignored', { combo: parsed ? format(parsed) : saved }), reason].filter(Boolean).join(' ')
})
</script>

<template>
  <SettingsRow
    :label="t(`settings.keyboard.commands.${binding.i18nKey}.label`)"
    :description="t(`settings.keyboard.commands.${binding.i18nKey}.description`)"
  >
    <template
      v-if="notice"
      #content
    >
      <div class="truncate text-control font-medium text-foreground">
        {{ t(`settings.keyboard.commands.${binding.i18nKey}.label`) }}
      </div>
      <p class="mt-0.5 text-body text-muted-foreground">
        {{ t(`settings.keyboard.commands.${binding.i18nKey}.description`) }}
      </p>
      <FieldError class="mt-1">
        {{ notice }}
      </FieldError>
    </template>
    <div class="flex shrink-0 items-center gap-2">
      <KbdGroup>
        <Kbd
          v-for="(token, i) in tokens"
          :key="i"
        >
          {{ token }}
        </Kbd>
      </KbdGroup>
      <Button
        v-if="overridden"
        variant="ghost"
        size="icon"
        :aria-label="t('settings.keyboard.row.reset')"
        :title="t('settings.keyboard.row.reset')"
        @click="store.resetBinding(binding.command)"
      >
        <RotateCcw class="size-4" />
      </Button>
      <Button
        variant="outline"
        size="sm"
        @click="emit('edit')"
      >
        {{ t('common.edit') }}
      </Button>
    </div>
  </SettingsRow>
</template>
