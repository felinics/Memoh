<template>
  <form
    class="space-y-4"
    @submit.prevent="handleSubmit"
  >
    <!-- Name + enable toggle share one aligned row: the toggle rides beside the
         input (items-end), not the label, so it stays a sibling of the field
         rather than living in the FieldStack label slot. -->
    <div class="flex items-end gap-3">
      <FieldStack
        class="min-w-0 flex-1"
        :label="t('bots.schedule.form.name')"
        for="sched-name"
      >
        <Input
          id="sched-name"
          v-model="form.name"
          :placeholder="t('bots.schedule.form.namePlaceholder')"
        />
      </FieldStack>
      <div class="flex h-9 shrink-0 items-center gap-2">
        <Label
          class="cursor-pointer text-muted-foreground"
          @click="form.enabled = !form.enabled"
        >
          {{ t('bots.schedule.form.enabled') }}
        </Label>
        <Switch
          :model-value="form.enabled"
          @update:model-value="(v: boolean) => form.enabled = !!v"
        />
      </div>
    </div>

    <FieldStack>
      <!-- Label carries an (optional) suffix, so it rides the #label slot to keep
           its exact markup rather than the plain-text default label. -->
      <template #label>
        <Label for="sched-desc">
          {{ t('bots.schedule.form.description') }}
          <span class="ml-1 text-caption text-muted-foreground font-normal">({{ t('common.optional') }})</span>
        </Label>
      </template>
      <Input
        id="sched-desc"
        v-model="form.description"
        :placeholder="t('bots.schedule.form.descriptionPlaceholder')"
      />
    </FieldStack>

    <FieldStack
      :label="t('bots.schedule.form.command')"
      for="sched-command"
    >
      <Textarea
        id="sched-command"
        v-model="form.command"
        class="min-h-[4.5rem] resize-none font-mono"
        :placeholder="t('bots.schedule.form.commandPlaceholder')"
        rows="3"
      />
    </FieldStack>

    <div class="space-y-3">
      <Label>
        {{ t('bots.schedule.form.pattern') }}
      </Label>

      <div class="flex items-center gap-2 flex-wrap">
        <Select v-model="schedModeModel">
          <SelectTrigger class="w-36 shrink-0">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem
              v-for="scheduleMode in SCHEDULE_MODES"
              :key="scheduleMode.value"
              :value="scheduleMode.value"
            >
              {{ t(scheduleMode.labelKey) }}
            </SelectItem>
          </SelectContent>
        </Select>

        <template v-if="patternState.mode === 'minutes'">
          <Input
            type="number"
            :min="1"
            :max="59"
            :model-value="patternState.intervalMinutes"
            class="w-20 text-center"
            @update:model-value="v => patchState({ intervalMinutes: clampInt(v, 1, 59, 1) })"
          />
          <span class="text-sm text-muted-foreground">{{ t('bots.schedule.picker.minutes') }}</span>
        </template>

        <template v-else-if="patternState.mode === 'hourly'">
          <span class="text-sm text-muted-foreground">{{ t('bots.schedule.picker.atMinute') }}</span>
          <Input
            type="number"
            :min="0"
            :max="59"
            :model-value="patternState.minute"
            class="w-20 text-center"
            @update:model-value="v => patchState({ minute: clampInt(v, 0, 59, 0) })"
          />
        </template>

        <TimeInput
          v-else-if="patternState.mode === 'daily'"
          :hour="patternState.hours[0] ?? 9"
          :minute="patternState.minute"
          @update:hour="v => patchState({ hours: [v] })"
          @update:minute="v => patchState({ minute: v })"
        />

        <TimeInput
          v-else-if="patternState.mode === 'weekly'"
          :hour="patternState.hours[0] ?? 9"
          :minute="patternState.minute"
          @update:hour="v => patchState({ hours: [v] })"
          @update:minute="v => patchState({ minute: v })"
        />

        <template v-else-if="patternState.mode === 'monthly'">
          <span class="text-sm text-muted-foreground">{{ t('bots.schedule.picker.day') }}</span>
          <Input
            type="number"
            :min="1"
            :max="31"
            :model-value="patternState.monthDays[0] ?? 1"
            class="w-16 text-center"
            @update:model-value="v => patchState({ monthDays: [clampInt(v, 1, 31, 1)] })"
          />
          <TimeInput
            :hour="patternState.hours[0] ?? 9"
            :minute="patternState.minute"
            @update:hour="v => patchState({ hours: [v] })"
            @update:minute="v => patchState({ minute: v })"
          />
        </template>
      </div>

      <div
        v-if="patternState.mode === 'weekly'"
        class="grid grid-cols-7 gap-1"
      >
        <button
          v-for="(key, idx) in WEEKDAY_KEYS"
          :key="key"
          type="button"
          class="h-9 rounded-md border text-sm transition-colors"
          :class="patternState.weekdays.includes(idx)
            ? 'bg-primary text-primary-foreground border-primary'
            : 'bg-background hover:bg-accent'"
          @click="toggleWeekday(idx)"
        >
          {{ t(`bots.schedule.weekday.${key}`) }}
        </button>
      </div>

      <div
        v-if="patternState.mode === 'advanced'"
        class="space-y-1.5"
      >
        <Input
          :model-value="patternState.advancedPattern"
          class="font-mono"
          placeholder="0 9 * * *"
          @update:model-value="v => patchState({ advancedPattern: String(v) })"
        />
        <p
          v-if="patternState.advancedPattern && !isValidCron(patternState.advancedPattern)"
          class="text-caption text-destructive"
        >
          {{ t('bots.schedule.form.invalidPattern') }}
        </p>
      </div>

      <p
        v-if="schedulePreviewText && ['weekly', 'monthly', 'advanced'].includes(patternState.mode)"
        class="text-caption text-muted-foreground"
      >
        {{ schedulePreviewText }}
      </p>
    </div>

    <div>
      <button
        type="button"
        class="flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
        @click="showMore = !showMore"
      >
        <ChevronRight
          class="size-3.5"
          :class="showMore ? 'rotate-90' : ''"
        />
        {{ t('bots.schedule.moreOptions') }}
      </button>

      <div
        class="grid overflow-hidden transition-[grid-template-rows] duration-200 ease-out"
        :class="showMore ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'"
      >
        <div class="min-h-0">
          <div class="mt-3">
            <div class="flex items-center justify-between gap-3">
              <Label class="text-muted-foreground">
                {{ t('bots.schedule.form.maxCalls') }}
              </Label>
              <Input
                v-model="runLimitModel"
                type="text"
                inputmode="numeric"
                size="sm"
                :placeholder="'∞'"
                class="w-24 text-center"
              />
            </div>
          </div>
        </div>
      </div>
    </div>

    <p
      v-if="submitError"
      class="text-caption text-destructive"
    >
      {{ submitError }}
    </p>

    <DialogFooter class="gap-2 sm:justify-between">
      <div
        v-if="mode === 'edit' && schedule"
        class="flex-1"
      >
        <Button
          type="button"
          variant="ghost"
          class="text-destructive hover:bg-destructive/10 hover:text-destructive"
          @click="$emit('delete', schedule)"
        >
          <Trash2 class="size-4" />
          {{ t('common.delete') }}
        </Button>
      </div>
      <div
        v-else
        class="flex-1"
      />

      <div class="flex gap-2">
        <Button
          type="button"
          variant="ghost"
          @click="$emit('cancel')"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          type="submit"
          :disabled="!canSubmit"
          :loading="isSaving"
        >
          {{ mode === 'create' ? t('common.create') : t('common.confirm') }}
        </Button>
      </div>
    </DialogFooter>
  </form>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight, Trash2 } from 'lucide-vue-next'
