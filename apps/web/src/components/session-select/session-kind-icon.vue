<template>
  <!-- Same vocabulary as the sidebar's session rows (session-item.vue): plain
       model chats stay text-only, ACP chats carry the agent mark, and schedule
       runs carry a yellow clock. `reserveSpace` keeps the label column aligned
       in list contexts where only some rows have a mark. -->
  <span
    v-if="kind === 'schedule'"
    class="flex size-4 shrink-0 items-center justify-center"
    role="img"
    :aria-label="label"
  >
    <!-- Status icons stay state-constant on the accent ramp. -->
    <Clock
      class="size-4 text-[color:var(--accent-yellow)]"
      aria-hidden="true"
    />
  </span>
  <span
    v-else-if="kind === 'acp'"
    class="flex size-4 shrink-0 items-center justify-center text-muted-foreground"
    role="img"
    :aria-label="label"
  >
    <component
      :is="acpAgentIcon(agentId, true)"
      class="size-4"
      aria-hidden="true"
    />
  </span>
  <span
    v-else-if="reserveSpace"
    class="size-4 shrink-0"
    aria-hidden="true"
  />
</template>

<script setup lang="ts">
import { Clock } from 'lucide-vue-next'
import { acpAgentIcon } from '@/utils/acp'

export type SessionKind = 'chat' | 'acp' | 'schedule'

withDefaults(defineProps<{
  kind: SessionKind
  agentId?: string
  label?: string
  reserveSpace?: boolean
}>(), {
  agentId: '',
  label: '',
  reserveSpace: false,
})
</script>
