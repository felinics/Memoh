<template>
  <div>
    <SwapTransition :direction="direction">
      <!-- Configured platforms -->
      <PageShell
        v-if="view === 'list'"
        variant="tab"
        :title="t('bots.channels.title')"
      >
        <template #actions>
          <Button
            :disabled="unconfiguredChannels.length === 0 && !isLoading"
            @click="addOpen = true"
          >
            <Plus class="size-4" />
            {{ t('bots.channels.addChannel') }}
          </Button>
        </template>

        <div
          v-if="isLoading && configuredChannels.length === 0"
          class="grid grid-cols-1 gap-3 sm:grid-cols-2"
        >
          <Skeleton
            v-for="n in 4"
            :key="n"
            class="h-[4.5rem] w-full rounded-[var(--radius-menu-shell)]"
          />
        </div>

        <!-- Platforms + a dashed add tile to drop another in -->
        <div
          v-else-if="configuredChannels.length > 0"
          class="grid grid-cols-1 gap-3 sm:grid-cols-2"
        >
          <BackendCard
            v-for="item in configuredChannels"
            :key="item.meta.type"
            :name="channelTitle(item.meta)"
            :subtitle="!item.config?.disabled ? t('bots.channels.statusActive') : t('bots.channels.configured')"
            :enabled="!item.config?.disabled"
            @click="openPlatform(item)"
          >
            <template #leading>
              <span class="flex size-10 items-center justify-center rounded-full bg-muted">
                <ChannelIcon
                  :channel="item.meta.type as string"
                  size="1.5em"
                />
              </span>
            </template>
          </BackendCard>

          <button
            v-if="unconfiguredChannels.length > 0"
            type="button"
            class="group/add flex min-h-[4.5rem] items-center justify-center gap-2 rounded-[var(--radius-menu-shell)] border border-dashed border-border bg-background text-sm text-muted-foreground transition-colors hover:border-foreground/30 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            @click="addOpen = true"
          >
            <Plus class="size-4" />
            {{ t('bots.channels.addChannel') }}
          </button>
        </div>

        <!-- Empty: a framed box that guides with one line + the add action, no decorative icon -->
        <Empty
          v-else
          class="rounded-[var(--radius-menu-shell)] border border-dashed border-border py-16"
        >
          <EmptyTitle>{{ t('bots.channels.emptyTitle') }}</EmptyTitle>
          <EmptyDescription>{{ t('bots.channels.emptyDescription') }}</EmptyDescription>
          <EmptyContent>
            <Button
              variant="outline"
              @click="addOpen = true"
            >
              <Plus class="size-4" />
              {{ t('bots.channels.addChannel') }}
            </Button>
          </EmptyContent>
        </Empty>
      </PageShell>

      <!-- Platform detail -->
      <section
        v-else
        class="mx-auto max-w-3xl pt-4 pb-8"
      >
        <Button
          variant="ghost"
          class="mb-2 text-foreground/85"
          @click="backToList()"
        >
          <ChevronLeft class="size-4" />
          {{ t('bots.channels.title') }}
        </Button>

        <ChannelSettingsPanel
          v-if="selectedItem"
          :key="selectedType ?? ''"
          :bot-id="botId"
          :channel-item="selectedItem"
          @saved="handleSaved"
          @deleted="handleDeleted"
        />
      </section>
    </SwapTransition>

    <!-- One add surface for every trigger (header / tile / empty): pick a platform type -->
    <Dialog v-model:open="addOpen">
      <DialogContent class="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>{{ t('bots.channels.addChannel') }}</DialogTitle>
        </DialogHeader>
        <div class="space-y-0.5">
          <button
            v-for="item in unconfiguredChannels"
            :key="item.meta.type"
            type="button"
            class="flex w-full items-center gap-3 rounded-[var(--radius-menu)] px-3 py-2 text-left text-sm transition-colors hover:bg-[color:var(--ui-hover)]"
            @click="addChannel(item.meta.type ?? '')"
          >
            <span class="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted">
              <ChannelIcon
                :channel="item.meta.type ?? ''"
                size="1.1em"
              />
            </span>
            <span class="truncate">{{ channelTitle(item.meta) }}</span>
          </button>
        </div>
      </DialogContent>
    </Dialog>
  </div>
</template>

<script setup lang="ts">
import { Plus, ChevronLeft } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Button, Skeleton,
  Dialog, DialogContent, DialogHeader, DialogTitle,
  Empty, EmptyTitle, EmptyDescription, EmptyContent,
} from '@felinic/ui'
import { useQuery } from '@pinia/colada'
import { getChannels, getBotsByIdChannelByPlatform } from '@memohai/sdk'
import type { HandlersChannelMeta, ChannelChannelConfig } from '@memohai/sdk'
import { BackendCard, PageShell, SwapTransition } from '@felinic/ui'
import ChannelSettingsPanel from './channel-settings-panel.vue'
import ChannelIcon from '@/components/channel-icon/index.vue'
import { useViewSwap } from '@/composables/useViewSwap'
import { channelTypeDisplayName } from '@/utils/channel-type-label'

export interface BotChannelItem {
  meta: HandlersChannelMeta
  config: ChannelChannelConfig | null
  configured: boolean
}

const props = defineProps<{ botId: string }>()
const { t } = useI18n()

const { view, direction, openDetail, backToList } = useViewSwap()
const addOpen = ref(false)

function channelTitle(meta: HandlersChannelMeta) {
  return channelTypeDisplayName(t, meta.type, meta.display_name)
}

const botIdRef = computed(() => props.botId)

const { data: channels, isLoading, refetch } = useQuery({
  key: () => ['bot-channels', botIdRef.value],
  query: async (): Promise<BotChannelItem[]> => {
    const { data: metas } = await getChannels({ throwOnError: true })
    if (!metas) return []
    const configurableTypes = metas.filter((m) => !m.configless)
    const results = await Promise.all(
      configurableTypes.map(async (meta) => {
        try {
          const { data: config } = await getBotsByIdChannelByPlatform({ path: { id: botIdRef.value, platform: meta.type ?? '' }, throwOnError: true })
          return { meta, config: config ?? null, configured: true } as BotChannelItem
        } catch {
          return { meta, config: null, configured: false } as BotChannelItem
        }
      })
    )
    return results
  },
  enabled: () => !!botIdRef.value,
})

const selectedType = ref<string | null>(null)

const allChannels = computed<BotChannelItem[]>(() => channels.value ?? [])
const configuredChannels = computed(() => allChannels.value.filter((c) => c.configured))
const unconfiguredChannels = computed(() => allChannels.value.filter((c) => !c.configured))

const selectedItem = computed(() => allChannels.value.find((c) => c.meta.type === selectedType.value) ?? null)

function openPlatform(item: BotChannelItem) {
  selectedType.value = item.meta.type ?? ''
  openDetail()
}

// Adding picks an as-yet-unconfigured type and drops straight into its blank form.
function addChannel(type: string) {
  addOpen.value = false
  selectedType.value = type
  openDetail()
}

function handleSaved() {
  refetch()
}

function handleDeleted() {
  refetch()
  backToList()
}

// If the open platform disappears after a refetch, fall back to the list rather
// than stranding the user on a detail with nothing behind it.
watch(allChannels, () => {
  if (view.value === 'detail' && selectedType.value && !selectedItem.value) {
    backToList()
  }
})
</script>
