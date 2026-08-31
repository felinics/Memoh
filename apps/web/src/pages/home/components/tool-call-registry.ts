import type { Component } from 'vue'
import {
  Activity,
  AppWindow,
  ArrowLeft,
  ArrowRight,
  AudioLines,
  Bot,
  Boxes,
  Braces,
  Brain,
  Cable,
  Calendar,
  CalendarCog,
  CalendarMinus,
  CalendarPlus,
  Camera,
  ChevronDown,
  Code,
  Eye,
  FilePen,
  FilePlus2,
  FileText,
  Film,
  FolderOpen,
  FolderTree,
  Focus,
  Globe,
  Heading,
  Image as ImageIcon,
  ImagePlus,
  Inbox,
  Keyboard,
  Link,
  ListChecks,
  Mail,
  MailOpen,
  MailPlus,
  MessageSquareReply,
  MessageSquareText,
  MessagesSquare,
  Monitor,
  MousePointer2,
  MousePointerClick,
  Move,
  MoveDown,
  MoveLeft,
  MoveRight,
  MoveUp,
  MoveVertical,
  Paperclip,
  Plug,
  Plus,
  Power,
  PowerOff,
  RotateCw,
  ScanEye,
  Search,
  SearchCheck,
  Send,
  ShieldQuestion,
  Smile,
  Sparkles,
  Square,
  SquareCheck,
  SquareTerminal,
  Split,
  TextCursorInput,
  Timer,
  Unplug,
  Upload,
  Users,
  Volume2,
  Workflow,
  Wrench,
  X,
} from 'lucide-vue-next'
import type { ToolCallBlock } from '@/store/chat-list'
import ToolCallDetailBrowser from './tool-call-detail-browser.vue'
import ToolCallDetailApplyPatch from './tool-call-detail-apply-patch.vue'
import ToolCallDetailComputer from './tool-call-detail-computer.vue'
import ToolCallDetailContacts from './tool-call-detail-contacts.vue'
import ToolCallDetailEdit from './tool-call-detail-edit.vue'
import ToolCallDetailEmailAccounts from './tool-call-detail-email-accounts.vue'
import ToolCallDetailEmailList from './tool-call-detail-email-list.vue'
import ToolCallDetailEmailRead from './tool-call-detail-email-read.vue'
import ToolCallDetailExec from './tool-call-detail-exec.vue'
import ToolCallDetailImage from './tool-call-detail-image.vue'
import ToolCallDetailMemory from './tool-call-detail-memory.vue'
import ToolCallDetailOutput from './tool-call-detail-output.vue'
import ToolCallDetailRemoteSession from './tool-call-detail-remote-session.vue'
import ToolCallDetailSchedule from './tool-call-detail-schedule.vue'
import ToolCallDetailSend from './tool-call-detail-send.vue'
import ToolCallDetailSpawn from './tool-call-detail-spawn.vue'
import ToolCallDetailWebFetch from './tool-call-detail-web-fetch.vue'
import ToolCallDetailWebSearch from './tool-call-detail-web-search.vue'
import ToolCallDetailWrite from './tool-call-detail-write.vue'
import { isGuiToolName } from '@/utils/gui-tools'

export interface ToolDisplay {
  icon: Component
  actionKey: string
  actionParams?: Record<string, unknown>
  target: string
  fullTarget?: string
  detail?: Component
  isError?: boolean
  // Non-zero exit of a finished exec; the row renders it through i18n.
  exitCode?: number
  expandable?: boolean
  defaultOpen?: boolean
  diffAdd?: number
  diffRemove?: number
  hideAction?: boolean
  // 'card' = output/diff/file content in a grayscale card; 'inline' = a
  // half-embedded key:value list (params), no card. Defaults to 'card'.
  detailVariant?: 'card' | 'inline'
}

const FILE_PATH_TOOLS = new Set(['read', 'write', 'edit', 'list'])

export function isFilePathTool(toolName: string): boolean {
  return FILE_PATH_TOOLS.has(toolName)
}

export function isDirPathTool(toolName: string): boolean {
  return toolName === 'list'
}

// Read-only / no-side-effect tools form an "explore" segment; everything else
// (write, edit, exec, send, schedule mutations, …) is an "action" segment.
// Consecutive tools of the same category are grouped together; reasoning rides
// along with whichever segment it sits next to.
const READONLY_TOOLS = new Set([
  'read', 'list', 'web_search', 'web_fetch', 'search_memory', 'search_messages',
  'list_execution_locations',
  'get_contacts', 'list_sessions', 'list_email', 'read_email', 'list_email_accounts',
  'list_schedule', 'get_schedule', 'list_skills', 'bg_status', 'list_background', 'get_background_status', 'wait', 'wait_until',
  'browser_observe', 'computer_observe',
])

export function isReadOnlyTool(toolName: string): boolean {
  return READONLY_TOOLS.has(toolName)
}

