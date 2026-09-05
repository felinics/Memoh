<template>
  <PageShell
    variant="tab"
    :title="t('bots.toolApproval.title')"
    :description="t('bots.toolApproval.intro')"
  >
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
                  <!-- Free-typing draft committed on blur (no Enter commit —
                       the rules list is multi-line). A per-keystroke model
                       would autosave every character and normalize away
                       trailing separators mid-word. -->
                  <Textarea
                    :id="ruleFieldId(target, tool, 'bypass')"
                    :model-value="ruleDraftFor(target, tool, 'bypass')"
                    :placeholder="rulePlaceholder(tool, 'bypass')"
                    rows="4"
                    class="font-mono text-xs"
                    spellcheck="false"
                    @update:model-value="(value) => updateRuleDraft(target, tool, 'bypass', String(value ?? ''))"
                    @focus="focusedRuleField = ruleFieldId(target, tool, 'bypass')"
                    @change="commitRuleDraft(target, tool, 'bypass')"
                    @blur="focusedRuleField = null; commitRuleDraft(target, tool, 'bypass')"
                  />
                </FieldStack>

                <FieldStack
                  :label="t('bots.toolApproval.mustReview')"
                  :for="ruleFieldId(target, tool, 'force')"
                >
                  <Textarea
                    :id="ruleFieldId(target, tool, 'force')"
                    :model-value="ruleDraftFor(target, tool, 'force')"
                    :placeholder="rulePlaceholder(tool, 'force')"
                    rows="4"
                    class="font-mono text-xs"
                    spellcheck="false"
                    @update:model-value="(value) => updateRuleDraft(target, tool, 'force', String(value ?? ''))"
                    @focus="focusedRuleField = ruleFieldId(target, tool, 'force')"
                    @change="commitRuleDraft(target, tool, 'force')"
                    @blur="focusedRuleField = null; commitRuleDraft(target, tool, 'force')"
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
import { computed, reactive, ref, watch } from 'vue'
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
  formatToolApprovalRules,
  normalizeToolApprovalConfig,
  parseToolApprovalRules,
  toolApprovalConfigsEqual,
  type ApprovalTool,
  type ToolApprovalConfig,
  type ToolApprovalMode,
  type WorkspaceTargetKind,
} from './tool-approval-config'
import { useAutosaveQueue, type AutosaveJob } from '@/composables/use-autosave-queue'

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

// ---- Autosaved per-target configs ----
// This page has no Save button by design (web skill §8). The unit of
// persistence is one workspace target's whole config (the PUT endpoint is a
// whole-block replace), so the autosave queue's flat record is keyed by
// target id: `form[targetId]` is the edited config, `synced[targetId]` the
// last-known-server one. The diff is OBJECT IDENTITY — every edit clones the
// config (replaceDraft), so an edited target is `!==` its synced snapshot.
// That only holds if hydration assigns the SAME object to both maps for an
// untouched target (never two clones), and if job payloads carry the live
// `form` object so a successful save makes both maps reference it again.
const form = reactive<Record<string, ToolApprovalConfig>>({})
const synced = reactive<Record<string, ToolApprovalConfig>>({})

const targetItems = ref<WorkspaceWorkspaceTarget[]>([])

// Rule textareas are free-typing drafts (see template comment), keyed
// `${targetId}:${tool}:${kind}`; they commit into `form` on blur.
const ruleDrafts = reactive<Record<string, string>>({})
const focusedRuleField = ref<string | null>(null)

