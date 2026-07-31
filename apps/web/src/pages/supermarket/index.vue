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
          :placeholder="$t('supermarket.searchPlaceholder')"
          class="pl-9"
          @keydown.enter="applySearch"
        />
      </div>

      <!-- Tabs: Plugins / Skills -->
      <Tabs
        default-value="plugins"
        class="w-full"
      >
        <TabsList>
          <TabsTrigger value="plugins">
            {{ $t('supermarket.pluginSection') }}
          </TabsTrigger>
          <TabsTrigger value="skills">
            {{ $t('supermarket.skillsSection') }}
          </TabsTrigger>
        </TabsList>

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

      <!-- Install Dialogs -->
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
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Search, Github } from 'lucide-vue-next'
import { Button, InlineLoadingRow, Input, PageShell, Tabs, TabsContent, TabsList, TabsTrigger } from '@felinic/ui'
import {
  getSupermarketPlugins,
  getSupermarketSkills,
  type HandlersSupermarketSkillEntry,
  type PluginsManifest,
} from '@memohai/sdk'
import { toast } from '@felinic/ui'
import { resolveApiErrorMessage } from '@/utils/api-error'
import PluginCard from './components/plugin-card.vue'
import SkillCard from './components/skill-card.vue'
import InstallPluginDialog from './components/install-plugin-dialog.vue'
import InstallSkillDialog from './components/install-skill-dialog.vue'

const { t } = useI18n()

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
