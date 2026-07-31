<template>
  <div class="mx-auto max-w-5xl px-4 py-6 md:px-6 md:py-8">
    <InlineLoadingRow
      v-if="loading"
      class="justify-center py-16"
    >
      {{ $t('common.loading') }}
    </InlineLoadingRow>

    <div
      v-else-if="!plugin"
      class="py-16 text-center"
    >
      <p class="text-sm font-medium">
        {{ $t('supermarket.pluginNotFound') }}
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
        :name="plugin.name || plugin.id"
        :tags="plugin.tags"
        @back="router.push({ name: 'supermarket' })"
        @install="installDialogOpen = true"
      >
        <template #icon>
          <ProviderIcon
            v-if="iconValue"
            :icon="iconValue"
            size="34"
            class="size-9 object-contain"
          >
            <PackageOpen class="size-8 text-muted-foreground" />
          </ProviderIcon>
          <PackageOpen
            v-else
            class="size-8 text-muted-foreground"
          />
        </template>
      </MarketDetailHeader>

      <p class="mt-8 max-w-4xl text-base leading-7 text-muted-foreground">
        {{ plugin.description || $t('supermarket.noDescription') }}
      </p>

      <section
        v-if="plugin.mcps?.length"
        class="mt-8"
      >
        <h2 class="mb-4 text-lg font-semibold">
          {{ $t('supermarket.mcps') }}
          <span class="ml-1 text-muted-foreground">{{ plugin.mcps.length }}</span>
        </h2>
        <SettingsSection>
          <SettingsRow
            v-for="mcp in plugin.mcps"
            :key="mcp.key || mcp.name"
            align="start"
          >
            <template #leading>
              <div class="flex size-10 shrink-0 items-center justify-center rounded-md border bg-background">
                <Plug class="size-5 text-muted-foreground" />
              </div>
            </template>
            <template #content>
              <div class="flex flex-wrap items-center gap-2">
                <p
                  class="min-w-0 truncate text-sm font-medium text-foreground"
                  :title="mcp.display_name || mcp.name || mcp.key"
                >
                  {{ mcp.display_name || mcp.name || mcp.key }}
                </p>
                <Badge
                  v-if="authTypeForMcp(mcp.key)"
                  variant="secondary"
                  size="sm"
                >
                  {{ authTypeLabel(authTypeForMcp(mcp.key)) }}
                </Badge>
              </div>
              <p class="mt-0.5 break-words text-xs text-muted-foreground">
                {{ mcp.description || mcp.url || mcp.command || $t('supermarket.noDescription') }}
              </p>
            </template>
          </SettingsRow>
        </SettingsSection>
      </section>

      <section
        v-if="pluginSkills.length"
        class="mt-8"
      >
        <h2 class="mb-4 text-lg font-semibold">
          {{ $t('supermarket.skillsSection') }}
          <span class="ml-1 text-muted-foreground">{{ pluginSkills.length }}</span>
        </h2>
        <SettingsSection>
          <SettingsRow
            v-for="skill in pluginSkills"
            :key="skillKey(skill)"
            align="start"
          >
            <template #leading>
              <div class="flex size-10 shrink-0 items-center justify-center rounded-md border bg-background">
                <Boxes class="size-5 text-muted-foreground" />
              </div>
            </template>
            <template #content>
              <p
                class="min-w-0 truncate text-sm font-medium text-foreground"
                :title="skillName(skill)"
              >
                {{ skillName(skill) }}
              </p>
              <p
                class="mt-0.5 line-clamp-2 break-words text-xs text-muted-foreground"
                :title="skillDescription(skill)"
              >
                {{ skillDescription(skill) }}
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
            :label="$t('supermarket.category')"
            :value="$t('supermarket.pluginSection')"
          />
          <InfoItem
            :label="$t('supermarket.developer')"
            :value="plugin.author?.name || $t('common.none')"
          />
          <InfoItem
            :label="$t('supermarket.version')"
            :value="plugin.version || $t('common.none')"
          />
          <InfoItem
            :label="$t('supermarket.schemaVersion')"
            :value="plugin.schema_version || $t('common.none')"
          />
          <div
            v-if="plugin.homepage"
            class="space-y-1"
          >
            <p class="text-sm text-muted-foreground">
              {{ $t('supermarket.links') }}
            </p>
            <a
              :href="plugin.homepage"
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex items-center gap-1 text-sm font-medium text-primary hover:underline"
            >
              {{ $t('supermarket.website') }}
              <ExternalLink class="size-3.5" />
            </a>
          </div>
        </div>
      </section>
    </template>

    <InstallPluginDialog
      v-model:open="installDialogOpen"
      :plugin="plugin"
      @installed="loadPlugin"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, Boxes, ExternalLink, PackageOpen, Plug } from 'lucide-vue-next'
import { Badge, Button, InlineLoadingRow, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { getSupermarketPluginsById, type PluginsManifest, type PluginsSkillEntry, type PluginsSkillResource } from '@memohai/sdk'
import ProviderIcon from '@/components/provider-icon/index.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'
import InstallPluginDialog from './components/install-plugin-dialog.vue'
import InfoItem from './components/info-item.vue'
import MarketDetailHeader from './components/market-detail-header.vue'

type PluginSkill = PluginsSkillEntry | PluginsSkillResource

const route = useRoute()
const router = useRouter()
const { t } = useI18n()

const plugin = ref<PluginsManifest | null>(null)
const loading = ref(false)
const installDialogOpen = ref(false)

const pluginId = computed(() => String(route.params.pluginId || ''))

const iconValue = computed(() => {
  const icon = plugin.value?.icon
  if (!icon) return ''
  if (icon.kind === 'external_url') return icon.url || ''
  if (icon.kind === 'builtin') return icon.name || ''
  return ''
})

const pluginSkills = computed<PluginSkill[]>(() => [
  ...(plugin.value?.bundled_skills ?? []),
  ...(plugin.value?.skills ?? []),
])

function skillKey(skill: PluginSkill) {
  return 'id' in skill ? skill.id : skill.key
}

function skillName(skill: PluginSkill) {
  if ('name' in skill && skill.name) return skill.name
  if ('id' in skill && skill.id) return skill.id
  if ('key' in skill && skill.key) return skill.key
  return t('supermarket.unnamedSkill')
}

function skillDescription(skill: PluginSkill) {
  if ('description' in skill && skill.description) return skill.description
  if ('path' in skill && skill.path) return skill.path
  return t('supermarket.noDescription')
}

function authTypeForMcp(key?: string) {
  if (!key) return ''
  return plugin.value?.auth_requirements?.find(item => item.key === key)?.type || ''
}

function authTypeLabel(type?: string) {
  switch (type) {
    case 'managed_oauth': return t('supermarket.authManagedOAuth')
    case 'user_secret': return t('supermarket.authUserSecret')
    case 'none': return t('supermarket.authNone')
    default: return type || ''
  }
}

async function loadPlugin() {
  if (!pluginId.value) return
  loading.value = true
  try {
    const { data } = await getSupermarketPluginsById({
      path: { id: pluginId.value },
      throwOnError: true,
    })
    plugin.value = data
  } catch (error) {
    plugin.value = null
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  } finally {
    loading.value = false
  }
}

onMounted(loadPlugin)
watch(pluginId, loadPlugin)
</script>
