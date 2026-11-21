import type { MemohApi } from './index'

declare global {
  interface Window {
    api: MemohApi
  }
}

export {}
