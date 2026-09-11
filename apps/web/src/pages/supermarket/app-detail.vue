<template>
  <DetailPane
    width="narrow"
    :back-label="$t('sidebar.supermarket')"
    @back="router.push({ name: 'supermarket' })"
  >
    <SettingsShell width="narrow">
      <InlineLoadingRow
        v-if="loading"
        class="justify-center py-16"
      >
        {{ $t('common.loading') }}
      </InlineLoadingRow>

      <div
        v-else-if="!pkg"
        class="py-16 text-center"
      >
        <p class="text-sm font-medium">
          {{ $t('supermarket.appNotFound') }}
        </p>
        <Button
          variant="outline"
          size="sm"
          class="mt-4"
          @click="router.push({ name: 'supermarket' })"
        >
          <ArrowLeft class="size-4" />
          {{ $t('supermarket.backToSupermarket') }}
        </Button>
      </div>

      <template v-else>
        <MarketDetailHeader
          :name="name"
          :version="pkg.version"
          :subtitle="subtitle"
          :tags="pkg.tags"
          @install="installDialogOpen = true"
        >
          <template #icon>
            <SkillIcon
              :icon="pkg.icon"
              variant="detail"
            />
          </template>
        </MarketDetailHeader>

        <p class="mt-8 max-w-4xl text-base leading-7 text-muted-foreground">
          {{ description || $t('supermarket.noDescription') }}
        </p>

        <section
          v-if="pkg.skills.length"
          class="mt-8"
        >
          <h2 class="mb-4 text-lg font-semibold">
            {{ $t('apps.sections.skills') }}
            <span class="ml-1.5 font-normal text-muted-foreground">{{ pkg.skills.length }}</span>
          </h2>
          <SettingsSection>
            <SettingsRow
              v-for="skill in pkg.skills"
              :key="skill.skill_id"
            >
              <template #leading>
                <div class="flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-md border bg-background">
                  <SkillIcon :icon="skill.icon" />
                </div>
              </template>
              <template #content>
                <p class="text-sm font-medium">
                  {{ skill.name || skill.skill_id }}
                </p>
                <p class="mt-1 text-xs text-muted-foreground">
                  {{ skill.description }}
                </p>
              </template>
            </SettingsRow>
          </SettingsSection>
        </section>

        <!-- Dependencies are shown through their canonical Apps: the same
           name, icon and description a user sees when browsing them alone. -->
        <section
          v-if="pkg.dependencies.length"
          class="mt-8"
        >
          <h2 class="mb-1 text-lg font-semibold">
            {{ $t('apps.sections.dependencies') }}
            <span class="ml-1.5 font-normal text-muted-foreground">{{ pkg.dependencies.length }}</span>
          </h2>
          <p class="mb-4 text-xs text-muted-foreground">
            {{ $t('supermarket.dependenciesHint') }}
          </p>
          <SettingsSection>
            <SettingsRow
              v-for="dep in dependencyRows"
              :key="dep.id"
            >
              <template #leading>
                <div class="flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-md border bg-background">
                  <SkillIcon
                    v-if="dep.summary?.icon"
                    :icon="dep.summary.icon"
                  />
                  <App
                    v-else
                    class="size-4 text-muted-foreground"
                  />
                </div>
              </template>
              <template #content>
                <p class="text-sm font-medium">
                  {{ dep.summary ? appDisplayName(dep.summary, locale) : dep.id }}
                </p>
                <p class="mt-1 text-xs text-muted-foreground">
                  {{ dep.summary ? appDisplayDescription(dep.summary, locale) : $t('supermarket.dependencyPending') }}
                </p>
              </template>
              <Button
                v-if="dep.summary"
                variant="ghost"
                size="sm"
                @click="openApp('memoh', dep.id)"
              >
                {{ $t('supermarket.viewDetails') }}
                <ChevronRight class="size-4" />
              </Button>
            </SettingsRow>
          </SettingsSection>
        </section>

        <section
          v-if="pkg.connectors.length"
          class="mt-8"
        >
          <h2 class="mb-1 text-lg font-semibold">
            {{ $t('apps.sections.connectors') }}
            <span class="ml-1.5 font-normal text-muted-foreground">{{ pkg.connectors.length }}</span>
          </h2>
          <p class="mb-4 text-xs text-muted-foreground">
            {{ capabilitiesStore.connectors ? $t('supermarket.connectorsHint') : $t('supermarket.connectorsUnavailableHint') }}
          </p>
          <SettingsSection>
            <SettingsRow
              v-for="connector in pkg.connectors"
              :key="connector.type"
            >
              <template #leading>
                <div class="flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-md border bg-background">
                  <ProviderIcon
                    :icon="connectorCatalog.get(connector.type)?.icon_url || ''"
                    class="size-5 object-contain"
                  >
                    <Plug class="size-4 text-muted-foreground" />
                  </ProviderIcon>
                </div>
              </template>
              <template #content>
                <div class="flex flex-wrap items-center gap-2">
                  <p class="text-sm font-medium">
                    {{ connectorCatalog.get(connector.type)?.name || connector.type }}
                  </p>
                  <Badge
                    v-if="!connector.required"
                    variant="outline"
                    size="sm"
                  >
                    {{ $t('apps.connector.optional') }}
                  </Badge>
                </div>
                <p class="mt-1 text-xs text-muted-foreground">
                  {{ connectorCatalog.get(connector.type)?.description || $t('supermarket.connectorAuthHint') }}
                </p>
              </template>
            </SettingsRow>
          </SettingsSection>
        </section>

        <section class="mt-10">
          <h2 class="text-lg font-semibold">
            {{ $t('supermarket.information') }}
          </h2>
          <div class="mt-4 grid gap-x-12 gap-y-5 md:grid-cols-2">
            <InfoItem
              :label="$t('supermarket.version')"
              :value="pkg.version || $t('common.none')"
            />
            <InfoItem
              :label="$t('supermarket.category')"
              :value="categoryLabel || $t('common.none')"
            />
            <InfoItem
              :label="$t('supermarket.registry')"
              :value="registryName || pkg.registry_id"
            />
            <InfoItem
              :label="$t('supermarket.author')"
              :value="pkg.author?.name || $t('common.none')"
            />
            <InfoItem
              :label="$t('supermarket.license')"
              :value="pkg.license || $t('common.none')"
            />
            <InfoItem
              :label="$t('supermarket.revision')"
              :value="pkg.revision.slice(0, 12)"
            />
            <InfoItem
              v-if="pkg.homepage"
              :label="$t('supermarket.homepage')"
              :value="pkg.homepage"
            />
            <InfoItem
              v-if="pkg.repository"
              :label="$t('supermarket.repository')"
              :value="pkg.repository"
            />
          </div>
        </section>
      </template>

      <InstallAppDialog
        v-model:open="installDialogOpen"
        :pkg="pkg"
        :default-bot-id="defaultBotId"
      />
    </SettingsShell>
  </DetailPane>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { ArrowLeft, ChevronRight, Package as App, Plug } from 'lucide-vue-next'
