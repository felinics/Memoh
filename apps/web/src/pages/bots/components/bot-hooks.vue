<template>
  <PageShell
    variant="tab"
    :title="$t('bots.hooks.title')"
    :description="$t('bots.hooks.subtitle')"
  >
    <template #actions>
      <span
        v-if="hasChanges"
        class="text-xs text-muted-foreground"
      >
        {{ $t('common.unsaved') }}
      </span>
      <Button
        variant="secondary"
        size="sm"
        class="h-8 gap-1.5"
        :disabled="loading"
        @click="void reload()"
      >
        <RefreshCw class="size-3.5" />
        {{ $t('bots.hooks.reload') }}
      </Button>
      <Button
        size="sm"
        class="h-8 gap-1.5"
        :disabled="!canSave"
        :loading="saving"
        @click="void save()"
      >
        <Save class="size-3.5" />
        {{ $t('bots.hooks.save') }}
      </Button>
    </template>

    <div class="space-y-8">
      <SettingsSection :title="$t('bots.hooks.statusSection')">
        <SettingsRow
          :label="$t('bots.hooks.filePath')"
          :description="configPath"
        >
          <Badge
            variant="outline"
            class="rounded-[var(--radius-menu-shell)]"
          >
            {{ configExists ? $t('bots.hooks.present') : $t('bots.hooks.missing') }}
          </Badge>
        </SettingsRow>
        <SettingsRow
          :label="$t('common.status')"
          :description="statusDescription"
        >
          <Badge
            :variant="configEnabled ? 'default' : 'secondary'"
            class="rounded-[var(--radius-menu-shell)]"
          >
            {{ configEnabled ? $t('common.active') : $t('common.inactive') }}
          </Badge>
        </SettingsRow>
        <SettingsRow
          :label="$t('bots.hooks.rules')"
          :description="$t('bots.hooks.rulesDescription')"
        >
          <div class="flex items-center gap-3 text-sm tabular-nums">
            <span class="text-foreground">{{ enabledHooksCount }}</span>
            <span class="text-muted-foreground">/</span>
            <span class="text-muted-foreground">{{ hooksCount }}</span>
          </div>
        </SettingsRow>
        <SettingsRow
          :label="$t('bots.hooks.actions')"
          :description="$t('bots.hooks.actionsDescription')"
        >
          <span class="text-sm tabular-nums text-foreground">{{ actionsCount }}</span>
        </SettingsRow>
      </SettingsSection>

      <SettingsSection :title="$t('bots.hooks.eventsSection')">
        <div class="px-4 py-4">
          <div
            v-if="eventsLoading"
            class="grid gap-2 sm:grid-cols-2"
          >
            <Skeleton
              v-for="idx in 8"
              :key="idx"
              class="h-7 rounded-[var(--radius-menu-shell)]"
            />
          </div>
          <div
            v-else
            class="flex flex-wrap gap-2"
          >
            <Badge
              v-for="event in eventItems"
              :key="event.name"
              :variant="event.runtime_supported ? 'default' : 'outline'"
              class="rounded-[var(--radius-menu-shell)]"
            >
              {{ event.name }}
            </Badge>
          </div>
        </div>
      </SettingsSection>

      <SettingsSection :title="$t('bots.hooks.editorSection')">
        <div class="space-y-3 px-4 py-4">
          <div class="flex items-center justify-between gap-3">
            <div class="min-w-0">
              <div class="text-sm font-medium text-foreground">
                {{ $t('bots.hooks.jsonConfig') }}
              </div>
              <p class="mt-0.5 text-xs text-muted-foreground">
                {{ editorHint }}
              </p>
            </div>
            <Button
              variant="ghost"
              size="sm"
              class="h-8 gap-1.5"
              @click="applyTemplate"
            >
              <FileJson class="size-3.5" />
              {{ $t('bots.hooks.template') }}
            </Button>
          </div>
          <Textarea
            v-model="editor"
            class="min-h-[24rem] resize-y font-mono text-xs leading-5"
            spellcheck="false"
          />
        </div>
      </SettingsSection>

      <SettingsSection :title="$t('bots.hooks.testSection')">
        <SettingsRow
          :label="$t('bots.hooks.testEvent')"
          :description="$t('bots.hooks.testEventDescription')"
        >
          <Select v-model="testEvent">
            <SelectTrigger
              size="sm"
              class="w-56"
            >
              <SelectValue :placeholder="$t('bots.hooks.selectEvent')" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem
                v-for="event in eventItems"
                :key="event.name"
                :value="event.name"
              >
                {{ event.name }}
              </SelectItem>
            </SelectContent>
          </Select>
        </SettingsRow>
        <div class="space-y-3 px-4 py-4">
          <Textarea
            v-model="testPayload"
            class="min-h-32 resize-y font-mono text-xs leading-5"
            spellcheck="false"
          />
          <div class="flex items-center justify-between gap-3">
            <p
              v-if="testPayloadError"
              class="text-xs text-destructive"
            >
              {{ testPayloadError }}
            </p>
            <span
              v-else
              class="text-xs text-muted-foreground"
            >
              {{ $t('bots.hooks.testReady') }}
            </span>
            <Button
              size="sm"
              class="h-8 gap-1.5"
              :disabled="!canRunTest"
              :loading="testing"
              @click="void runTest()"
            >
              <Play class="size-3.5" />
              {{ $t('bots.hooks.runTest') }}
            </Button>
          </div>
          <pre
            v-if="testResult"
            class="max-h-72 overflow-auto rounded-[var(--radius-menu-shell)] border border-border bg-muted/30 p-3 text-xs leading-5 text-foreground"
          >{{ testResult }}</pre>
        </div>
      </SettingsSection>
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMutation, useQuery } from '@pinia/colada'
import {
  Badge,
  Button,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Skeleton,
  Textarea,
  toast,
} from '@felinic/ui'
import { FileJson, Play, RefreshCw, Save } from 'lucide-vue-next'
import {
  getBotsByBotIdContainerFsRead,
  getBotsByBotIdHooksEvents,
  postBotsByBotIdContainerFsWrite,
  postBotsByBotIdHooksTest,
} from '@memohai/sdk'
import type { HandlersHookEventInfo } from '@memohai/sdk'
import { PageShell, SettingsRow, SettingsSection } from '@felinic/ui'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const configPath = '/data/.memoh/hooks.json'