// Buckets used to summarize a finished multi-tool run in the collapsed group
// header. They live here, next to the tool catalog itself, so the header and
// the per-row registry cannot drift apart as tools are added.
export type ToolBucket = 'browse' | 'edit' | 'run' | 'message' | 'schedule' | 'media' | 'agent' | 'other'

// Listed in the order the summary joins them ("Read 3 files · Ran 2 commands").
// The sets are disjoint, so this order is presentation only.
const BUCKETS: Array<[ToolBucket, Set<string>]> = [
  ['browse', new Set([
    'read', 'list', 'web_search', 'web_fetch', 'search_memory', 'search_messages', 'get_messages',
    'get_contacts', 'list_sessions', 'list_email', 'read_email', 'list_email_accounts',
    'list_schedule', 'get_schedule', 'list_skills', 'list_models', 'list_workdirs',
    'list_acp_agents', 'list_execution_locations',
  ])],
  ['edit', new Set(['write', 'edit', 'apply_patch'])],
  ['run', new Set(['exec'])],
  ['message', new Set(['send', 'react', 'send_email', 'speak'])],
  ['schedule', new Set(['create_schedule', 'update_schedule', 'delete_schedule'])],
  ['media', new Set(['generate_image', 'generate_video', 'transcribe_audio'])],
  ['agent', new Set(['spawn_agent', 'send_message', 'list_agents'])],
]

export const SUMMARY_BUCKET_ORDER: ToolBucket[] = BUCKETS.map(([bucket]) => bucket)

export function toolBucket(toolName: string): ToolBucket {
  for (const [bucket, names] of BUCKETS) {
    if (names.has(toolName)) return bucket
  }
  return 'other'
}

// GUI tools (browser + computer) interleave read-only "observe" and
// side-effecting "action" calls as one continuous browsing activity. Splitting
// them on every observe↔action flip would strand each step in its own segment,
// so they share a single category and stay grouped together.
export type ToolSegmentCategory = 'explore' | 'action' | 'gui'

export function isGuiTool(toolName: string): boolean {
  return isGuiToolName(toolName)
}

// Segment category used to group consecutive tool calls in a process run.
export function toolSegmentCategory(toolName: string): ToolSegmentCategory {
  if (isGuiToolName(toolName)) return 'gui'
  return isReadOnlyTool(toolName) ? 'explore' : 'action'
}

// An image read (e.g. the path a browser/computer screenshot was saved to) is
// the model looking at a picture — an observation that belongs with the
// surrounding GUI activity, not a standalone file-exploration read. Folding it
// in keeps the "navigate → screenshot → look" loop as one browsing segment.
const IMAGE_READ_EXT = /\.(png|jpe?g|gif|webp|bmp|avif)$/i
const PDF_READ_EXT = /\.pdf$/i

export function toolSegmentCategoryForBlock(block: ToolCallBlock): ToolSegmentCategory {
  if (block.toolName === 'read' && IMAGE_READ_EXT.test(pickString(asObject(block.input), 'path'))) {
    return 'gui'
  }
  return toolSegmentCategory(block.toolName)
}

function asObject(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' ? (value as Record<string, unknown>) : {}
}

function pickString(obj: Record<string, unknown>, ...keys: string[]): string {
  for (const k of keys) {
    const v = obj[k]
    if (typeof v === 'string' && v.length > 0) return v
  }
  return ''
}

function firstQuestionText(input: Record<string, unknown>): string {
  const questions = input.questions
  if (!Array.isArray(questions) || questions.length === 0) return ''
  return pickString(asObject(questions[0]), 'text')
}

// A bounded read names its slice: an open-ended tail and an explicit range are
// different enough to warrant their own label.
function readRangeVariant(lineOffset: number, nLines: number): { actionKey: string; actionParams: Record<string, unknown> } {
  const from = lineOffset > 0 ? lineOffset : 1
  if (nLines > 0) return { actionKey: 'read_range', actionParams: { from, to: from + nLines - 1 } }
  return { actionKey: 'read_from', actionParams: { from } }
}

function firstQuestionKind(input: Record<string, unknown>): string {
  const questions = input.questions
  if (!Array.isArray(questions) || questions.length === 0) return ''
  return pickString(asObject(questions[0]), 'kind')
}

function pickNumber(obj: Record<string, unknown>, ...keys: string[]): number {
  for (const k of keys) {
    const v = obj[k]
    if (typeof v === 'number' && Number.isFinite(v)) return v
  }
  return 0
}

function truncate(s: string, max = 60): string {
  if (!s) return ''
  if (s.length <= max) return s
  return `${s.slice(0, max)}…`
}

// File-path tools show just the filename in the row; the absolute path becomes
// the tooltip via fullTarget.
function basename(path: string): string {
  if (!path) return ''
  const parts = path.split('/').filter(Boolean)
  return parts[parts.length - 1] ?? path
}

