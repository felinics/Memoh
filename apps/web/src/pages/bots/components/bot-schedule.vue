<template>
  <PageShell
    variant="tab"
    :title="$t('bots.schedule.title')"
  >
    <template #actions>
      <DropdownMenu v-if="schedules.length > 1">
        <DropdownMenuTrigger as-child>
          <Button variant="ghost">
            <ArrowUpDown />
            {{ currentSortLabel }}
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            v-for="opt in SORT_OPTIONS"
            :key="opt.key"
            class="justify-between gap-4"
            @select="sortKey = opt.key"
          >
            {{ $t(opt.labelKey) }}
            <Check :class="sortKey === opt.key ? 'opacity-100' : 'opacity-0'" />
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <Button @click="handleNew">
        <Plus />
        {{ $t('bots.schedule.create') }}
      </Button>
    </template>

    <!-- One titleless card: the page title already names the concern, so a
         section label here would say "tasks" a second time. -->
    <SettingsSection>
      <InlineLoadingRow
        v-if="isLoading && schedules.length === 0"
        surface="card-row"
      >
        {{ $t('common.loading') }}
      </InlineLoadingRow>

      <Empty
        v-else-if="schedules.length === 0"
        class="py-12"
      >
        <EmptyHeader>
          <EmptyTitle>{{ $t('bots.schedule.empty') }}</EmptyTitle>
          <EmptyDescription>{{ $t('bots.schedule.emptyDescription') }}</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button
            variant="outline"
            @click="handleNew"
          >
            <Plus />
            {{ $t('bots.schedule.create') }}
          </Button>
        </EmptyContent>
      </Empty>

      <SettingsRow
        v-for="row in rows"
        v-else
        :key="row.item.id"
        :label="row.item.name || ''"
        :description="row.description"
      >
        <div class="flex items-center gap-2">
          <Switch
            :model-value="!!row.item.enabled"
            :disabled="busyIds.has(row.item.id || '')"
            :aria-label="$t('bots.schedule.form.enabled')"
            @update:model-value="(value: boolean) => handleToggleEnabled(row.item, !!value)"
          />

          <DropdownMenu>
            <DropdownMenuTrigger as-child>
              <Button
                variant="ghost"
                size="icon-sm"
                :aria-label="$t('common.actions')"
              >
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem @select="handleEdit(row.item)">
                <Pencil />
                {{ $t('bots.schedule.edit') }}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                @select="deleteTarget = row.item"
              >
                <Trash2 />
                {{ $t('bots.schedule.delete') }}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </SettingsRow>
    </SettingsSection>

    <!-- Create / Edit Dialog -->
    <Dialog v-model:open="formVisible">
      <!-- max-w-2xl matches the create-bot form: the editor is settings cards of
           label-left / control-right rows now, and lg left the labels crowding
           their controls. -->
      <DialogScrollContent class="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {{ formMode === 'create' ? $t('bots.schedule.create') : $t('bots.schedule.edit') }}
          </DialogTitle>
        </DialogHeader>

        <ScheduleEditor
          :bot-id="props.botId"
          :mode="formMode"
          :schedule="editingSchedule"
          @cancel="handleFormCancel"
          @delete="(item) => { deleteTarget = item; formVisible = false }"
          @saved="handleEditorSaved"
        />
      </DialogScrollContent>
    </Dialog>

    <ConfirmDeleteDialog
      :open="!!deleteTarget"
      :title="$t('bots.schedule.deleteTitle')"
      :description="$t('bots.schedule.deleteConfirm', { name: deleteTarget?.name ?? '' })"
      :cancel-label="$t('common.cancel')"
      :confirm-label="$t('bots.schedule.delete')"
      :loading="isDeleting"
      @update:open="(v: boolean) => { if (!v) deleteTarget = null }"
      @confirm="confirmDelete"
    />
  </PageShell>
</template>

<script setup lang="ts">
import {
  ArrowUpDown, Check, MoreHorizontal, Pencil, Plus, Trash2,
} from 'lucide-vue-next'
import { ref, computed, onMounted, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Button,
  ConfirmDeleteDialog,
  Dialog, DialogScrollContent, DialogHeader, DialogTitle,
  DropdownMenu, DropdownMenuContent, DropdownMenuTrigger, DropdownMenuItem, DropdownMenuSeparator,
  Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle,
  InlineLoadingRow, PageShell, SettingsRow, SettingsSection, Switch, toast,
} from '@felinic/ui'
import {
  deleteBotsByBotIdScheduleById,
  getBotsByBotIdSchedule,
  getBotsByBotIdSettings,
  putBotsByBotIdScheduleById,
} from '@memohai/sdk'
import type { ScheduleSchedule } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { formatCalendarTime } from '@/utils/date-time'
import { describeCron, nextRuns } from '@/utils/cron-pattern'
import ScheduleEditor from './schedule-editor.vue'

const props = defineProps<{
  botId: string
  initialScheduleId?: string
}>()

const { t, locale } = useI18n()

const isLoading = ref(false)
const schedules = ref<ScheduleSchedule[]>([])
const botTimezone = ref<string | undefined>(undefined)
const busyIds = reactive(new Set<string>())

// --- Sort ---
type SortKey = 'name' | 'status' | 'next-run'
const sortKey = ref<SortKey>('name')

const SORT_OPTIONS: { key: SortKey; labelKey: string }[] = [
  { key: 'name', labelKey: 'bots.schedule.sortName' },
  { key: 'status', labelKey: 'bots.schedule.sortStatus' },
  { key: 'next-run', labelKey: 'bots.schedule.sortNextRun' },
]

