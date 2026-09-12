import { computed, type Ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { useDocumentVisibility, useIntervalFn } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import {
  getBotsByBotIdAgentsByIdRuntimeControls,
  getBotsByBotIdSessionsBySessionIdRuntimeControls,
  getBotsByBotIdSessionsBySessionIdRuntimeControlsGoal,
  patchBotsByBotIdSessionsBySessionIdRuntimeControlsMode,
  postBotsByBotIdSessionsBySessionIdRuntimeControlsCommands,
  postBotsByBotIdSessionsBySessionIdRuntimeControlsGoal,
} from '@memohai/sdk'

export function useRuntimeControls(options: {
  botId: Ref<string | null | undefined>
  sessionId: Ref<string | null | undefined>
  draftAgentId?: Ref<string>
  visible: Ref<boolean>
}) {
  const { locale } = useI18n()
  const visibility = useDocumentVisibility()
  const scope = computed(() => `${options.botId.value ?? ''}:${options.sessionId.value || options.draftAgentId?.value || ''}`)
  const enabled = () => !!options.botId.value && !!(options.sessionId.value || options.draftAgentId?.value) && options.visible.value && visibility.value === 'visible'
  const query = useQuery({
    key: () => ['runtime-controls', options.botId.value ?? '', options.sessionId.value ?? '', options.draftAgentId?.value ?? '', locale.value],
    enabled,
    query: async ({ signal }) => {
      const target = scope.value
      if (!options.sessionId.value) {
        const { data } = await getBotsByBotIdAgentsByIdRuntimeControls({
          path: { bot_id: options.botId.value!, id: options.draftAgentId!.value },
          headers: { 'Accept-Language': locale.value }, signal, throwOnError: true,
        })
        return { target, controls: data }
      }
      const { data } = await getBotsByBotIdSessionsBySessionIdRuntimeControls({
        path: { bot_id: options.botId.value!, session_id: options.sessionId.value! },
        headers: { 'Accept-Language': locale.value }, signal, throwOnError: true,
      })
      return { target, controls: data }
    },
  })
  const controls = computed(() => query.data.value?.target === scope.value ? query.data.value.controls : undefined)

  // Goal state has its own failure boundary; it never gates command discovery
  // or permission/Plan controls, and drafts do not start a runtime to read it.
  const goalEnabled = () => enabled() && !!options.sessionId.value && controls.value?.capabilities?.goal === true
  const goalQuery = useQuery({
    key: () => ['runtime-goal', options.botId.value ?? '', options.sessionId.value ?? ''],
    enabled: goalEnabled,
    query: async ({ signal }) => {
      const target = scope.value
      const { data } = await getBotsByBotIdSessionsBySessionIdRuntimeControlsGoal({
        path: { bot_id: options.botId.value!, session_id: options.sessionId.value! },
        signal, throwOnError: true,
      })
      return { target, goal: data?.goal }
    },
  })
  const goal = computed(() => goalEnabled() && goalQuery.data.value?.target === scope.value ? goalQuery.data.value.goal : undefined)
  const goalError = computed(() => goalEnabled() ? goalQuery.error.value : undefined)
  async function refresh() {
    await Promise.all([query.refetch(), ...(goalEnabled() ? [goalQuery.refetch()] : [])])
  }
  useIntervalFn(() => {
    if (enabled()) void query.refetch()
    if (goalEnabled()) void goalQuery.refetch()
  }, 3000)

  async function setMode(modeId: string, modeKind: 'permission' | 'plan' = 'permission') {
    if (!enabled() || !options.sessionId.value) return
    const target = scope.value
    await patchBotsByBotIdSessionsBySessionIdRuntimeControlsMode({
      path: { bot_id: options.botId.value!, session_id: options.sessionId.value! },
      body: { mode_id: modeId, mode_kind: modeKind }, headers: { 'Accept-Language': locale.value }, throwOnError: true,
    })
    if (scope.value === target) await query.refetch()
  }

  async function execute(command: string) {
    const { data } = await postBotsByBotIdSessionsBySessionIdRuntimeControlsCommands({
      path: { bot_id: options.botId.value!, session_id: options.sessionId.value! },
      body: { command }, headers: { 'Accept-Language': locale.value }, throwOnError: true,
    })
    return data?.text ?? ''
  }

  async function controlGoal(action: 'pause' | 'clear') {
    if (!options.sessionId.value) return
    const target = scope.value
    await postBotsByBotIdSessionsBySessionIdRuntimeControlsGoal({
      path: { bot_id: options.botId.value!, session_id: options.sessionId.value },
      body: { action }, throwOnError: true,
    })
    if (scope.value === target) await goalQuery.refetch()
  }

  return { controls, goal, goalError, setMode, execute, controlGoal, refresh }
}