function firstLine(s: string, max = 80): string {
  if (!s) return ''
  const idx = s.indexOf('\n')
  const line = idx === -1 ? s : `${s.slice(0, idx)} …`
  return truncate(line, max)
}

function lineCount(s: string): number {
  if (!s) return 0
  const lines = s.split('\n')
  return lines.at(-1) === '' ? lines.length - 1 : lines.length
}

function resultObject(block: ToolCallBlock): Record<string, unknown> {
  const result = asObject(block.result)
  const sc = asObject(result.structuredContent)
  return Object.keys(sc).length > 0 ? sc : result
}

interface PatchFileTarget {
  operation: 'add' | 'modify' | 'delete'
  path: string
}

// Same marks the apply_patch detail panel uses, so the row tooltip and the
// expanded list read as one vocabulary — and neither needs translating.
const PATCH_OPERATION_MARK: Record<PatchFileTarget['operation'], string> = {
  add: 'A',
  modify: 'M',
  delete: 'D',
}

function normalizePatchOperation(value: unknown): PatchFileTarget['operation'] | '' {
  if (value === 'add' || value === 'added') return 'add'
  if (value === 'modify' || value === 'modified' || value === 'update') return 'modify'
  if (value === 'delete' || value === 'deleted') return 'delete'
  return ''
}

function patchFilesFromChanges(value: unknown): PatchFileTarget[] {
  if (!Array.isArray(value)) return []
  return value
    .map((item) => {
      const obj = asObject(item)
      const path = pickString(obj, 'path')
      const operation = normalizePatchOperation(asObject(obj.kind).type ?? obj.kind ?? obj.operation)
      return path && operation ? { operation, path } : null
    })
    .filter((item): item is PatchFileTarget => Boolean(item))
}

function patchFilesFromResult(block: ToolCallBlock): PatchFileTarget[] {
  const result = resultObject(block)
  const changes = patchFilesFromChanges(result.changes)
  if (changes.length > 0) return changes
  const rawFiles = result.files
  if (Array.isArray(rawFiles)) {
    return rawFiles
      .map((item) => {
        const obj = asObject(item)
        const path = pickString(obj, 'path')
        const operation = normalizePatchOperation(obj.operation)
        return path && operation ? { operation, path } : null
      })
      .filter((item): item is PatchFileTarget => Boolean(item))
  }

  const out: PatchFileTarget[] = []
  for (const [key, operation] of [
    ['added', 'add'],
    ['modified', 'modify'],
    ['deleted', 'delete'],
  ] as const) {
    const paths = result[key]
    if (!Array.isArray(paths)) continue
    for (const path of paths) {
      if (typeof path === 'string' && path) out.push({ operation, path })
    }
  }
  return out
}

function patchFilesFromInput(patch: string): PatchFileTarget[] {
  if (!patch) return []
  const out: PatchFileTarget[] = []
  for (const rawLine of patch.split('\n')) {
    const line = rawLine.trim()
    if (line.startsWith('*** Add File: ')) {
      const path = line.slice('*** Add File: '.length).trim()
      if (path) out.push({ operation: 'add', path })
    }
    else if (line.startsWith('*** Delete File: ')) {
      const path = line.slice('*** Delete File: '.length).trim()
      if (path) out.push({ operation: 'delete', path })
    }
    else if (line.startsWith('*** Update File: ')) {
      const path = line.slice('*** Update File: '.length).trim()
      if (path) out.push({ operation: 'modify', path })
    }
  }
  return out
}

function patchLineCounts(patch: string): { add: number; remove: number } {
  let add = 0
  let remove = 0
  for (const line of patch.split('\n')) {
    if (line.startsWith('+')) add++
    else if (line.startsWith('-') && !line.startsWith('***')) remove++
  }
  return { add, remove }
}

function changeLineCounts(value: unknown): { add: number; remove: number } {
  if (!Array.isArray(value)) return { add: 0, remove: 0 }
  let add = 0
  let remove = 0
  for (const item of value) {
    const obj = asObject(item)
    const diff = pickString(obj, 'diff')
    const operation = normalizePatchOperation(asObject(obj.kind).type ?? obj.kind ?? obj.operation)
    if (operation === 'add') add += lineCount(diff)
    else if (operation === 'delete') remove += lineCount(diff)
    else {
      const counts = patchLineCounts(diff)
      add += counts.add
      remove += counts.remove
    }
  }
  return { add, remove }
}

function hostnameOrUrl(url: string): string {
  if (!url) return ''
  try {
    const parsed = new URL(url)
    return parsed.hostname || url
  }
  catch {
    return url
  }
}

const WEB_FETCH_FORMATS = new Set(['markdown', 'json', 'xml', 'text'])

// Tools that can detach accept the flag under either spelling depending on how
// the call was serialized.
function isBackgroundInput(input: Record<string, unknown>): boolean {
  return input.run_in_background === true || input.runInBackground === true
}

