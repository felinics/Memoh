import type { HElement, Options as DocxPreviewOptions } from 'docx-preview'

export const DOCX_EXTERNAL_LINK_MESSAGE_TYPE = 'memoh:docx-open-external'
export const MAX_DOCX_RENDERED_HTML_CHARS = 32 * 1024 * 1024

const SVG_NS = 'http://www.w3.org/2000/svg'
const SAFE_IMG_DATA_URL_RE = /^data:image\/(?:png|jpe?g|gif|webp|bmp|svg\+xml|x-icon|vnd\.microsoft\.icon|tiff|avif)(?:;[a-z0-9.+-]+=[^;,]+)*;base64,[a-z0-9+/=\s]+$/i
const DOCX_IMAGE_MIME_BY_EXT: Record<string, string> = {
  png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif', webp: 'image/webp',
  bmp: 'image/bmp', svg: 'image/svg+xml', ico: 'image/x-icon', tif: 'image/tiff', tiff: 'image/tiff', avif: 'image/avif',
}
const BLOCKED_TAGS = new Set([
  'script', 'iframe', 'frame', 'frameset', 'object', 'embed', 'applet', 'form', 'input', 'button',
  'select', 'textarea', 'option', 'optgroup', 'meta', 'link', 'base', 'portal', 'template', 'noscript',
  'audio', 'video', 'source', 'track', 'xmp', 'noembed', 'noframes', 'plaintext', 'title', 'annotation-xml',
])
const BLOCKED_ATTRS = new Set(['srcdoc', 'srcset', 'formaction', 'action', 'ping', 'background', 'dynsrc', 'lowsrc', 'poster', 'xlink:actuate'])

export type RenderDocxResult = { ok: true, srcdoc: string } | { ok: false, reason: 'invalid' | 'too-large' }
export interface BuildDocxSrcdocOptions { stylesHtml: string, bodyHtml: string, channelToken: string }

export function normalizeSafeExternalHref(rawHref: unknown): string | null {
  const trimmed = typeof rawHref === 'string' ? rawHref.trim() : ''
  if (!trimmed || /[\u0000-\u001f\u007f]/.test(trimmed) || !/^(?:https?:|mailto:)/i.test(trimmed)) return null
  try {
    const { protocol } = new URL(trimmed)
    return protocol === 'https:' || protocol === 'http:' || protocol === 'mailto:' ? trimmed : null
  } catch { return null }
}

function installSafeInertGetBBox(el: Element): void {
  const native = (el as unknown as { getBBox?: () => DOMRect }).getBBox
  Object.defineProperty(el, 'getBBox', {
    configurable: true,
    value: () => {
      try { if (typeof native === 'function') return native.call(el) } catch { /* detached SVG */ }
      return { x: 0, y: 0, width: 0, height: 0 }
    },
  })
}

function stripNonContentNodes(root: ParentNode): void {
  const walker = (root.ownerDocument ?? document).createTreeWalker(root, NodeFilter.SHOW_COMMENT | NodeFilter.SHOW_PROCESSING_INSTRUCTION)
  const nodes: Node[] = []
  for (let node = walker.nextNode(); node; node = walker.nextNode()) nodes.push(node)
  for (const node of nodes) node.parentNode?.removeChild(node)
}

function sanitizeStyleElement(el: Element): void {
  for (const attr of Array.from(el.attributes)) if (attr.name.toLowerCase() !== 'type') el.removeAttribute(attr.name)
  el.textContent = (el.textContent ?? '').replace(/<\//g, '\\3c /')
}

export function sanitizeDocxDom(styleRoot: ParentNode, bodyRoot: ParentNode): void {
  stripNonContentNodes(styleRoot)
  stripNonContentNodes(bodyRoot)
  for (const child of Array.from(styleRoot.childNodes)) {
    if (child.nodeType === Node.ELEMENT_NODE && (child as Element).localName.toLowerCase() === 'style') sanitizeStyleElement(child as Element)
    else child.parentNode?.removeChild(child)
  }
  for (const el of Array.from(bodyRoot.querySelectorAll('*'))) {
    if (!el.isConnected && !bodyRoot.contains(el)) continue
    const tag = el.localName.toLowerCase()
    const validSvgFo = tag === 'foreignobject' && el.namespaceURI === SVG_NS && el.parentElement?.namespaceURI === SVG_NS && el.parentElement.localName.toLowerCase() !== 'foreignobject'
    if (BLOCKED_TAGS.has(tag) || (tag === 'foreignobject' && !validSvgFo)) { el.remove(); continue }
    if (tag === 'style') { sanitizeStyleElement(el); continue }
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase()
      if (name.startsWith('on') || BLOCKED_ATTRS.has(name)) { el.removeAttribute(attr.name); continue }
      if (name !== 'src' && name !== 'href' && name !== 'xlink:href') continue
      const val = attr.value.trim()
      const isFrag = val.startsWith('#') && val.length > 1 && !/[\u0000-\u001f\u007f]/.test(val)
      const compactImg = (tag === 'img' || tag === 'image') ? val.replace(/\s+/g, '') : ''
      const safeVal = tag === 'a' && name === 'href'
        ? (isFrag ? val : normalizeSafeExternalHref(val))
        : compactImg ? (SAFE_IMG_DATA_URL_RE.test(compactImg) ? compactImg : null) : (isFrag ? val : null)
      if (safeVal) el.setAttribute(name === 'href' && tag === 'a' ? 'href' : attr.name, safeVal)
      else el.removeAttribute(attr.name)
    }
    if (tag === 'a') {
      const href = el.getAttribute('href') ?? ''
      if (href && !href.startsWith('#')) { el.setAttribute('target', '_blank'); el.setAttribute('rel', 'noopener noreferrer') }
      else { el.removeAttribute('target'); el.removeAttribute('rel') }
    }
  }
}

