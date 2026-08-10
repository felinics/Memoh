<template>
  <PageShell
    :title="t('projects.title')"
    width="lg"
  >
    <template #actions>
      <Button @click="router.push({ name: 'project-new' })">
        <Plus />
        {{ t('projects.newProject') }}
      </Button>
    </template>

    <!-- Loading holds the grid's shape so nothing jumps when data lands. -->
    <div
      v-if="isLoading"
      :class="gridClass"
    >
      <Skeleton
        v-for="i in 3"
        :key="i"
        class="h-32 rounded-xl"
      />
    </div>

    <!-- Empty keeps the populated frame: the same card surface, message
         inside, one guiding action. -->
    <SettingsSection v-else-if="projects.length === 0">
      <div class="flex flex-col items-center gap-3 py-12 text-center">
        <p class="text-control font-medium text-foreground">
          {{ t('projects.emptyTitle') }}
        </p>
        <p class="max-w-sm text-body text-muted-foreground">
          {{ t('projects.emptyHint') }}
        </p>
        <Button
          variant="outline"
          @click="router.push({ name: 'project-new' })"
        >
          <Plus />
          {{ t('projects.newProject') }}
        </Button>
      </div>
    </SettingsSection>

    <div
      v-else
      :class="gridClass"
    >
      <ProjectCard
        v-for="project in projects"
        :key="project.id"
        :project="project"
        @open="openProject(project)"
      />
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Plus } from 'lucide-vue-next'
import { Button, PageShell, SettingsSection, Skeleton } from '@felinic/ui'
import { useQuery } from '@pinia/colada'
import { getProjects, type ProjectProject } from '@memohai/sdk'
import ProjectCard from './components/project-card.vue'

const { t } = useI18n()
const router = useRouter()

// Three across at the page's widest, stepping down with the pane so a narrow
// window reflows instead of squeezing.
const gridClass = 'grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3'

const { data, status } = useQuery({
  key: () => ['projects'],
  query: async () => {
    const { data } = await getProjects({ throwOnError: true })
    return data ?? []
  },
})

const isLoading = computed(() => status.value === 'loading')
const projects = computed(() => data.value ?? [])

function openProject(project: ProjectProject) {
  if (!project.id) return
  void router.push({ name: 'project-detail', params: { projectId: project.id } })
}
</script>
