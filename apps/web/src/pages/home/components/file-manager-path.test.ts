import { describe, expect, it } from 'vitest'

import { FILE_MANAGER_ROOT, normalizeFileManagerPath } from './file-manager-path'

describe('normalizeFileManagerPath', () => {
  it('keeps paths already under the workspace root', () => {
    expect(normalizeFileManagerPath('/data/a.md')).toBe('/data/a.md')
    expect(normalizeFileManagerPath('/data/one/wechat-bot/package.json')).toBe('/data/one/wechat-bot/package.json')
    expect(normalizeFileManagerPath(FILE_MANAGER_ROOT)).toBe(FILE_MANAGER_ROOT)
  })

  it('returns absolute paths outside the workspace root untouched', () => {
    // Regression: the workspace root used to be concatenated onto every
    // absolute path, so a tool call on a container-local file opened
    // '/data/home/user/...' and the read failed with "does not exist".
    expect(normalizeFileManagerPath('/home/user/project/package.json')).toBe('/home/user/project/package.json')
    expect(normalizeFileManagerPath('/opt/memoh/toolkit/bin/node')).toBe('/opt/memoh/toolkit/bin/node')
    expect(normalizeFileManagerPath('/datastore/a.md')).toBe('/datastore/a.md')
  })

  it('anchors relative paths at the workspace root', () => {
    expect(normalizeFileManagerPath('src/main.ts')).toBe('/data/src/main.ts')
    expect(normalizeFileManagerPath('./src/main.ts')).toBe('/data/src/main.ts')
  })

  it('resolves the filesystem root and empty input to the workspace root', () => {
    expect(normalizeFileManagerPath('/')).toBe(FILE_MANAGER_ROOT)
    expect(normalizeFileManagerPath('')).toBe(FILE_MANAGER_ROOT)
    expect(normalizeFileManagerPath('   ')).toBe(FILE_MANAGER_ROOT)
    expect(normalizeFileManagerPath('/..')).toBe(FILE_MANAGER_ROOT)
  })

  it('collapses redundant segments so one file yields one tab identity', () => {
    expect(normalizeFileManagerPath('  /data/./a.md  ')).toBe('/data/a.md')
    expect(normalizeFileManagerPath('/data//nested///a.md')).toBe('/data/nested/a.md')
    expect(normalizeFileManagerPath('/data/nested/../a.md')).toBe('/data/a.md')
    expect(normalizeFileManagerPath('/data/nested/')).toBe('/data/nested')
  })
})
