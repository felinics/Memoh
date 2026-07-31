<template>
  <section class="mx-auto flex min-h-full max-w-3xl flex-col px-6 py-10">
    <!-- Import is a low-frequency secondary action, parked top-right so it never
         competes with the centered group below (create lives as a tile). -->
    <div class="flex h-8 justify-end">
      <Button
        variant="outline"
        size="sm"
        @click="router.push({ name: 'bot-new', query: { mode: 'import' } })"
      >
        <Upload class="size-4" />
        {{ $t('bots.backup.importBot') }}
      </Button>
    </div>

    <!-- Title + tiles float together in the upper-middle, like About: a calm,
         centered launcher rather than a top-left settings list. -->
    <div class="flex flex-1 flex-col items-center justify-center gap-7 pb-[8vh]">
      <div class="text-center">
        <h1 class="text-lg font-semibold">
          {{ $t('sidebar.bots') }}
        </h1>
        <p
          v-if="!isLoading && allBots.length === 0"
          class="mt-1.5 text-[13px] text-muted-foreground"
        >
          {{ $t('bots.emptyDescription') }}
        </p>
      </div>

      <!-- Search only appears once there are enough bots to warrant it. -->
      <div
        v-if="allBots.length > 5"
        class="relative w-full max-w-sm"
      >
        <Search class="absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          v-model="searchText"
          :placeholder="$t('bots.searchPlaceholder')"
          class="pl-9"
        />
      </div>

      <!-- Loading skeleton tiles -->
      <div
        v-if="isLoading"
        class="flex flex-wrap justify-center gap-4"
      >
        <div
          v-for="i in 2"
          :key="i"
          class="flex w-52 flex-col items-center rounded-[var(--radius-menu-shell)] border border-border bg-card p-5"
        >
          <Skeleton class="size-14 rounded-full" />
          <Skeleton class="mt-3 h-4 w-24" />
          <Skeleton class="mt-1.5 h-3 w-16" />
        </div>
      </div>

      <!-- Bot tiles + the create tile (its companion, so a single bot is never a
           lonely card). The create tile is hidden while filtering. -->
      <div
        v-else
        class="flex flex-wrap justify-center gap-4"
      >
        <BotCard
          v-for="bot in filteredBots"
          :key="bot.id"
          :bot="bot"
        />
        <!-- Create tile: the add companion, so a single bot is never a lonely
             card. Hidden while filtering. -->
        <PersonaTile
          v-if="!searchText"
          variant="add"
          :name="$t('bots.createBot')"
          @click="router.push({ name: 'bot-new' })"
        >
          <template #media>
            <div class="flex size-14 items-center justify-center rounded-full bg-accent-gray-soft-active">
              <Plus class="size-6" />
            </div>
          </template>
        </PersonaTile>
      </div>

      <!-- Bots exist but the search matched none -->
      <p
        v-if="!isLoading && searchText && filteredBots.length === 0"
        class="text-sm text-muted-foreground"
      >
        {{ $t('common.noData') }}
      </p>
    </div>
  </section>
</template>

<script setup lang="ts">
import {
  Button,
  Input,
  Skeleton,
} from '@felinic/ui'
import { Search, Plus, Upload } from 'lucide-vue-next'
import { ref, computed, watch, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { PersonaTile } from '@felinic/ui'
import BotCard from './components/bot-card.vue'
import { useQuery, useQueryCache } from '@pinia/colada'
import { getBotsQuery, getBotsQueryKey } from '@memohai/sdk/colada'

declare global {
  interface Window {
    __previewBots?: (n?: number | null) => void
  }
}

const router = useRouter()
const searchText = ref('')
const queryCache = useQueryCache()

const { data: botData, status } = useQuery(getBotsQuery())

const isLoading = computed(() => status.value === 'loading')

const realBots = computed(() => botData.value?.items ?? [])

// --- dev-only layout preview ---------------------------------------------
// Eyeball how the grid lays out at any bot count without touching real data.
// In the browser console:  __previewBots(1) / (2) / (7) to preview that many
// tiles, __previewBots(0) for the empty state, __previewBots() to restore.
// The whole block is compiled out of production builds.
const previewCount = ref<number | null>(null)

const allBots = computed(() => {
  const real = realBots.value
  if (!import.meta.env.DEV || previewCount.value == null) return real
  const n = previewCount.value
  if (n <= 0 || real.length === 0) return []
  return Array.from({ length: n }, (_, i) => {
    const base = real[i % real.length]
    if (i < real.length) return base
    // clone extras with a unique id/name so :key and routing stay sane
    return { ...base, id: `${base.id}__preview-${i}`, name: `${base.name ?? base.id}-preview-${i}` }
  })
})

if (import.meta.env.DEV && typeof window !== 'undefined') {
  window.__previewBots = (n?: number | null) => {
    previewCount.value = n == null ? null : Math.max(0, Math.floor(n))
  }
  console.info('[dev] __previewBots(n): preview the bots grid at n tiles · __previewBots() to restore')
  onUnmounted(() => {
    delete window.__previewBots
  })
}

const filteredBots = computed(() => {
  const keyword = searchText.value.trim().toLowerCase()
  if (!keyword) return allBots.value
  return allBots.value.filter(bot =>
    bot.display_name?.toLowerCase().includes(keyword)
    || bot.name?.toLowerCase().includes(keyword)
    || bot.id?.toLowerCase().includes(keyword),
  )
})

const hasPendingBots = computed(() =>
  allBots.value.some(bot => bot.status === 'creating' || bot.status === 'deleting'),
)

let pollTimer: ReturnType<typeof setInterval> | null = null

watch(hasPendingBots, (pending) => {
  if (pending) {
    if (pollTimer == null) {
      pollTimer = setInterval(() => {
        queryCache.invalidateQueries({ key: getBotsQueryKey() })
      }, 2000)
    }
    return
  }
  if (pollTimer != null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}, { immediate: true })

onUnmounted(() => {
  if (pollTimer != null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
})
</script>
