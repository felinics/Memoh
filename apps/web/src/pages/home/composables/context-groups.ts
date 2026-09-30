import type { ContextCategoryId, ContextCategoryStat } from './context-categories'

// The eight runtime categories answer "how was the prompt assembled"; users
// only need "what is taking the room", so the context views fold them into
// three groups.
export type ContextGroupId = 'instructions' | 'tools' | 'conversation'

export interface ContextGroup {
  id: ContextGroupId
  tokens: number
  colorClass: string
  categories: ContextCategoryStat[]
}

const GROUP_OF: Record<ContextCategoryId, ContextGroupId> = {
  system: 'instructions',
  rules: 'instructions',
  tools: 'tools',
  skills: 'tools',
  memory: 'conversation',
  summary: 'conversation',
  conversation: 'conversation',
  other: 'conversation',
}

// Data-viz series tokens; chart-4/5 stay free because warning and destructive
// already mean context pressure in these views.
const GROUP_COLOR: Record<ContextGroupId, string> = {
  instructions: 'bg-chart-2',
  tools: 'bg-chart-3',
  conversation: 'bg-chart-1',
}

const GROUP_ORDER: ContextGroupId[] = ['instructions', 'tools', 'conversation']

export function groupContextCategories(categories: ContextCategoryStat[] | undefined): ContextGroup[] {
  const byGroup = new Map<ContextGroupId, ContextCategoryStat[]>()
  for (const category of categories ?? []) {
    const id = GROUP_OF[category.id]
    byGroup.set(id, [...(byGroup.get(id) ?? []), category])
  }
  return GROUP_ORDER
    .map(id => {
      const members = byGroup.get(id) ?? []
      return { id, tokens: members.reduce((sum, c) => sum + c.tokens, 0), colorClass: GROUP_COLOR[id], categories: members }
    })
    .filter(group => group.tokens > 0)
}
