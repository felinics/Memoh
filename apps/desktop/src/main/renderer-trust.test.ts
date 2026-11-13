/**
 * Failure modes covered for Desktop main-process trust & permission gates:
 * 1. Child `<iframe>` (e.g. workspace browser/HTML preview) inside `chatWindow`
 *    invoking privileged Electron IPC (`isMainFrame === false` bypassed when only
 *    checking `BrowserWindow.fromWebContents(event.sender) === chatWindow`).
 * 2. Untrusted top-level navigation or foreign window invoking privileged IPC.
 * 3. Default session granting camera (`mediaTypes: ['video']` or empty `[]`),
 *    geolocation, notifications, display-capture, or iframe permissions instead of
 *    default-denying everything outside the narrow chat-shell allowlist.
 */
import { describe, expect, it } from 'vitest'
import { pathToFileURL } from 'node:url'
import {
  assertTrustedIpcSender,
  isAllowedMainFramePermissionCheck,
  isAllowedMainFramePermissionRequest,
  isTrustedRendererUrl,
} from './renderer-trust'

describe('isTrustedRendererUrl', () => {
  const productionEntry = '/Applications/Memoh/resources/app.asar/out/renderer/index.html'

  it('allows only the exact development renderer entry', () => {
    const options = { devBaseUrl: 'http://127.0.0.1:8082', productionEntry }
    expect(isTrustedRendererUrl('http://127.0.0.1:8082/index.html?mode=desktop#chat', options)).toBe(true)
    expect(isTrustedRendererUrl('http://127.0.0.1:8082/attacker.html', options)).toBe(false)
    expect(isTrustedRendererUrl('https://example.com/index.html', options)).toBe(false)
  })

  it('allows only the packaged renderer file', () => {
    const options = { productionEntry }
    expect(isTrustedRendererUrl(`${pathToFileURL(productionEntry).href}?mode=desktop`, options)).toBe(true)
    expect(isTrustedRendererUrl(pathToFileURL('/tmp/untrusted.html').href, options)).toBe(false)
    expect(isTrustedRendererUrl('https://example.com/', options)).toBe(false)
  })
})

describe('assertTrustedIpcSender', () => {
  const productionEntry = '/Applications/Memoh/resources/app.asar/out/renderer/index.html'
  const trustedUrl = `${pathToFileURL(productionEntry).href}#/`
  const trustOptions = { productionEntry }

  it('accepts IPC only from the main frame of chatWindow at the trusted renderer URL', () => {
    expect(() => assertTrustedIpcSender({
      hasChatWindow: true,
      isChatWindowSender: true,
      hasSenderFrame: true,
      isMainFrame: true,
      senderUrl: trustedUrl,
    }, trustOptions)).not.toThrow()
  })

  it('rejects child iframes even inside chatWindow and even if senderUrl looks trusted', () => {
    expect(() => assertTrustedIpcSender({
      hasChatWindow: true,
      isChatWindowSender: true,
      hasSenderFrame: true,
      isMainFrame: false,
      senderUrl: trustedUrl,
    }, trustOptions)).toThrow('IPC request rejected from an untrusted renderer')
  })

  it('rejects missing senderFrame, foreign windows, missing chatWindow, and untrusted origins', () => {
    for (const bad of [
      { hasChatWindow: false, isChatWindowSender: false, hasSenderFrame: true, isMainFrame: true, senderUrl: trustedUrl },
      { hasChatWindow: true, isChatWindowSender: false, hasSenderFrame: true, isMainFrame: true, senderUrl: trustedUrl },
      { hasChatWindow: true, isChatWindowSender: true, hasSenderFrame: false, isMainFrame: false, senderUrl: trustedUrl },
      { hasChatWindow: true, isChatWindowSender: true, hasSenderFrame: true, isMainFrame: true, senderUrl: 'https://evil.example/index.html' },
      { hasChatWindow: true, isChatWindowSender: true, hasSenderFrame: true, isMainFrame: true, senderUrl: 'about:blank' },
    ]) {
      expect(() => assertTrustedIpcSender(bad, trustOptions))
        .toThrow('IPC request rejected from an untrusted renderer')
    }
  })
})

