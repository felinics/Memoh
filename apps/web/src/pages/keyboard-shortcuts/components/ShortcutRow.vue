<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, FieldError, Kbd, KbdGroup, SettingsRow } from '@felinic/ui'
import { RotateCcw } from 'lucide-vue-next'
import { comboFromBinding, displayKeyCombo, parseKeyCombo } from '@/lib/keyboard-combo'
import { detectPlatform, type KeyboardBinding } from '@/lib/keyboard-bindings'
import { useKeyboardShortcutsStore } from '@/store/keyboard-shortcuts'

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

const ignoredReasons: Record<string, string> = {
  reserved: 'settings.keyboard.dialog.reservedError',
  editing: 'settings.keyboard.dialog.editingError',
  'no-modifier': 'settings.keyboard.dialog.noModifierError',
}
const ignoredNotice = computed(() => {
  const kind = store.ignoredOverrides[props.binding.command]
  if (!kind) return ''
  const saved = store.overrides[props.binding.command] ?? ''
  const parsed = parseKeyCombo(saved)
  const combo = parsed ? displayKeyCombo(parsed, platform).join(platform === 'mac' ? '' : '+') : saved
  const reason = ignoredReasons[kind]
  return [t('settings.keyboard.row.ignored', { combo }), reason ? t(reason) : ''].filter(Boolean).join(' ')
})
</script>

<template>
  <SettingsRow
    :label="t(`settings.keyboard.commands.${binding.i18nKey}.label`)"
    :description="t(`settings.keyboard.commands.${binding.i18nKey}.description`)"
  >
    <template
      v-if="ignoredNotice"
      #content
    >
      <div class="truncate text-control font-medium text-foreground">
        {{ t(`settings.keyboard.commands.${binding.i18nKey}.label`) }}
      </div>
      <p class="mt-0.5 text-body text-muted-foreground">
        {{ t(`settings.keyboard.commands.${binding.i18nKey}.description`) }}
      </p>
      <FieldError class="mt-1">
        {{ ignoredNotice }}
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
