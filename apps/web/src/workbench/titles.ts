import i18n from '@/i18n'
import { routeConversationLabel } from '@/store/chat-list.utils'
import type { WorkbenchTabTarget } from './types'

export interface SessionTitleLookup {
  knownSessionSummary: (sessionId: string) => {
    id?: string
    title?: string
    route_metadata?: Record<string, unknown>
  } | null | undefined
  isSessionTentative: (sessionId: string) => boolean
}

export function workbenchFileBaseName(filePath: string): string {
  const idx = filePath.lastIndexOf('/')
  return idx >= 0 ? filePath.slice(idx + 1) : filePath
}

export function resolveWorkbenchTabTitle(
  target: WorkbenchTabTarget,
  sessions?: SessionTitleLookup,
): string {
  // Explicitly track locale ref so Vue computed callers re-evaluate on hot locale switch.
  void i18n.global.locale.value
  const t = i18n.global.t

  switch (target.kind) {
    case 'chat': {
      const sid = target.sessionId?.trim() || null
      if (!sid || sessions?.isSessionTentative(sid)) {
        return t('chat.newSession').trim() || 'New Session'
      }
      const summary = sessions?.knownSessionSummary(sid)
      if (summary) {
        return (summary.title ?? '').trim()
          || routeConversationLabel(summary as never)
          || t('chat.untitledSession')
      }
      return target.fallbackTitle?.trim() || t('chat.untitledSession')
    }
    case 'file':
      return workbenchFileBaseName(target.filePath)
    case 'preview':
      return t('chat.previewTab', { name: workbenchFileBaseName(target.filePath) })
    case 'asset':
      return target.name.trim() || 'file'
    case 'terminal': {
      const live = target.shellTitle?.trim()
      if (live) return live
      const prefix = t('bots.terminal.defaultTabLabel').trim() || 'Terminal'
      return `${prefix} ${target.instance}`
    }
    case 'browser': {
      if (target.customTitle && target.address.trim()) {
        return target.address.trim()
      }
      const prefix = t('chat.tabs.browser').trim() || 'Browser'
      return `${prefix} ${target.instance}`
    }
    case 'display':
      return t('chat.display.title')
    case 'schedule': {
      const named = target.customTitle?.trim()
      if (named) return named
      return t('chat.tabs.schedule').trim() || 'Schedule'
    }
  }
}
