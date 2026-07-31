<template>
  <PageShell
    variant="tab"
    :title="$t('bots.schedule.title')"
  >
    <template #actions>
      <DropdownMenu v-if="schedules.length > 1">
        <DropdownMenuTrigger as-child>
          <Button
            variant="ghost"
            class="text-muted-foreground"
          >
            <ArrowUpDown class="size-3.5" />
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
            <Check
              class="size-3.5 shrink-0"
              :class="sortKey === opt.key ? 'opacity-100' : 'opacity-0'"
            />
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <Button @click="handleNew">
        <Plus class="size-4" />
        {{ $t('bots.schedule.create') }}
      </Button>
    </template>

    <!-- Loading -->
    <InlineLoadingRow
      v-if="isLoading && schedules.length === 0"
      class="px-2"
    >
      {{ $t('common.loading') }}
    </InlineLoadingRow>

    <!-- Empty -->
    <div
      v-else-if="schedules.length === 0"
      class="flex flex-col items-center justify-center rounded-[var(--radius-menu-shell)] border border-dashed border-border py-16 text-center"
    >
      <Calendar class="mb-3 size-8 text-muted-foreground/40" />
      <p class="text-sm font-medium text-foreground">
        {{ $t('bots.schedule.empty') }}
      </p>
      <Button
        variant="outline"
        size="sm"
        class="mt-4"
        @click="handleNew"
      >
        <Plus class="size-4" />
        {{ $t('bots.schedule.create') }}
      </Button>
    </div>

    <!-- Card Grid -->
    <div
      v-else
      class="grid grid-cols-1 gap-3 sm:grid-cols-2"
    >
      <ScheduleListItem
        v-for="item in sortedSchedules"
        :key="item.id"
        :item="item"
        :description="item.description?.trim() || describeItem(item.pattern) || item.pattern || ''"
        :busy="busyIds.has(item.id || '')"
        @open="handleEdit(item)"
        @edit="handleEdit(item)"
        @delete="deleteTarget = item"
        @toggle="(enabled) => handleToggleEnabled(item, enabled)"
      />
    </div>

    <!-- Create / Edit Dialog -->
    <Dialog v-model:open="formVisible">
      <DialogScrollContent class="sm:max-w-lg">
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

    <!-- Delete confirmation dialog -->
    <Dialog
      :open="!!deleteTarget"
      @update:open="(v) => { if (!v) deleteTarget = null }"
    >
      <DialogContent class="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>{{ $t('bots.schedule.deleteTitle') }}</DialogTitle>
        </DialogHeader>
        <p class="text-sm text-muted-foreground">
          {{ $t('bots.schedule.deleteConfirm', { name: deleteTarget?.name ?? '' }) }}
        </p>
        <DialogFooter class="gap-2">
          <Button
            variant="outline"
            @click="deleteTarget = null"
          >
            {{ $t('common.cancel') }}
          </Button>
          <Button
            variant="destructive"
            :loading="isDeleting"
            @click="confirmDelete"
          >
            {{ $t('bots.schedule.delete') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </PageShell>
</template>

<script setup lang="ts">
import {
  ArrowUpDown, Calendar, Check, Plus,
} from 'lucide-vue-next'
import { ref, computed, onMounted, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { InlineLoadingRow, PageShell, toast } from '@felinic/ui'
import { useQueryCache } from '@pinia/colada'
import {
  Button,
  Dialog, DialogContent, DialogScrollContent, DialogHeader, DialogTitle, DialogFooter,
  DropdownMenu, DropdownMenuContent, DropdownMenuTrigger, DropdownMenuItem,
} from '@felinic/ui'
import {
  deleteBotsByBotIdScheduleById,
  getBotsByBotIdSchedule,
  getBotsByBotIdSettings,
  putBotsByBotIdScheduleById,
} from '@memohai/sdk'
import type { ScheduleSchedule } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { describeCron, nextRuns } from '@/utils/cron-pattern'
import ScheduleEditor from './schedule-editor.vue'
import ScheduleListItem from './schedule-list-item.vue'

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
    return list.sort((a, b) => {
      const aTime = a.pattern ? (nextRuns(a.pattern, effectiveTimezone.value, 1)[0]?.getTime() ?? Infinity) : Infinity
      const bTime = b.pattern ? (nextRuns(b.pattern, effectiveTimezone.value, 1)[0]?.getTime() ?? Infinity) : Infinity
      return aTime - bTime
    })
  }
  return list
})

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
    invalidateSidebarSchedule()
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

const cronLocale = computed<'en' | 'zh' | 'ja'>(() => (locale.value.startsWith('zh') ? 'zh' : locale.value.startsWith('ja') ? 'ja' : 'en'))

// --- Card helpers ---
function describeItem(pattern: string | undefined): string | undefined {
  if (!pattern) return undefined
  return describeCron(pattern, cronLocale.value)
}

// --- API ---
const queryCache = useQueryCache()
function invalidateSidebarSchedule() {
  queryCache.invalidateQueries({ key: ['bot-schedule', props.botId] })
}

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
  invalidateSidebarSchedule()
}

async function handleToggleEnabled(item: ScheduleSchedule, enabled: boolean) {
  const id = item.id
  if (!id) return
  busyIds.add(id)
  try {
    await putBotsByBotIdScheduleById({ path: { bot_id: props.botId, id }, body: { enabled }, throwOnError: true })
    await fetchSchedules()
    invalidateSidebarSchedule()
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
  () => {
    const entries = queryCache.getEntries({ key: ['bot-schedule', props.botId] })
    return entries[0]?.state.value.data
  },
  (next, prev) => {
    if (!props.botId || next === prev) return
    void fetchSchedules()
  },
)

watch(
  () => props.initialScheduleId,
  () => {
    consumedInitialScheduleId.value = undefined
    openInitialScheduleIfNeeded()
  },
)
</script>