// Forking inherits the parent's context and backgrounding detaches the run:
// two independent switches, four genuinely different outcomes.
function spawnAgentActionKey(fork: boolean, background: boolean): string {
  if (fork && background) return 'spawn_agent_fork_background'
  if (fork) return 'spawn_agent_fork'
  if (background) return 'spawn_agent_background'
  return 'spawn_agent'
}

// For a keystroke the key itself is the subject; a selector would say nothing.
function guiKeyTarget(action: string, input: Record<string, unknown>): string {
  if (action !== 'press' && action !== 'key' && action !== 'keydown' && action !== 'keyup') return ''
  return pickString(input, 'key')
}

// Flipping a schedule on or off, and replacing its whole execution block, are
// the two updates a user reads the row to spot.
function updateScheduleVariant(input: Record<string, unknown>): { icon: Component; actionKey: string } {
  if (input.enabled === true) return { icon: Power, actionKey: 'update_schedule_enable' }
  if (input.enabled === false) return { icon: PowerOff, actionKey: 'update_schedule_disable' }
  if (input.update_execution === true || input.updateExecution === true) {
    return { icon: CalendarCog, actionKey: 'update_schedule_execution' }
  }
  return { icon: CalendarCog, actionKey: 'update_schedule' }
}

// `send` accepts either a text shortcut or a structured message object; the
// row shows whichever one carries the body.
function sendText(input: Record<string, unknown>): string {
  const shortcut = pickString(input, 'text')
  if (shortcut) return shortcut
  const message = asObject(input.message)
  const structured = pickString(message, 'text')
  if (structured) return structured
  return pickString(input, 'message')
}

// Replying to a message and shipping files are separate acts from posting
// text, so they get their own verb instead of hiding in the params.
function sendVariant(input: Record<string, unknown>): { icon: Component; actionKey: string } {
  if (pickString(input, 'reply_to', 'replyTo')) return { icon: MessageSquareReply, actionKey: 'send_reply' }
  const attachments = input.attachments
  if (Array.isArray(attachments) && attachments.length > 0) {
    return { icon: Paperclip, actionKey: 'send_attachments' }
  }
  return { icon: Send, actionKey: 'send' }
}

// Compatibility aliases accepted by the backend browser/computer tools. The
// keyboard_* spellings reach the same insert-text path as `type`, so they read
// as typing rather than as an unlabeled action.
const GUI_ACTION_ALIASES: Record<string, string> = {
  dblclick: 'double_click',
  scrollintoview: 'scroll_into_view',
  keyboard_type: 'type',
  keyboard_inserttext: 'type',
}

function normalizeGuiAction(raw: string): string {
  const key = raw.trim().toLowerCase()
  return GUI_ACTION_ALIASES[key] ?? key
}

const BROWSER_ACTION_ICONS: Record<string, Component> = {
  navigate: Globe,
  click: MousePointerClick,
  double_click: MousePointerClick,
  focus: Focus,
  type: Keyboard,
  fill: TextCursorInput,
  press: Keyboard,
  hover: MousePointer2,
  select: ChevronDown,
  check: SquareCheck,
  uncheck: Square,
  scroll: MoveVertical,
  scroll_up: MoveUp,
  scroll_down: MoveDown,
  scroll_left: MoveLeft,
  scroll_right: MoveRight,
  scroll_into_view: MoveVertical,
  drag: Move,
  upload: Upload,
  wait: Timer,
  keydown: Keyboard,
  keyup: Keyboard,
  go_back: ArrowLeft,
  go_forward: ArrowRight,
  reload: RotateCw,
  tab_new: Plus,
  tab_select: AppWindow,
  tab_close: X,
}

const BROWSER_OBSERVE_ICONS: Record<string, Component> = {
  snapshot: ScanEye,
  get_content: FileText,
  screenshot_annotate: Camera,
  screenshot: Camera,
  get_html: Code,
  evaluate: Braces,
  get_url: Link,
  get_title: Heading,
  pdf: FileText,
  tab_list: AppWindow,
}

const COMPUTER_OBSERVE_ICONS: Record<string, Component> = {
  snapshot: ScanEye,
  screenshot: Camera,
}

const COMPUTER_ACTION_ICONS: Record<string, Component> = {
  click: MousePointerClick,
  click_right: MousePointerClick,
  click_middle: MousePointerClick,
  double_click: MousePointerClick,
  type: Keyboard,
  fill: TextCursorInput,
  key: Keyboard,
  scroll: MoveVertical,
  scroll_up: MoveUp,
  scroll_down: MoveDown,
  scroll_left: MoveLeft,
  scroll_right: MoveRight,
  drag: Move,
  wait: Timer,
  mouse_move: MousePointer2,
  pointer: MousePointer2,
}

const REMOTE_SESSION_ICONS: Record<string, Component> = {
  create: Plug,
  close: Unplug,
  status: Activity,
}

