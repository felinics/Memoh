<template>
  <!-- The dialog states its steps the way the create surfaces do: a muted
       section label over a card of label-left / control-right rows. -->
  <form
    class="space-y-8"
    @submit.prevent="handleSubmit"
  >
    <SettingsSection :title="t('bots.steps.basicInfo')">
      <SettingsRow
        :label="t('bots.schedule.form.name')"
        stack="sm"
      >
        <div class="w-full sm:w-56">
          <Input
            id="sched-name"
            v-model="form.name"
            :placeholder="t('bots.schedule.form.namePlaceholder')"
          />
        </div>
      </SettingsRow>

      <SettingsRow
        :label="t('bots.schedule.form.enabled')"
        stack="sm"
      >
        <Switch
          :model-value="form.enabled"
          @update:model-value="(v: boolean) => form.enabled = !!v"
        />
      </SettingsRow>

      <SettingsRow stack="sm">
        <!-- Own body: the (optional) suffix rides with the label copy, which the
             bound label prop cannot express. -->
        <template #content>
          <label
            for="sched-desc"
            class="truncate text-control font-medium text-foreground"
          >
            {{ t('bots.schedule.form.description') }}
            <span class="ml-1 text-xs font-normal text-muted-foreground">({{ t('common.optional') }})</span>
          </label>
        </template>
        <div class="w-full sm:w-56">
          <Input
            id="sched-desc"
            v-model="form.description"
            :placeholder="t('bots.schedule.form.descriptionPlaceholder')"
          />
        </div>
      </SettingsRow>

      <!-- The picker is a cluster — a mode select, its operands, a weekday
           grid — so its control column is wider than the sm:w-56 the plain
           fields use: sm:w-72 is the width the seven weekday buttons need.
           -->
      <SettingsRow
        :label="t('bots.schedule.form.pattern')"
        stack="sm"
      >
        <div class="w-full space-y-3 sm:w-72">
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
      </SettingsRow>
    </SettingsSection>

    <!-- The command is the task. It gets its own titled section and no card:
         the textarea is already a bordered surface, and a card around it
         would draw a second edge around one field. -->
    <SectionGroup
      tone="muted"
      :title="t('bots.schedule.form.command')"
    >
      <Textarea
        id="sched-command"
        v-model="form.command"
        class="min-h-[9rem] resize-none font-mono"
        :placeholder="t('bots.schedule.form.commandPlaceholder')"
        rows="6"
      />
    </SectionGroup>

    <!-- Execution parameters used to hide behind a "More options" disclosure.
         A card that collapses to an empty bordered strip reads as broken, and
         these are three rows, not a thicket — they sit open like every other
         settings card on the create surfaces. -->
    <SettingsSection :title="t('bots.steps.settings')">
      <ScheduleExecutionFields
        ref="executionFields"
        :bot-id="botId"
        :form="execution"
      />
      <!-- The placeholder carries the empty-value meaning, so no help line
           repeats it. -->
      <SettingsRow
        :label="t('bots.schedule.form.maxCalls')"
        stack="sm"
      >
        <div class="w-full sm:w-56">
          <Input
            id="sched-max-calls"
            v-model="runLimitModel"
            type="text"
            inputmode="numeric"
            :placeholder="t('bots.schedule.unlimited')"
          />
        </div>
      </SettingsRow>
    </SettingsSection>

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
import { Trash2 } from 'lucide-vue-next'
import {
  Button,
  DialogFooter,
  Input,
  SectionGroup,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SettingsRow,
  SettingsSection,
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
import ScheduleExecutionFields, { type ScheduleExecutionForm } from './schedule-execution-fields.vue'
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
const execution = reactive<ScheduleExecutionForm>(defaultExecutionForm())
const executionFields = ref<InstanceType<typeof ScheduleExecutionFields> | null>(null)
const manualCron = ref('')
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
  if (execution.runTarget === 'existing_session' && !execution.targetSessionId) return false
  // The execution fields seed a model when the bot has no default, so this
  // only bites when there is no chat model to seed from — the backend would
  // reject that fire anyway, and the field says why.
  if (executionFields.value && !executionFields.value.modelSatisfied) return false
  return true
})

function defaultExecutionForm(): ScheduleExecutionForm {
  return {
    runTarget: 'new_session',
    targetSessionId: '',
    runtimeType: '',
    botAgentId: '',
    acpAgentId: '',
    modelId: '',
    acpModelId: '',
    reasoningEffort: '',
    workdirId: '',
  }
}

function hydrateExecution(schedule: ScheduleSchedule) {
  execution.runTarget = schedule.run_target === 'existing_session' ? 'existing_session' : 'new_session'
  execution.targetSessionId = schedule.target_session_id ?? ''
  execution.runtimeType = schedule.runtime_type === 'acp_agent'
    || schedule.runtime_type === 'codex'
    || schedule.runtime_type === 'claude-code'
    ? schedule.runtime_type
    : ''
  execution.botAgentId = schedule.bot_agent_id ?? ''
  execution.acpAgentId = schedule.acp_agent_id ?? ''
  execution.modelId = schedule.model_id ?? ''
  execution.acpModelId = schedule.acp_model_id ?? ''
  execution.reasoningEffort = schedule.reasoning_effort ?? ''
  execution.workdirId = schedule.workdir_id ?? ''
}

function executionRequestBlock() {
  return {
    run_target: execution.runTarget,
    target_session_id: execution.targetSessionId || undefined,
    runtime_type: execution.runTarget === 'new_session' && execution.runtimeType ? execution.runtimeType : undefined,
    bot_agent_id: execution.runTarget === 'new_session' && execution.botAgentId ? execution.botAgentId : undefined,
    acp_agent_id: execution.runTarget === 'new_session' && execution.acpAgentId ? execution.acpAgentId : undefined,
    model_id: execution.modelId || undefined,
    acp_model_id: execution.acpModelId || undefined,
    reasoning_effort: execution.reasoningEffort || undefined,
    workdir_id: execution.runTarget === 'new_session' && execution.workdirId ? execution.workdirId : undefined,
  }
}

function resetForm() {
  form.name = ''
  form.description = ''
  form.command = ''
  form.maxCalls = null
  form.enabled = true
  patternState.value = defaultScheduleFormState()
  manualCron.value = toCron(patternState.value)
  Object.assign(execution, defaultExecutionForm())
  submitError.value = null
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
  hydrateExecution(schedule)
  submitError.value = null
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
      // Create carries the execution block flat; update sends it as one
      // nested unit that replaces the stored block wholesale.
      const body = { ...base, ...executionRequestBlock() }
      await postBotsByBotIdSchedule({ path: { bot_id: props.botId }, body: body as unknown as ScheduleCreateRequest, throwOnError: true })
    } else {
      const id = props.schedule?.id
      if (!id) throw new Error('schedule id missing')
      const body = { ...base, execution: executionRequestBlock() }
      await putBotsByBotIdScheduleById({ path: { bot_id: props.botId, id }, body: body as unknown as ScheduleUpdateRequest, throwOnError: true })
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
