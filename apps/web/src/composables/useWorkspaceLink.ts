import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { tryParseLocalhostHref } from '@/utils/localhost-link'

// Markdown and plain-text questions share the same workspace URL routing.
export function useWorkspaceLink() {
  const tabs = useWorkspaceTabsStore()
  return (event: MouseEvent, href: string) => {
    const parsed = tryParseLocalhostHref(href)
    if (!parsed || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    if (!tabs.openBrowserAt(parsed.display)) {
      window.open(href, '_blank', 'noopener')
    }
  }
}
