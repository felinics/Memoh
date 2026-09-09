<template>
  <div class="flex h-full min-h-0 min-w-0 flex-col overflow-hidden">
    <!-- No bot -->
    <p
      v-if="!currentBotId"
      class="px-[23px] pt-3 text-caption text-muted-foreground/60"
    >
      {{ t('chat.noBotSelected') }}
    </p>

    <!-- Loading (initial) -->
    <div
      v-else-if="isLoading && schedules.length === 0"
      class="flex items-center gap-2 px-[23px] pt-3 text-caption text-muted-foreground/60"
    >
      <Spinner class="size-3" />
    </div>

    <!-- Grouped list (scroller = @felinic/ui ScrollArea, see sidebar-scroll.css) -->
    <ScrollArea
      v-else
      class="sidebar-scroll flex-1 min-h-0"
      :scroll-hide-delay="300"
    >
      <!-- Empty -->
      <div
        v-if="groupedSchedules.length === 0"
        class="px-2 pt-2 space-y-1.5"
      >
        <p class="pl-[11px] text-caption text-muted-foreground/60">
          {{ t('bots.schedule.empty') }}
        </p>
        <button
          type="button"
          class="pl-[11px] text-left text-caption text-muted-foreground transition-colors hover:text-foreground"
          @click="handleManage"
        >
          + {{ t('bots.schedule.create') }}
        </button>
      </div>

      <!-- Groups -->
      <template v-else>
        <div
          v-for="(group, groupIdx) in groupedSchedules"
          :key="group.key"
        >
          <!-- Group header: time label + gear (first group only) -->
          <SidebarPanelHeader
            class="h-8 px-2 mt-2"
            :label="group.label"
            label-class="pl-[11px]"
          >
            <Button
              v-if="groupIdx === 0"
              variant="ghost"
              size="icon-sm"
              shape="circle"
              class="shrink-0 text-muted-foreground hover:text-foreground"
              :title="t('bots.schedule.create')"
              :aria-label="t('bots.schedule.create')"
              @click="handleManage"
            >
              <Plus
                :stroke-width="1.75"
                class="size-[15px]"
              />
            </Button>
          </SidebarPanelHeader>

          <!-- Cards in group -->
          <div class="px-2 pb-1 space-y-1.5">
            <ScheduleItem
              v-for="item in group.tasks"
              :key="item.id"
              :item="item"
              :description="item.description?.trim() || describeItem(item.pattern) || item.pattern || ''"
              :time-label="nextRunTimeLabel(item)"
              :busy="busyIds.has(item.id ?? '')"
              @open="handleOpenTask(item)"
              @edit="handleOpenTask(item)"
              @delete="deleteTarget = item"
              @toggle="(enabled) => handleToggleEnabled(item, enabled)"
            />
          </div>
        </div>
      </template>
    </ScrollArea>

    <!-- Delete confirmation -->
    <ConfirmDeleteDialog
      :open="!!deleteTarget"
      :title="t('bots.schedule.deleteTitle')"
      :description="t('bots.schedule.deleteConfirm', { name: deleteTarget?.name ?? '' })"
      :cancel-label="t('common.cancel')"
      :confirm-label="t('bots.schedule.delete')"
      :loading="isDeleting"
      @update:open="(v) => { if (!v) deleteTarget = null }"
      @confirm="confirmDelete"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, watch, computed, onMounted, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { Plus } from 'lucide-vue-next'
import { ConfirmDeleteDialog, toast } from '@felinic/ui'
import {
  Button, ScrollArea, Spinner,
} from '@felinic/ui'
import { getBotsByBotIdSchedule, deleteBotsByBotIdScheduleById, putBotsByBotIdScheduleById } from '@memohai/sdk'
import type { ScheduleSchedule } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { describeCron, nextRuns } from '@/utils/cron-pattern'
import ScheduleItem from './schedule-item.vue'
import SidebarPanelHeader from './panel-header.vue'
// The shared sidebar scroll treatment (fade, thumb ink, skeleton tuning) lives
// in this stylesheet — NOT a scoped style, because it is used by both
// panel-sessions.vue and this component (scoped selectors only match the
// defining component's DOM).
import '@/styles/sidebar-scroll.css'

const { t, locale } = useI18n()
const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)
const workspaceTabs = useWorkspaceTabsStore()
const { sidebarView } = storeToRefs(workspaceTabs)

const isLoading = ref(false)
const schedules = ref<ScheduleSchedule[]>([])
const deleteTarget = ref<ScheduleSchedule | null>(null)
const isDeleting = ref(false)
const busyIds = reactive(new Set<string>())

const cronLocale = computed<'en' | 'zh' | 'ja'>(() => (locale.value.startsWith('zh') ? 'zh' : locale.value.startsWith('ja') ? 'ja' : 'en'))
const uiLocale = computed(() => locale.value.startsWith('zh') ? 'zh-CN' : locale.value.startsWith('ja') ? 'ja-JP' : 'en-US')

