<template>
  <div class="mx-auto max-w-5xl px-4 py-6 md:px-6 md:py-8">
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
        {{ $t('supermarket.packageNotFound') }}
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
        @back="router.push({ name: 'supermarket' })"
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
          {{ $t('supermarket.includedSkills') }}
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

      <!-- Dependencies are shown through their canonical Packages: the same
           name, icon and description a user sees when browsing them alone. -->
      <section
        v-if="pkg.dependencies.length"
        class="mt-8"
      >
        <h2 class="mb-1 text-lg font-semibold">
          {{ $t('supermarket.includedDependencies') }}
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
                <Package
                  v-else
                  class="size-4 text-muted-foreground"
                />
              </div>
            </template>
            <template #content>
              <p class="text-sm font-medium">
                {{ dep.summary ? packageDisplayName(dep.summary, locale) : dep.id }}
              </p>
              <p class="mt-1 text-xs text-muted-foreground">
                {{ dep.summary ? packageDisplayDescription(dep.summary, locale) : $t('supermarket.dependencyPending') }}
              </p>
            </template>
            <Button
              v-if="dep.summary"
              variant="ghost"
              size="sm"
              @click="openPackage('memoh', dep.id)"
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
          {{ $t('supermarket.includedConnectors') }}
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
                  {{ $t('packages.connector.optional') }}
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

    <InstallPackageDialog
      v-model:open="installDialogOpen"
      :pkg="pkg"
      :default-bot-id="defaultBotId"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { ArrowLeft, ChevronRight, Package, Plug } from 'lucide-vue-next'
import { Badge, Button, InlineLoadingRow, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import {
  getConnectorsCatalog,
  getSupermarketRegistries,
  getSupermarketRegistriesByRegistryIdPackagesByPackageId,
  getSupermarketRegistriesByRegistryIdPackagesByPackageIdReleasesByRevision,
  type HandlersSupermarketSkillPackageDescriptor,
} from '@memohai/sdk'
import ProviderIcon from '@/components/provider-icon/index.vue'
import {
  categoryDisplayName,
  packageDisplayDescription,
  packageDisplayName,
  usePackageCategoriesQuery,
} from '@/composables/api/usePackages'
import { useCapabilitiesStore } from '@/store/capabilities'
import { resolveApiErrorMessage } from '@/utils/api-error'
import InfoItem from './components/info-item.vue'
import InstallPackageDialog from './components/install-package-dialog.vue'
import MarketDetailHeader from './components/market-detail-header.vue'
import SkillIcon from './components/skill-icon.vue'

const route = useRoute()
const router = useRouter()
const { t, locale } = useI18n()
const capabilitiesStore = useCapabilitiesStore()
const pkg = ref<HandlersSupermarketSkillPackageDescriptor | null>(null)
const registryName = ref('')
const loading = ref(false)
const installDialogOpen = ref(false)
const registryId = computed(() => String(route.params.registryId || ''))
const packageId = computed(() => String(route.params.packageId || ''))
const revision = computed(() => String(route.query.revision || ''))
const defaultBotId = computed(() => (typeof route.query.botId === 'string' ? route.query.botId : ''))
const packageIdentity = computed(() => `${registryId.value}/${packageId.value}/${revision.value}`)
let loadSequence = 0

const name = computed(() => (pkg.value ? packageDisplayName(pkg.value, locale.value) : ''))
const description = computed(() => (pkg.value ? packageDisplayDescription(pkg.value, locale.value) : ''))
const subtitle = computed(() => {
  const parts = [pkg.value?.author?.name, registryName.value || pkg.value?.registry_id].filter(Boolean)
  return parts.join(' · ')
})

const categoriesQuery = usePackageCategoriesQuery()
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

// Canonical Package summaries of the referenced dependencies, loaded lazily.
const dependencySummaries = ref<Record<string, HandlersSupermarketSkillPackageDescriptor | null>>({})
const dependencyRows = computed(() => (pkg.value?.dependencies ?? []).map(id => ({ id, summary: dependencySummaries.value[id] ?? null })))

async function loadDependencySummaries(ids: string[]) {
  const results = await Promise.all(ids.map(async (id) => {
    try {
      const { data } = await getSupermarketRegistriesByRegistryIdPackagesByPackageId({
        path: { registry_id: 'memoh', package_id: id },
        throwOnError: true,
      })
      return [id, data] as const
    } catch {
      return [id, null] as const
    }
  }))
  dependencySummaries.value = Object.fromEntries(results)
}

function openPackage(registry: string, id: string) {
  void router.push({ name: 'supermarket-package-detail', params: { registryId: registry, packageId: id }, query: route.query })
}

async function loadPackage() {
  if (!registryId.value || !packageId.value) return
  const sequence = ++loadSequence
  loading.value = true
  pkg.value = null
  dependencySummaries.value = {}
  try {
    const packageRequest = revision.value
      ? getSupermarketRegistriesByRegistryIdPackagesByPackageIdReleasesByRevision({
          path: { registry_id: registryId.value, package_id: packageId.value, revision: revision.value },
          throwOnError: true,
        })
      : getSupermarketRegistriesByRegistryIdPackagesByPackageId({
          path: { registry_id: registryId.value, package_id: packageId.value },
          throwOnError: true,
        })
    const [{ data }, registryResponse] = await Promise.all([
      packageRequest,
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

onMounted(loadPackage)
watch(packageIdentity, loadPackage)
</script>
