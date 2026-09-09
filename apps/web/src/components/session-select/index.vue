<template>
  <SearchableSelectPopover
    v-model="selected"
    :options="options"
    :placeholder="resolvedPlaceholder"
    :aria-label="resolvedPlaceholder"
    :search-placeholder="t('chat.searchSessionPlaceholder')"
    :search-aria-label="t('chat.searchSessions')"
    :empty-text="emptyText"
    popover-class="w-[var(--reka-popover-trigger-width)]"
  >
    <!-- Custom trigger: the default one is text-only, and the selected
         session's mark (schedule clock / agent) has to survive collapse —
         otherwise the kind is visible only while the menu is open. -->
    <template #trigger="{ open }">
      <button
        data-slot="select-trigger"
        data-size="default"
        :data-placeholder="triggerLabel ? undefined : ''"
        type="button"
        :aria-expanded="open"
        :aria-label="resolvedPlaceholder"
        :class="[selectTriggerClass, 'w-full']"
      >
        <span class="flex min-w-0 items-center gap-2">
          <SessionKindIcon
            v-if="selectedMark"
            :kind="selectedMark.kind"
            :agent-id="selectedMark.agentId"
            :label="selectedMark.label"
          />
          <span class="line-clamp-1">{{ triggerLabel || resolvedPlaceholder }}</span>
        </span>
        <ChevronsUpDown class="opacity-50" />
      </button>
    </template>

    <template #option-icon="{ option }">
      <SessionKindIcon
        :kind="markOf(option).kind"
        :agent-id="markOf(option).agentId"
        :label="markOf(option).label"
        reserve-space
      />
    </template>
  </SearchableSelectPopover>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronsUpDown } from 'lucide-vue-next'
import { selectTriggerClass } from '@felinic/ui'
import { getBotsByBotIdSessions } from '@memohai/sdk'
import type { SessionSession } from '@memohai/sdk'
import SearchableSelectPopover from '@/components/searchable-select-popover/index.vue'
import type { SearchableSelectOption } from '@/components/searchable-select-popover/index.vue'
import { useWorkdirsStore } from '@/store/workdirs'
import { normalizedSessionMode } from '@/store/chat-list.utils'
import SessionKindIcon from './session-kind-icon.vue'
import {
  buildSessionOptions,
  sessionMark,
  sessionTitle,
  NO_MARK,
  type SessionMark,
  type SessionSelectLabels,
} from './options'

// A bot's sessions as a picker: the same three things the sidebar list gives
// you — folder grouping, search, and a per-kind mark — in one field-sized
// control, so every surface that has to name an existing session reads the
// same way.
const props = withDefaults(defineProps<{
  botId: string
  placeholder?: string
  // Restrict the list to these session modes (chat | discuss | schedule | …).
  // Empty means every mode the API returns.
  modes?: string[]
  limit?: number
}>(), {
  placeholder: '',
  modes: () => [],
  limit: 200,
})

const emit = defineEmits<{
  // The resolved row behind the selected id, so callers don't refetch the list
  // just to read the target's runtime.
  'update:session': [SessionSession | null]
}>()

const { t } = useI18n()
const workdirsStore = useWorkdirsStore()

const selected = defineModel<string>({ default: '' })

const sessions = ref<SessionSession[]>([])
const loading = ref(false)
const loaded = ref(false)

const labels = computed<SessionSelectLabels>(() => ({
  untitled: t('chat.untitledSession'),
  recents: t('chat.recents'),
  unavailableFolder: t('chat.folderUnavailable'),
  schedule: t('chat.sessionTypeSchedule'),
  agent: t('chat.sessionTypeACPAgent'),
}))

const resolvedPlaceholder = computed(() => props.placeholder || t('chat.selectSession'))

const emptyText = computed(() =>
  loading.value ? t('common.loading') : t('chat.noSearchResults'),
)

const listedSessions = computed(() => {
  const modes = props.modes
  if (modes.length === 0) return sessions.value
  return sessions.value.filter(session => modes.includes(normalizedSessionMode(session)))
})

const options = computed<SearchableSelectOption[]>(() =>
  buildSessionOptions(listedSessions.value, workdirsStore.workdirsFor(props.botId), labels.value),
)

function markOf(option: SearchableSelectOption): SessionMark {
  return (option.meta as SessionMark | undefined) ?? NO_MARK
}

const selectedSession = computed(() =>
  sessions.value.find(session => session.id === selected.value) ?? null,
)

const selectedMark = computed(() =>
  selectedSession.value ? sessionMark(selectedSession.value, labels.value) : null,
)

// Until the list lands, a hydrated id has no title to show; the raw uuid the
// popover would fall back to is noise, so the field reads as loading instead.
const triggerLabel = computed(() => {
  if (selectedSession.value) return sessionTitle(selectedSession.value, labels.value)
  if (selected.value && !loaded.value) return t('common.loading')
  return ''
})

async function fetchSessions(botId: string) {
  if (!botId) {
    sessions.value = []
    loaded.value = false
    return
  }
  loading.value = true
  try {
    const { data } = await getBotsByBotIdSessions({
      path: { bot_id: botId },
      query: { limit: props.limit },
      throwOnError: true,
    })
    sessions.value = data.items ?? []
  } catch {
    sessions.value = []
  } finally {
    loading.value = false
    loaded.value = true
  }
}

watch(() => props.botId, (botId) => {
  loaded.value = false
  void workdirsStore.ensureWorkdirs(botId)
  void fetchSessions(botId)
}, { immediate: true })

watch([selectedSession, loaded], () => {
  emit('update:session', selectedSession.value)
}, { immediate: true })

defineExpose({ refresh: () => fetchSessions(props.botId) })
</script>
