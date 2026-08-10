<template>
  <!-- Same open/close motion as the bot detail surface, so the two detail
       pages enter and leave identically. -->
  <Transition
    appear
    enter-active-class="transition-all duration-[90ms] ease-out"
    enter-from-class="opacity-0 translate-x-2.5"
    leave-active-class="transition-all duration-[40ms] ease-in"
    leave-to-class="opacity-0 translate-x-2.5"
    @after-leave="onAfterLeave"
  >
    <section
      v-if="show"
      class="absolute inset-0 flex flex-col bg-background"
      data-desktop-window-layer
    >
      <div class="relative flex-1">
        <MasterDetailSidebarLayout flush>
          <template #sidebar-header>
            <DetailNavSidebar
              :back-label="t('projects.title')"
              :groups="navGroups"
              :active-value="activeTab"
              :mac-traffic-reserve="macTrafficReserve"
              :searchable="false"
              @back="goBack"
              @select="(v: string) => (activeTab = v)"
            >
              <template #identity>
                <div class="flex items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card p-3">
                  <div class="flex size-10 shrink-0 items-center justify-center rounded-[var(--radius-menu)] bg-accent-gray-soft-active">
                    <FolderKanban class="size-5 text-foreground" />
                  </div>
                  <div class="flex min-w-0 flex-1 flex-col justify-center">
                    <h2 class="truncate text-sm font-semibold text-foreground">
                      {{ project?.name || projectId }}
                    </h2>
                    <p
                      v-if="project"
                      class="mt-1 truncate text-caption text-muted-foreground"
                    >
                      {{ t('projects.openIssues', { count: project.open_issue_count ?? 0 }) }}
                    </p>
                  </div>
                </div>
              </template>
            </DetailNavSidebar>
          </template>

          <template #sidebar-content />
          <template #sidebar-footer />

          <template #detail>
            <div class="absolute inset-0 overflow-y-auto bg-background [scrollbar-gutter:stable]">
              <div
                v-if="macTrafficReserve"
                class="h-8 shrink-0 [-webkit-app-region:drag]"
              />
              <div class="px-4 pb-4 pt-4 md:px-6">
                <PageShell
                  variant="tab"
                  :title="t(`projects.tabs.${activeTab}`)"
                >
                  <SettingsSection>
                    <p class="px-4 py-6 text-body text-muted-foreground">
                      {{ t(`projects.tabPlaceholder.${activeTab}`) }}
                    </p>
                  </SettingsSection>
                </PageShell>
              </div>
            </div>
          </template>
        </MasterDetailSidebarLayout>
      </div>
    </section>
  </Transition>
</template>

<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import { useRoute, useRouter, onBeforeRouteLeave } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { FolderKanban, LayoutDashboard, Users } from 'lucide-vue-next'
import { PageShell, SettingsSection, toast } from '@felinic/ui'
import { getProjectsByProjectId, type ProjectProject } from '@memohai/sdk'
import MasterDetailSidebarLayout from '@/components/master-detail-sidebar-layout/index.vue'
import DetailNavSidebar from '@/components/detail-nav-sidebar/index.vue'
import { DesktopShellKey } from '@/lib/desktop-shell'
import { useSyncedQueryParam } from '@/composables/useSyncedQueryParam'
import { resolveApiErrorMessage } from '@/utils/api-error'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const projectId = computed(() => (route.params.projectId as string | undefined) ?? '')
const project = ref<ProjectProject | null>(null)

const activeTab = useSyncedQueryParam('tab', 'overview')

const navGroups = computed(() => [
  {
    key: 'main',
    items: [
      { value: 'overview', label: 'projects.tabs.overview', icon: LayoutDashboard },
      { value: 'member', label: 'projects.tabs.member', icon: Users },
    ],
  },
])

const desktopShell = inject(DesktopShellKey, false)
const macTrafficReserve = computed(() =>
  desktopShell
  && typeof navigator !== 'undefined'
  && navigator.platform.toLowerCase().includes('mac'),
)

// Hold navigation until the leave transition plays, mirroring bot detail.
const show = ref(true)
let leaveResolver: (() => void) | null = null

function onAfterLeave() {
  leaveResolver?.()
  leaveResolver = null
}

onBeforeRouteLeave(() => new Promise<void>((resolve) => {
  leaveResolver = resolve
  show.value = false
}))

function goBack() {
  void router.push({ name: 'projects' })
}

async function load() {
  const id = projectId.value
  if (!id) return
  try {
    const { data } = await getProjectsByProjectId({
      path: { project_id: id },
      throwOnError: true,
    })
    project.value = data ?? null
  } catch (error) {
    project.value = null
    toast.error(resolveApiErrorMessage(error, t('projects.loadFailed')))
  }
}

watch(projectId, () => { void load() }, { immediate: true })
</script>
