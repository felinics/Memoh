<script setup lang="ts">
import { computed, nextTick, onDeactivated, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import {
  Button, Dialog, DialogBody, DialogHeader, DialogPanel, DialogTitle,
  PageShell, SectionGroup, SettingsRow, SettingsSection,
} from '@felinic/ui'
import type { BotWorkspaceBackend } from '@/utils/bot-detail-tabs'
import BotToolApproval from './bot-tool-approval.vue'
import BotHooks from './bot-hooks.vue'
import BotDesktop from './bot-desktop.vue'
import BotNetwork from './bot-network.vue'
import BotCompaction from './bot-compaction.vue'

const props = defineProps<{
  botId: string
  workspaceBackend: BotWorkspaceBackend
}>()
const route = useRoute()
const root = ref<HTMLElement>()
const hooksOpen = ref(false)
onDeactivated(() => { hooksOpen.value = false })
const hasContainer = computed(() => props.workspaceBackend === 'container')

watch(() => [route.query.tab, route.query.section, props.botId, hasContainer.value], async () => {
  if (route.query.tab !== 'advanced') return
  const section = route.query.section
  if (typeof section !== 'string') return
  await nextTick()
  const target = Array.from(root.value?.querySelectorAll<HTMLElement>('[data-advanced-section]') ?? [])
    .find(element => element.dataset.advancedSection === section)
  target?.scrollIntoView({ block: 'start' })
  if (section === 'hooks' && hasContainer.value) hooksOpen.value = true
}, { immediate: true })
</script>

<template>
  <PageShell
    variant="tab"
    :title="$t('bots.tabs.advanced')"
  >
    <div
      ref="root"
      class="space-y-8"
    >
      <BotToolApproval
        data-advanced-section="tool-approval"
        :bot-id="botId"
      />
      <SectionGroup
        v-if="hasContainer"
        data-advanced-section="hooks"
        :title="$t('bots.tabs.hooks')"
      >
        <SettingsSection>
          <SettingsRow
            :label="$t('bots.hooks.title')"
            :description="$t('bots.hooks.subtitle')"
          >
            <Button
              variant="outline"
              size="sm"
              @click="hooksOpen = true"
            >
              {{ $t('common.edit') }}
            </Button>
          </SettingsRow>
        </SettingsSection>
      </SectionGroup>
      <BotDesktop
        v-if="hasContainer"
        data-advanced-section="desktop"
        :bot-id="botId"
      />
      <BotNetwork
        v-if="hasContainer"
        data-advanced-section="network"
        :bot-id="botId"
      />
      <div data-advanced-section="compaction">
        <BotCompaction :bot-id="botId" />
      </div>
    </div>
    <Dialog
      v-if="hasContainer"
      v-model:open="hooksOpen"
    >
      <DialogPanel width="3xl">
        <DialogHeader>
          <DialogTitle>{{ $t('bots.hooks.title') }}</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <BotHooks :bot-id="botId" />
        </DialogBody>
      </DialogPanel>
    </Dialog>
  </PageShell>
</template>
