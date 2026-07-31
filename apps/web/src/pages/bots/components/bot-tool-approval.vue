<template>
  <PageShell
    variant="tab"
    :title="t('bots.toolApproval.title')"
    :description="t('bots.toolApproval.intro')"
  >
    <template #actions>
      <Button
        v-if="!initialLoading && !loadFailed"
        size="sm"
        :disabled="!hasDirtyTargets || isSaving"
        :loading="isSaving"
        @click="saveChanges"
      >
        {{ t('common.saveChanges') }}
      </Button>
    </template>

    <SettingsSection v-if="initialLoading">
      <InlineLoadingRow surface="card-row">
        {{ t('bots.toolApproval.loading') }}
      </InlineLoadingRow>
    </SettingsSection>

    <SettingsSection v-else-if="loadFailed">
      <SettingsRow
        :label="t('bots.toolApproval.loadFailed')"
        :description="t('bots.toolApproval.loadFailedDescription')"
      >
        <Button
          variant="outline"
          size="sm"
          @click="refetchWorkspaceTargets()"
        >
          {{ t('runtimes.retry') }}
        </Button>
      </SettingsRow>
    </SettingsSection>

    <div
      v-else
      class="space-y-8"
    >
      <SettingsSection
        v-for="target in validTargets"
        :key="target.target_id"
        :title="targetName(target)"
      >
        <template #actions>
          <Switch
            :model-value="draftFor(target).enabled"
            :disabled="isSaving"
            :aria-label="t('bots.toolApproval.enabled')"
            @update:model-value="(value) => updateEnabled(target, !!value)"
          />
        </template>

        <template
          v-for="tool in approvalTools"
          :key="target.target_id + ':' + tool"
        >
          <SettingsRow
            stack="sm"
            :divider="modeFor(target, tool) !== 'ask'"
            :label="t('bots.toolApproval.toolNames.' + tool)"
            :description="t('bots.toolApproval.tools.' + tool)"
          >
            <SegmentedControl
              :model-value="modeFor(target, tool)"
              :items="modeItems"
              :aria-label="t('bots.toolApproval.toolNames.' + tool)"
              class="w-full sm:w-fit"
              @update:model-value="(value) => updateMode(target, tool, value)"
            />
          </SettingsRow>

          <SettingsRow
            v-if="modeFor(target, tool) === 'ask'"
            stack="always"
          >
            <template #content>
              <div class="grid gap-4 sm:grid-cols-2">
                <FieldStack
                  :label="t('bots.toolApproval.bypass')"
                  :for="ruleFieldId(target, tool, 'bypass')"
                >
                  <Textarea
                    :id="ruleFieldId(target, tool, 'bypass')"
                    :model-value="ruleText(target, tool, 'bypass')"
                    :placeholder="rulePlaceholder(tool, 'bypass')"
                    :disabled="isSaving"
                    rows="4"
                    class="font-mono text-xs"
                    spellcheck="false"
                    @update:model-value="(value) => updateRules(target, tool, 'bypass', String(value ?? ''))"
                  />
                </FieldStack>

                <FieldStack
                  :label="t('bots.toolApproval.mustReview')"
                  :for="ruleFieldId(target, tool, 'force')"
                >
                  <Textarea
                    :id="ruleFieldId(target, tool, 'force')"
                    :model-value="ruleText(target, tool, 'force')"
                    :placeholder="rulePlaceholder(tool, 'force')"
                    :disabled="isSaving"
                    rows="4"
                    class="font-mono text-xs"
                    spellcheck="false"
                    @update:model-value="(value) => updateRules(target, tool, 'force', String(value ?? ''))"
                  />
                </FieldStack>
              </div>
            </template>
          </SettingsRow>
        </template>
      </SettingsSection>
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import {
  getBotsByBotIdWorkspaceTargets,
  putBotsByBotIdWorkspaceTargetsByTargetIdToolApproval,
  type WorkspaceUpdateWorkspaceTargetToolApprovalRequest,
  type WorkspaceWorkspaceTarget,
} from '@memohai/sdk'
import {
  Button,
  SegmentedControl,
  Switch,
  Textarea,
  toast,
} from '@felinic/ui'
import { FieldStack, InlineLoadingRow, PageShell, SettingsRow, SettingsSection } from '@felinic/ui'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
  cloneToolApprovalConfig,
  defaultToolApprovalConfig,
  dirtyToolApprovalTargetIds,
  formatToolApprovalRules,
  normalizeToolApprovalConfig,
  parseToolApprovalRules,
  saveDirtyToolApprovalTargets,
  toolApprovalConfigsEqual,
  type ApprovalTool,
  type ToolApprovalConfig,
  type ToolApprovalMode,
  type WorkspaceTargetKind,
} from './tool-approval-config'