function createInertElementFactory(inertDoc: Document): DocxPreviewOptions['h'] {
  const buildNode = (elem: HElement | Node | string): Node => {
    if (typeof elem === 'string') return inertDoc.createTextNode(elem)
    if (elem instanceof Node) {
      return elem.nodeType === 1 || elem.nodeType === 3 || elem.nodeType === 11
        ? (elem.ownerDocument === inertDoc ? elem : inertDoc.importNode(elem, true))
        : inertDoc.createDocumentFragment()
    }
    const { ns, tagName, className, style, children, ...props } = elem
    if (tagName === '#comment') return inertDoc.createDocumentFragment()
    const out = tagName === '#fragment' ? inertDoc.createDocumentFragment() : (ns ? inertDoc.createElementNS(ns, tagName) : inertDoc.createElement(tagName))
    if (out instanceof Element) {
      if (ns === SVG_NS) installSafeInertGetBBox(out)
      if (className) out.setAttribute('class', className)
      if (typeof style === 'string') out.setAttribute('style', style)
      else if (style && 'style' in out) Object.assign((out as HTMLElement).style, style)
      for (const [k, v] of Object.entries(props)) if (v !== undefined) (out as unknown as Record<string, unknown>)[k] = v
    }
    for (const child of children ?? []) out.appendChild(buildNode(child))
    return out
  }
  return buildNode
}

export function buildDocxSrcdoc({ stylesHtml, bodyHtml, channelToken }: BuildDocxSrcdocOptions): string {
  const nonceBytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(nonceBytes)
  const cspNonce = btoa(String.fromCharCode(...nonceBytes))
  const typeJson = JSON.stringify(DOCX_EXTERNAL_LINK_MESSAGE_TYPE)
  const tokenJson = JSON.stringify(channelToken)
  return `<!doctype html><html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; media-src data:; font-src data:; style-src 'unsafe-inline'; script-src 'nonce-${cspNonce}'; connect-src 'none'; form-action 'none'; base-uri 'none'">
${stylesHtml}<style>html,body{margin:0;padding:0;min-height:100%;background:transparent;color:oklch(0.18 0 0);font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;overflow-x:auto}.docx-wrapper{background:transparent!important;padding:20px 16px!important;min-width:fit-content;box-sizing:border-box}.docx-wrapper>section.docx{padding:48pt;background:oklch(1 0 0);color:oklch(0.18 0 0);box-shadow:0 1px 3px oklch(0 0 0 / 0.14),0 1px 2px oklch(0 0 0 / 0.08)!important;margin-bottom:20px!important}.docx svg{overflow:visible}a[href]{cursor:pointer}</style></head><body>${bodyHtml}<script nonce="${cspNonce}">(() => {
  document.currentScript?.remove();
  requestAnimationFrame(() => {
    for (const svg of document.querySelectorAll(".docx svg")) {
      if (svg.hasAttribute("width") || (svg instanceof SVGElement && svg.style.width)) continue;
      const first = svg.firstElementChild; if (!first || typeof first.getBBox !== "function") continue;
      try { const bb = first.getBBox(), w = Math.ceil(bb.x + bb.width), h = Math.ceil(bb.y + bb.height); if (bb.width > 0 && bb.height > 0 && w > 0 && h > 0) { svg.setAttribute("width", String(w)); svg.setAttribute("height", String(h)); } } catch {}
    }
  });
  document.addEventListener("click", (event) => {
    if (!event.isTrusted || !(event.target instanceof Element)) return;
    const rawHref = (event.target.closest("a[href]")?.getAttribute("href") || "").trim(); if (!rawHref) return;
    event.preventDefault();
    if (rawHref.startsWith("#")) {
      const rawId = rawHref.slice(1); if (!rawId) return;
      let id = rawId; try { id = decodeURIComponent(rawId); } catch {}
      (document.getElementById(id) || document.getElementById(rawId) || document.getElementsByName(id)[0] || document.getElementsByName(rawId)[0])?.scrollIntoView({ behavior: "smooth", block: "start" });
      return;
    }
    if (/^(?:https?:|mailto:)/i.test(rawHref)) window.parent.postMessage({ type: ${typeJson}, token: ${tokenJson}, href: rawHref }, "*");
  });
})();</script></body></html>`
}

