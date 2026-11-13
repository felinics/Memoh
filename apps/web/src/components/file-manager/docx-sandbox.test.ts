// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { DOCX_PREVIEW_MAX_BYTES, PDF_PREVIEW_MAX_BYTES, getDocumentPreviewKind, getDocumentPreviewMaxBytes, isDocumentFile } from './utils'
import { DOCX_EXTERNAL_LINK_MESSAGE_TYPE, buildDocxSrcdoc, normalizeSafeExternalHref, renderDocxBlobToSrcdoc, sanitizeDocxDom } from './docx-sandbox'

const TINY_PNG = Uint8Array.from(
  atob('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII='),
  ch => ch.charCodeAt(0),
)

function crc32(data: Uint8Array): number {
  let crc = 0xffffffff
  for (const byte of data) {
    crc ^= byte
    for (let b = 0; b < 8; b++) crc = (crc >>> 1) ^ (crc & 1 ? 0xedb88320 : 0)
  }
  return (crc ^ 0xffffffff) >>> 0
}

function buildStoredZipBlob(files: Array<{ name: string, content: string | Uint8Array }>): Blob {
  const enc = new TextEncoder()
  const locals: Uint8Array[] = []
  const centrals: Uint8Array[] = []
  let localOffset = 0
  let centralSize = 0
  for (const f of files) {
    const name = enc.encode(f.name)
    const body = typeof f.content === 'string' ? enc.encode(f.content) : f.content
    const sum = crc32(body)
    const lh = new Uint8Array(30 + name.byteLength + body.byteLength)
    const lv = new DataView(lh.buffer)
    lv.setUint32(0, 0x04034b50, true); lv.setUint16(4, 20, true); lv.setUint32(14, sum, true)
    lv.setUint32(18, body.byteLength, true); lv.setUint32(22, body.byteLength, true); lv.setUint16(26, name.byteLength, true)
    lh.set(name, 30); lh.set(body, 30 + name.byteLength); locals.push(lh)

    const ch = new Uint8Array(46 + name.byteLength)
    const cv = new DataView(ch.buffer)
    cv.setUint32(0, 0x02014b50, true); cv.setUint16(4, 20, true); cv.setUint16(6, 20, true); cv.setUint32(16, sum, true)
    cv.setUint32(20, body.byteLength, true); cv.setUint32(24, body.byteLength, true); cv.setUint16(28, name.byteLength, true)
    cv.setUint32(42, localOffset, true); ch.set(name, 46); centrals.push(ch)
    localOffset += lh.byteLength; centralSize += ch.byteLength
  }
  const archive = new Uint8Array(localOffset + centralSize + 22)
  let offset = 0
  for (const part of [...locals, ...centrals]) { archive.set(part, offset); offset += part.byteLength }
  const ev = new DataView(archive.buffer, offset, 22)
  ev.setUint32(0, 0x06054b50, true); ev.setUint16(8, files.length, true); ev.setUint16(10, files.length, true)
  ev.setUint32(12, centralSize, true); ev.setUint32(16, localOffset, true)
  return new Blob([archive as BlobPart], { type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' })
}

describe('docx-sandbox and document classification', () => {
  it('classifies only .pdf and .docx and enforces 50 MiB / 20 MiB limits', () => {
    expect(getDocumentPreviewKind('SPEC.PDF')).toBe('pdf')
    expect(getDocumentPreviewKind('Quarterly.DOCX')).toBe('docx')
    expect(getDocumentPreviewKind('legacy.doc')).toBeNull()
    expect(isDocumentFile('report.pdf')).toBe(true)
    expect(isDocumentFile('readme.md')).toBe(false)
    expect(getDocumentPreviewMaxBytes('pdf')).toBe(PDF_PREVIEW_MAX_BYTES)
    expect(getDocumentPreviewMaxBytes('docx')).toBe(DOCX_PREVIEW_MAX_BYTES)
  })

  it('sanitizes comments, style breakouts, dangerous/rawtext tags, SVG foreignObject, images, and links', () => {
    const inert = document.implementation.createHTMLDocument('')
    const styleRoot = inert.createElement('div')
    const bodyRoot = inert.createElement('div')
    styleRoot.appendChild(inert.createComment('style--><b id="style-probe">x</b>'))
    const styleEl = inert.createElement('style')
    styleEl.textContent = '.docx { color: black; content: "</style><script>alert(1)</script>"; }'
    styleRoot.appendChild(styleEl)

    bodyRoot.innerHTML = `
      <!--body--><script>window.evil = true</script><iframe srcdoc="x"></iframe><form action="https://a.test"><input></form>
      <p onclick="alert(1)">Hello</p>
      <img id="evil-img" src="https://attacker.example/p.png" srcset="https://attacker.example/2x.png 2x" onerror="alert(1)">
      <img id="safe-img" src="data:image/png;base64,iVBORw0KGgo=" alt="logo">
      <svg><g><foreignObject width="100%" height="100%" onclick="alert(1)"><div id="fo-inner">Textbox Copy<script>alert(1)</script><xmp>raw</xmp><textarea>raw</textarea><title>raw</title><a id="fo-bad" href="javascript:alert(1)">Bad</a><a id="fo-good" href="https://memoh.ai/textbox">Good</a></div></foreignObject></g><image href="https://attacker.example/svg.png"/></svg>
      <div id="html-fo-host"></div>
      <a id="ext" href="https://memoh.ai/docs">Docs</a><a id="mail" href="mailto:hi@memoh.ai">Mail</a>
      <a id="anchor" href="#50%-off" target="_blank">Sec</a><a id="js" href="javascript:alert(1)">Click</a><a id="rel" href="secret">Rel</a>
    `
    bodyRoot.querySelector('#fo-inner')!.appendChild(inert.createElement('plaintext'))
    bodyRoot.querySelector('#html-fo-host')!.appendChild(inert.createElement('foreignObject'))
    sanitizeDocxDom(styleRoot, bodyRoot)

    expect(styleRoot.innerHTML).not.toContain('<!--')
    expect(styleRoot.innerHTML).not.toContain('</style><script>')
    expect(styleRoot.textContent).toContain('\\3c /style>')
    expect(bodyRoot.innerHTML).not.toContain('<!--')
    expect(bodyRoot.querySelector('script, iframe, form, input, xmp, plaintext, textarea, title')).toBeNull()
    expect(bodyRoot.querySelector('#html-fo-host foreignObject')).toBeNull()
    expect(bodyRoot.querySelector('svg foreignObject')?.getAttribute('onclick')).toBeNull()
    expect(bodyRoot.querySelector('#fo-inner')?.textContent).toContain('Textbox Copy')
    expect(bodyRoot.querySelector('#fo-bad')?.getAttribute('href')).toBeNull()
    expect(bodyRoot.querySelector('#fo-good')?.getAttribute('target')).toBe('_blank')
    expect(bodyRoot.querySelector('#evil-img')?.getAttribute('src')).toBeNull()
    expect(bodyRoot.querySelector('#evil-img')?.getAttribute('srcset')).toBeNull()
    expect(bodyRoot.querySelector('#safe-img')?.getAttribute('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
    expect(bodyRoot.querySelector('image')?.getAttribute('href')).toBeNull()
    expect(bodyRoot.querySelector('#ext')?.getAttribute('rel')).toBe('noopener noreferrer')
    expect(bodyRoot.querySelector('#mail')?.getAttribute('href')).toBe('mailto:hi@memoh.ai')
    expect(bodyRoot.querySelector('#anchor')?.getAttribute('target')).toBeNull()
    expect(bodyRoot.querySelector('#js')?.getAttribute('href')).toBeNull()
    expect(bodyRoot.querySelector('#rel')?.getAttribute('href')).toBeNull()
    expect(normalizeSafeExternalHref('https://memoh.ai/a')).toBe('https://memoh.ai/a')
    expect(normalizeSafeExternalHref('javascript:void(0)')).toBeNull()
  })

  it('locks srcdoc CSP to a per-render 128-bit nonce, removes currentScript immediately, and checks isTrusted', () => {
    const first = buildDocxSrcdoc({ stylesHtml: '<style>.docx{}</style>', bodyHtml: '<p>1</p>', channelToken: 't1' })
    const second = buildDocxSrcdoc({ stylesHtml: '<style>.docx{}</style>', bodyHtml: '<p>2</p>', channelToken: 't2' })
    expect(first).toContain('default-src \'none\'')
    expect(first).toContain('connect-src \'none\'')
    expect(first).not.toMatch(/script-src[^;]*'unsafe-inline'/i)
    const n1 = first.match(/script-src 'nonce-([A-Za-z0-9+/=]+)'/)?.[1] ?? ''
    const n2 = second.match(/script-src 'nonce-([A-Za-z0-9+/=]+)'/)?.[1] ?? ''
    expect(n1.length).toBeGreaterThanOrEqual(20)
    expect(n1).not.toBe(n2)
    expect(first).toContain(`<script nonce="${n1}">`)
    expect(first).toContain('document.currentScript?.remove()')
    expect(first).toContain('if (!event.isTrusted')
    expect(first).toContain(DOCX_EXTERNAL_LINK_MESSAGE_TYPE)
  })

  it('renders DOCX inertly, deduplicates images, preserves VML textboxes without rAF throws, strips fontTable comments, and enforces size/validity', async () => {
    const rafCallbacks: FrameRequestCallback[] = []
    const rafSpy = vi.spyOn(window, 'requestAnimationFrame').mockImplementation(cb => rafCallbacks.push(cb))
    const readAsDataURLSpy = vi.spyOn(FileReader.prototype, 'readAsDataURL')
    const mainDocCreateSpy = vi.spyOn(document, 'createElement')

    const drawPic = '<w:r xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><w:drawing><wp:inline><wp:extent cx="95250" cy="95250"/><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:blipFill><a:blip r:embed="rIdImg"/></pic:blipFill><pic:spPr><a:xfrm><a:ext cx="95250" cy="95250"/></a:xfrm></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r>'
    const blob = buildStoredZipBlob([
      { name: '[Content_Types].xml', content: '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Default Extension="png" ContentType="image/png"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/fontTable.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.fontTable+xml"/></Types>' },
      { name: '_rels/.rels', content: '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>' },
      { name: 'word/_rels/document.xml.rels', content: '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdImg" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/logo.png"/><Relationship Id="rIdFont1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/fontTable" Target="fontTable.xml"/></Relationships>' },
      { name: 'word/media/logo.png', content: TINY_PNG },
      { name: 'word/fontTable.xml', content: '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:fonts xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:font w:name="Heading--&gt;&lt;span id=&quot;font-comment-probe&quot;&gt;x&lt;/span&gt;"><w:embedRegular r:id="rIdUnmapped" w:fontKey="{00000000-0000-0000-0000-000000000000}"/></w:font></w:fonts>' },
      { name: 'word/document.xml', content: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" xmlns:v="urn:schemas-microsoft-com:vml"><w:body><w:p><w:r><w:t>Quarterly Report</w:t></w:r>${drawPic}${drawPic}${drawPic}</w:p><w:p><w:r><mc:AlternateContent><mc:Choice Requires="wps"><w:drawing/></mc:Choice><mc:Fallback><w:pict><v:shape style="width:200pt;height:54pt"><v:textbox><w:txbxContent><w:p><w:r><w:t>Executive Summary Callout</w:t></w:r></w:p></w:txbxContent></v:textbox></v:shape></w:pict></mc:Fallback></mc:AlternateContent></w:r><w:r><w:pict><v:textbox><w:txbxContent/></v:textbox></w:pict></w:r></w:p></w:body></w:document>` },
    ])

    try {
      const result = await renderDocxBlobToSrcdoc(blob, { channelToken: 'tok-main' })
      expect(result.ok).toBe(true)
      if (!result.ok) return
      expect(readAsDataURLSpy).toHaveBeenCalledTimes(1)
      expect(mainDocCreateSpy.mock.calls.map(c => c[0].toLowerCase())).not.toContain('img')
      expect(result.srcdoc).toContain('Quarterly Report')
      expect(result.srcdoc).toContain('Executive Summary Callout')
      expect(result.srcdoc).toContain('<foreignObject')
      expect(result.srcdoc).not.toContain('<!--')
      expect(new DOMParser().parseFromString(result.srcdoc, 'text/html').getElementById('font-comment-probe')).toBeNull()
      for (const cb of rafCallbacks) expect(() => cb(16.7)).not.toThrow()

      expect(await renderDocxBlobToSrcdoc(blob, { channelToken: 'tok-cap', maxRenderedChars: 16 })).toEqual({ ok: false, reason: 'too-large' })
      expect(await renderDocxBlobToSrcdoc(new Blob(['not-a-zip']), { channelToken: 'tok-bad' })).toEqual({ ok: false, reason: 'invalid' })
    } finally {
      rafSpy.mockRestore(); readAsDataURLSpy.mockRestore(); mainDocCreateSpy.mockRestore()
    }
  })

  it('preserves embedded images with uppercase extensions or Content_Types Override entries', async () => {
    const makePic = (relId: string) => `<w:r xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><w:drawing><wp:inline><wp:extent cx="95250" cy="95250"/><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:blipFill><a:blip r:embed="${relId}"/></pic:blipFill><pic:spPr><a:xfrm><a:ext cx="95250" cy="95250"/></a:xfrm></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r>`
    const blob = buildStoredZipBlob([
      { name: '[Content_Types].xml', content: '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Default Extension="jpg" ContentType="image/jpeg"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/media/override-hero.png" ContentType="image/png"/></Types>' },
      { name: '_rels/.rels', content: '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>' },
      { name: 'word/_rels/document.xml.rels', content: '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdUpper" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/IMG_001.JPG"/><Relationship Id="rIdOverride" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/override-hero.png"/></Relationships>' },
      { name: 'word/media/IMG_001.JPG', content: TINY_PNG },
      { name: 'word/media/override-hero.png', content: TINY_PNG },
      { name: 'word/document.xml', content: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body><w:p>${makePic('rIdUpper')}${makePic('rIdOverride')}</w:p></w:body></w:document>` },
    ])

    const result = await renderDocxBlobToSrcdoc(blob, { channelToken: 'tok-media' })
    expect(result.ok).toBe(true)
    if (!result.ok) return
    const parsed = new DOMParser().parseFromString(result.srcdoc, 'text/html')
    const srcs = Array.from(parsed.querySelectorAll('img')).map(img => img.getAttribute('src') ?? '')
    expect(srcs).toHaveLength(2)
    expect(srcs[0]).toMatch(/^data:image\/jpeg;base64,/)
    expect(srcs[1]).toMatch(/^data:image\/png;base64,/)
  })
})