const props = defineProps<{
  botId: string
}>()

type ValidWorkspaceTarget = WorkspaceWorkspaceTarget & {
  target_id: string
  kind: string
}

type RuleKind = 'bypass' | 'force'

const approvalTools: ApprovalTool[] = ['read', 'write', 'exec']
const { t } = useI18n()

const {
  data: workspaceTargetsResponse,
  error: workspaceTargetsError,
  isLoading: workspaceTargetsLoading,
  refetch: refetchWorkspaceTargets,
} = useQuery({
  key: () => ['bot-workspace-targets', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdWorkspaceTargets({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
  refetchOnWindowFocus: true,
})

const targetItems = ref<WorkspaceWorkspaceTarget[]>([])
const drafts = ref<Record<string, ToolApprovalConfig>>({})
const savedConfigs = ref<Record<string, ToolApprovalConfig>>({})
const isSaving = ref(false)

watch(workspaceTargetsResponse, (response) => {
  if (!response) return
  const previousDrafts = drafts.value
  const previousSaved = savedConfigs.value
  const nextDrafts: Record<string, ToolApprovalConfig> = {}
  const nextSaved: Record<string, ToolApprovalConfig> = {}

  targetItems.value = response.targets ?? []
  for (const target of targetItems.value) {
    if (!target.target_id) continue
    const kind = targetKind(target)
    const serverConfig = normalizeToolApprovalConfig(
      target.tool_approval_config,
      target.tool_approval ?? {},
      kind,
    )
    const previousDraft = previousDrafts[target.target_id]
    const previousServer = previousSaved[target.target_id]
    const wasDirty = !!previousDraft
      && !!previousServer
      && !toolApprovalConfigsEqual(previousDraft, previousServer)
    nextDrafts[target.target_id] = wasDirty
      ? previousDraft
      : cloneToolApprovalConfig(serverConfig)
    nextSaved[target.target_id] = cloneToolApprovalConfig(serverConfig)
  }

  drafts.value = nextDrafts
  savedConfigs.value = nextSaved
}, { immediate: true })

const validTargets = computed<ValidWorkspaceTarget[]>(() => (
  targetItems.value.filter((target): target is ValidWorkspaceTarget => (
    typeof target.target_id === 'string'
    && target.target_id.length > 0
    && typeof target.kind === 'string'
    && target.kind.length > 0
  ))
))
const initialLoading = computed(() => workspaceTargetsLoading.value && !workspaceTargetsResponse.value)
const loadFailed = computed(() => !!workspaceTargetsError.value && !workspaceTargetsResponse.value)
const dirtyTargetIds = computed(() => dirtyToolApprovalTargetIds(drafts.value, savedConfigs.value))
const hasDirtyTargets = computed(() => dirtyTargetIds.value.length > 0)
const modeItems = computed(() => [
  {
    value: 'allow' as const,
    label: t('bots.toolApproval.modes.allow'),
    disabled: isSaving.value,
  },
  {
    value: 'ask' as const,
    label: t('bots.toolApproval.modes.ask'),
    disabled: isSaving.value,
  },
  {
    value: 'deny' as const,
    label: t('bots.toolApproval.modes.deny'),
    disabled: isSaving.value,
  },
])

function targetKind(target: WorkspaceWorkspaceTarget): WorkspaceTargetKind {
  return target.kind === 'remote' ? 'remote' : 'native'
}

function targetName(target: WorkspaceWorkspaceTarget): string {
  if (target.kind === 'native') return t('bots.remoteRuntime.nativeWorkspace')
  return target.name || t('bots.remoteRuntime.unknownComputer')
}

function draftFor(target: ValidWorkspaceTarget): ToolApprovalConfig {
  return drafts.value[target.target_id] ?? defaultToolApprovalConfig(targetKind(target))
}

function replaceDraft(target: ValidWorkspaceTarget, config: ToolApprovalConfig): void {
  drafts.value = {
    ...drafts.value,
    [target.target_id]: config,
  }
}

function updateEnabled(target: ValidWorkspaceTarget, enabled: boolean): void {
  const config = cloneToolApprovalConfig(draftFor(target))
  config.enabled = enabled
  replaceDraft(target, config)
}

function isMode(value: unknown): value is ToolApprovalMode {
  return value === 'allow' || value === 'ask' || value === 'deny'
}

function modeFor(target: ValidWorkspaceTarget, tool: ApprovalTool): ToolApprovalMode {
  return draftFor(target)[tool].mode
}

function updateMode(target: ValidWorkspaceTarget, tool: ApprovalTool, value: string | number): void {
  if (!isMode(value)) return
  const config = cloneToolApprovalConfig(draftFor(target))
  config[tool].mode = value
  config[tool].require_approval = value === 'ask'
  replaceDraft(target, config)
}

function ruleList(target: ValidWorkspaceTarget, tool: ApprovalTool, kind: RuleKind): string[] {
  const config = draftFor(target)
  if (tool === 'exec') {
    return kind === 'bypass' ? config.exec.bypass_commands : config.exec.force_review_commands
  }
  const policy = config[tool]
  return kind === 'bypass' ? policy.bypass_globs : policy.force_review_globs
}

function ruleText(target: ValidWorkspaceTarget, tool: ApprovalTool, kind: RuleKind): string {
  return formatToolApprovalRules(ruleList(target, tool, kind))
}

function updateRules(target: ValidWorkspaceTarget, tool: ApprovalTool, kind: RuleKind, value: string): void {
  const config = cloneToolApprovalConfig(draftFor(target))
  const rules = parseToolApprovalRules(value)
  if (tool === 'exec') {
    if (kind === 'bypass') config.exec.bypass_commands = rules
    else config.exec.force_review_commands = rules
  } else if (kind === 'bypass') {
    config[tool].bypass_globs = rules
  } else {
    config[tool].force_review_globs = rules
  }
  replaceDraft(target, config)
}

function rulePlaceholder(tool: ApprovalTool, kind: RuleKind): string {
  if (tool === 'exec') {
    return t(kind === 'bypass'
      ? 'bots.toolApproval.placeholders.execBypass'
      : 'bots.toolApproval.placeholders.execMustReview')
  }
  return t(kind === 'bypass'
    ? 'bots.toolApproval.placeholders.fileBypass'
    : 'bots.toolApproval.placeholders.fileMustReview')
}

function ruleFieldId(target: ValidWorkspaceTarget, tool: ApprovalTool, kind: RuleKind): string {
  return `tool-approval-${target.target_id}-${tool}-${kind}`
}

async function saveChanges(): Promise<void> {
  if (!hasDirtyTargets.value || isSaving.value) return
  isSaving.value = true
  try {
    const result = await saveDirtyToolApprovalTargets(
      drafts.value,
      savedConfigs.value,
      async (targetId, config) => {
        const body: WorkspaceUpdateWorkspaceTargetToolApprovalRequest = {
          enabled: config.enabled,
          read: config.read.mode,
          write: config.write.mode,
          exec: config.exec.mode,
          tool_approval_config: config,
        }
        await putBotsByBotIdWorkspaceTargetsByTargetIdToolApproval({
          path: {
            bot_id: props.botId,
            target_id: targetId,
          },
          body,
          throwOnError: true,
        })
      },
    )

    if (result.savedTargetIds.length > 0) {
      const nextSaved = { ...savedConfigs.value }
      for (const targetId of result.savedTargetIds) {
        const draft = drafts.value[targetId]
        if (draft) nextSaved[targetId] = cloneToolApprovalConfig(draft)
      }
      savedConfigs.value = nextSaved
      await refetchWorkspaceTargets()
    }

    if (result.failedTargets.length === 0) {
      toast.success(t('bots.toolApproval.saveSuccess'))
    } else if (result.savedTargetIds.length > 0) {
      toast.error(t('bots.toolApproval.partialSaveFailed', { count: result.failedTargets.length }))
    } else {
      toast.error(resolveApiErrorMessage(
        result.failedTargets[0]?.error,
        t('bots.toolApproval.saveFailed'),
      ))
    }
  } finally {
    isSaving.value = false
  }
}
</script>