import {
  Button,
  DialogFooter,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Switch,
  Textarea,
  TimeInput,
} from '@felinic/ui'
import {
  getBotsByBotIdSettings,
  postBotsByBotIdSchedule,
  putBotsByBotIdScheduleById,
} from '@memohai/sdk'
import type { ScheduleCreateRequest, ScheduleSchedule, ScheduleUpdateRequest } from '@memohai/sdk'
import { FieldStack } from '@felinic/ui'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
  describeCron,
  defaultScheduleFormState,
  fromCron,
  isValidCron,
  toCron,
  WEEKDAY_KEYS,
  type ScheduleFormState,
  type ScheduleMode,
} from '@/utils/cron-pattern'

const props = defineProps<{
  botId: string
  mode: 'create' | 'edit'
  schedule?: ScheduleSchedule | null
}>()

const emit = defineEmits<{
  saved: []
  cancel: []
  delete: [schedule: ScheduleSchedule]
}>()

const { t, locale } = useI18n()

const SCHEDULE_MODES: { value: ScheduleMode; labelKey: string }[] = [
  { value: 'minutes', labelKey: 'bots.schedule.mode.minutes' },
  { value: 'hourly', labelKey: 'bots.schedule.mode.hourly' },
  { value: 'daily', labelKey: 'bots.schedule.mode.daily' },
  { value: 'weekly', labelKey: 'bots.schedule.mode.weekly' },
  { value: 'monthly', labelKey: 'bots.schedule.mode.monthly' },
  { value: 'advanced', labelKey: 'bots.schedule.mode.advanced' },
]

interface SchedulePlainForm {
  name: string
  description: string
  command: string
  maxCalls: number | null
  enabled: boolean
}

const form = reactive<SchedulePlainForm>({
  name: '',
  description: '',
  command: '',
  maxCalls: null,
  enabled: true,
})
const patternState = ref<ScheduleFormState>(defaultScheduleFormState())
const manualCron = ref('')
const showMore = ref(false)
const isSaving = ref(false)
const submitError = ref<string | null>(null)
const botTimezone = ref<string | undefined>(undefined)

const cronLocale = computed<'en' | 'zh' | 'ja'>(() => (locale.value.startsWith('zh') ? 'zh' : locale.value.startsWith('ja') ? 'ja' : 'en'))

const effectiveTimezone = computed(() => {
  const tz = botTimezone.value?.trim()
  if (tz) return tz
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone } catch { return 'UTC' }
})

