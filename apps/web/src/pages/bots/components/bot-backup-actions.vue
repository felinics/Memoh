<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { InlineLoadingRow, toast } from '@felinic/ui'
import {
  Button,
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  Label,
} from '@felinic/ui'
import {
  postBotsByBotIdBackupExport,
  getBotsByBotIdBackupSummary,
  type BotbackupSummaryResult,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import BotImportPanel from './bot-import-panel.vue'
import BackupSectionCards from './backup-section-cards.vue'

const props = defineProps<{
  botId: string
  botName?: string
  disabled?: boolean
}>()

const emit = defineEmits<{
  imported: [botId: string]
}>()

const { t } = useI18n()

const exportOpen = ref(false)
const importOpen = ref(false)
const exporting = ref(false)
const summarizing = ref(false)
const summary = ref<BotbackupSummaryResult | null>(null)
const exportPassphrase = ref('')

const EXPORT_SECTIONS = [
  'settings', 'models', 'acl', 'channels', 'mcp', 'schedules', 'email', 'history', 'assets', 'workspace',
] as const

const exportSections = reactive<Record<string, 'skip' | 'merge' | 'replace'>>({})

// Build cards from the live-bot summary: counts/items per section, with empty
// sections marked unavailable so they render disabled.
const exportSectionList = computed(() => {
  const byKey = new Map((summary.value?.sections ?? []).map(s => [s.key, s]))
  return EXPORT_SECTIONS.map((key) => {
    const s = byKey.get(key)
    const count = s?.count ?? 0
    return {
      key,
      count,
      items: s?.items ?? [],
      sensitive: s?.sensitive ?? false,
      available: count !== 0,
    }
  })
})

const canExport = computed(() => {
  if (props.disabled || exporting.value) return false
  return Object.values(exportSections).some(v => v !== 'skip')
})

watch(exportOpen, (open) => {
  if (open) {
    exportPassphrase.value = ''
    void loadSummary()
  }
})

async function loadSummary() {
  summarizing.value = true
  try {
    const { data } = await getBotsByBotIdBackupSummary({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    summary.value = data
    for (const key of Object.keys(exportSections)) delete exportSections[key]
    for (const section of data.sections ?? []) {
      if (section.key && (section.count ?? 0) !== 0) exportSections[section.key] = 'merge'
    }
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.backup.exportFailed')))
  } finally {
    summarizing.value = false
  }
}

function filename() {
  const name = (props.botName || props.botId).trim().replace(/[^A-Za-z0-9_-]+/g, '-').replace(/^-+|-+$/g, '')
  const timestamp = new Date().toISOString().replaceAll(':', '-')
  return `bot-${name || props.botId}-${timestamp}.memoh.zip`
}

function downloadBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = name
  anchor.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

async function handleExport() {
  if (!canExport.value) return
  exporting.value = true
  try {
    const sections = EXPORT_SECTIONS.filter(key => exportSections[key] && exportSections[key] !== 'skip')
    const response = await postBotsByBotIdBackupExport({
      path: { bot_id: props.botId },
      body: { sections, passphrase: exportPassphrase.value || undefined },
      parseAs: 'blob',
      throwOnError: true,
    })
    downloadBlob(response.data as unknown as Blob, filename())
    toast.success(t('bots.backup.exportSuccess'))
    exportOpen.value = false
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.backup.exportFailed')))
  } finally {
    exporting.value = false
  }
}

function handleImported(botId: string) {
  importOpen.value = false
  emit('imported', botId)
}
</script>

<template>
  <div class="flex shrink-0 flex-wrap justify-end gap-2">
    <Button
      variant="outline"
      size="sm"
      :disabled="disabled"
      @click="exportOpen = true"
    >
      {{ t('bots.backup.exportBot') }}
    </Button>
    <Button
      variant="outline"
      size="sm"
      :disabled="disabled"
      @click="importOpen = true"
    >
      {{ t('bots.backup.importBot') }}
    </Button>

    <Dialog v-model:open="exportOpen">
      <DialogContent class="sm:max-w-lg flex max-h-[85vh] flex-col">
        <DialogHeader>
          <DialogTitle>{{ t('bots.backup.exportTitle') }}</DialogTitle>
          <DialogDescription>{{ t('bots.backup.exportDescription') }}</DialogDescription>
        </DialogHeader>

        <div class="flex min-h-0 flex-1 flex-col">
          <p class="mb-2 text-[11px] font-medium text-muted-foreground">
            {{ t('bots.backup.selectSectionsExport') }}
          </p>
          <InlineLoadingRow
            v-if="summarizing"
            bordered
          >
            {{ t('bots.backup.reading') }}
          </InlineLoadingRow>
          <div
            v-else
            class="flex-1 overflow-y-auto px-0.5"
          >
            <BackupSectionCards
              mode="include"
              :sections="exportSectionList"
              :model-value="exportSections"
              :empty-text="t('bots.backup.noData')"
              @update:model-value="(v) => Object.assign(exportSections, v)"
            />
          </div>

          <div
            v-if="!summarizing"
            class="mt-3 space-y-1.5 border-t pt-3"
          >
            <Label
              for="export-passphrase"
              class="text-[11px] font-medium text-muted-foreground"
            >
              {{ t('bots.backup.passphraseLabel') }}
            </Label>
            <Input
              id="export-passphrase"
              v-model="exportPassphrase"
              type="password"
              autocomplete="new-password"
              class="h-8 text-xs"
              :placeholder="t('bots.backup.passphrasePlaceholder')"
            />
            <p class="text-[10px] text-muted-foreground">
              {{ t('bots.backup.passphraseExportHint') }}
            </p>
          </div>
        </div>

        <DialogFooter>
          <DialogClose as-child>
            <Button variant="outline">
              {{ t('common.cancel') }}
            </Button>
          </DialogClose>
          <Button
            :disabled="!canExport"
            :loading="exporting"
            @click="handleExport"
          >
            {{ t('common.export') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="importOpen">
      <DialogContent class="sm:max-w-lg flex max-h-[85vh] flex-col">
        <DialogHeader>
          <DialogTitle>{{ t('bots.backup.importTitle') }}</DialogTitle>
          <DialogDescription>{{ t('bots.backup.importDescription') }}</DialogDescription>
        </DialogHeader>

        <BotImportPanel
          :target-bot-id="botId"
          :disabled="disabled"
          show-cancel
          @imported="handleImported"
          @cancel="importOpen = false"
        />
      </DialogContent>
    </Dialog>
  </div>
</template>