import { Badge, Button, DetailPane, InlineLoadingRow, SettingsRow, SettingsSection, SettingsShell, toast } from '@felinic/ui'
import {
  getConnectorsCatalog,
  getSupermarketRegistries,
  getSupermarketRegistriesByRegistryIdAppsByAppId,
  getSupermarketRegistriesByRegistryIdAppsByAppIdReleasesByRevision,
  type HandlersSupermarketAppDescriptor,
} from '@memohai/sdk'
import ProviderIcon from '@/components/provider-icon/index.vue'
import {
  categoryDisplayName,
  appDisplayDescription,
  appDisplayName,
  useAppCategoriesQuery,
} from '@/composables/api/useApps'
import { useCapabilitiesStore } from '@/store/capabilities'
import { resolveApiErrorMessage } from '@/utils/api-error'
import InfoItem from './components/info-item.vue'
import InstallAppDialog from './components/install-app-dialog.vue'
import MarketDetailHeader from './components/market-detail-header.vue'
import SkillIcon from './components/skill-icon.vue'

const route = useRoute()
const router = useRouter()
const { t, locale } = useI18n()
const capabilitiesStore = useCapabilitiesStore()
const pkg = ref<HandlersSupermarketAppDescriptor | null>(null)
const registryName = ref('')
const loading = ref(false)
const installDialogOpen = ref(false)
const registryId = computed(() => String(route.params.registryId || ''))
const appId = computed(() => String(route.params.appId || ''))
const revision = computed(() => String(route.query.revision || ''))
const defaultBotId = computed(() => (typeof route.query.botId === 'string' ? route.query.botId : ''))
const appIdentity = computed(() => `${registryId.value}/${appId.value}/${revision.value}`)
let loadSequence = 0

