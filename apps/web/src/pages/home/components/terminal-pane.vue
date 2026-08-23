<template>
  <div class="absolute inset-0 flex flex-col bg-[var(--terminal-background)]">
    <div
      ref="wrapperRef"
      class="flex-1 relative min-h-0 terminal-wrapper"
    >
      <div
        ref="containerRef"
        class="absolute inset-2 terminal-container"
      />
    </div>
    <div
      v-if="status === 'disconnected'"
      class="shrink-0 flex items-center justify-end gap-2 px-3 py-1.5 text-xs text-muted-foreground border-t border-border bg-background"
    >
      <span>{{ t('bots.terminal.status.disconnected') }}</span>
      <Button
        size="sm"
        variant="outline"
        @click="reconnect"
      >
        {{ t('bots.terminal.reconnect') }}
      </Button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, nextTick, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Terminal, type ILink } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { SerializeAddon } from '@xterm/addon-serialize'
import { Button } from '@felinic/ui'
import {
  readTerminalSnapshot,
  terminalCacheKey,
  writeTerminalSnapshot,
} from '@/composables/useTerminalCache'
import { sdkAuthQuery, sdkWebSocketUrl } from '@/lib/api-client'
import { useSettingsStore } from '@/store/settings'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { LOCALHOST_URL_REGEX, tryParseLocalhostHref } from '@/utils/localhost-link'
import '@xterm/xterm/css/xterm.css'

const props = withDefaults(defineProps<{
  botId: string
  tabId: string
  active?: boolean
}>(), {
  active: false,
})

const { t } = useI18n()
const settingsStore = useSettingsStore()
const tabsStore = useWorkspaceTabsStore()

function cssVar(name: string): string {
  if (typeof document === 'undefined') return ''
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

function resolveTerminalTheme() {
  return {
    background: cssVar('--terminal-background') || 'black',
    foreground: cssVar('--terminal-foreground') || 'white',
    cursor: cssVar('--terminal-cursor') || 'white',
    selectionBackground: cssVar('--terminal-selection') || 'gray',
  }
}

const TERMINAL_OPTIONS = {
  cursorBlink: true,
} as const

// All code surfaces follow the shared code font size (default 13). Only the
// family falls back to xterm's built-in monospace until the user customizes it.
const DEFAULT_TERMINAL_FONT_FAMILY = 'Menlo, Monaco, "Courier New", monospace'

function resolveTerminalFont() {
  return {
    fontSize: settingsStore.codeFontSizePx,
    fontFamily: settingsStore.codeFontFamily
      ? settingsStore.codeFontStack
      : DEFAULT_TERMINAL_FONT_FAMILY,
  }
}

const wrapperRef = ref<HTMLDivElement | null>(null)
const containerRef = ref<HTMLDivElement | null>(null)
const status = ref<'idle' | 'connecting' | 'connected' | 'disconnected'>('idle')

let terminal: Terminal | null = null
let fitAddon: FitAddon | null = null
let serializeAddon: SerializeAddon | null = null
let ws: WebSocket | null = null
let resizeObserver: ResizeObserver | null = null
let fitTimer: ReturnType<typeof setTimeout> | null = null
let disposables: Array<{ dispose(): void }> = []
let connectionDisposables: Array<{ dispose(): void }> = []

function currentCacheKey(): string {
  return terminalCacheKey(props.botId, props.tabId)
}

function persistSnapshot() {
  if (!serializeAddon) return
  try {
    writeTerminalSnapshot(currentCacheKey(), serializeAddon.serialize())
  } catch (error) {
    console.warn('Failed to serialize terminal buffer:', error)
  }
}

function fitTerminal() {
  if (!props.active) return
  fitAddon?.fit()
}

function resolveTerminalWsUrl(cols: number, rows: number): string {
  return sdkWebSocketUrl({
    url: '/bots/{bot_id}/container/terminal/ws',
    path: { bot_id: props.botId },
    query: { ...sdkAuthQuery(), cols, rows },
  })
}

function closeWs() {
  if (ws) {
    ws.onclose = null
    ws.onerror = null
    ws.onmessage = null
    ws.close()
    ws = null
  }
}

function connectWs() {
  if (!terminal) return
  closeWs()

  for (const d of connectionDisposables) d.dispose()
  connectionDisposables = []

  fitTerminal()

  const cols = terminal.cols
  const rows = terminal.rows

  status.value = 'connecting'
  const url = resolveTerminalWsUrl(cols, rows)
  const socket = new WebSocket(url)
  socket.binaryType = 'arraybuffer'
  ws = socket

  socket.onopen = () => {
    status.value = 'connected'
  }

  socket.onmessage = (event) => {
    if (event.data instanceof ArrayBuffer) {
      terminal?.write(new Uint8Array(event.data))
    } else if (typeof event.data === 'string') {
      terminal?.write(event.data)
    }
  }

  socket.onclose = (event) => {
    if (event.code === 1000) {
      tabsStore.closeTab(props.tabId)
      return
    }
    status.value = 'disconnected'
    terminal?.write('\r\n\x1b[31m[Connection closed]\x1b[0m\r\n')
  }

  socket.onerror = () => {
    status.value = 'disconnected'
  }

  connectionDisposables.push(
    terminal.onData((data) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(new TextEncoder().encode(data))
      }
    }),
    terminal.onResize(({ cols: c, rows: r }) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols: c, rows: r }))
      }
    }),
  )
}

