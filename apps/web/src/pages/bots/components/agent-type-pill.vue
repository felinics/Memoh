<script setup lang="ts">
// Agent-kind chooser for bot creation: built-in Memoh, direct external
// runtimes, and generic ACP profiles.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { SegmentedControl } from '@felinic/ui'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import { acpAgentIcon } from '@/utils/acp'
import { MEMOH_AGENT_VALUE, agentTypeItems } from './agent-type'

const props = defineProps<{
  profiles: AcpprofilePublicProfile[]
}>()

const modelValue = defineModel<string>({ default: MEMOH_AGENT_VALUE })

const { t } = useI18n()

const items = computed(() => agentTypeItems(props.profiles))
</script>

<template>
  <SegmentedControl
    v-if="items.length > 1"
    v-model="modelValue"
    :items="items"
    :aria-label="t('bots.agentCreate.typeAriaLabel')"
    class="w-full sm:w-fit"
  >
    <template #item="{ item }">
      <span class="inline-flex min-w-0 items-center justify-center gap-1.5">
        <img
          v-if="item.value === MEMOH_AGENT_VALUE"
          src="/logo.svg"
          alt=""
          class="size-4 shrink-0"
        >
        <component
          :is="acpAgentIcon(item.value, true)"
          v-else
          class="size-4 shrink-0"
        />
        <span class="truncate">{{ item.label }}</span>
      </span>
    </template>
  </SegmentedControl>
</template>
