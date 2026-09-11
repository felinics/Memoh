<template>
  <!-- Name, icon and description only: version, category and component
       counts live on the detail page, the list is for recognising Apps. -->
  <MarketItemCard
    :name="name"
    :description="description"
    :homepage="pkg.homepage"
    @open="openDetail"
  >
    <template #leading>
      <SkillIcon :icon="pkg.icon" />
    </template>
  </MarketItemCard>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import type { HandlersSupermarketAppSummary } from '@memohai/sdk'
import { appDisplayDescription, appDisplayName } from '@/composables/api/useApps'
import MarketItemCard from './market-item-card.vue'
import SkillIcon from './skill-icon.vue'

const props = defineProps<{
  pkg: HandlersSupermarketAppSummary
  /** Bot to preselect in the install dialog. */
  botId?: string
}>()
const router = useRouter()
const { locale } = useI18n()

const name = computed(() => appDisplayName(props.pkg, locale.value))
const description = computed(() => appDisplayDescription(props.pkg, locale.value))

function openDetail() {
  if (!props.pkg.registry_id || !props.pkg.app_id) return
  router.push({
    name: 'supermarket-app-detail',
    params: { registryId: props.pkg.registry_id, appId: props.pkg.app_id },
    query: props.botId ? { botId: props.botId } : undefined,
  })
}
</script>
