<script setup lang="ts">
import { SettingsIcon, ConnectorIcon } from '@memohai/icon/ui'

import { computed, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { DropdownMenuItem, DropdownMenuSub, DropdownMenuSubContent, DropdownMenuSubTrigger, Spinner } from '@felinic/ui'
import { getBotsByBotIdConnectors, getConnectorsCatalog } from '@memohai/sdk'
import ProviderIcon from '@/components/provider-icon/index.vue'
import { useCapabilitiesStore } from '@/store/capabilities'

const props = defineProps<{ botId: string, botName: string }>()
// Connectors only exist when the server has ConnectIt configured; everywhere
// else consuming them gates on this capability (useConnectorLogos, bot-apps),
// and the endpoints answer ErrNotConfigured without it.
const capabilities = useCapabilitiesStore()
void capabilities.load()
const { t } = useI18n()
const router = useRouter()
const open = ref(false)
const catalog = useQuery({
  key: ['connectors-catalog'],
  query: async () => (await getConnectorsCatalog({ throwOnError: true })).data,
  enabled: () => open.value,
})
const connections = useQuery({
  key: () => ['bot-connectors', props.botId],
  query: async () => (await getBotsByBotIdConnectors({ path: { bot_id: props.botId }, throwOnError: true })).data.items ?? [],
  enabled: () => open.value && !!props.botId,
})
const rows = computed(() => (catalog.data.value ?? []).filter(item => item.type).map(item => ({
  ...item,
  connected: (connections.data.value ?? []).some(connection => connection.connector_type === item.type && connection.enabled && connection.status === 'active'),
})))
const loading = computed(() => catalog.isLoading.value || connections.isLoading.value)
const failed = computed(() => catalog.error.value || connections.error.value)
function goToSettings() {
  void router.push({ name: 'bot-detail', params: { botName: props.botName || props.botId }, query: { tab: 'apps' } })
}
</script>

<template>
  <DropdownMenuSub
    v-if="capabilities.connectors"
    v-model:open="open"
  >
    <DropdownMenuSubTrigger :disabled="!botId">
      <ConnectorIcon />
      <span>{{ t('connectors.title') }}</span>
    </DropdownMenuSubTrigger>
    <DropdownMenuSubContent class="w-72">
      <DropdownMenuItem
        v-if="loading"
        disabled
      >
        <Spinner />
        <span>{{ t('common.loading') }}</span>
      </DropdownMenuItem>
      <DropdownMenuItem
        v-else-if="failed"
        @select.prevent="catalog.refetch(); connections.refetch()"
      >
        <span>{{ t('connectors.loadFailed') }}</span>
        <span>{{ t('common.retry') }}</span>
      </DropdownMenuItem>
      <template v-else>
        <DropdownMenuItem
          v-for="item in rows"
          :key="item.type"
          @select="goToSettings"
        >
          <ProviderIcon
            :icon="item.icon_url || ''"
            class="size-4 object-contain"
          >
            <ConnectorIcon />
          </ProviderIcon>
          <span class="min-w-0 truncate">{{ item.name || item.type }}</span>
          <span
            data-menu-item-hint
            class="ml-2 text-muted-foreground"
          >{{ item.connected ? t('connectors.status.active') : t('connectors.connect') }}</span>
        </DropdownMenuItem>
      </template>
      <DropdownMenuItem @select="goToSettings">
        <SettingsIcon />
        <span>{{ t('chat.manageConnectors') }}</span>
      </DropdownMenuItem>
    </DropdownMenuSubContent>
  </DropdownMenuSub>
</template>
