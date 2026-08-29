<template>
  <MarketItemCard
    :name="pkg.name || pkg.package_id"
    :description="pkg.description"
    @open="openDetail"
  >
    <template #leading>
      <SkillIcon :icon="pkg.icon" />
    </template>
  </MarketItemCard>
</template>

<script setup lang="ts">
import { useRouter } from 'vue-router'
import type { HandlersSupermarketSkillPackageSummary } from '@memohai/sdk'
import MarketItemCard from './market-item-card.vue'
import SkillIcon from './skill-icon.vue'

const props = defineProps<{ pkg: HandlersSupermarketSkillPackageSummary }>()
const router = useRouter()

function openDetail() {
  if (!props.pkg.registry_id || !props.pkg.package_id) return
  router.push({
    name: 'supermarket-package-detail',
    params: { registryId: props.pkg.registry_id, packageId: props.pkg.package_id },
  })
}
</script>
