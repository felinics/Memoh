<template>
  <MarketItemCard
    :name="name"
    :description="description"
    :homepage="pkg.homepage"
    @open="openDetail"
  >
    <template #leading>
      <SkillIcon :icon="pkg.icon" />
    </template>
    <template #meta>
      <Badge
        v-if="pkg.version"
        variant="secondary"
        size="sm"
        font="mono"
      >
        v{{ pkg.version }}
      </Badge>
      <Badge
        v-if="categoryLabel"
        variant="outline"
        size="sm"
      >
        {{ categoryLabel }}
      </Badge>
      <span
        v-if="pkg.skill_count"
        class="inline-flex items-center gap-1 text-caption text-muted-foreground"
        :title="$t('packages.section.skills', { count: pkg.skill_count ?? 0 }, pkg.skill_count ?? 0)"
      >
        <BrainCircuit class="size-3.5" />
        {{ pkg.skill_count }}
      </span>
      <span
        v-if="pkg.dependency_count"
        class="inline-flex items-center gap-1 text-caption text-muted-foreground"
        :title="$t('packages.section.dependencies', { count: pkg.dependency_count ?? 0 }, pkg.dependency_count ?? 0)"
      >
        <Package class="size-3.5" />
        {{ pkg.dependency_count }}
      </span>
      <span
        v-if="pkg.connector_count"
        class="inline-flex items-center gap-1 text-caption text-muted-foreground"
        :title="$t('packages.section.connectors', { count: pkg.connector_count ?? 0 }, pkg.connector_count ?? 0)"
      >
        <Plug class="size-3.5" />
        {{ pkg.connector_count }}
      </span>
    </template>
  </MarketItemCard>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { BrainCircuit, Package, Plug } from 'lucide-vue-next'
import { Badge } from '@felinic/ui'
import type { HandlersSupermarketSkillPackageSummary } from '@memohai/sdk'
import { packageDisplayDescription, packageDisplayName } from '@/composables/api/usePackages'
import MarketItemCard from './market-item-card.vue'
import SkillIcon from './skill-icon.vue'

const props = defineProps<{
  pkg: HandlersSupermarketSkillPackageSummary
  /** Localized category name, resolved by the list from the shared table. */
  categoryLabel?: string
  /** Bot to preselect in the install dialog. */
  botId?: string
}>()
const router = useRouter()
const { locale } = useI18n()

const name = computed(() => packageDisplayName(props.pkg, locale.value))
const description = computed(() => packageDisplayDescription(props.pkg, locale.value))

function openDetail() {
  if (!props.pkg.registry_id || !props.pkg.package_id) return
  router.push({
    name: 'supermarket-package-detail',
    params: { registryId: props.pkg.registry_id, packageId: props.pkg.package_id },
    query: props.botId ? { botId: props.botId } : undefined,
  })
}
</script>
