export interface FileEntry {
  path: string
  is_dir: boolean
  size: string | number
  mode: string
  mod_time: string
  summary: string
}

export interface ReadFileRequest {
  path?: string
  line_offset?: number
  n_lines?: number
}

export interface WriteFileRequest {
  path?: string
  content?: Buffer | Uint8Array
}

export interface ListDirRequest {
  path?: string
  recursive?: boolean
  offset?: number
  limit?: number
  collapse_threshold?: number
  max_entries?: number
}

export interface StatRequest {
  path?: string
}

export interface MkdirRequest {
  path?: string
}

export interface RenameRequest {
  old_path?: string
  new_path?: string
}

export interface DeleteFileRequest {
  path?: string
  recursive?: boolean
}

export interface ReadRawRequest {
  path?: string
}

export interface WriteRawChunk {
  path?: string
  data?: Buffer | Uint8Array
}

export interface ExecInput {
  command?: string
  work_dir?: string
  env?: string[]
  timeout_seconds?: number
  stdin_data?: Buffer | Uint8Array
  pty?: boolean
  resize?: { cols?: number, rows?: number }
  clean_env?: boolean
  unset_env?: string[]
}

export interface ExecOutput {
  stream: number
  data: Buffer
  exit_code: number
}