// Resolves a per-action icon and i18n action key. When the action is known the
// label comes from a nested namespace key (e.g. chat.tools.browserAction.click);
// unknown actions fall back to the tool's generic label with the raw action as
// a parameter.
function resolveGuiAction(
  icons: Record<string, Component>,
  namespace: string,
  fallbackIcon: Component,
  fallbackKey: string,
  rawAction: string,
  input: Record<string, unknown> = {},
): { icon: Component; actionKey: string; actionParams?: Record<string, unknown>; action: string } {
  const action = normalizeGuiAction(rawAction)
  if (icons[action]) {
    const variant = guiActionVariant(action, input)
    // A variant only wins when it has its own icon and label; an unknown
    // modifier value quietly keeps the plain action.
    const resolved = variant && icons[variant] ? variant : action
    return { icon: icons[resolved]!, actionKey: `${namespace}.${resolved}`, action }
  }
  return { icon: fallbackIcon, actionKey: fallbackKey, actionParams: { action: rawAction }, action }
}

const GUI_SCROLL_DIRECTIONS = new Set(['up', 'down', 'left', 'right'])
const GUI_NON_LEFT_BUTTONS = new Set(['right', 'middle'])

// Some GUI actions mean materially different things depending on one modifier
// argument: a right click is not a click, and a bare "Scroll" drops the single
// detail the user is reading the row for. Those get their own label; every
// other argument stays in the expanded detail.
function guiActionVariant(action: string, input: Record<string, unknown>): string {
  if (action === 'scroll') {
    const direction = pickString(input, 'direction').toLowerCase()
    return GUI_SCROLL_DIRECTIONS.has(direction) ? `scroll_${direction}` : ''
  }
  if (action === 'click') {
    const button = pickString(input, 'button').toLowerCase()
    return GUI_NON_LEFT_BUTTONS.has(button) ? `click_${button}` : ''
  }
  return ''
}

// A result the runtime marked as failed. Kept separate from the per-tool
// display so every tool — including ones with no dedicated case — renders its
// failures in destructive ink instead of looking like a clean call.
function isErrorResult(block: ToolCallBlock): boolean {
  const result = asObject(block.result)
  if (result.isError === true) return true
  return asObject(result.structuredContent).isError === true
}

// A non-zero exit is the one machine-readable failure detail worth carrying on
// the collapsed row; everything else stays in the expanded output.
function execExitCode(block: ToolCallBlock): number {
  return pickNumber(resultObject(block), 'exit_code', 'exitCode')
}

export function getToolDisplay(block: ToolCallBlock): ToolDisplay {
  const display = resolveToolDisplay(block)
  if (!block.done) return display
  const exitCode = block.toolName === 'exec' ? execExitCode(block) : 0
  if (!exitCode && !isErrorResult(block)) return display
  return { ...display, isError: true, exitCode: exitCode || undefined }
}