const effectiveTimezone = computed(() => {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone } catch { return 'UTC' }
})

// --- Grouping by day; each item carries its own next-run time. ---

interface ScheduleGroup {
  key: string
  label: string
  tasks: ScheduleSchedule[]
}

function isSameDay(a: Date, b: Date) {
  return a.getFullYear() === b.getFullYear()
    && a.getMonth() === b.getMonth()
    && a.getDate() === b.getDate()
}

function groupLabel(date: Date, now: Date): string {
  const tomorrow = new Date(now)
  tomorrow.setDate(now.getDate() + 1)
  const diff = date.getTime() - now.getTime()
  const week = 7 * 86_400_000

  if (isSameDay(date, now)) {
    return locale.value.startsWith('zh') ? '今天' : locale.value.startsWith('ja') ? '今日' : 'Today'
  }
  if (isSameDay(date, tomorrow)) {
    return locale.value.startsWith('zh') ? '明天' : locale.value.startsWith('ja') ? '明日' : 'Tomorrow'
  }
  if (diff < week) {
    return date.toLocaleDateString(uiLocale.value, { weekday: 'short' })
  }
  return date.toLocaleDateString(uiLocale.value, { month: 'short', day: 'numeric' })
}

const groupedSchedules = computed<ScheduleGroup[]>(() => {
  const now = new Date()
  const tz = effectiveTimezone.value

  const pairs = schedules.value.flatMap(task => {
    if (!task.pattern) return []
    const runs = nextRuns(task.pattern, tz, 1)
    const next = runs[0]
    if (!next) return []
    return [{ task, next }]
  })

  // Group by day; cards inside each group still sort by their exact next run.
  const map = new Map<string, { date: Date; tasks: ScheduleSchedule[] }>()
  for (const { task, next } of pairs) {
    const key = `${next.getFullYear()}-${next.getMonth()}-${next.getDate()}`
    if (!map.has(key)) map.set(key, { date: next, tasks: [] })
    map.get(key)!.tasks.push(task)
  }

  return Array.from(map.entries())
    .sort(([, a], [, b]) => a.date.getTime() - b.date.getTime())
    .map(([key, { date, tasks }]) => ({
      key,
      label: groupLabel(date, now),
      tasks: tasks.sort((a, b) => {
        const aTime = nextRunForItem(a)?.getTime() ?? Infinity
        const bTime = nextRunForItem(b)?.getTime() ?? Infinity
        return aTime - bTime
      }),
    }))
})

// ---

function describeItem(pattern: string | undefined): string | undefined {
  if (!pattern) return undefined
  return describeCron(pattern, cronLocale.value)
}

function nextRunForItem(item: ScheduleSchedule): Date | null {
  if (!item.pattern) return null
  return nextRuns(item.pattern, effectiveTimezone.value, 1)[0] ?? null
}

function nextRunTimeLabel(item: ScheduleSchedule): string {
  const next = nextRunForItem(item)
  if (!next) return item.pattern ?? ''
  return next.toLocaleTimeString(uiLocale.value, { hour: '2-digit', minute: '2-digit', hour12: false })
}

async function fetchSchedules() {
  const botId = currentBotId.value
  if (!botId) { schedules.value = []; return }
  isLoading.value = true
  try {
    const { data } = await getBotsByBotIdSchedule({ path: { bot_id: botId } })
    schedules.value = data?.items || []
  } catch {
    schedules.value = []
  } finally {
    isLoading.value = false
  }
}

// Refresh when user switches TO the schedule tab
watch(sidebarView, (view) => {
  if (view === 'schedule') void fetchSchedules()
})

// Refresh when active bot changes
watch(currentBotId, () => void fetchSchedules())

async function confirmDelete() {
  const item = deleteTarget.value
  if (!item?.id || !currentBotId.value) return
  isDeleting.value = true
  try {
    await deleteBotsByBotIdScheduleById({
      path: { bot_id: currentBotId.value, id: item.id },
      throwOnError: true,
    })
    toast.success(t('bots.schedule.deleteSuccess'))
    deleteTarget.value = null
    await fetchSchedules()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.schedule.deleteFailed')))
  } finally {
    isDeleting.value = false
  }
}

async function handleToggleEnabled(item: ScheduleSchedule, enabled: boolean) {
  const id = item.id
  const botId = currentBotId.value
  if (!id || !botId) return
  busyIds.add(id)
  try {
    await putBotsByBotIdScheduleById({
      path: { bot_id: botId, id },
      body: { enabled },
      throwOnError: true,
    })
    await fetchSchedules()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.schedule.saveFailed')))
  } finally {
    busyIds.delete(id)
  }
}

function handleManage() {
  const botId = currentBotId.value
  if (!botId) return
  workspaceTabs.openSchedule()
}

function handleOpenTask(item: ScheduleSchedule) {
  const botId = currentBotId.value
  if (!botId) return
  workspaceTabs.openSchedule(item.id, item.name)
}

onMounted(() => void fetchSchedules())
</script>