describe('default-deny session permission gates', () => {
  const trustOptions = {
    devBaseUrl: 'http://127.0.0.1:8082',
    productionEntry: '/Applications/Memoh/resources/app.asar/out/renderer/index.html',
  }
  const trustedDevUrl = 'http://127.0.0.1:8082/index.html'

  it('grants clipboard-sanitized-write, fullscreen, and audio-only media to the trusted main frame', () => {
    for (const permission of ['clipboard-sanitized-write', 'fullscreen']) {
      expect(isAllowedMainFramePermissionRequest({
        isChatWindowContents: true,
        isMainFrame: true,
        permission,
        requestingUrl: trustedDevUrl,
      }, trustOptions)).toBe(true)

      expect(isAllowedMainFramePermissionCheck({
        isChatWindowContents: true,
        isMainFrame: true,
        permission,
        requestingUrl: trustedDevUrl,
      }, trustOptions)).toBe(true)
    }

    expect(isAllowedMainFramePermissionRequest({
      isChatWindowContents: true,
      isMainFrame: true,
      permission: 'media',
      mediaTypes: ['audio'],
      requestingUrl: trustedDevUrl,
    }, trustOptions)).toBe(true)

    expect(isAllowedMainFramePermissionCheck({
      isChatWindowContents: true,
      isMainFrame: true,
      permission: 'media',
      mediaType: 'audio',
      requestingUrl: trustedDevUrl,
    }, trustOptions)).toBe(true)
  })

  it('denies camera/video, empty mediaTypes, geolocation, notifications, and all unapproved permissions', () => {
    for (const mediaTypes of [['video'], ['audio', 'video'], [], undefined]) {
      expect(isAllowedMainFramePermissionRequest({
        isChatWindowContents: true,
        isMainFrame: true,
        permission: 'media',
        mediaTypes,
        requestingUrl: trustedDevUrl,
      }, trustOptions)).toBe(false)
    }

    for (const mediaType of ['video', 'unknown', undefined]) {
      expect(isAllowedMainFramePermissionCheck({
        isChatWindowContents: true,
        isMainFrame: true,
        permission: 'media',
        mediaType,
        requestingUrl: trustedDevUrl,
      }, trustOptions)).toBe(false)
    }

    for (const permission of ['geolocation', 'notifications', 'midi', 'display-capture', 'openExternal', 'pointerLock', 'storage-access']) {
      expect(isAllowedMainFramePermissionRequest({
        isChatWindowContents: true,
        isMainFrame: true,
        permission,
        requestingUrl: trustedDevUrl,
      }, trustOptions)).toBe(false)

      expect(isAllowedMainFramePermissionCheck({
        isChatWindowContents: true,
        isMainFrame: true,
        permission,
        requestingUrl: trustedDevUrl,
      }, trustOptions)).toBe(false)
    }
  })

  it('denies allowlisted permissions requested from child iframes, foreign webContents, or untrusted URLs', () => {
    expect(isAllowedMainFramePermissionRequest({
      isChatWindowContents: true,
      isMainFrame: false,
      permission: 'clipboard-sanitized-write',
      requestingUrl: trustedDevUrl,
    }, trustOptions)).toBe(false)

    expect(isAllowedMainFramePermissionRequest({
      isChatWindowContents: false,
      isMainFrame: true,
      permission: 'fullscreen',
      requestingUrl: trustedDevUrl,
    }, trustOptions)).toBe(false)

    expect(isAllowedMainFramePermissionRequest({
      isChatWindowContents: true,
      isMainFrame: true,
      permission: 'media',
      mediaTypes: ['audio'],
      requestingUrl: 'https://evil.example/index.html',
    }, trustOptions)).toBe(false)

    expect(isAllowedMainFramePermissionCheck({
      isChatWindowContents: true,
      isMainFrame: false,
      permission: 'fullscreen',
      requestingUrl: trustedDevUrl,
    }, trustOptions)).toBe(false)
  })
})
