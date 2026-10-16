<template>
  <div class="flex h-full min-h-0 min-w-0 flex-col">
    <div class="shrink-0 px-2 py-2">
      <Input
        v-model="search"
        :placeholder="t('supermarket.searchPlaceholder')"
        :aria-label="t('supermarket.searchPlaceholder')"
      />
    </div>
    <ScrollArea
      ref="scrollAreaRef"
      class="sidebar-scroll min-h-0 flex-1"
    >
      <div class="px-2 pb-6">
        <p
          v-if="!botId"
          class="text-caption text-muted-foreground"
        >
          {{ t('chat.noBotSelected') }}
        </p>
        <p
          v-else-if="!canManage"
          class="text-caption text-muted-foreground"
        >
          {{ t('supermarket.sidebar.manageRequired') }}
        </p>
        <template v-if="canManage && botId">
          <SidebarPanelHeader
            :label="t('supermarket.sidebar.installed')"
            class="mt-2 h-8"
            label-class="pl-3"
          />
          <div class="space-y-1.5 pb-1">
            <InlineLoadingRow v-if="installedQuery.isLoading.value">
              {{ t('common.loading') }}
            </InlineLoadingRow>
            <div
              v-else-if="installedQuery.error.value"
              class="space-y-2"
            >
              <p class="text-caption text-muted-foreground">
                {{ t('apps.loadFailed') }}
              </p>
              <Button
                variant="outline"
                size="sm"
                @click="installedQuery.refetch()"
              >
                {{ t('common.retry') }}
              </Button>
            </div>
            <template v-else>
              <p
                v-if="installedQuery.data.value?.workspace_state !== 'running'"
                class="text-caption text-muted-foreground"
              >
                {{ t('apps.workspaceNotRunningDescription') }}
              </p>
              <p
                v-if="!installed.length"
                class="text-caption text-muted-foreground"
              >
                {{ t(query.trim() ? 'supermarket.noAppResults' : 'apps.emptyTitle') }}
              </p>
              <div
                v-for="app in installed"
                :key="`${app.registry_id}/${app.app_id}`"
                :class="appRowClass"
                role="button"
                tabindex="0"
                @click="openInstalled(app)"
                @keydown.enter.exact.prevent="openInstalled(app)"
                @keydown.space.prevent="openInstalled(app)"
              >
                <div :class="appIconFrameClass">
                  <SkillIcon
                    v-if="app.icon"
                    :icon="app.icon"
                  />
                  <img
                    v-else-if="appDependencyIconUrl(app)"
                    :src="appDependencyIconUrl(app)"
                    alt=""
                    class="size-5 object-contain"
                  >
                  <SkillIcon v-else />
                </div>
                <div class="min-w-0 flex-1">
                  <p
                    class="truncate text-control font-normal text-foreground"
                    :title="appDisplayName(app, locale)"
                  >
                    {{ appDisplayName(app, locale) }}
                  </p>
                  <p class="mt-0.5 truncate text-caption text-muted-foreground">
                    {{ appDisplayDescription(app, locale) }}
                  </p>
                  <div class="mt-1 flex h-5.5 min-w-0 items-center justify-between gap-1.5">
                    <span class="min-w-0 flex-1 truncate text-caption text-muted-foreground">{{ app.author?.name || app.registry_id }}</span>
                    <p
                      v-if="app.status === 'failed' || app.status === 'partial'"
                      class="flex shrink-0 items-center gap-1 text-caption"
                      :class="app.status === 'failed' ? 'text-destructive' : 'text-warning-foreground'"
                    >
                      <AlertTriangle
                        class="size-3 shrink-0"
                        aria-hidden="true"
                      />
                      {{ t(app.status === 'failed' ? 'apps.diagnostics.failed' : 'apps.diagnostics.partial') }}
                    </p>
                    <p
                      v-else-if="app.status && app.status !== 'installed' && app.status !== 'discovered'"
                      class="shrink-0 text-caption text-muted-foreground"
                    >
                      {{ t(`bots.dependencies.status.${app.status}`) }}
                    </p>
                  </div>
                </div>
              </div>
            </template>
          </div>
        </template>
        <SidebarPanelHeader
          :label="t('supermarket.title')"
          class="mt-2 h-8"
          label-class="pl-3"
        />
        <p
          v-if="!catalog.length && !feed.loading.value && !feed.error.value && !feed.hasMore.value"
          class="text-caption text-muted-foreground"
        >
          {{ t('supermarket.noAppResults') }}
        </p>
        <div
          v-if="catalog.length"
          class="space-y-1.5 pb-1"
        >
          <div
            v-for="app in catalog"
            :key="`${app.registry_id}/${app.app_id}`"
            :class="appRowClass"
            role="button"
            tabindex="0"
            @click="openCatalog(app)"
            @keydown.enter.exact.prevent="openCatalog(app)"
            @keydown.space.prevent="openCatalog(app)"
          >
            <div :class="appIconFrameClass">
              <SkillIcon :icon="app.icon" />
            </div>
            <div class="min-w-0 flex-1">
              <p
                class="truncate text-control font-normal text-foreground"
                :title="appDisplayName(app, locale)"
              >
                {{ appDisplayName(app, locale) }}
              </p>
              <p class="mt-0.5 truncate text-caption text-muted-foreground">
                {{ appDisplayDescription(app, locale) }}
              </p>
              <div class="mt-1 flex h-5.5 min-w-0 items-center justify-between gap-1.5">
                <span class="min-w-0 flex-1 truncate text-caption text-muted-foreground">{{ app.author?.name || app.registry_id }}</span>
                <Button
                  size="text"
                  variant="outline"
                  class="h-5.5 py-0 text-caption font-normal"
                  :disabled="!canInstall || !!pendingApp"
                  :loading="pendingApp === appKey(app)"
                  @click.stop="prepareInstall(app)"
                  @keydown.enter.exact.stop
                  @keydown.space.stop
                >
                  {{ t('supermarket.install') }}
                </Button>
              </div>
            </div>
          </div>
        </div>
        <div
          v-if="showSentinel && !feed.loading.value"
          :key="feed.page.value"
          :ref="el => (loadMoreSentinel = el as HTMLElement | null)"
          aria-hidden="true"
          class="h-px"
        />
        <InlineLoadingRow
          v-if="feed.loading.value"
          role="status"
        >
          {{ t('common.loading') }}
        </InlineLoadingRow>
        <div
          v-else-if="feed.error.value"
          class="space-y-2"
          role="status"
        >
          <p class="text-caption text-muted-foreground">
            {{ t('supermarket.loadError') }}
          </p>
          <Button
            variant="outline"
            size="sm"
            @click="feed.retry()"
          >
            {{ t('common.retry') }}
          </Button>
        </div>
      </div>
    </ScrollArea>
    <InstallAppDialog
      v-model:open="installOpen"
      :pkg="selectedApp"
      :default-bot-id="installBotId"
      lock-bot
    />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { refDebounced } from '@vueuse/core'
