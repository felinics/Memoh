<template>
  <PageShell
    variant="tab"
    :title="$t('bots.plugins.title')"
    :description="$t('bots.plugins.intro')"
  >
    <template #actions>
      <Button
        variant="outline"
        size="sm"
        class="shrink-0"
        @click="router.push({ name: 'supermarket' })"
      >
        <Store class="size-4" />
        {{ $t('sidebar.supermarket') }}
      </Button>
    </template>

    <SettingsSection :title="$t('bots.plugins.installedTitle')">
      <!-- Loading borrows the row height so the list keeps its space and
           doesn't jump (CLS) when plugins arrive — same card-row family as the
           SettingsRow list it stands in for. -->
      <InlineLoadingRow
        v-if="loading && !plugins.length"
        size="md"
        surface="card-row"
      >
        {{ $t('common.loading') }}
      </InlineLoadingRow>

      <Empty
        v-else-if="!plugins.length"
        class="py-12"
      >
        <EmptyHeader>
          <EmptyTitle>{{ $t('bots.plugins.emptyTitle') }}</EmptyTitle>
          <EmptyDescription>{{ $t('bots.plugins.emptyDescription') }}</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button
            variant="outline"
            size="sm"
            @click="router.push({ name: 'supermarket' })"
          >
            <Store class="size-4" />
            {{ $t('sidebar.supermarket') }}
          </Button>
        </EmptyContent>
      </Empty>

      <!-- Dense object-list row: a plugin's icon, name+link+description, and its
           status/toggle controls. align="start" top-pins the icon and controls to
           the title line since the description can wrap to two lines. -->
      <SettingsRow
        v-for="plugin in plugins"
        :key="plugin.id"
        align="start"
      >
        <template #leading>
          <div class="flex size-9 shrink-0 items-center justify-center overflow-hidden rounded-md bg-accent">
            <ProviderIcon
              v-if="pluginIcon(plugin)"
              :icon="pluginIcon(plugin)"
              size="20"
              class="size-5 object-contain"
            >
              <PackageOpen class="size-4 text-muted-foreground" />
            </ProviderIcon>
            <PackageOpen
              v-else
              class="size-4 text-muted-foreground"
            />
          </div>
        </template>

        <template #content>
          <div class="flex items-center gap-1.5">
            <h3
              class="truncate text-sm font-medium text-foreground"
              :title="pluginManifest(plugin).name"
            >
              {{ pluginManifest(plugin).name }}
            </h3>
            <a
              v-if="pluginManifest(plugin).homepage"
              :href="pluginManifest(plugin).homepage"
              target="_blank"
              rel="noopener noreferrer"
              class="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
            >
              <ExternalLink class="size-3" />
            </a>
          </div>
          <p class="mt-1 line-clamp-2 text-xs text-muted-foreground">
            {{ pluginManifest(plugin).description }}
          </p>
        </template>

        <div class="flex items-center gap-2">
          <Badge
            variant="outline"
            size="sm"
          >
            {{ statusLabel(plugin.status) }}
          </Badge>
          <Button
            v-if="plugin.status === 'needs_auth'"
            variant="outline"
            size="sm"
            :disabled="isPending(plugin, 'oauth')"
            @click="startOAuth(plugin)"
          >
            <ExternalLink class="size-4" />
            {{ $t('bots.plugins.authorizeAction') }}
          </Button>
          <div
            v-else
            class="flex items-center gap-2"
          >
            <span class="text-xs text-muted-foreground">
              {{ plugin.enabled ? $t('common.enabled') : $t('common.disabled') }}
            </span>
            <Switch
              :model-value="!!plugin.enabled"
              :disabled="!canTogglePlugin(plugin)"
              @update:model-value="(enabled) => togglePlugin(plugin, !!enabled)"
            />
          </div>
        </div>
      </SettingsRow>
    </SettingsSection>
  </PageShell>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ExternalLink, PackageOpen, Store } from 'lucide-vue-next'