watch(workspaceTargetsResponse, (response) => {
  if (!response) return
  targetItems.value = response.targets ?? []
  const seen = new Set<string>()

  for (const target of targetItems.value) {
    if (!target.target_id) continue
    seen.add(target.target_id)
    const serverConfig = normalizeToolApprovalConfig(
      target.tool_approval_config,
      target.tool_approval ?? {},
      targetKind(target),
    )
    // Per-target guard (same contract as bot-settings.vue hydration): a
    // refetch landing while the user has unsaved edits on this target must
    // not clobber them — advance only `synced` so the queue re-pushes the
    // draft. Untouched targets adopt the server object under ONE identity.
    if (form[target.target_id] === synced[target.target_id]) {
      form[target.target_id] = serverConfig
      refreshRuleDrafts(target.target_id, serverConfig)
    }
    synced[target.target_id] = serverConfig
  }

  // Targets that vanished from the response leave no drafts behind.
  for (const targetId of Object.keys(form)) {
    if (!seen.has(targetId)) {
      delete form[targetId]
      delete synced[targetId]
      for (const key of Object.keys(ruleDrafts)) {
        if (key.startsWith(`${targetId}:`)) delete ruleDrafts[key]
      }
    }
  }
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
const modeItems = computed(() => [
  {
    value: 'allow' as const,
    label: t('bots.toolApproval.modes.allow'),
  },
  {
    value: 'ask' as const,
    label: t('bots.toolApproval.modes.ask'),
  },
  {
    value: 'deny' as const,
    label: t('bots.toolApproval.modes.deny'),
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
  return form[target.target_id] ?? defaultToolApprovalConfig(targetKind(target))
}

function updateEnabled(target: ValidWorkspaceTarget, enabled: boolean): void {
  const config = cloneToolApprovalConfig(draftFor(target))
  config.enabled = enabled
  form[target.target_id] = config
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
  form[target.target_id] = config
}

function ruleFieldId(target: ValidWorkspaceTarget, tool: ApprovalTool, kind: RuleKind): string {
  return `tool-approval-${target.target_id}-${tool}-${kind}`
}

function ruleDraftKey(target: ValidWorkspaceTarget, tool: ApprovalTool, kind: RuleKind): string {
  return `${target.target_id}:${tool}:${kind}`
}

function ruleDraftFor(target: ValidWorkspaceTarget, tool: ApprovalTool, kind: RuleKind): string {
  return ruleDrafts[ruleDraftKey(target, tool, kind)] ?? ''
}

function updateRuleDraft(target: ValidWorkspaceTarget, tool: ApprovalTool, kind: RuleKind, value: string): void {
  ruleDrafts[ruleDraftKey(target, tool, kind)] = value
}

function commitRuleDraft(target: ValidWorkspaceTarget, tool: ApprovalTool, kind: RuleKind): void {
  const config = cloneToolApprovalConfig(draftFor(target))
  const rules = parseToolApprovalRules(ruleDrafts[ruleDraftKey(target, tool, kind)] ?? '')
  if (tool === 'exec') {
    if (kind === 'bypass') config.exec.bypass_commands = rules
    else config.exec.force_review_commands = rules
  } else if (kind === 'bypass') {
    config[tool].bypass_globs = rules
  } else {
    config[tool].force_review_globs = rules
  }
  // change-then-blur fires commit twice for one edit; a content-equal commit
  // must not replace the config object, or the identity diff would read it as
  // a fresh change and autosave again.
  if (!toolApprovalConfigsEqual(config, draftFor(target))) {
    form[target.target_id] = config
  }
  // Re-derive the draft from the committed config so separators normalize the
  // same way they will render after the server round-trip.
  ruleDrafts[ruleDraftKey(target, tool, kind)] = formatToolApprovalRules(rules)
}

// Re-derive a target's rule drafts after hydration replaced its config; the
// focused field keeps what the user is typing.
function refreshRuleDrafts(targetId: string, config: ToolApprovalConfig): void {
  for (const tool of approvalTools) {
    for (const kind of ['bypass', 'force'] as const) {
      const key = `${targetId}:${tool}:${kind}`
      // focusedRuleField holds the element id format (ruleFieldId), not the
      // draft key format — keep the two namespace shapes straight here.
      if (focusedRuleField.value === `tool-approval-${targetId}-${tool}-${kind}`) continue
      const rules = tool === 'exec'
        ? (kind === 'bypass' ? config.exec.bypass_commands : config.exec.force_review_commands)
        : (kind === 'bypass' ? config[tool].bypass_globs : config[tool].force_review_globs)
      ruleDrafts[key] = formatToolApprovalRules(rules)
    }
  }
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

function buildJobs(changed: string[]): AutosaveJob<Record<string, ToolApprovalConfig>>[] {
  const jobs: AutosaveJob<Record<string, ToolApprovalConfig>>[] = []
  for (const targetId of changed) {
    // Capture the live object: the save ships exactly what was diffed, and a
    // successful save makes `synced` reference this same object (identity
    // clean). A target removed between diff and save is skipped.
    const config = form[targetId]
    if (!config) continue
    jobs.push({
      payload: { [targetId]: config },
      // rollback: false — a failed save must not snap typed rules back; the
      // error toast carries the failure and the next edit retries.
      rollback: false,
      save: async () => {
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
      onError: (error) => toast.error(resolveApiErrorMessage(error, t('bots.toolApproval.saveFailed'))),
    })
  }
  return jobs
}

useAutosaveQueue<Record<string, ToolApprovalConfig>>({
  form,
  synced,
  buildJobs,
  // Refetch on drain so server-side normalization flows back into the drafts
  // (the hydration guard keeps any target the user has since re-dirtied).
  onDrained: () => refetchWorkspaceTargets(),
})
</script>