function clampInt(value: unknown, min: number, max: number, fallback: number): number {
  const n = Number(value)
  if (!Number.isFinite(n)) return fallback
  return Math.max(min, Math.min(max, Math.round(n)))
}

function patchState(patch: Partial<ScheduleFormState>) {
  patternState.value = { ...patternState.value, ...patch }
}

const schedModeModel = computed({
  get: (): string => patternState.value.mode,
  set: (val: unknown) => {
    const next = String(val) as ScheduleMode
    const allowed: ScheduleMode[] = ['minutes', 'hourly', 'daily', 'weekly', 'monthly', 'advanced']
    if (!allowed.includes(next)) return
    const patch: Partial<ScheduleFormState> = { mode: next }
    if (next === 'weekly' || next === 'monthly' || next === 'hourly') {
      patch.hours = [patternState.value.hours[0] ?? 9]
    }
    if (next === 'advanced' && !patternState.value.advancedPattern.trim()) {
      try { patch.advancedPattern = toCron(patternState.value) } catch { patch.advancedPattern = '' }
    }
    patternState.value = { ...patternState.value, ...patch }
  },
})

function toggleWeekday(d: number) {
  const set = new Set(patternState.value.weekdays)
  if (set.has(d)) set.delete(d)
  else set.add(d)
  const next = Array.from(set).sort((a, b) => a - b)
  patchState({ weekdays: next.length ? next : [d] })
}

const schedulePreviewText = computed(() => {
  if (!manualCron.value || !isValidCron(manualCron.value)) return ''
  const description = describeCron(manualCron.value, cronLocale.value) || ''
  if (!description) return ''
  return effectiveTimezone.value ? `${description} · ${effectiveTimezone.value}` : description
})

watch(
  () => patternState.value,
  (next) => {
    try {
      const canonical = toCron(next)
      if (toCron(fromCron(manualCron.value)) !== canonical) manualCron.value = canonical
    } catch { /* invalid intermediate state */ }
  },
  { deep: true },
)

watch(manualCron, (next) => {
  const nextState = fromCron(next)
  if (JSON.stringify(patternState.value) !== JSON.stringify(nextState)) patternState.value = nextState
})

const runLimitModel = computed({
  get(): string {
    return form.maxCalls === null ? '' : String(form.maxCalls)
  },
  set(val: string | number) {
    const str = String(val).replace(/\D/g, '')
    if (!str) {
      form.maxCalls = null
    } else {
      const n = parseInt(str, 10)
      form.maxCalls = Number.isFinite(n) && n > 0 ? n : null
    }
  },
})

const maxCallsUnlimited = computed(() => form.maxCalls === null)

const canSubmit = computed(() => {
  if (isSaving.value) return false
  if (!form.name.trim()) return false
  if (!form.command.trim()) return false
  if (!manualCron.value || !isValidCron(manualCron.value)) return false
  if (!maxCallsUnlimited.value && (form.maxCalls === null || form.maxCalls < 1)) return false
  return true
})

function resetForm() {
  form.name = ''
  form.description = ''
  form.command = ''
  form.maxCalls = null
  form.enabled = true
  patternState.value = defaultScheduleFormState()
  manualCron.value = toCron(patternState.value)
  submitError.value = null
  showMore.value = false
}

function hydrateForm(schedule: ScheduleSchedule) {
  form.name = schedule.name ?? ''
  form.description = schedule.description ?? ''
  form.command = schedule.command ?? ''
  const raw = schedule.max_calls as unknown
  form.maxCalls = (typeof raw === 'number' && raw > 0) ? raw : null
  form.enabled = schedule.enabled ?? true
  patternState.value = fromCron(schedule.pattern ?? '')
  manualCron.value = schedule.pattern ?? ''
  submitError.value = null
  showMore.value = (typeof raw === 'number' && raw > 0)
}

function hydrateFromProps() {
  if (props.mode === 'edit' && props.schedule) {
    hydrateForm(props.schedule)
  } else {
    resetForm()
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

async function handleSubmit() {
  if (!canSubmit.value) return
  submitError.value = null
  isSaving.value = true
  try {
    const base = {
      name: form.name.trim(),
      description: form.description.trim(),
      command: form.command.trim(),
      pattern: manualCron.value.trim(),
      enabled: form.enabled,
      max_calls: form.maxCalls ?? null,
    }
    if (props.mode === 'create') {
      await postBotsByBotIdSchedule({ path: { bot_id: props.botId }, body: base as unknown as ScheduleCreateRequest, throwOnError: true })
    } else {
      const id = props.schedule?.id
      if (!id) throw new Error('schedule id missing')
      await putBotsByBotIdScheduleById({ path: { bot_id: props.botId, id }, body: base as unknown as ScheduleUpdateRequest, throwOnError: true })
    }
    emit('saved')
  } catch (error) {
    submitError.value = resolveApiErrorMessage(error, t('bots.schedule.saveFailed'))
  } finally {
    isSaving.value = false
  }
}

watch(
  () => [props.mode, props.schedule?.id],
  () => hydrateFromProps(),
  { immediate: true },
)

onMounted(() => {
  void fetchBotSettings()
})
</script>
