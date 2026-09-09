import { h } from 'vue'
import type { LucideIcon } from 'lucide-vue-next'
import { createLucideIcon, Paperclip, SquareArrowOutUpRight, Plus, ChevronDown, ChevronLeft, ChevronRight, PanelLeftOpen, PanelLeftClose, Terminal, Globe, Columns2, Rows2 } from 'lucide-vue-next'

// Keep the default visual weight consistent across authored and Lucide glyphs.
function withUiStroke(icon: LucideIcon): LucideIcon {
  return (props, { attrs, slots }) => h(icon, { strokeWidth: 1.75, ...attrs, ...props }, slots)
}

// Heroicons cog-8-tooth; path data preserved from optimized/24/outline.
// MIT license: ./LICENSE.heroicons. Host components choose the rendered size.
export const SettingsIcon = withUiStroke(createLucideIcon('Settings', [
  ['path', { d: 'M10.343 3.94c.09-.542.56-.94 1.11-.94h1.093c.55 0 1.02.398 1.11.94l.149.894c.07.424.384.764.78.93.398.164.855.142 1.205-.108l.737-.527a1.125 1.125 0 0 1 1.45.12l.773.774c.39.389.44 1.002.12 1.45l-.527.737c-.25.35-.272.806-.107 1.204.165.397.505.71.93.78l.893.15c.543.09.94.559.94 1.109v1.094c0 .55-.397 1.02-.94 1.11l-.894.149c-.424.07-.764.383-.929.78-.165.398-.143.854.107 1.204l.527.738c.32.447.269 1.06-.12 1.45l-.774.773a1.125 1.125 0 0 1-1.449.12l-.738-.527c-.35-.25-.806-.272-1.203-.107-.398.165-.71.505-.781.929l-.149.894c-.09.542-.56.94-1.11.94h-1.094c-.55 0-1.019-.398-1.11-.94l-.148-.894c-.071-.424-.384-.764-.781-.93-.398-.164-.854-.142-1.204.108l-.738.527c-.447.32-1.06.269-1.45-.12l-.773-.774a1.125 1.125 0 0 1-.12-1.45l.527-.737c.25-.35.272-.806.108-1.204-.165-.397-.506-.71-.93-.78l-.894-.15c-.542-.09-.94-.56-.94-1.109v-1.094c0-.55.398-1.02.94-1.11l.894-.149c.424-.07.765-.383.93-.78.165-.398.143-.854-.108-1.204l-.526-.738a1.125 1.125 0 0 1 .12-1.45l.773-.773a1.125 1.125 0 0 1 1.45-.12l.737.527c.35.25.807.272 1.204.107.397-.165.71-.505.78-.929l.15-.894Z' }],
  ['path', { d: 'M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z' }],
]))

// Lucide Plug 0.562.0 (ISC); canonical diagonal direction in SVG coordinates.
export const ConnectorIcon = withUiStroke(createLucideIcon('Connector', [
  ['path', { d: 'M12 22v-5', transform: 'rotate(45 12 12)' }],
  ['path', { d: 'M15 8V2', transform: 'rotate(45 12 12)' }],
  ['path', { d: 'M17 8a1 1 0 0 1 1 1v4a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V9a1 1 0 0 1 1-1z', transform: 'rotate(45 12 12)' }],
  ['path', { d: 'M9 8V2', transform: 'rotate(45 12 12)' }],
]))

// Consumers choose semantics; glyph direction and geometry stay in this module.
export const UploadIcon = withUiStroke(Paperclip)
// Monitor and cloud silhouette, composed in the shared 24-unit icon frame.
// Semantics: this marks the bot's NATIVE workspace ("Cloud Computer" in the
// composer target selector — hosted by Memoh, as opposed to a user's remote
// computer, which uses the plain monitor ComputerIcon below). The name is
// about the hosting side, not about "cloud storage".
export const CloudIcon = withUiStroke(createLucideIcon('CloudComputer', [
  ['path', { d: 'M8 17H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v7M8 21h3M10 17v4' }],
  ['path', { d: 'M16 21a4 4 0 1 1 3.77-5.33H20a2.67 2.67 0 0 1 0 5.33Z' }],
]))
// Adapted from Lucide Monitor (ISC). Inset geometry and a shorter base reduce
// the closed silhouette's visual weight without scaling the shared stroke.
export const ComputerIcon = withUiStroke(createLucideIcon('Computer', [
  ['rect', { width: '18', height: '12.6', x: '3', y: '3.9', rx: '1.8' }],
  ['path', { d: 'M9 20.1h6M12 16.5v3.6' }],
]))

export const TerminalIcon = withUiStroke(Terminal)
export const BrowserIcon = withUiStroke(Globe)
export const SplitRightIcon = withUiStroke(Columns2)
export const SplitDownIcon = withUiStroke(Rows2)

export const AddIcon = withUiStroke(Plus)
export const ExpandIcon = withUiStroke(ChevronDown)

export const BackIcon = withUiStroke(ChevronLeft)
export const ForwardIcon = withUiStroke(ChevronRight)
export const SidebarOpenIcon = withUiStroke(PanelLeftOpen)
export const SidebarCloseIcon = withUiStroke(PanelLeftClose)

// The window outline anchors the opening gesture at small control sizes.
export const OpenInTabIcon = withUiStroke(SquareArrowOutUpRight)
