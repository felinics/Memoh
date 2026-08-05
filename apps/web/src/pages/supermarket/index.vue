<template>
  <PageShell :title="$t('supermarket.title')">
    <template #actions>
      <Button
        variant="outline"
        as="a"
        href="https://github.com/memohai/supermarket"
        target="_blank"
        rel="noopener noreferrer"
      >
        <Github class="size-4" />
        {{ $t('supermarket.submit') }}
      </Button>
    </template>

    <div class="space-y-6">
      <!-- Search -->
      <div class="relative">
        <Search class="absolute left-3 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
        <Input
          v-model="searchInput"
          :placeholder="$t(capabilitiesStore.connectors
            ? 'supermarket.searchPlaceholderWithConnectors'
            : 'supermarket.searchPlaceholder')"
          class="pl-9"
          @keydown.enter="applySearch"
        />
      </div>

      <!-- 连接器是首位 tab 且是默认选中项,而它是否存在要等 capabilities 拉回来才知道。
           在 loaded 之前渲染 tab 条会先画出 [插件][技能]/选中插件,ping 落地后整条重排并
           改选中——所以这里等 loaded 再渲染,一次画对。已加载过(store 常驻)则无感。 -->
      <InlineLoadingRow
        v-if="!capabilitiesStore.loaded"
        class="justify-center py-8"
      >
        {{ $t('common.loading') }}
      </InlineLoadingRow>

      <Tabs
        v-else
        v-model="activeTab"
        class="w-full"
      >
        <TabsList>
          <TabsTrigger
            v-if="capabilitiesStore.connectors"
            value="connectors"
          >
            {{ $t('supermarket.connectorsSection') }}
          </TabsTrigger>
          <TabsTrigger value="plugins">
            {{ $t('supermarket.pluginSection') }}
          </TabsTrigger>
          <TabsTrigger value="skills">
            {{ $t('supermarket.skillsSection') }}
          </TabsTrigger>
        </TabsList>

        <TabsContent
          v-if="capabilitiesStore.connectors"
          value="connectors"
        >
          <InlineLoadingRow
            v-if="connectorsQuery.isLoading.value"
            class="justify-center py-8"
          >
            {{ $t('common.loading') }}
          </InlineLoadingRow>

          <div
            v-else-if="searchQuery && !filteredConnectors.length"
            class="py-8 text-center text-xs text-muted-foreground"
          >
            {{ $t('supermarket.noConnectorResults') }}
          </div>

          <Empty
            v-else-if="!filteredConnectors.length"
            class="py-12"
          >
            <EmptyHeader>
              <EmptyTitle>{{ $t('connectors.catalogEmptyTitle') }}</EmptyTitle>
              <EmptyDescription>{{ $t('connectors.catalogEmptyDescription') }}</EmptyDescription>
            </EmptyHeader>
          </Empty>

          <div
            v-else
            class="grid grid-cols-1 gap-4 sm:grid-cols-2"
          >
            <MarketItemCard
              v-for="connector in filteredConnectors"
              :key="connector.type"
              :name="connector.name || connector.type"
              :description="connector.description"
              :homepage="connector.homepage_url"
              @open="openConnectorConnect(connector)"
            >
              <template #leading>
                <ProviderIcon
                  :icon="connector.icon_url || ''"
                  size="20"
                  class="size-5 object-contain"
                >
                  <Plug class="size-4 text-muted-foreground" />
                </ProviderIcon>
              </template>
              <template #actions>
                <Button
                  size="sm"
                  :disabled="connector.status !== 'ready'"
                  @click="openConnectorConnect(connector)"
                >
                  {{ connector.status === 'ready'
                    ? $t('connectors.connect')
                    : $t('connectors.unavailable') }}
                </Button>
              </template>
            </MarketItemCard>
          </div>
        </TabsContent>

        <!-- Plugins Tab -->
        <TabsContent value="plugins">
          <InlineLoadingRow
            v-if="pluginsLoading"
            class="justify-center py-8"
          >
            {{ $t('common.loading') }}
          </InlineLoadingRow>

          <div
            v-else-if="!plugins.length"
            class="py-8 text-center text-xs text-muted-foreground"
          >
            {{ $t('supermarket.noPluginResults') }}
          </div>

          <div
            v-else
            class="grid grid-cols-1 sm:grid-cols-2 gap-4"
          >
            <PluginCard
              v-for="plugin in plugins"
              :key="plugin.id"
              :plugin="plugin"
              @install="openPluginInstall"
            />
          </div>
        </TabsContent>

        <!-- Skills Tab -->
        <TabsContent value="skills">
          <InlineLoadingRow
            v-if="skillsLoading"
            class="justify-center py-8"
          >
            {{ $t('common.loading') }}
          </InlineLoadingRow>

          <div
            v-else-if="!skills.length"
            class="py-8 text-center text-xs text-muted-foreground"
          >
            {{ $t('supermarket.noSkillResults') }}
          </div>

          <div
            v-else
            class="grid grid-cols-1 sm:grid-cols-2 gap-4"
          >
            <SkillCard
              v-for="skill in skills"
              :key="skill.id"
              :skill="skill"
              @install="openSkillInstall"
            />
          </div>
        </TabsContent>
      </Tabs>

      <InstallPluginDialog
        v-model:open="pluginDialogOpen"
        :plugin="selectedPlugin"
        @installed="refreshAll"
      />
      <InstallSkillDialog
        v-model:open="skillDialogOpen"
        :skill="selectedSkill"
        @installed="refreshAll"
      />
      <ConnectConnectorDialog
        v-model:open="connectorDialogOpen"
        :connector="selectedConnector"
        :default-bot-id="defaultBotId"
        @connected="openBotConnectors"
      />
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { Search, Github, Plug } from 'lucide-vue-next'
import {
  Button,
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
  InlineLoadingRow,
  Input,
  PageShell,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
  toast,
} from '@felinic/ui'
import {
  getConnectorsCatalog,
  getSupermarketPlugins,
  getSupermarketSkills,
  type ConnectitConnector,
  type HandlersSupermarketSkillEntry,
  type PluginsManifest,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import PluginCard from './components/plugin-card.vue'
import SkillCard from './components/skill-card.vue'
import InstallPluginDialog from './components/install-plugin-dialog.vue'
import InstallSkillDialog from './components/install-skill-dialog.vue'
import ConnectConnectorDialog from './components/connect-connector-dialog.vue'
import MarketItemCard from './components/market-item-card.vue'
import ProviderIcon from '@/components/provider-icon/index.vue'
import { useSyncedQueryParam } from '@/composables/useSyncedQueryParam'
import { useCapabilitiesStore } from '@/store/capabilities'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const capabilitiesStore = useCapabilitiesStore()
// Empty default (not 'plugins'): the landing tab depends on a capability that is
// only known after ping, so the fallback below owns it instead of the composable —
// an empty param means "no explicit tab", and nothing gets written into the URL.
const tabParam = useSyncedQueryParam('tab', '')
// Settings pages are KeepAlive-cached and share the `tab` query key, so a
// foreign value (e.g. bot detail's ?tab=memory) can land in the synced param
// while this page is deactivated. Render anything outside this page's own
// tabs as the default tab; writes go back through the synced param.
const activeTab = computed({
  get: () => {
    const valid = capabilitiesStore.connectors
      ? ['connectors', 'plugins', 'skills']
      : ['plugins', 'skills']
    // Connectors leads the tab strip, so it is also the default landing tab
    // wherever the capability exists; otherwise plugins keeps that role.
    const fallback = capabilitiesStore.connectors ? 'connectors' : 'plugins'
    return valid.includes(tabParam.value) ? tabParam.value : fallback
  },
  set: (value: string) => {
    tabParam.value = value
  },
})

const searchInput = ref('')
const searchQuery = ref('')

const plugins = ref<PluginsManifest[]>([])
const skills = ref<HandlersSupermarketSkillEntry[]>([])
const pluginsLoading = ref(false)
const skillsLoading = ref(false)

const pluginDialogOpen = ref(false)
const skillDialogOpen = ref(false)
const selectedPlugin = ref<PluginsManifest | null>(null)
const selectedSkill = ref<HandlersSupermarketSkillEntry | null>(null)
const connectorDialogOpen = ref(false)
const selectedConnector = ref<ConnectitConnector | null>(null)

const defaultBotId = computed(() => {
  const value = route.query.botId
  return typeof value === 'string' ? value : ''
})

const connectorsQuery = useQuery({
  key: () => ['connectors-catalog'],
  query: async () => {
    const { data } = await getConnectorsCatalog({ throwOnError: true })
    return data
  },
  enabled: () => capabilitiesStore.connectors,
})

const filteredConnectors = computed(() => {
  const query = searchQuery.value.toLowerCase()
  const connectors = connectorsQuery.data.value ?? []
  if (!query) return connectors
  return connectors.filter(connector =>
    [connector.name, connector.type, connector.description, ...(connector.categories ?? [])]
      .some(value => value?.toLowerCase().includes(query)),
  )
})

watch(connectorsQuery.error, error => {
  if (error) toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
})

watch(
  () => [capabilitiesStore.loaded, capabilitiesStore.connectors] as const,
  ([loaded, connectors]) => {
    // Normalize the URL param only while this page owns the current route —
    // firing while KeepAlive-deactivated would rewrite another page's ?tab.
    if (loaded && !connectors && route.name === 'supermarket' && tabParam.value === 'connectors') {
      tabParam.value = 'plugins'
    }
  },
  { immediate: true },
)

onMounted(() => {
  void capabilitiesStore.load()
})

function applySearch() {
  searchQuery.value = searchInput.value.trim()
}

let searchDebounce: ReturnType<typeof setTimeout> | undefined
watch(searchInput, () => {
  clearTimeout(searchDebounce)
  searchDebounce = setTimeout(() => {
    searchQuery.value = searchInput.value.trim()
  }, 300)
})

function openPluginInstall(plugin: PluginsManifest) {
  selectedPlugin.value = plugin
  pluginDialogOpen.value = true
}

function openSkillInstall(skill: HandlersSupermarketSkillEntry) {
  selectedSkill.value = skill
  skillDialogOpen.value = true
}

function openConnectorConnect(connector: ConnectitConnector) {
  if (connector.status !== 'ready') return
  selectedConnector.value = connector
  connectorDialogOpen.value = true
}

function openBotConnectors(botId: string) {
  void router.push({
    name: 'bot-detail',
    params: { botName: botId },
    query: { tab: 'connectors' },
  })
}

async function loadPlugins() {
  pluginsLoading.value = true
  try {
    const { data } = await getSupermarketPlugins({
      query: {
        q: searchQuery.value || undefined,
        limit: 50,
      },
      throwOnError: true,
    })
    plugins.value = data.data ?? []
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  } finally {
    pluginsLoading.value = false
  }
}

async function loadSkills() {
  skillsLoading.value = true
  try {
    const { data } = await getSupermarketSkills({
      query: {
        q: searchQuery.value || undefined,
        limit: 50,
      },
      throwOnError: true,
    })
    skills.value = data.data ?? []
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  } finally {
    skillsLoading.value = false
  }
}

function refreshAll() {
  loadPlugins()
  loadSkills()
}

watch(searchQuery, () => {
  loadPlugins()
  loadSkills()
}, { immediate: true })
</script>
