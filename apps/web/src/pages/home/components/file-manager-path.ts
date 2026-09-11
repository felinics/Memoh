// Workspace mount inside the bot container. The bridge resolves relative paths
// against this directory (bridgesvc.DefaultWorkDir) and the sidebar file tree is
// rooted here.
export const FILE_MANAGER_ROOT = '/data'

// POSIX-style cleanup: collapse empty/'.' segments and resolve '..'. Tab
// identity and the file tree's active-row match are both plain string
// comparisons on the path, so '/data/./a.md' and '/data/a.md' must not diverge
// into two tabs for one file.
function cleanPosixPath(path: string): string {
  const absolute = path.startsWith('/')
  const segments: string[] = []
  for (const segment of path.split('/')) {
    if (!segment || segment === '.') continue
    if (segment === '..') {
      const last = segments[segments.length - 1]
      if (last !== undefined && last !== '..') {
        segments.pop()
        continue
      }
      // '/..' is '/'; a relative path keeps the '..' it cannot resolve.
      if (absolute) continue
    }
    segments.push(segment)
  }
  const joined = segments.join('/')
  return absolute ? `/${joined}` : joined
}

// Resolve a tool-call path into the path the file manager should open.
//
// Absolute paths are returned as they are: the bridge forwards host absolute
// paths untouched (bridgesvc.resolvePath with AllowHostAbsolute), so prefixing
// the workspace root would fabricate a path that does not exist — '/data' +
// '/home/user/x' — and the read would fail with "does not exist".
//
// Relative paths are anchored at the workspace root, matching how the bridge
// resolves them against its default work dir.
export function normalizeFileManagerPath(path: string): string {
  const trimmedPath = path.trim()
  if (!trimmedPath) return FILE_MANAGER_ROOT
  const joinedPath = trimmedPath.startsWith('/')
    ? trimmedPath
    : `${FILE_MANAGER_ROOT}/${trimmedPath}`
  const cleanedPath = cleanPosixPath(joinedPath)
  // The tree is rooted at the workspace mount and cannot render the filesystem
  // root, so '/' resolves to that mount.
  return cleanedPath === '/' ? FILE_MANAGER_ROOT : cleanedPath
}
