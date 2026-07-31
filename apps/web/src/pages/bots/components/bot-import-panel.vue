<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { InlineLoadingRow, toast } from '@felinic/ui'
import {
  Avatar,
  AvatarFallback,
  AvatarImage,
  Button,
  Input,
  Tabs,
  TabsList,
  TabsTrigger,
} from '@felinic/ui'
import { Check, FileArchive, Lock, Upload, X } from 'lucide-vue-next'
import {
  postBotsBackupImportPreview,
  type BotbackupImportResult,
  type BotbackupPreviewResult,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { formatFileSize } from '@/components/file-manager/utils'
import { uploadWithProgress } from '@/lib/upload-with-progress'
import BackupSectionCards from './backup-section-cards.vue'

type SectionState = 'skip' | 'merge' | 'replace'

const props = defineProps<{
  targetBotId?: string
  disabled?: boolean
  showCancel?: boolean
}>()

const emit = defineEmits<{
  imported: [botId: string]
  cancel: []
}>()

const { t } = useI18n()

const fileInput = ref<HTMLInputElement | null>(null)
const dragActive = ref(false)
const previewing = ref(false)
const importing = ref(false)
const selectedFile = ref<File | null>(null)
const preview = ref<BotbackupPreviewResult | null>(null)
const previewError = ref('')
const passphrase = ref('')
const uploadPercent = ref(0)

const allowOverwrite = computed(() => !!props.targetBotId)
const importMode = ref<'create' | 'overwrite'>('create')
const overwriteProfile = ref(false)
const sectionStates = reactive<Record<string, SectionState>>({})

const busy = computed(() => previewing.value || importing.value)
const profile = computed(() => preview.value?.profile ?? null)
const isOverwrite = computed(() => allowOverwrite.value && importMode.value === 'overwrite')

// Canonical section order. Every section is always shown; those absent from the
// backup (or empty) are rendered disabled so users see the full picture.
const ALL_SECTIONS = [
  'settings', 'models', 'acl', 'channels', 'mcp', 'schedules', 'email', 'history', 'assets', 'workspace',
] as const

const sectionItems = computed(() => {
  const byKey = new Map((preview.value?.sections ?? []).map(s => [s.key, s]))
  return ALL_SECTIONS.map((key) => {
    const s = byKey.get(key)
    const count = s?.count ?? 0
    return {
      key,
      count,
      targetCount: s?.target_count ?? 0,
      conflict: s?.conflict ?? false,
      sensitive: s?.sensitive ?? false,
      items: s?.items ?? [],
      available: count > 0,
    }
  })
})

const hasAvailableSection = computed(() => sectionItems.value.some(s => s.available))

// The bundle is encrypted and still locked: we have no readable preview until a
// correct passphrase is supplied.
const needsPassphrase = computed(() => preview.value?.requires_passphrase === true)

const blocked = computed(
  () => !!previewError.value || needsPassphrase.value || (preview.value?.conflicts?.length ?? 0) > 0,
)
const canImport = computed(
  () => !!selectedFile.value && !busy.value && !props.disabled && !!preview.value && !blocked.value,
)

const avatarInitial = computed(() => (profile.value?.display_name || '?').trim().charAt(0).toUpperCase())

function openFilePicker() {
  if (props.disabled || busy.value) return
  fileInput.value?.click()
}

function setFile(file: File | null) {
  selectedFile.value = file
  preview.value = null
  previewError.value = ''
  passphrase.value = ''
  uploadPercent.value = 0
  for (const key of Object.keys(sectionStates)) delete sectionStates[key]
  if (file) void loadPreview()
}

function handleFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  setFile(input.files?.[0] ?? null)
}

function handleDrop(event: DragEvent) {
  dragActive.value = false
  if (props.disabled || busy.value) return
  const file = event.dataTransfer?.files?.[0]
  if (file) setFile(file)
}

function clearFile() {
  setFile(null)
  if (fileInput.value) fileInput.value.value = ''
}

function baseBody() {
  if (!selectedFile.value) throw new Error('file required')
  return {
    file: selectedFile.value,
    mode: isOverwrite.value ? ('overwrite' as const) : ('create' as const),
    target_bot_id: isOverwrite.value ? props.targetBotId : undefined,
    passphrase: passphrase.value || undefined,
  }
}

// Build a multipart form for the XHR upload path (which reports progress).
function buildFormData(sections?: string): FormData {
  if (!selectedFile.value) throw new Error('file required')
  const form = new FormData()
  form.append('file', selectedFile.value)
  form.append('mode', isOverwrite.value ? 'overwrite' : 'create')
  if (isOverwrite.value && props.targetBotId) form.append('target_bot_id', props.targetBotId)
  if (sections !== undefined) form.append('sections', sections)
  if (passphrase.value) form.append('passphrase', passphrase.value)
  return form
}

// Re-preview when the create/overwrite mode changes so target conflict counts
// reflect the chosen target.
watch(importMode, () => {
  if (selectedFile.value) void loadPreview()
})

