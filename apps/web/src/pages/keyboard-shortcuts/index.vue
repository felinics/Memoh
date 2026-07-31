<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, PageShell, SettingsSection } from '@felinic/ui'
import KeyCaptureDialog from './components/KeyCaptureDialog.vue'
import ShortcutRow from './components/ShortcutRow.vue'
import { useKeyboardShortcutsStore } from '@/store/keyboard-shortcuts'
import type { KeyboardBinding, KeyboardScope } from '@/lib/keyboard-bindings'
import type { AppKeyboardCommand } from '@/lib/keyboard-commands'

const { t } = useI18n()
const store = useKeyboardShortcutsStore()

const dialogOpen = ref(false)
const editingCommand = ref<AppKeyboardCommand | null>(null)
const editingI18nKey = ref<string | null>(null)

const grouped = computed(() => {
  const result: Record<KeyboardScope, KeyboardBinding[]> = {
    global: [],
    mediaLightbox: [],
  }
  for (const binding of store.effectiveBindings) {
    result[binding.scope].push(binding)
  }
  return result
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
    :description="t('settings.keyboard.intro')"
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
      <SettingsSection
        v-if="grouped.global.length"
        :title="t('settings.keyboard.scopes.global')"
      >
        <ShortcutRow
          v-for="binding in grouped.global"
          :key="binding.command"
          :binding="binding"
          @edit="openEdit(binding)"
        />
      </SettingsSection>

      <SettingsSection
        v-if="grouped.mediaLightbox.length"
        :title="t('settings.keyboard.scopes.mediaLightbox')"
      >
        <ShortcutRow
          v-for="binding in grouped.mediaLightbox"
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
