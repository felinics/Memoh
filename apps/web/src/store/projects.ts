import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { useLocalStorage } from '@vueuse/core'
import {
  deleteProjectsByProjectId,
  deleteProjectsByProjectIdNodesByNodeId,
  getProjects,
  getProjectsByProjectIdTree,
  patchProjectsByProjectId,
  patchProjectsByProjectIdNodesByNodeId,
  postProjects,
  postProjectsByProjectIdNodes,
  type ProjectProject,
  type ProjectTreeNode,
} from '@memohai/sdk'

// Team-level Projects navigation state. Deliberately SEPARATE from
// workspace-tabs: the dockview layout is Record<botId, …> (per-bot on
// purpose), but a Project is a team-level entity — the navigation panel, its
// expansion state and its loaded trees must survive a bot switch untouched.
// Only the opened TABS live in the per-bot layout (accepted debt, see the
// design doc §5.4).
export const useProjectsStore = defineStore('projects', () => {
  // ---- persisted panel UI state (global, not per-bot, not per-tab) ---------
  const panelOpen = useLocalStorage('projects-panel-open', false)
  const panelWidth = useLocalStorage('projects-panel-width', 288)
  // Expanded tree rows. Projects and doc nodes share one key space: project
  // ids and node ids are both UUIDs and never collide.
  const expandedIds = useLocalStorage<string[]>('projects-panel-expanded', [])

  // ---- server data ---------------------------------------------------------
  const projects = ref<ProjectProject[]>([])
  const projectsLoaded = ref(false)
  const projectsLoading = ref(false)
  // Doc trees keyed by project id; loaded lazily on first expansion and
  // refreshed after any structural mutation.
  const trees = ref<Record<string, ProjectTreeNode[]>>({})
  const treeLoading = ref<Record<string, boolean>>({})

  const expandedSet = computed(() => new Set(expandedIds.value))

  function isExpanded(id: string): boolean {
    return expandedSet.value.has(id)
  }

  function setExpanded(id: string, expanded: boolean) {
    const has = expandedSet.value.has(id)
    if (expanded === has) return
    expandedIds.value = expanded
      ? [...expandedIds.value, id]
      : expandedIds.value.filter(v => v !== id)
  }

  function toggleExpanded(id: string) {
    setExpanded(id, !isExpanded(id))
  }

  async function loadProjects(force = false) {
    if (projectsLoading.value) return
    if (projectsLoaded.value && !force) return
    projectsLoading.value = true
    try {
      const { data } = await getProjects({ throwOnError: true })
      projects.value = data ?? []
      projectsLoaded.value = true
    } finally {
      projectsLoading.value = false
    }
  }

  async function loadTree(projectId: string, force = false) {
    if (treeLoading.value[projectId]) return
    if (trees.value[projectId] && !force) return
    treeLoading.value = { ...treeLoading.value, [projectId]: true }
    try {
      const { data } = await getProjectsByProjectIdTree({
        path: { project_id: projectId },
        throwOnError: true,
      })
      trees.value = { ...trees.value, [projectId]: data ?? [] }
    } finally {
      treeLoading.value = { ...treeLoading.value, [projectId]: false }
    }
  }

  // Children of one tree level, ordered by rank (ties broken by id — same
  // ordering contract as the backend queries).
  function childrenOf(projectId: string, parentId: string | null): ProjectTreeNode[] {
    const tree = trees.value[projectId] ?? []
    return tree
      .filter(node => (node.parent_id || null) === parentId)
      .sort((a, b) => {
        const ra = a.rank ?? ''
        const rb = b.rank ?? ''
        if (ra !== rb) return ra < rb ? -1 : 1
        return (a.id ?? '') < (b.id ?? '') ? -1 : 1
      })
  }

  async function createProject(name: string, description = ''): Promise<ProjectProject> {
    const { data } = await postProjects({
      body: { name, description },
      throwOnError: true,
    })
    await loadProjects(true)
    return data
  }

  async function renameProject(projectId: string, name: string) {
    await patchProjectsByProjectId({
      path: { project_id: projectId },
      body: { name, description: null },
      throwOnError: true,
    })
    await loadProjects(true)
  }

  async function deleteProject(projectId: string) {
    await deleteProjectsByProjectId({
      path: { project_id: projectId },
      throwOnError: true,
    })
    const next = { ...trees.value }
    delete next[projectId]
    trees.value = next
    await loadProjects(true)
  }

  async function createDoc(projectId: string, title: string, parentId?: string): Promise<string | undefined> {
    const { data } = await postProjectsByProjectIdNodes({
      path: { project_id: projectId },
      body: {
        type: 'doc',
        title,
        body: '',
        parent_id: parentId ?? null,
        status: '',
      },
      throwOnError: true,
    })
    await loadTree(projectId, true)
    return data?.node?.id
  }

  // Rename goes through the content endpoint (title rides the content
  // optimistic lock). The tree row carries the version the panel last saw;
  // a 409 here simply refreshes the tree — the caller retries on fresh data.
  async function renameDoc(projectId: string, nodeId: string, title: string, expectedVersion: number) {
    try {
      await patchProjectsByProjectIdNodesByNodeId({
        path: { project_id: projectId, node_id: nodeId },
        body: { title, body: null, expected_version: expectedVersion },
        throwOnError: true,
      })
    } finally {
      await loadTree(projectId, true)
    }
  }

  async function deleteNode(projectId: string, nodeId: string) {
    await deleteProjectsByProjectIdNodesByNodeId({
      path: { project_id: projectId, node_id: nodeId },
      throwOnError: true,
    })
    await loadTree(projectId, true)
  }

  return {
    panelOpen,
    panelWidth,
    expandedIds,
    projects,
    projectsLoaded,
    projectsLoading,
    trees,
    treeLoading,
    isExpanded,
    setExpanded,
    toggleExpanded,
    loadProjects,
    loadTree,
    childrenOf,
    createProject,
    renameProject,
    deleteProject,
    createDoc,
    renameDoc,
    deleteNode,
  }
})
