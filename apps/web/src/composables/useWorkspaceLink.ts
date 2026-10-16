import { toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { classifyWorkspaceLink } from '@/utils/workspace-link'

// Workspace addresses cannot be opened by the user's OS browser, including
// modifier/middle clicks. Keep them in their owning workspace or explain why
// that workspace is unavailable instead of silently navigating elsewhere.
export function useWorkspaceLink() {
  const tabs = useWorkspaceTabsStore()
  const { t } = useI18n()
  return (event: MouseEvent, href: string) => {
    const link = classifyWorkspaceLink(href)
    if (event.button > 1 || (link?.kind !== 'file' && link?.kind !== 'browser')) return
    event.preventDefault()
    const opened = link.kind === 'file'
      ? tabs.openFile(link.path)
      : tabs.openBrowserAt(link.address)
    if (!opened) toast.error(t('chat.workspaceLinkUnavailable'))
  }
}