const name = computed(() => (pkg.value ? appDisplayName(pkg.value, locale.value) : ''))
const description = computed(() => (pkg.value ? appDisplayDescription(pkg.value, locale.value) : ''))
const subtitle = computed(() => {
  const parts = [pkg.value?.author?.name, registryName.value || pkg.value?.registry_id].filter(Boolean)
  return parts.join(' · ')
})

const categoriesQuery = useAppCategoriesQuery()
const categoryLabel = computed(() => {
  if (!pkg.value) return ''
  const category = (categoriesQuery.data.value ?? []).find(item => item.id === pkg.value?.category)
  return categoryDisplayName(category, pkg.value.category_name, locale.value)
})

onMounted(() => {
  void capabilitiesStore.load()
})

const connectorsQuery = useQuery({
  key: () => ['connectors-catalog'],
  query: async () => {
    const { data } = await getConnectorsCatalog({ throwOnError: true })
    return data
  },
  enabled: () => capabilitiesStore.connectors,
})
const connectorCatalog = computed(() => new Map(
  (connectorsQuery.data.value ?? [])
    .filter((item): item is typeof item & { type: string } => !!item.type)
    .map(item => [item.type, item]),
))

// Canonical App summaries of the referenced dependencies, loaded lazily.
const dependencySummaries = ref<Record<string, HandlersSupermarketAppDescriptor | null>>({})
const dependencyRows = computed(() => (pkg.value?.dependencies ?? []).map(id => ({ id, summary: dependencySummaries.value[id] ?? null })))

async function loadDependencySummaries(ids: string[]) {
  const results = await Promise.all(ids.map(async (id) => {
    try {
      const { data } = await getSupermarketRegistriesByRegistryIdAppsByAppId({
        path: { registry_id: 'memoh', app_id: id },
        throwOnError: true,
      })
      return [id, data] as const
    } catch {
      return [id, null] as const
    }
  }))
  dependencySummaries.value = Object.fromEntries(results)
}

function openApp(registry: string, id: string) {
  void router.push({ name: 'supermarket-app-detail', params: { registryId: registry, appId: id }, query: route.query })
}

async function loadApp() {
  if (!registryId.value || !appId.value) return
  const sequence = ++loadSequence
  loading.value = true
  pkg.value = null
  dependencySummaries.value = {}
  try {
    const appRequest = revision.value
      ? getSupermarketRegistriesByRegistryIdAppsByAppIdReleasesByRevision({
          path: { registry_id: registryId.value, app_id: appId.value, revision: revision.value },
          throwOnError: true,
        })
      : getSupermarketRegistriesByRegistryIdAppsByAppId({
          path: { registry_id: registryId.value, app_id: appId.value },
          throwOnError: true,
        })
    const [{ data }, registryResponse] = await Promise.all([
      appRequest,
      getSupermarketRegistries({ throwOnError: true }).catch(() => null),
    ])
    if (sequence !== loadSequence) return
    pkg.value = data
    registryName.value = registryResponse?.data.data
      ?.find(registry => registry.id === registryId.value)?.name || registryId.value
    if (data.dependencies?.length) void loadDependencySummaries(data.dependencies)
  } catch (error) {
    if (sequence !== loadSequence) return
    pkg.value = null
    registryName.value = registryId.value
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  } finally {
    if (sequence === loadSequence) loading.value = false
  }
}

onMounted(loadApp)
watch(appIdentity, loadApp)
</script>