function reconnect() {
  connectWs()
}

function setupResizeObserver() {
  if (resizeObserver || !wrapperRef.value) return
  resizeObserver = new ResizeObserver(() => {
    if (!props.active) return
    if (fitTimer) clearTimeout(fitTimer)
    fitTimer = setTimeout(() => {
      fitTerminal()
    }, 50)
  })
  resizeObserver.observe(wrapperRef.value)
}

onMounted(() => {
  if (!containerRef.value) return
  const term = new Terminal({ ...TERMINAL_OPTIONS, ...resolveTerminalFont(), theme: resolveTerminalTheme() })
  const fa = new FitAddon()
  const sa = new SerializeAddon()
  term.loadAddon(fa)
  term.loadAddon(sa)
  term.open(containerRef.value)

  // Make container-local URLs in command output clickable: clicking opens the
  // workspace browser panel (falling back to an OS tab when it is unavailable),
  // rather than the user's OS browser, since the container's localhost differs.
  disposables.push(term.registerLinkProvider({
    provideLinks(bufferLineNumber, callback) {
      const line = term.buffer.active.getLine(bufferLineNumber - 1)
      const text = line?.translateToString(true)
      if (!text) {
        callback(undefined)
        return
      }
      const links: ILink[] = []
      const regex = new RegExp(LOCALHOST_URL_REGEX.source, 'gi')
      let match: RegExpExecArray | null
      while ((match = regex.exec(text)) !== null) {
        const raw = match[0]
        const parsed = tryParseLocalhostHref(raw)
        if (!parsed) continue
        const startIndex = match.index
        links.push({
          text: raw,
          range: {
            start: { x: startIndex + 1, y: bufferLineNumber },
            end: { x: startIndex + raw.length, y: bufferLineNumber },
          },
          activate: () => {
            if (!tabsStore.openBrowserAt(parsed.display)) {
              const href = /^https?:\/\//i.test(raw) ? raw : `http://${raw}`
              window.open(href, '_blank', 'noopener')
            }
          },
        })
      }
      callback(links.length ? links : undefined)
    },
  }))

  terminal = term
  fitAddon = fa
  serializeAddon = sa

  disposables.push(
    // xterm reports OSC 0/2 application titles, not foreground-process identity.
    term.onTitleChange((title) => {
      tabsStore.updateTerminalTitle(props.tabId, title)
    }),
  )

  const snapshot = readTerminalSnapshot(currentCacheKey())
  if (snapshot) {
    term.write(snapshot)
  }

  nextTick(() => {
    setupResizeObserver()
    if (props.active) {
      fa.fit()
      connectWs()
    }
  })
})

watch(
  () => props.active,
  async (active) => {
    if (!active) {
      persistSnapshot()
      return
    }
    await nextTick()
    fitTerminal()
    if (status.value === 'idle') {
      connectWs()
    }
  },
  { flush: 'post' },
)

watch(
  [() => settingsStore.theme, () => settingsStore.colorScheme],
  () => {
    if (terminal) {
      terminal.options.theme = resolveTerminalTheme()
    }
  },
)

watch(
  [() => settingsStore.codeFontSizePx, () => settingsStore.codeFontStack],
  () => {
    if (!terminal) return
    const { fontSize, fontFamily } = resolveTerminalFont()
    terminal.options.fontSize = fontSize
    terminal.options.fontFamily = fontFamily
    fitTerminal()
  },
)

onBeforeUnmount(() => {
  persistSnapshot()
  if (fitTimer) {
    clearTimeout(fitTimer)
    fitTimer = null
  }
  resizeObserver?.disconnect()
  resizeObserver = null
  closeWs()
  for (const d of disposables) d.dispose()
  disposables = []
  for (const d of connectionDisposables) d.dispose()
  connectionDisposables = []
  terminal?.dispose()
  terminal = null
  fitAddon = null
  serializeAddon = null
})
</script>