type HookActionConfig = {
  type?: string
  command?: string
  tool?: string
}

type HookConfigEntry = {
  name?: string
  event?: string
  enabled?: boolean
  actions?: HookActionConfig[]
}

type HooksConfig = {
  version?: number
  enabled?: boolean
  hooks?: HookConfigEntry[]
}

const editor = ref('')
const original = ref('')
const configExists = ref(false)
const testEvent = ref('PreToolUse')
const testPayload = ref(formatJSON(defaultTestPayload('PreToolUse')))
const testResult = ref('')

const { data: eventsData, isLoading: eventsLoading } = useQuery({
  key: () => ['bot-hooks-events', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdHooksEvents({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
})

const { isLoading: loading, refetch } = useQuery({
  key: () => ['bot-hooks-config', props.botId],
  query: async () => {
    if (!props.botId) return ''
    try {
      const { data } = await getBotsByBotIdContainerFsRead({
        path: { bot_id: props.botId },
        query: { path: configPath },
        throwOnError: true,
      })
      const content = data.content ?? ''
      configExists.value = true
      editor.value = content
      original.value = content
      return content
    } catch (error) {
      if (!isNotFoundError(error)) {
        toast.error(resolveApiErrorMessage(error, t('bots.hooks.loadFailed')))
        const content = formatJSON(defaultHooksConfig())
        configExists.value = false
        editor.value = content
        original.value = content
        return content
      }
      const content = formatJSON(emptyHooksConfig())
      await postBotsByBotIdContainerFsWrite({
        path: { bot_id: props.botId },
        body: { path: configPath, content },
        throwOnError: true,
      })
      configExists.value = true
      editor.value = content
      original.value = content
      return content
    }
  },
  enabled: () => !!props.botId,
})

const { mutateAsync: writeConfig, isLoading: saving } = useMutation({
  mutation: async (content: string) => {
    await postBotsByBotIdContainerFsWrite({
      path: { bot_id: props.botId },
      body: { path: configPath, content },
      throwOnError: true,
    })
  },
})

const { mutateAsync: testHooks, isLoading: testing } = useMutation({
  mutation: async (body: Record<string, unknown>) => {
    const { data } = await postBotsByBotIdHooksTest({
      path: { bot_id: props.botId },
      body,
      throwOnError: true,
    })
    return data
  },
})

const eventItems = computed<HandlersHookEventInfo[]>(() => eventsData.value?.events ?? [])

watch(eventItems, (items) => {
  if (!items.length) return
  if (!items.some(item => item.name === testEvent.value)) {
    testEvent.value = items[0].name
  }
}, { immediate: true })

watch(testEvent, (event) => {
  testPayload.value = formatJSON(defaultTestPayload(event))
})

const parseState = computed(() => {
  try {
    return { value: JSON.parse(editor.value) as HooksConfig, error: '' }
  } catch (error) {
    return { value: null, error: error instanceof Error ? error.message : String(error) }
  }
})

const parsedConfig = computed(() => parseState.value.value)
const parseError = computed(() => parseState.value.error)
const hooks = computed(() => Array.isArray(parsedConfig.value?.hooks) ? parsedConfig.value.hooks : [])
const hooksCount = computed(() => hooks.value.length)
const enabledHooksCount = computed(() => hooks.value.filter(hook => hook.enabled !== false).length)
const actionsCount = computed(() => hooks.value.reduce((sum, hook) => sum + (Array.isArray(hook.actions) ? hook.actions.length : 0), 0))
const configEnabled = computed(() => parsedConfig.value?.enabled !== false && !parseError.value)
const hasChanges = computed(() => editor.value !== original.value)
const canSave = computed(() => !!props.botId && !parseError.value && hasChanges.value)

const statusDescription = computed(() => {
  if (parseError.value) return t('bots.hooks.invalidDescription')
  if (!configExists.value) return t('bots.hooks.missingDescription')
  if (!configEnabled.value) return t('bots.hooks.disabledDescription')
  return t('bots.hooks.enabledDescription')
})

const editorHint = computed(() => {
  if (parseError.value) return parseError.value
  return t('bots.hooks.editorHint')
})

const testPayloadError = computed(() => {
  try {
    JSON.parse(testPayload.value)
    return ''
  } catch (error) {
    return error instanceof Error ? error.message : String(error)
  }
})

const canRunTest = computed(() => !!props.botId && !!testEvent.value && !testPayloadError.value)

async function reload() {
  await refetch()
}

async function save() {
  if (!canSave.value) return
  try {
    const formatted = formatJSON(JSON.parse(editor.value))
    await writeConfig(formatted)
    editor.value = formatted
    original.value = formatted
    configExists.value = true
    toast.success(t('bots.hooks.saveSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.hooks.saveFailed')))
  }
}

async function runTest() {
  if (!canRunTest.value) return
  try {
    const body = JSON.parse(testPayload.value) as Record<string, unknown>
    body.event = testEvent.value
    const result = await testHooks(body)
    testResult.value = formatJSON(result)
    toast.success(t('bots.hooks.testSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.hooks.testFailed')))
  }
}

function applyTemplate() {
  editor.value = formatJSON(defaultHooksConfig())
}

function emptyHooksConfig(): HooksConfig & Record<string, unknown> {
  return {
    version: 1,
    enabled: true,
    hooks: [],
  }
}

function defaultHooksConfig(): HooksConfig & Record<string, unknown> {
  return {
    version: 1,
    enabled: true,
    defaults: {
      timeout: '10s',
      on_error: 'fail',
      max_output_bytes: 65536,
      trigger_nested_hooks: false,
    },
    env: {},
    hooks: [
      {
        name: 'log tool results',
        event: 'PostToolUse',
        enabled: true,
        priority: 0,
        actions: [
          {
            type: 'command',
            command: 'mkdir -p .memoh && cat >> .memoh/hooks.log',
            on_error: 'ignore',
          },
        ],
      },
      {
        name: 'review shell commands',
        event: 'PreToolUse',
        matcher: '^exec$',
        enabled: false,
        priority: 10,
        actions: [
          {
            type: 'command',
            command: 'python3 -c \'import json,sys; d=json.load(sys.stdin); cmd=str(((d.get("tool") or {}).get("input") or {}).get("command","")); decision="ask_approval" if cmd else "allow"; print(json.dumps({"decision": decision, "reason": "shell command requested"}))\'',
            on_error: 'block',
          },
        ],
      },
    ],
  }
}

function defaultTestPayload(event: string): Record<string, unknown> {
  return {
    event,
    tool: {
      name: 'exec',
      call_id: 'hooks-ui-test',
      input: {
        command: 'echo hook-test',
      },
    },
    extra: {
      source: 'bot-details',
    },
  }
}

function formatJSON(value: unknown): string {
  return JSON.stringify(value, null, 2)
}

function isNotFoundError(error: unknown): boolean {
  const maybe = error as { status?: number, response?: { status?: number }, message?: string }
  return maybe?.status === 404 || maybe?.response?.status === 404 || String(maybe?.message ?? '').toLowerCase().includes('not found')
}
</script>
