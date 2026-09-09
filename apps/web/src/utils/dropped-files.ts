// Reading an OS file drop.
//
// Two hard constraints shape this file:
//
// 1. `DataTransfer` is a synchronous window. Chromium neuters `dataTransfer.items`
//    as soon as the `drop` handler returns, so every entry handle must be pulled
//    out of it BEFORE the first await. The handles themselves stay valid
//    afterwards, which is why the async walk below is safe but the collection
//    loop is not allowed to move.
// 2. Folders only exist through `webkitGetAsEntry()`. `dataTransfer.files` lists
//    a dropped folder as a nameless zero-byte File, so a files-only reader would
//    silently upload garbage instead of the folder's contents.

export interface DroppedFile {
  file: File
  // Path relative to the folder root, e.g. "src/main.ts". Empty for loose files.
  relativePath: string
}

export interface DroppedFolder {
  name: string
  files: DroppedFile[]
  // Every directory walked, including empty ones — they'd be lost otherwise,
  // since a consumer can only derive parent dirs from file paths.
  directories: string[]
}

export interface DroppedContents {
  // Files dropped on their own, not inside one of the folders below.
  files: File[]
  folders: DroppedFolder[]
}

function joinRelative(...parts: string[]): string {
  return parts.filter(Boolean).join('/')
}

// One readEntries() call returns at most 100 children in Chromium, and signals
// "done" with an empty batch — so a single call silently truncates large folders.
function readBatch(reader: FileSystemDirectoryReader): Promise<FileSystemEntry[]> {
  return new Promise((resolve) => {
    reader.readEntries(resolve, () => resolve([]))
  })
}

function readFile(entry: FileSystemFileEntry): Promise<File | null> {
  return new Promise((resolve) => {
    entry.file(resolve, () => resolve(null))
  })
}

async function walkFolder(root: FileSystemDirectoryEntry): Promise<DroppedFolder> {
  const files: DroppedFile[] = []
  const directories: string[] = []

  async function walk(dir: FileSystemDirectoryEntry, prefix: string) {
    const reader = dir.createReader()
    for (;;) {
      const batch = await readBatch(reader)
      if (batch.length === 0) return
      for (const entry of batch) {
        const relativePath = joinRelative(prefix, entry.name)
        if (entry.isDirectory) {
          directories.push(relativePath)
          await walk(entry as FileSystemDirectoryEntry, relativePath)
        } else {
          const file = await readFile(entry as FileSystemFileEntry)
          // A file the browser can't open (permission, vanished mid-drop) is
          // skipped rather than failing the whole drop.
          if (file) files.push({ file, relativePath })
        }
      }
    }
  }

  await walk(root, '')
  return { name: root.name, files, directories }
}

// The synchronous half, shared by both readers below. Everything it touches
// (`transfer.items`, `transfer.files`) dies with the drop event, so it must run
// to completion before any caller awaits.
interface DroppedEntries {
  fileEntries: FileSystemFileEntry[]
  folderEntries: FileSystemDirectoryEntry[]
  // Only populated when entries are unavailable (rare): files with no folder
  // support, since `transfer.files` can't tell a folder from a 0-byte file.
  plainFiles: File[]
}

function collectEntries(transfer: DataTransfer): DroppedEntries {
  const fileEntries: FileSystemFileEntry[] = []
  const folderEntries: FileSystemDirectoryEntry[] = []
  for (const item of Array.from(transfer.items)) {
    if (item.kind !== 'file') continue
    const entry = item.webkitGetAsEntry?.()
    if (!entry) continue
    if (entry.isDirectory) folderEntries.push(entry as FileSystemDirectoryEntry)
    else fileEntries.push(entry as FileSystemFileEntry)
  }
  const plainFiles = fileEntries.length === 0 && folderEntries.length === 0
    ? Array.from(transfer.files)
    : []
  return { fileEntries, folderEntries, plainFiles }
}

// Full read: loose files plus every folder walked to the leaves. For a consumer
// that can materialize directories (the workspace file tree).
export async function readDroppedContents(transfer: DataTransfer): Promise<DroppedContents> {
  const { fileEntries, folderEntries, plainFiles } = collectEntries(transfer)

  const files = [...plainFiles]
  for (const entry of fileEntries) {
    const file = await readFile(entry)
    if (file) files.push(file)
  }

  const folders: DroppedFolder[] = []
  for (const entry of folderEntries) {
    folders.push(await walkFolder(entry))
  }

  return { files, folders }
}

// Files-only read for consumers with no notion of a directory (the composer
// attachment tray). Folders are counted, never walked — walking a tree only to
// refuse it would stall a drop of something large for nothing. The count exists
// so the caller can say why the folder didn't land instead of going silent.
export async function readDroppedFiles(transfer: DataTransfer): Promise<{ files: File[], skippedFolders: number }> {
  const { fileEntries, folderEntries, plainFiles } = collectEntries(transfer)

  const files = [...plainFiles]
  for (const entry of fileEntries) {
    const file = await readFile(entry)
    if (file) files.push(file)
  }

  return { files, skippedFolders: folderEntries.length }
}