import { Badge, Button, Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle, InlineLoadingRow, PageShell, SettingsRow, SettingsSection, Switch, toast } from '@felinic/ui'
import {
  getBotsByBotIdPlugins,
  getBotsByBotIdPluginsByIdOauthStatus,
  postBotsByBotIdPluginsByIdDisable,
  postBotsByBotIdPluginsByIdEnable,
  postBotsByBotIdPluginsByIdOauthAuthorize,
  type PluginsInstallation,
  type PluginsManifest,
} from '@memohai/sdk'
import { client } from '@memohai/sdk/client'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { BOT_PLUGINS_UPDATED_EVENT, isBotPluginsUpdatedEvent } from '@/utils/bot-plugin-events'
import ProviderIcon from '@/components/provider-icon/index.vue'

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const router = useRouter()
const plugins = ref<PluginsInstallation[]>([])
const loading = ref(false)
const pendingKey = ref('')

onMounted(() => {
  void loadPlugins()
  window.addEventListener(BOT_PLUGINS_UPDATED_EVENT, handleBotPluginsUpdated)
})

onUnmounted(() => {
  window.removeEventListener(BOT_PLUGINS_UPDATED_EVENT, handleBotPluginsUpdated)
})

watch(() => props.botId, () => {
  void loadPlugins()
})

function handleBotPluginsUpdated(event: Event) {
  if (!isBotPluginsUpdatedEvent(event)) return
  if (event.detail.botId !== props.botId) return
  void loadPlugins()
}