async function loadPreview() {
  if (!selectedFile.value) return
  previewing.value = true
  previewError.value = ''
  try {
    const { data } = await postBotsBackupImportPreview({
      body: baseBody(),
      throwOnError: true,
    })
    preview.value = data
    for (const key of Object.keys(sectionStates)) delete sectionStates[key]
    for (const section of data.sections ?? []) {
      if (section.key && (section.count ?? 0) > 0) sectionStates[section.key] = 'merge'
    }
  } catch (error) {
    previewError.value = resolveApiErrorMessage(error, t('bots.backup.previewFailed'))
  } finally {
    previewing.value = false
  }
}

// importedSummary turns the per-section counts into a short, human-readable
// list, e.g. "3 Channels, 120 Chat history, 5 Attachments".
function importedSummary(imported?: Record<string, number>): string {
  if (!imported) return ''
  return ALL_SECTIONS
    .filter(key => (imported[key] ?? 0) > 0)
    .map(key => `${imported[key]} ${t(`bots.backup.sections.${key}`)}`)
    .join(', ')
}

async function handleImport() {
  if (!canImport.value) return
  const map: Record<string, SectionState> = { ...sectionStates }
  if (isOverwrite.value && overwriteProfile.value) map.profile = 'merge'
  importing.value = true
  uploadPercent.value = 0
  try {
    const data = await uploadWithProgress<BotbackupImportResult>({
      url: '/bots/backup/import',
      formData: buildFormData(JSON.stringify(map)),
      onProgress: p => (uploadPercent.value = p.percent),
    })
    const summary = importedSummary(data.imported)
    toast.success(summary ? t('bots.backup.importedSummary', { summary }) : t('bots.backup.importedNothing'))
    if (data.warnings?.length) {
      toast.warning(t('bots.backup.importWarnings', { count: data.warnings.length }))
    }
    clearFile()
    if (data.bot_id) emit('imported', data.bot_id)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.backup.importFailed')))
  } finally {
    importing.value = false
  }
}
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col gap-3">
    <input
      ref="fileInput"
      type="file"
      accept=".zip,.memoh.zip,application/zip"
      class="hidden"
      @change="handleFileChange"
    >

    <div class="flex-1 space-y-3 overflow-y-auto px-0.5">
      <!-- Drop zone (no file selected) -->
      <button
        v-if="!selectedFile"
        type="button"
        class="flex w-full flex-col items-center justify-center gap-1.5 rounded-md border border-dashed px-4 py-6 text-center transition-colors"
        :class="[
          dragActive ? 'border-primary bg-primary/5' : 'border-border-soft hover:border-primary/50 hover:bg-muted/40',
          disabled ? 'cursor-not-allowed opacity-60' : 'cursor-pointer',
        ]"
        :disabled="disabled"
        @click="openFilePicker"
        @dragover.prevent="dragActive = true"
        @dragenter.prevent="dragActive = true"
        @dragleave.prevent="dragActive = false"
        @drop.prevent="handleDrop"
      >
        <div class="flex size-8 items-center justify-center rounded-full bg-muted text-muted-foreground">
          <Upload class="size-4" />
        </div>
        <p class="text-xs font-medium">
          {{ t('bots.backup.dropzoneTitle') }}
        </p>
        <p class="text-[11px] text-muted-foreground">
          {{ t('bots.backup.dropzoneHint') }}
        </p>
      </button>

      <!-- Selected file -->
      <div
        v-else
        class="flex items-center gap-3 rounded-md border border-border-soft bg-background p-2.5"
      >
        <div class="flex size-8 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
          <FileArchive class="size-4" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="truncate text-xs font-medium">
            {{ selectedFile.name }}
          </p>
          <p class="text-[11px] text-muted-foreground">
            {{ formatFileSize(selectedFile.size) }}
          </p>
        </div>
        <Button
          variant="ghost"
          size="icon-sm"
          class="size-7"
          :disabled="busy"
          :aria-label="t('bots.backup.removeFile')"
          @click="clearFile"
        >
          <X class="size-3.5" />
        </Button>
      </div>

      <!-- Reading backup -->
      <InlineLoadingRow
        v-if="selectedFile && previewing"
        bordered
      >
        {{ t('bots.backup.reading') }}
      </InlineLoadingRow>

      <!-- Backup unreadable / unsupported -->
      <div
        v-else-if="selectedFile && previewError"
        class="rounded-md border border-destructive-border bg-destructive-soft p-3 text-xs text-destructive"
      >
        {{ previewError }}
      </div>

      <!-- Preview -->
      <template v-else-if="preview">
        <div
          v-for="conflict in preview.conflicts || []"
          :key="conflict"
          class="rounded-md border border-destructive-border bg-destructive-soft p-2.5 text-[11px] text-destructive"
        >
          {{ conflict }}
        </div>

        <!-- Encrypted bundle: prompt for the passphrase before anything else -->
        <div
          v-if="needsPassphrase"
          class="space-y-2 rounded-md border border-border-soft bg-background p-3"
        >
          <div class="flex items-center gap-2 text-xs font-medium">
            <Lock class="size-3.5 text-muted-foreground" />
            {{ t('bots.backup.encryptedTitle') }}
          </div>
          <p class="text-[11px] text-muted-foreground">
            {{ t('bots.backup.encryptedHint') }}
          </p>
          <div class="flex items-center gap-2">
            <Input
              v-model="passphrase"
              type="password"
              autocomplete="off"
              class="h-8 flex-1 text-xs"
              :placeholder="t('bots.backup.passphraseImportPlaceholder')"
              :disabled="previewing"
              @keyup.enter="loadPreview"
            />
            <Button
              size="sm"
              :disabled="!passphrase"
              :loading="previewing"
              @click="loadPreview"
            >
              {{ previewing ? t('bots.backup.unlocking') : t('bots.backup.unlock') }}
            </Button>
          </div>
        </div>

        <!-- Create vs overwrite -->
        <Tabs
          v-if="!needsPassphrase && allowOverwrite"
          v-model="importMode"
        >
          <TabsList class="grid w-full grid-cols-2">
            <TabsTrigger value="create">
              {{ t('bots.backup.modeCreate') }}
            </TabsTrigger>
            <TabsTrigger value="overwrite">
              {{ t('bots.backup.modeOverwrite') }}
            </TabsTrigger>
          </TabsList>
        </Tabs>

        <!-- Section selection (profile identity card is the first entry) -->
        <div
          v-if="!needsPassphrase && (hasAvailableSection || sectionItems.length)"
          class="space-y-2"
        >
          <p class="text-[11px] font-medium text-muted-foreground">
            {{ t('bots.backup.selectSections') }}
          </p>

          <!-- Profile identity card -->
          <div
            class="flex items-center gap-3 rounded-md border px-3 py-2.5 transition-colors"
            :class="[
              isOverwrite
                ? (overwriteProfile ? 'border-foreground bg-muted cursor-pointer' : 'border-border-soft bg-background/50 hover:bg-muted/30 cursor-pointer')
                : 'border-border-soft bg-background/50',
            ]"
            @click="isOverwrite && !busy && (overwriteProfile = !overwriteProfile)"
          >
            <Avatar class="size-9 shrink-0">
              <AvatarImage
                v-if="profile?.avatar_url"
                :src="profile.avatar_url"
                :alt="profile?.display_name"
              />
              <AvatarFallback class="text-xs">
                {{ avatarInitial }}
              </AvatarFallback>
            </Avatar>
            <div class="min-w-0 flex-1">
              <p class="truncate text-xs font-medium">
                {{ profile?.display_name || preview.manifest?.source_bot_id }}
              </p>
              <p class="text-[10px] text-muted-foreground">
                {{ isOverwrite ? t('bots.backup.overwriteProfileHint') : t('bots.backup.profileHint') }}
                <span v-if="profile?.timezone">· {{ profile.timezone }}</span>
              </p>
            </div>
            <div
              v-if="isOverwrite"
              class="flex size-4 shrink-0 items-center justify-center rounded-full border"
              :class="overwriteProfile ? 'border-foreground bg-foreground text-background' : 'border-border'"
            >
              <Check
                v-if="overwriteProfile"
                class="size-3"
              />
            </div>
          </div>

          <BackupSectionCards
            :sections="sectionItems"
            :mode="isOverwrite ? 'strategy' : 'include'"
            :model-value="sectionStates"
            :disabled="busy"
            @update:model-value="(v) => Object.assign(sectionStates, v)"
          />
        </div>
      </template>
    </div>

    <!-- Actions -->
    <div class="shrink-0 space-y-2 border-t pt-3">
      <!-- Upload / import progress -->
      <div
        v-if="importing"
        class="space-y-1"
      >
        <div class="flex items-center justify-between text-[11px] text-muted-foreground">
          <span>{{ uploadPercent < 100 ? t('bots.backup.uploading', { percent: uploadPercent }) : t('bots.backup.importingProgress') }}</span>
        </div>
        <div class="h-1 overflow-hidden rounded-full bg-muted">
          <div
            class="h-full rounded-full bg-primary transition-all duration-200"
            :class="{ 'animate-pulse': uploadPercent >= 100 }"
            :style="{ width: `${Math.max(uploadPercent, uploadPercent >= 100 ? 100 : 4)}%` }"
          />
        </div>
      </div>

      <div class="flex justify-end gap-2">
        <Button
          v-if="showCancel"
          variant="ghost"
          size="sm"
          :disabled="busy"
          @click="emit('cancel')"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          size="sm"
          :disabled="!canImport"
          :loading="importing"
          @click="handleImport"
        >
          {{ t('common.import') }}
        </Button>
      </div>
    </div>
  </div>
</template>