function resolveToolDisplay(block: ToolCallBlock): ToolDisplay {
  const input = asObject(block.input)

  switch (block.toolName) {
    case 'ask_user': {
      // pickString covers pre-v2 history where input was { question: "..." }.
      const question = block.userInput?.questions?.[0]?.text || firstQuestionText(input) || pickString(input, 'question')
      const showQuestionInBody = block.userInput?.status === 'pending'
      const kind = block.userInput?.questions?.[0]?.kind || firstQuestionKind(input)
      const isChoice = kind === 'single_select' || kind === 'multi_select'
      return {
        icon: isChoice ? ListChecks : TextCursorInput,
        actionKey: isChoice ? 'ask_user_choice' : 'ask_user',
        target: showQuestionInBody ? '' : truncate(question, 80),
        fullTarget: showQuestionInBody ? '' : question,
        expandable: true,
      }
    }
    case 'read': {
      const path = pickString(input, 'path')
      const lineOffset = pickNumber(input, 'line_offset', 'lineOffset')
      const nLines = pickNumber(input, 'n_lines', 'nLines')
      const partial = lineOffset > 1 || nLines > 0
      const variant = IMAGE_READ_EXT.test(path)
        ? { icon: ImageIcon, actionKey: 'read_image' }
        : PDF_READ_EXT.test(path)
          ? { icon: FileText, actionKey: 'read_document' }
          : partial
            ? { icon: FileText, ...readRangeVariant(lineOffset, nLines) }
            : { icon: FileText, actionKey: 'read' }
      return { ...variant, target: basename(path), fullTarget: path, detail: ToolCallDetailOutput }
    }
    case 'write': {
      const result = resultObject(block)
      const changes = patchFilesFromChanges(input.changes)
      const files = changes.length > 0 ? changes : patchFilesFromResult(block)
      if (files.length > 0) {
        const target = files.length === 1 ? basename(files[0]!.path) : `${files.length} files`
        const fullTarget = files.map(file => `${PATCH_OPERATION_MARK[file.operation]} ${file.path}`).join('\n')
        const counts = changeLineCounts(Array.isArray(input.changes) ? input.changes : result.changes)
        return {
          icon: FilePen,
          actionKey: 'write',
          target,
          fullTarget,
          detail: ToolCallDetailApplyPatch,
          defaultOpen: true,
          diffAdd: counts.add,
          diffRemove: counts.remove,
        }
      }
      const path = pickString(input, 'path')
      const content = pickString(input, 'content')
      const contentLineCount = pickNumber(input, 'content_line_count')
      return {
        icon: FilePlus2,
        actionKey: 'write',
        target: basename(path),
        fullTarget: path,
        detail: ToolCallDetailWrite,
        defaultOpen: false,
        diffAdd: contentLineCount || lineCount(content),
        hideAction: true,
      }
    }
    case 'edit': {
      const path = pickString(input, 'path')
      const oldText = pickString(input, 'old_text')
      const newText = pickString(input, 'new_text')
      return {
        icon: FilePen,
        actionKey: 'edit',
        target: basename(path),
        fullTarget: path,
        detail: ToolCallDetailEdit,
        diffAdd: lineCount(newText),
        diffRemove: lineCount(oldText),
      }
    }
    case 'apply_patch': {
      const patch = pickString(input, 'patch')
      const files = patchFilesFromResult(block)
      const fileTargets = files.length > 0 ? files : patchFilesFromInput(patch)
      const target = fileTargets.length === 1
        ? basename(fileTargets[0]!.path)
        : fileTargets.length > 1
          ? `${fileTargets.length} files`
          : ''
      const fullTarget = fileTargets
        .map(file => `${PATCH_OPERATION_MARK[file.operation]} ${file.path}`)
        .join('\n')
      const counts = patchLineCounts(patch)
      return {
        icon: FilePen,
        actionKey: 'apply_patch',
        target,
        fullTarget,
        detail: ToolCallDetailApplyPatch,
        defaultOpen: true,
        diffAdd: counts.add,
        diffRemove: counts.remove,
      }
    }
    case 'list': {
      const path = pickString(input, 'path')
      return { icon: FolderOpen, actionKey: 'list', target: basename(path), fullTarget: path, detail: ToolCallDetailOutput }
    }
    case 'list_execution_locations':
      return { icon: Monitor, actionKey: 'list_execution_locations', target: '', expandable: true, detailVariant: 'inline' }
    case 'exec': {
      const cmd = pickString(input, 'command')
      const background = input.run_in_background === true || input.runInBackground === true
      return {
        icon: SquareTerminal,
        actionKey: background ? 'exec_background' : 'exec',
        target: firstLine(cmd, 80),
        fullTarget: cmd,
        detail: ToolCallDetailExec,
      }
    }
    case 'bg_status': {
      const action = pickString(input, 'action') || 'list'
      return { icon: ListChecks, actionKey: 'bg_status', target: action, expandable: true, detailVariant: 'inline' }
    }
    case 'list_background':
      return { icon: ListChecks, actionKey: 'list_background', target: '', expandable: true, detailVariant: 'inline' }
    case 'get_background_status': {
      const taskId = pickString(input, 'task_id', 'taskId')
      return { icon: SearchCheck, actionKey: 'get_background_status', target: taskId, expandable: true, detailVariant: 'inline' }
    }
    case 'kill_background': {
      const taskId = pickString(input, 'task_id', 'taskId')
      return { icon: X, actionKey: 'kill_background', target: taskId, expandable: true, detailVariant: 'inline' }
    }
    case 'wait': {
      const duration = pickNumber(input, 'duration')
      return { icon: Timer, actionKey: 'wait', target: duration ? `${duration}s` : '', expandable: true, detailVariant: 'inline' }
    }
    case 'wait_until': {
      const taskId = pickString(input, 'task_id', 'taskId')
      return { icon: Timer, actionKey: 'wait_until', target: taskId, expandable: true, detailVariant: 'inline' }
    }
    case 'web_search': {
      const query = pickString(input, 'query')
      return {
        icon: Search,
        actionKey: 'web_search',
        target: query ? `"${query}"` : '',
        fullTarget: query,
        detail: ToolCallDetailWebSearch,
      }
    }
    case 'web_fetch': {
      const url = pickString(input, 'url')
      const format = pickString(input, 'format').toLowerCase()
      const named = format && format !== 'auto' && WEB_FETCH_FORMATS.has(format)
      return {
        icon: Globe,
        actionKey: named ? 'web_fetch_as' : 'web_fetch',
        actionParams: named ? { format: format.toUpperCase() } : undefined,
        target: hostnameOrUrl(url),
        fullTarget: url,
        detail: ToolCallDetailWebFetch,
      }
    }
    case 'search_memory': {
      const query = pickString(input, 'query')
      return {
        icon: Brain,
        actionKey: 'search_memory',
        target: query ? `"${query}"` : '',
        fullTarget: query,
        detail: ToolCallDetailMemory,
      }
    }
    case 'send': {
      const target = pickString(input, 'target')
      const text = sendText(input)
      const display = target || truncate(text, 60)
      const variant = sendVariant(input)
      return {
        ...variant,
        target: display,
        fullTarget: text || target,
        detail: ToolCallDetailSend,
      }
    }
    case 'react': {
      const emoji = pickString(input, 'emoji')
      const remove = input.remove === true
      if (remove) {
        return {
          icon: Smile,
          actionKey: 'react_remove',
          target: pickString(input, 'message_id'),
          expandable: true,
          detailVariant: 'inline',
        }
      }
      return { icon: Smile, actionKey: 'react', target: emoji, expandable: true, detailVariant: 'inline' }
    }
    case 'get_contacts': {
      return {
        icon: Users,
        actionKey: 'get_contacts',
        target: pickString(input, 'platform'),
        detail: ToolCallDetailContacts,
      }
    }
    case 'list_sessions': {
      const type = pickString(input, 'type')
      const actionKey = type === 'chat' || type === 'schedule' ? `list_sessions_${type}` : 'list_sessions'
      return {
        icon: type === 'schedule' ? Calendar : MessagesSquare,
        actionKey,
        target: pickString(input, 'platform'),
        expandable: true,
        detailVariant: 'inline',
      }
    }
    case 'search_messages': {
      const keyword = pickString(input, 'keyword')
      const role = pickString(input, 'role')
      const actionKey = role === 'user' || role === 'assistant' ? `search_messages_${role}` : 'search_messages'
      return {
        icon: SearchCheck,
        actionKey,
        target: keyword ? `"${keyword}"` : '',
        fullTarget: keyword,
        expandable: true,
        detailVariant: 'inline',
      }
    }
    case 'get_messages': {
      const messageId = pickString(input, 'message_id', 'messageId')
      const sessionId = pickString(input, 'session_id', 'sessionId')
      return {
        icon: MessageSquareText,
        actionKey: messageId ? 'get_messages_one' : 'get_messages',
        target: messageId || sessionId,
        expandable: true,
        detailVariant: 'inline',
      }
    }
    case 'list_models':
      return { icon: Boxes, actionKey: 'list_models', target: '', expandable: true, detailVariant: 'inline' }
    case 'list_workdirs':
      return { icon: FolderTree, actionKey: 'list_workdirs', target: '', expandable: true, detailVariant: 'inline' }
    case 'list_acp_agents': {
      const agentId = pickString(input, 'agent_id', 'agentId')
      return {
        icon: Bot,
        // Naming an agent also boots it to fetch models/efforts — a slower,
        // materially different call than reading the catalog.
        actionKey: agentId ? 'list_acp_agents_one' : 'list_acp_agents',
        target: agentId,
        expandable: true,
        detailVariant: 'inline',
      }
    }
    case 'list_schedule':
      return { icon: Calendar, actionKey: 'list_schedule', target: '', detail: ToolCallDetailSchedule }
    case 'get_schedule':
      return {
        icon: Calendar,
        actionKey: 'get_schedule',
        target: pickString(input, 'id'),
        expandable: true,
        detailVariant: 'inline',
      }
    case 'create_schedule':
      return {
        icon: CalendarPlus,
        actionKey: 'create_schedule',
        target: pickString(input, 'name'),
        expandable: true,
        detailVariant: 'inline',
      }
    case 'update_schedule':
      return {
        ...updateScheduleVariant(input),
        target: pickString(input, 'name', 'id'),
        expandable: true,
        detailVariant: 'inline',
      }
    case 'delete_schedule':
      return {
        icon: CalendarMinus,
        actionKey: 'delete_schedule',
        target: pickString(input, 'id'),
        expandable: true,
        detailVariant: 'inline',
      }
    case 'list_email_accounts':
      return {
        icon: Mail,
        actionKey: 'list_email_accounts',
        target: '',
        detail: ToolCallDetailEmailAccounts,
      }
    case 'send_email': {
      const subject = pickString(input, 'subject')
      const to = pickString(input, 'to')
      return {
        icon: MailPlus,
        actionKey: 'send_email',
        target: subject || to,
        fullTarget: subject ? `${to} — ${subject}` : to,
        expandable: true,
        detailVariant: 'inline',
      }
    }
    case 'list_email':
      return {
        icon: Inbox,
        actionKey: 'list_email',
        target: '',
        detail: ToolCallDetailEmailList,
      }
    case 'read_email': {
      const uid = input.uid
      const target = uid != null ? `#${String(uid)}` : ''
      return {
        icon: MailOpen,
        actionKey: 'read_email',
        target,
        detail: ToolCallDetailEmailRead,
      }
    }
    case 'speak': {
      const text = pickString(input, 'text')
      return {
        icon: Volume2,
        actionKey: 'speak',
        target: truncate(text, 60),
        fullTarget: text,
        expandable: true,
        detailVariant: 'inline',
      }
    }
    case 'transcribe_audio': {
      const target = pickString(
        input,
        'path',
        'audio_path',
        'file_path',
        'url',
        'audio_url',
      )
      return {
        icon: AudioLines,
        actionKey: 'transcribe_audio',
        target,
        expandable: true,
        detailVariant: 'inline',
      }
    }
    case 'generate_image': {
      const prompt = pickString(input, 'prompt')
      return {
        icon: ImagePlus,
        actionKey: 'generate_image',
        target: truncate(prompt, 60),
        fullTarget: prompt,
        detail: ToolCallDetailImage,
      }
    }
    case 'generate_video': {
      const prompt = pickString(input, 'prompt')
      return {
        icon: Film,
        actionKey: 'generate_video',
        target: truncate(prompt, 60),
        fullTarget: prompt,
        detail: ToolCallDetailOutput,
      }
    }
    case 'spawn_agent': {
      const task = pickString(input, 'task')
      const fork = input.fork === true
      const background = isBackgroundInput(input)
      return {
        icon: fork ? Split : Workflow,
        actionKey: spawnAgentActionKey(fork, background),
        target: pickString(input, 'id') || truncate(task, 60),
        fullTarget: task,
        detail: ToolCallDetailSpawn,
      }
    }
    case 'send_message': {
      const message = pickString(input, 'message')
      return {
        icon: MessagesSquare,
        actionKey: isBackgroundInput(input) ? 'send_message_background' : 'send_message',
        target: pickString(input, 'id'),
        fullTarget: message,
        detail: ToolCallDetailSpawn,
      }
    }
    case 'list_agents':
      return {
        icon: ListChecks,
        actionKey: 'list_agents',
        target: '',
        detail: ToolCallDetailSpawn,
      }
    case 'use_skill':
      return {
        icon: Sparkles,
        actionKey: 'use_skill',
        target: pickString(input, 'skillName'),
        expandable: true,
        detailVariant: 'inline',
      }
    case 'list_skills':
      return {
        icon: Sparkles,
        actionKey: 'list_skills',
        target: '',
        expandable: true,
        detailVariant: 'inline',
      }
    case 'browser_action': {
      const resolved = resolveGuiAction(BROWSER_ACTION_ICONS, 'browserAction', MousePointerClick, 'browser_action', pickString(input, 'action'), input)
      const target = guiKeyTarget(resolved.action, input) || pickString(input, 'url', 'ref', 'selector')
      return {
        ...resolved,
        target,
        fullTarget: pickString(input, 'url') || target,
        detail: ToolCallDetailBrowser,
      }
    }
    case 'browser_observe': {
      const resolved = resolveGuiAction(BROWSER_OBSERVE_ICONS, 'browserObserve', Eye, 'browser_observe', pickString(input, 'observe'))
      return {
        ...resolved,
        target: pickString(input, 'ref', 'selector'),
        detail: ToolCallDetailBrowser,
      }
    }
    case 'computer_observe': {
      const resolved = resolveGuiAction(COMPUTER_OBSERVE_ICONS, 'computerObserve', Monitor, 'computer_observe', pickString(input, 'observe'))
      return {
        ...resolved,
        target: '',
        detail: ToolCallDetailComputer,
      }
    }
    case 'computer_action': {
      const resolved = resolveGuiAction(COMPUTER_ACTION_ICONS, 'computerAction', MousePointer2, 'computer_action', pickString(input, 'action'), input)
      const x = input.x
      const y = input.y
      const coords = typeof x === 'number' && typeof y === 'number' ? `${x}, ${y}` : ''
      return {
        ...resolved,
        target: guiKeyTarget(resolved.action, input) || pickString(input, 'ref') || coords,
        detail: ToolCallDetailComputer,
      }
    }
    case 'browser_remote_session': {
      const resolved = resolveGuiAction(REMOTE_SESSION_ICONS, 'remoteSession', Cable, 'browser_remote_session', pickString(input, 'action'))
      return {
        ...resolved,
        target: pickString(input, 'session_id'),
        detail: ToolCallDetailRemoteSession,
      }
    }
    case 'permission': {
      // An agent permission question that maps to no concrete tool (network
      // access, mode switch, elicitation fallback): the agent's own title is
      // the subject, so show it rather than the synthetic tool name.
      const title = pickString(input, 'title')
      const request = pickString(input, 'request')
      return {
        icon: ShieldQuestion,
        actionKey: 'permission',
        target: truncate(title || request, 80),
        fullTarget: title || request,
        expandable: true,
        detailVariant: 'inline',
      }
    }
    default:
      return {
        icon: Wrench,
        actionKey: 'generic',
        target: block.toolName,
        expandable: true,
        detailVariant: 'inline',
      }
  }
}