async function loadPlugins() {
  if (!props.botId) return
  loading.value = true
  try {
    const { data } = await getBotsByBotIdPlugins({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    plugins.value = data.items ?? []
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.plugins.loadFailed')))
  } finally {
    loading.value = false
  }
}

function pluginManifest(plugin: PluginsInstallation): PluginsManifest {
  return {
    ...(plugin.manifest ?? {}),
    id: plugin.plugin_id || plugin.manifest?.id || plugin.id,
    name: plugin.plugin_name || plugin.manifest?.name || plugin.plugin_id || plugin.id,
    description: plugin.manifest?.description || '',
  }
}

function pluginIcon(plugin: PluginsInstallation): string {
  const icon = pluginManifest(plugin).icon
  if (!icon) return ''
  if (icon.kind === 'external_url') return icon.url || ''
  if (icon.kind === 'builtin') return icon.name || ''
  return ''
}

function statusLabel(status?: string): string {
  switch (status) {
    case 'ready': return t('bots.plugins.status.ready')
    case 'needs_config': return t('bots.plugins.status.needsConfig')
    case 'needs_auth': return t('bots.plugins.status.needsAuth')
    case 'admin_required': return t('bots.plugins.status.adminRequired')
    case 'disabled': return t('bots.plugins.status.disabled')
    case 'uninstalled': return t('bots.plugins.status.uninstalled')
    default: return status || t('mcp.statusUnknown')
  }
}

function isPending(plugin: PluginsInstallation, action: string): boolean {
  return pendingKey.value === `${plugin.id}:${action}`
}

function canTogglePlugin(plugin: PluginsInstallation): boolean {
  if (isPending(plugin, 'enable') || isPending(plugin, 'disable')) return false
  return plugin.status === 'ready' || plugin.status === 'disabled'
}

function togglePlugin(plugin: PluginsInstallation, enabled: boolean) {
  if (enabled === !!plugin.enabled) return
  if (enabled) {
    void enablePlugin(plugin)
  } else {
    void disablePlugin(plugin)
  }
}

function mcpOAuthCallbackUrl() {
  const rawBase = String(client.getConfig().baseUrl || '/api')
  const base = new URL(rawBase, window.location.origin)
  const basePath = base.pathname.replace(/\/+$/, '')
  base.pathname = `${basePath}/oauth/mcp/callback`
  base.search = ''
  base.hash = ''
  return base.toString()
}

async function withPending(plugin: PluginsInstallation, action: string, task: () => Promise<void>) {
  if (!plugin.id) return
  pendingKey.value = `${plugin.id}:${action}`
  try {
    await task()
  } finally {
    pendingKey.value = ''
  }
}

async function enablePlugin(plugin: PluginsInstallation) {
  await withPending(plugin, 'enable', async () => {
    await postBotsByBotIdPluginsByIdEnable({
      path: { bot_id: props.botId, id: plugin.id! },
      throwOnError: true,
    })
    toast.success(t('bots.plugins.enableSuccess'))
    await loadPlugins()
  })
}

async function disablePlugin(plugin: PluginsInstallation) {
  await withPending(plugin, 'disable', async () => {
    await postBotsByBotIdPluginsByIdDisable({
      path: { bot_id: props.botId, id: plugin.id! },
      throwOnError: true,
    })
    toast.success(t('bots.plugins.disableSuccess'))
    await loadPlugins()
  })
}

async function startOAuth(plugin: PluginsInstallation) {
  await withPending(plugin, 'oauth', async () => {
    try {
      const { data } = await postBotsByBotIdPluginsByIdOauthAuthorize({
        path: { bot_id: props.botId, id: plugin.id! },
        body: { callback_url: mcpOAuthCallbackUrl() },
        throwOnError: true,
      })
      if (!data.authorization_url) throw new Error(t('mcp.oauth.flowInitFailed'))

      const popup = window.open(data.authorization_url, 'mcp-oauth', 'width=600,height=700')
      await waitForMCPOAuth(plugin, popup)
      const synced = await syncOAuthStatus(plugin)
      if (synced.status !== 'ready' && !synced.enabled) throw new Error(t('mcp.oauth.authFailed'))
      toast.success(t('mcp.oauth.authSuccess'))
      await loadPlugins()
    } catch (error) {
      const synced = await syncOAuthStatus(plugin).catch(() => null)
      if (synced?.status === 'ready' || synced?.enabled) {
        toast.success(t('mcp.oauth.authSuccess'))
        await loadPlugins()
        return
      }
      toast.error(resolveApiErrorMessage(error, t('mcp.oauth.flowInitFailed')))
    }
  })
}

function waitForMCPOAuth(plugin: PluginsInstallation, popup: Window | null): Promise<void> {
  return new Promise((resolve, reject) => {
    let completed = false
    const startedAt = Date.now()
    let pollTimer: ReturnType<typeof setInterval> | undefined

    const finish = (status: 'success' | 'error', error?: string) => {
      if (completed) return
      completed = true
      if (pollTimer) clearInterval(pollTimer)
      window.removeEventListener('message', onMessage)
      if (status === 'success') {
        resolve()
      } else {
        reject(new Error(error || t('mcp.oauth.authFailed')))
      }
    }

    const onMessage = (event: MessageEvent) => {
      if (event.data?.type !== 'mcp-oauth-callback') return
      finish(event.data.status === 'success' ? 'success' : 'error', event.data.error)
    }

    window.addEventListener('message', onMessage)
    pollTimer = setInterval(() => {
      getBotsByBotIdPluginsByIdOauthStatus({
        path: { bot_id: props.botId, id: plugin.id! },
        throwOnError: true,
      }).then(({ data }) => {
        if (completed) return
        if (data.status === 'ready' || data.enabled) {
          finish('success')
          return
        }
        if (popup?.closed || Date.now() - startedAt > 120_000) {
          finish('error')
        }
      }).catch(() => {
        if (!completed && (popup?.closed || Date.now() - startedAt > 120_000)) {
          finish('error')
        }
      })
    }, 2000)
  })
}

async function syncOAuthStatus(plugin: PluginsInstallation): Promise<PluginsInstallation> {
  const { data } = await getBotsByBotIdPluginsByIdOauthStatus({
    path: { bot_id: props.botId, id: plugin.id! },
    throwOnError: true,
  })
  plugins.value = plugins.value.map(item => item.id === data.id ? data : item)
  return data
}
</script>
