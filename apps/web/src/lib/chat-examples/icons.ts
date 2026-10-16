import type { Component } from 'vue'
import {
  Brain,
  CalendarDays,
  ChartPie,
  Lightbulb,
  Link2Off,
  Mail,
  MailOpen,
  MessagesSquare,
  Newspaper,
  NotebookPen,
  PenLine,
  Search,
  SquareTerminal,
  TrendingDown,
  Users,
} from 'lucide-vue-next'

/**
 * Icon keys an example may reference. Examples carry a string key instead of
 * a component so the catalog stays serializable for a future backend payload.
 */
export const CHAT_EXAMPLE_ICONS: Record<string, Component> = {
  'brain': Brain,
  'calendar-days': CalendarDays,
  'chart-pie': ChartPie,
  'lightbulb': Lightbulb,
  'link-2-off': Link2Off,
  'mail': Mail,
  'mail-open': MailOpen,
  'messages-square': MessagesSquare,
  'newspaper': Newspaper,
  'notebook-pen': NotebookPen,
  'pen-line': PenLine,
  'search': Search,
  'square-terminal': SquareTerminal,
  'trending-down': TrendingDown,
  'users': Users,
}

export const FALLBACK_CHAT_EXAMPLE_ICON: Component = Lightbulb

export function chatExampleIcon(key: string): Component {
  return CHAT_EXAMPLE_ICONS[key] ?? FALLBACK_CHAT_EXAMPLE_ICON
}
