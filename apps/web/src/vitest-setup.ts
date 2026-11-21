import { loadLocaleMessages } from '@/i18n/messages'

// The running app downloads only its active language and mounts once it has
// loaded (see i18n/messages.ts). Tests render components and read copy in any
// of the three languages without that mount step, so load all of them before
// any test module runs; i18n.ts then registers them with vue-i18n on creation.
await Promise.all((['en', 'zh', 'ja'] as const).map(loadLocaleMessages))

// jsdom omits window.matchMedia; components and composables such as
// useMediaQuery and send-morph-icon read it on setup or mount. Guard `window`
// so individual test files opting into `@vitest-environment node` keep working.
if (typeof window !== 'undefined' && typeof window.matchMedia !== 'function') {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string): MediaQueryList => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }),
  })
}

// Node 25 defines an unconfigured `{}` stub on globalThis.localStorage /
// sessionStorage when --localstorage-file is unset, preventing Vitest from
// populating global scope with jsdom's Storage instances; read them from jsdom's
// underlying Window since globalThis.window === globalThis inside Vitest.
const jsdomWindow = (globalThis as { jsdom?: { window?: Window } }).jsdom?.window
if (jsdomWindow) {
  Object.defineProperty(globalThis, 'localStorage', {
    value: jsdomWindow.localStorage,
    writable: true,
    configurable: true,
  })
  Object.defineProperty(globalThis, 'sessionStorage', {
    value: jsdomWindow.sessionStorage,
    writable: true,
    configurable: true,
  })
}

// jsdom's Blob omits Blob.prototype.arrayBuffer and Blob.prototype.text.
// Reading jsdom's internal buffer resolves in a microtask like Web/Node Blob
// instead of FileReader's macrotask timer so unawaited fetches do not leak across tests.
if (typeof Blob !== 'undefined' && typeof Blob.prototype.arrayBuffer !== 'function') {
  const blobImplSymbol = Object.getOwnPropertySymbols(new Blob())[0]
  Blob.prototype.arrayBuffer = function arrayBuffer(): Promise<ArrayBuffer> {
    const impl = blobImplSymbol
      ? (this as unknown as Record<symbol, { _buffer?: Uint8Array } | undefined>)[blobImplSymbol]
      : undefined
    if (impl?._buffer) {
      return Promise.resolve(new Uint8Array(impl._buffer).slice().buffer)
    }
    return new Promise((resolve, reject) => {
      if (typeof FileReader === 'undefined') {
        reject(new Error('Blob.arrayBuffer is unavailable without FileReader'))
        return
      }
      const reader = new FileReader()
      reader.onload = () => resolve(reader.result as ArrayBuffer)
      reader.onerror = () => reject(reader.error ?? new Error('Failed to read Blob'))
      reader.readAsArrayBuffer(this)
    })
  }
}

if (typeof Blob !== 'undefined' && typeof Blob.prototype.text !== 'function') {
  Blob.prototype.text = async function text(): Promise<string> {
    return new TextDecoder().decode(await this.arrayBuffer())
  }
}
