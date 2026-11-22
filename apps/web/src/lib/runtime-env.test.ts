import { describe, expect, it } from 'vitest'
import {
  REDUCED_MOTION_MEDIA,
  detectOsFamily,
  matchesMedia,
  prefersReducedMotion,
} from './runtime-env'

describe('detectOsFamily', () => {
  it('distinguishes desktop and mobile OS families without conflating Android/Linux or iPhone/macOS', () => {
    expect(detectOsFamily({ platform: 'MacIntel' })).toBe('mac')
    expect(detectOsFamily({ userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)' })).toBe('mac')
    expect(detectOsFamily({ platform: 'Win32' })).toBe('windows')
    expect(detectOsFamily({ userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64)' })).toBe('windows')
    expect(detectOsFamily({ platform: 'Linux x86_64' })).toBe('linux')
    expect(detectOsFamily({ userAgent: 'Mozilla/5.0 (Linux; Android 14; Pixel 8)' })).toBe('android')
    expect(detectOsFamily({ userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)' })).toBe('ios')
    expect(detectOsFamily({ userAgent: 'Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X)' })).toBe('ios')
  })

  it('does not match darwin as windows and returns unknown for empty identities', () => {
    expect(detectOsFamily({ userAgent: 'Node.js/22 (darwin; arm64)' })).toBe('unknown')
    expect(detectOsFamily({})).toBe('unknown')
    expect(detectOsFamily(undefined)).toBe('unknown')
  })
})

describe('matchesMedia & prefersReducedMotion', () => {
  it('reads explicit window fixtures and handles missing matchMedia safely', () => {
    const winWithReduce = {
      matchMedia: (query: string) => ({ matches: query === REDUCED_MOTION_MEDIA }) as MediaQueryList,
    }
    expect(prefersReducedMotion(winWithReduce)).toBe(true)
    expect(matchesMedia('(pointer: coarse)', winWithReduce)).toBe(false)
    expect(prefersReducedMotion({ matchMedia: undefined as never })).toBe(false)
  })
})