import { useQuery } from '@pinia/colada'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { AlertTriangle } from 'lucide-vue-next'
import { Button, InlineLoadingRow, Input, ScrollArea, toast } from '@felinic/ui'
import { getBotsByBotIdApps, getSupermarketRegistriesByRegistryIdAppsByAppId, type HandlersAppItem, type HandlersSupermarketAppDescriptor, type HandlersSupermarketAppSummary } from '@memohai/sdk'
import { appDisplayDescription, appDisplayName, appKey, botAppsQueryKey } from '@/composables/api/useApps'
import { useWorkspaceDependencyText } from '@/composables/useWorkspaceDependencyText'
import { resolveApiErrorMessage } from '@/utils/api-error'
import SkillIcon from '@/pages/supermarket/components/skill-icon.vue'
import InstallAppDialog from '@/pages/supermarket/components/install-app-dialog.vue'
import SidebarPanelHeader from './panel-header.vue'
import { useSupermarketFeed } from './use-supermarket-feed'
import { useSidebarInfiniteScroll } from './use-sidebar-infinite-scroll'
import { filterInstalledApps, uninstalledApps } from './supermarket-apps'

/** The frame spans both text lines and their gap; the logo keeps the market card glyph size. */
const appIconFrameClass = 'flex size-9.5 shrink-0 items-center justify-center overflow-hidden rounded-md bg-accent'