async function readBlobBytes(blob: Blob): Promise<Uint8Array> {
  if (typeof blob.arrayBuffer === 'function') return new Uint8Array(await blob.arrayBuffer())
  return await new Promise<Uint8Array>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => (reader.result instanceof ArrayBuffer ? resolve(new Uint8Array(reader.result)) : reject(new Error('Invalid buffer')))
    reader.onerror = () => reject(reader.error ?? new Error('FileReader error'))
    reader.readAsArrayBuffer(blob)
  })
}

export async function renderDocxBlobToSrcdoc(blob: Blob, options: { channelToken: string, maxRenderedChars?: number }): Promise<RenderDocxResult> {
  try {
    const { parseAsync, renderDocument } = await import('docx-preview')
    const inertDoc = document.implementation.createHTMLDocument('')
    const previewOptions: Partial<DocxPreviewOptions> = {
      inWrapper: true, breakPages: true, debug: false, experimental: false, className: 'docx',
      trimXmlDeclaration: true, ignoreLastRenderedPageBreak: true, renderHeaders: true,
      renderFooters: true, renderFootnotes: true, renderEndnotes: true, useBase64URL: true,
      renderChanges: false, renderComments: false, renderAltChunks: false,
      h: createInertElementFactory(inertDoc),
    }
    const parsedDoc = await parseAsync(await readBlobBytes(blob), previewOptions) as {
      documentPart?: unknown
      blobToURL?: (partBlob: Blob | null | undefined, partPath?: string) => Promise<string | null> | string | null
    }
    if (!parsedDoc?.documentPart) return { ok: false, reason: 'invalid' }
    if (typeof parsedDoc.blobToURL === 'function') {
      const orig = parsedDoc.blobToURL.bind(parsedDoc)
      const cache = new Map<string, Promise<string | null>>()
      parsedDoc.blobToURL = (partBlob, partPath) => {
        if (!partBlob) return Promise.resolve(null)
        const key = partPath ? partPath.replace(/^\/+/, '').toLowerCase() : ''
        const inferredMime = !partBlob.type ? DOCX_IMAGE_MIME_BY_EXT[key.match(/\.([a-z0-9]+)$/)?.[1] ?? ''] : undefined
        const normalizedBlob = inferredMime ? new Blob([partBlob], { type: inferredMime }) : partBlob
        if (!key) return Promise.resolve(orig(normalizedBlob, partPath))
        const hit = cache.get(key) ?? Promise.resolve(orig(normalizedBlob, partPath))
        cache.set(key, hit)
        return hit
      }
    }
    const styleRoot = inertDoc.createElement('div')
    const bodyRoot = inertDoc.createElement('div')
    for (const node of await renderDocument(parsedDoc, previewOptions)) {
      const imported = node.ownerDocument === inertDoc ? node : inertDoc.importNode(node, true)
      ;(imported.nodeName.toUpperCase() === 'STYLE' ? styleRoot : bodyRoot).appendChild(imported)
    }
    sanitizeDocxDom(styleRoot, bodyRoot)
    const stylesHtml = styleRoot.innerHTML
    const bodyHtml = bodyRoot.innerHTML
    if (stylesHtml.length + bodyHtml.length > (options.maxRenderedChars ?? MAX_DOCX_RENDERED_HTML_CHARS)) return { ok: false, reason: 'too-large' }
    return { ok: true, srcdoc: buildDocxSrcdoc({ stylesHtml, bodyHtml, channelToken: options.channelToken }) }
  } catch { return { ok: false, reason: 'invalid' } }
}