const currentSortLabel = computed(
  () => t(SORT_OPTIONS.find((o) => o.key === sortKey.value)?.labelKey ?? 'bots.schedule.sortName'),
)

const cronLocale = computed<'en' | 'zh' | 'ja'>(() => (locale.value.startsWith('zh') ? 'zh' : locale.value.startsWith('ja') ? 'ja' : 'en'))
const uiLocale = computed(() => locale.value.startsWith('zh') ? 'zh-CN' : locale.value.startsWith('ja') ? 'ja-JP' : 'en-US')

const effectiveTimezone = computed(() => {
  const tz = botTimezone.value?.trim()
  if (tz) return tz
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone } catch { return 'UTC' }
})

const sortedSchedules = computed(() => {
  const list = [...schedules.value]
  if (sortKey.value === 'name') {
    return list.sort((a, b) => (a.name ?? '').localeCompare(b.name ?? ''))
  }
  if (sortKey.value === 'status') {
    return list.sort((a, b) => Number(b.enabled ?? false) - Number(a.enabled ?? false))
  }
  if (sortKey.value === 'next-run') {
    return list.sort((a, b) => (nextRunAt(a)?.getTime() ?? Infinity) - (nextRunAt(b)?.getTime() ?? Infinity))
  }
  return list
})

function nextRunAt(item: ScheduleSchedule): Date | null {
  if (!item.pattern) return null
  return nextRuns(item.pattern, effectiveTimezone.value, 1)[0] ?? null
}

// One line per task carrying the three things this page is asked for: what the
// user called it, when it fires, and when that next happens. A disabled task
// promises no next run, so the clause simply drops.
function rowDescription(item: ScheduleSchedule): string {
  const parts = [item.description?.trim() || '']
  parts.push((item.pattern ? describeCron(item.pattern, cronLocale.value) : '') || item.pattern || '')
  if (item.enabled) {
    const next = nextRunAt(item)
    if (next) {
      parts.push(t('bots.schedule.nextRun', {
        time: formatCalendarTime(next.toISOString(), { locale: uiLocale.value }),
      }))
    }
  }
  return parts.filter(Boolean).join(' · ')
}

const rows = computed(() =>
  sortedSchedules.value.map(item => ({ item, description: rowDescription(item) })),
)

// --- Delete via card menu ---
const deleteTarget = ref<ScheduleSchedule | null>(null)
const isDeleting = ref(false)

async function confirmDelete() {
  const item = deleteTarget.value
  if (!item?.id) return
  isDeleting.value = true
  const id = item.id
  busyIds.add(id)
  try {
    await deleteBotsByBotIdScheduleById({ path: { bot_id: props.botId, id }, throwOnError: true })
    toast.success(t('bots.schedule.deleteSuccess'))
    deleteTarget.value = null
    await fetchSchedules()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.schedule.deleteFailed')))
  } finally {
    isDeleting.value = false
    busyIds.delete(id)
  }
}

// --- Dialog state ---
const formVisible = ref(false)
const formMode = ref<'create' | 'edit'>('create')
const editingSchedule = ref<ScheduleSchedule | null>(null)
const consumedInitialScheduleId = ref<string | undefined>(undefined)

// --- API ---
async function fetchSchedules() {
  if (!props.botId) return
  isLoading.value = true
  try {
    const { data } = await getBotsByBotIdSchedule({ path: { bot_id: props.botId }, throwOnError: true })
    schedules.value = data?.items || []
    openInitialScheduleIfNeeded()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.schedule.loadFailed')))
  } finally {
    isLoading.value = false
  }
}

async function fetchBotSettings() {
  if (!props.botId) return
  try {
    const { data } = await getBotsByBotIdSettings({ path: { bot_id: props.botId }, throwOnError: true })
    const tz = (data as { timezone?: string } | undefined)?.timezone
    botTimezone.value = tz?.trim() || undefined
  } catch {
    botTimezone.value = undefined
  }
}

function handleNew() {
  formMode.value = 'create'
  editingSchedule.value = null
  formVisible.value = true
}

function handleEdit(item: ScheduleSchedule) {
  formMode.value = 'edit'
  editingSchedule.value = item
  formVisible.value = true
}

function openInitialScheduleIfNeeded() {
  const id = props.initialScheduleId
  if (!id || consumedInitialScheduleId.value === id) return
  const item = schedules.value.find(schedule => schedule.id === id)
  if (!item) return
  consumedInitialScheduleId.value = id
  handleEdit(item)
}

function handleFormCancel() {
  formVisible.value = false
  editingSchedule.value = null
}

async function handleEditorSaved() {
  toast.success(t('bots.schedule.saveSuccess'))
  formVisible.value = false
  editingSchedule.value = null
  await fetchSchedules()
}

async function handleToggleEnabled(item: ScheduleSchedule, enabled: boolean) {
  const id = item.id
  if (!id) return
  busyIds.add(id)
  try {
    await putBotsByBotIdScheduleById({ path: { bot_id: props.botId, id }, body: { enabled }, throwOnError: true })
    await fetchSchedules()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.schedule.saveFailed')))
  } finally {
    busyIds.delete(id)
  }
}

onMounted(() => {
  fetchSchedules()
  fetchBotSettings()
})

watch(
  () => props.initialScheduleId,
  () => {
    consumedInitialScheduleId.value = undefined
    openInitialScheduleIfNeeded()
  },
)
</script>
