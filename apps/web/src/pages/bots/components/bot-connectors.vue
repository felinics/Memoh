<template>
  <PageShell
    variant="tab"
    :title="t('connectors.title')"
  >
    <template #actions>
      <Button @click="browseConnectors">
        <Plus />
        {{ t('connectors.browse') }}
      </Button>
    </template>

    <SettingsSection :title="t('connectors.connected')">
      <InlineLoadingRow
        v-if="isLoading"
        surface="card-row"
      >
        {{ t('common.loading') }}
      </InlineLoadingRow>

      <Empty
        v-else-if="loadError && !items.length"
        class="py-12"
      >
        <EmptyHeader>
          <EmptyTitle>{{ t('connectors.loadFailed') }}</EmptyTitle>
        </EmptyHeader>
        <EmptyContent>
          <Button
            variant="outline"
            @click="retryLoad"
          >
            <RefreshCw />
            {{ t('common.refresh') }}
          </Button>
        </EmptyContent>
      </Empty>

      <Empty
        v-else-if="!items.length"
        class="py-12"
      >
        <EmptyHeader>
          <EmptyTitle>{{ t('connectors.emptyTitle') }}</EmptyTitle>
          <EmptyDescription>{{ t('connectors.emptyDescription') }}</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button
            variant="outline"
            @click="browseConnectors"
          >
            <Plus />
            {{ t('connectors.browse') }}
          </Button>
        </EmptyContent>
      </Empty>

      <SettingsRow
        v-for="item in items"
        v-else
        :key="item.connection_id"
        :label="connectorName(item)"
        :description="connectorDescription(item)"
      >
        <template #leading>
          <ProviderIcon
            :icon="connectorIcon(item)"
            class="size-6 object-contain"
          >
            <Plug class="size-5 text-muted-foreground" />
          </ProviderIcon>
        </template>

        <div class="flex items-center gap-2">
          <Button
            v-if="needsAuthorization(item)"
            variant="outline"
            size="sm"
            :loading="pending.has(`${item.connection_id}:reauth`)"
            @click="reauthorize(item)"
          >
            {{ t('connectors.reauthorize') }}
          </Button>

          <Switch
            :model-value="item.enabled"
            :disabled="pending.has(`${item.connection_id}:enabled`)"
            :aria-label="t('connectors.enabled')"
            @update:model-value="setEnabled(item, $event)"
          />

          <DropdownMenu>
            <DropdownMenuTrigger as-child>
              <Button
                variant="ghost"
                size="icon-sm"
                :aria-label="t('common.actions')"
              >
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                v-if="isOAuth(item) && !needsAuthorization(item)"
                :disabled="pending.has(`${item.connection_id}:reauth`)"
                @select="reauthorize(item)"
              >
                <RefreshCw />
                {{ t('connectors.reauthorize') }}
              </DropdownMenuItem>
              <DropdownMenuItem
                variant="destructive"
                @select="pendingDelete = item"
              >
                <Trash2 />
                {{ t('connectors.disconnect') }}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </SettingsRow>
    </SettingsSection>
  </PageShell>

  <ConfirmDeleteDialog
    :open="!!pendingDelete"
    :title="t('connectors.disconnectTitle')"
    :description="t('connectors.disconnectDescription', { name: pendingDelete ? connectorName(pendingDelete) : '' })"
    :confirm-label="t('connectors.disconnect')"
    :cancel-label="t('common.cancel')"
    :loading="pending.has(`${pendingDelete?.connection_id}:delete`)"
    @update:open="value => { if (!value) pendingDelete = null }"
    @confirm="disconnect"
  />
</template>

