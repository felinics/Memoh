<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { Button, PageShell, Select, SelectContent, SelectItem, SelectTrigger, SelectValue, SettingsRow, SettingsSection } from '@felinic/ui'
import KeyCaptureDialog from './components/KeyCaptureDialog.vue'
import ShortcutRow from './components/ShortcutRow.vue'
import { useKeyboardShortcutsStore } from '@/store/keyboard-shortcuts'
import { COMPOSER_SEND_KEYS, type ComposerSendKey, isComposerSendKey, useSettingsStore } from '@/store/settings'
import { detectPlatform, type KeyboardBinding, type KeyboardScope } from '@/lib/keyboard-bindings'
import type { AppKeyboardCommand } from '@/lib/keyboard-commands'

const { t } = useI18n()
const store = useKeyboardShortcutsStore()
const settingsStore = useSettingsStore()
const { composerSendKey } = storeToRefs(settingsStore)

// Key names stay untranslated; only the modifier follows the platform.
const modKeyLabel = detectPlatform() === 'mac' ? '⌘' : 'Ctrl'
const sendKeyLabels: Record<ComposerSendKey, string> = {
  'enter': 'Enter',
  'mod-enter': `${modKeyLabel}+Enter`,
}
const lineBreakKeyLabel = computed(() => composerSendKey.value === 'mod-enter' ? 'Enter' : 'Shift+Enter')

const dialogOpen = ref(false)
const editingCommand = ref<AppKeyboardCommand | null>(null)
const editingI18nKey = ref<string | null>(null)

const grouped = computed(() => {
  const result: Record<KeyboardScope, KeyboardBinding[]> = {
    global: [],
    workspace: [],
    mediaLightbox: [],
  }
  for (const binding of store.allBindings) {
    result[binding.scope].push(binding)
  }
  return Object.entries(result).filter(([, bindings]) => bindings.length).map(([scope, bindings]) => ({ scope, bindings }))
})

function openEdit(binding: KeyboardBinding) {
  editingCommand.value = binding.command
  editingI18nKey.value = binding.i18nKey
  dialogOpen.value = true
}

const hasAnyOverride = computed(() => Object.keys(store.overrides).length > 0)
</script>

<template>
  <PageShell
    :title="t('settings.keyboard.title')"
  >
    <template #actions>
      <Button
        v-if="hasAnyOverride"
        variant="ghost"
        size="sm"
        @click="store.resetAll"
      >
        {{ t('settings.keyboard.resetAll') }}
      </Button>
    </template>

    <div class="space-y-8">
      <SettingsSection :title="t('settings.keyboard.chat')">
        <SettingsRow
          :label="t('settings.keyboard.sendKey')"
          :description="t('settings.keyboard.sendKeyDescription', { key: lineBreakKeyLabel })"
        >
          <Select
            :model-value="composerSendKey"
            @update:model-value="(value) => isComposerSendKey(value) && settingsStore.setComposerSendKey(value)"
          >
            <SelectTrigger class="min-w-36">
              <SelectValue>
                {{ sendKeyLabels[composerSendKey] }}
              </SelectValue>
            </SelectTrigger>
            <SelectContent
              align="end"
              :align-offset="0"
            >
              <SelectItem
                v-for="value in COMPOSER_SEND_KEYS"
                :key="value"
                :value="value"
              >
                {{ sendKeyLabels[value] }}
              </SelectItem>
            </SelectContent>
          </Select>
        </SettingsRow>
      </SettingsSection>

      <SettingsSection
        v-for="group in grouped"
        :key="group.scope"
        :title="t(`settings.keyboard.scopes.${group.scope}`)"
      >
        <ShortcutRow
          v-for="binding in group.bindings"
          :key="binding.command"
          :binding="binding"
          @edit="openEdit(binding)"
        />
      </SettingsSection>
    </div>

    <KeyCaptureDialog
      v-model:open="dialogOpen"
      :command="editingCommand"
      :i18n-key="editingI18nKey"
    />
  </PageShell>
</template>