const appRowClass = 'flex min-w-0 cursor-pointer items-start gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card px-3 py-2.5 transition-colors hover:bg-[color:var(--sidebar-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring' /* ui-allow-style: App cards reuse the schedule sidebar card surface (bordered in both modes — rationale in schedule-item.vue) and hover token while retaining their content layout. */

const props = defineProps<{ botId: string, canManage: boolean }>()
const { t, locale } = useI18n()
const { appDependencyIconUrl } = useWorkspaceDependencyText()
const router = useRouter()
const search = ref('')
const query = refDebounced(computed(() => search.value.trim()), 300)
const installedQuery = useQuery({
  key: () => botAppsQueryKey(props.botId),
  query: async () => (await getBotsByBotIdApps({ path: { bot_id: props.botId }, throwOnError: true })).data,
  enabled: () => !!props.botId && props.canManage,
})
const feed = useSupermarketFeed(query)
const installedItems = computed(() => props.canManage && props.botId ? installedQuery.data.value?.items ?? [] : [])
const installed = computed(() => filterInstalledApps(installedItems.value, query.value, locale.value))
const catalog = computed(() => uninstalledApps(feed.items.value, installedItems.value))
const scrollAreaRef = ref<InstanceType<typeof ScrollArea> | null>(null)
const scrollEl = computed(() => {
  const root = scrollAreaRef.value?.$el as HTMLElement | undefined
  return root?.querySelector<HTMLElement>('[data-slot="scroll-area-viewport"]') ?? null
})
const { loadMoreSentinel, showSentinel, resetScrollTop, isNearEnd } = useSidebarInfiniteScroll({
  scrollEl,
  hasMore: computed(() => feed.hasMore.value && !feed.error.value && search.value.trim() === query.value),
  loading: feed.loading,
  /** Wait for committed layout before applying the shared observer geometry. */
  loadMore: async () => {
    await nextTick()
    if (search.value.trim() !== query.value || !isNearEnd()) return
    await feed.loadMore()
  },
  progressCursor: computed(() => String(feed.page.value)),
  itemCount: computed(() => catalog.value.length),
})
watch(query, resetScrollTop, { flush: 'post' })
const canInstall = computed(() => !!props.botId && props.canManage && !installedQuery.error.value && installedQuery.data.value?.workspace_state === 'running')
const pendingApp = ref('')
const selectedApp = ref<HandlersSupermarketAppDescriptor | null>(null)
const installOpen = ref(false)
const installBotId = ref('')
let previewSequence = 0

/** Discard a preview fetched for a bot whose selection or permissions changed. */
watch(() => [props.botId, props.canManage], () => {
  previewSequence++
  pendingApp.value = ''
  installOpen.value = false
})

/** Fetch the immutable release descriptor before opening the shared confirmation flow. */
async function prepareInstall(app: HandlersSupermarketAppSummary) {
  if (!canInstall.value || pendingApp.value) return
  const sequence = ++previewSequence
  const botId = props.botId
  const key = appKey(app)
  pendingApp.value = key
  try {
    const { data } = await getSupermarketRegistriesByRegistryIdAppsByAppId({ path: { registry_id: app.registry_id, app_id: app.app_id }, throwOnError: true })
    if (sequence !== previewSequence || props.botId !== botId || !canInstall.value) return
    selectedApp.value = data
    installBotId.value = botId
    installOpen.value = true
  } catch (error) {
    if (sequence === previewSequence) toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  } finally {
    if (sequence === previewSequence) pendingApp.value = ''
  }
}

function openInstalled(app: HandlersAppItem) {
  void router.push({ name: 'bot-detail', params: { botName: props.botId }, query: { tab: 'apps', app: appKey(app) } })
}
/** Preserve the selected bot when browsing an uninstalled App. */
function openCatalog(app: HandlersSupermarketAppSummary) {
  if (!app.registry_id || !app.app_id) return
  void router.push({
    name: 'supermarket-app-detail',
    params: { registryId: app.registry_id, appId: app.app_id },
    query: props.botId ? { botId: props.botId } : undefined,
  })
}
</script>