<script setup lang="ts">
import { computed, onActivated, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import {
  Button,
  ConfirmDeleteDialog,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
  InlineLoadingRow,
  PageShell,
  SettingsRow,
  SettingsSection,
  Switch,
  toast,
} from '@felinic/ui'
import { MoreHorizontal, Plug, Plus, RefreshCw, Trash2 } from 'lucide-vue-next'
import {
  deleteBotsByBotIdConnectorsByConnectionId,
  getBotsByBotIdConnectors,
  getConnectorsCatalog,
  patchBotsByBotIdConnectorsByConnectionId,
  postBotsByBotIdConnectorsByConnectionIdReauth,
  type ConnectitConnector,
  type ConnectorsConnector,
} from '@memohai/sdk'
import ProviderIcon from '@/components/provider-icon/index.vue'
import {
  connectorOAuthErrorKey,
  openConnectorOAuthURL,
  prepareConnectorOAuthPopup,
  waitForConnectorOAuth,
} from '@/composables/useConnectorOAuth'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{ botId: string }>()
const { t } = useI18n()
const router = useRouter()
const queryCache = useQueryCache()

// One in-flight key per connection+action, so a mutation on one row never
// re-enables another row's controls mid-flight.
const pending = ref(new Set<string>())
const pendingDelete = ref<ConnectorsConnector | null>(null)

const connectorsQuery = useQuery({
  key: () => ['bot-connectors', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdConnectors({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data.items ?? []
  },
  enabled: () => !!props.botId,
})

const catalogQuery = useQuery({
  key: () => ['connectors-catalog'],
  query: async () => {
    const { data } = await getConnectorsCatalog({ throwOnError: true })
    return data
  },
})

const items = computed(() => connectorsQuery.data.value ?? [])
const isLoading = computed(() => connectorsQuery.isLoading.value && !items.value.length)
const loadError = computed(() => connectorsQuery.error.value)
const catalogByType = computed(() => new Map(
  (catalogQuery.data.value ?? []).map(item => [item.type, item]),
))

watch(connectorsQuery.error, error => {
  if (error) toast.error(resolveApiErrorMessage(error, t('connectors.loadFailed')))
})

onActivated(() => {
  void connectorsQuery.refetch()
})

function retryLoad() {
  void connectorsQuery.refetch()
}

function metadata(item: ConnectorsConnector): ConnectitConnector | undefined {
  return catalogByType.value.get(item.connector_type)
}

function connectorName(item: ConnectorsConnector): string {
  return metadata(item)?.name || item.connector_type || item.connection_id || t('connectors.unknown')
}

function connectorIcon(item: ConnectorsConnector): string {
  return metadata(item)?.icon_url || ''
}

function statusLabel(status?: string): string {
  switch (status) {
    case 'active': return t('connectors.status.active')
    case 'pending': return t('connectors.status.pending')
    case 'reauth_required': return t('connectors.status.reauthRequired')
    case 'authorization_failed': return t('connectors.status.authorizationFailed')
    default: return t('connectors.status.unavailable')
  }
}

function connectorDescription(item: ConnectorsConnector): string {
  if (!item.enabled) return t('connectors.status.disabled')
  return statusLabel(item.status)
}

function authMethod(item: ConnectorsConnector) {
  return metadata(item)?.auth_methods?.find(method => method.key === item.auth_method)
}

function isOAuth(item: ConnectorsConnector): boolean {
  return authMethod(item)?.type === 'oauth2'
}

function needsAuthorization(item: ConnectorsConnector): boolean {
  return isOAuth(item) && (
    item.status === 'pending'
    || item.status === 'reauth_required'
    || item.status === 'authorization_failed'
  )
}

function browseConnectors() {
  void router.push({
    name: 'supermarket',
    query: { tab: 'connectors', botId: props.botId },
  })
}

const enabledMutation = useMutation({
  mutation: async ({ item, enabled }: { item: ConnectorsConnector, enabled: boolean }) => {
    await patchBotsByBotIdConnectorsByConnectionId({
      path: { bot_id: props.botId, connection_id: item.connection_id! },
      body: { enabled },
      throwOnError: true,
    })
  },
})

async function setEnabled(item: ConnectorsConnector, enabled: boolean) {
  if (!item.connection_id || item.enabled === enabled) return
  const key = `${item.connection_id}:enabled`
  if (pending.value.has(key)) return
  pending.value.add(key)
  try {
    await enabledMutation.mutateAsync({ item, enabled })
    await queryCache.invalidateQueries({ key: ['bot-connectors', props.botId] })
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('connectors.updateFailed')))
  } finally {
    pending.value.delete(key)
  }
}

async function reauthorize(item: ConnectorsConnector) {
  if (!item.connection_id) return
  const key = `${item.connection_id}:reauth`
  if (pending.value.has(key)) return
  const popup = prepareConnectorOAuthPopup(t('common.loading'))
  pending.value.add(key)
  try {
    const { data } = await postBotsByBotIdConnectorsByConnectionIdReauth({
      path: { bot_id: props.botId, connection_id: item.connection_id },
      throwOnError: true,
    })
    if (!data.authorization_url) throw new Error('oauth_failed')
    await openConnectorOAuthURL(data.authorization_url, popup)
    await waitForConnectorOAuth(props.botId, item.connection_id, popup)
    await queryCache.invalidateQueries({ key: ['bot-connectors', props.botId] })
    toast.success(t('connectors.oauthSuccess'))
  } catch (error) {
    popup?.close()
    const oauthKey = connectorOAuthErrorKey(error)
    toast.error(oauthKey ? t(oauthKey) : resolveApiErrorMessage(error, t('connectors.oauthFailed')))
    void connectorsQuery.refetch()
  } finally {
    pending.value.delete(key)
  }
}

async function disconnect() {
  const item = pendingDelete.value
  if (!item?.connection_id) return
  const key = `${item.connection_id}:delete`
  if (pending.value.has(key)) return
  pending.value.add(key)
  try {
    await deleteBotsByBotIdConnectorsByConnectionId({
      path: { bot_id: props.botId, connection_id: item.connection_id },
      throwOnError: true,
    })
    pendingDelete.value = null
    await queryCache.invalidateQueries({ key: ['bot-connectors', props.botId] })
    toast.success(t('connectors.disconnected'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('connectors.disconnectFailed')))
  } finally {
    pending.value.delete(key)
  }
}
</script>
