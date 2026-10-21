import { sdkApiUrl } from './api-client'

export interface UploadProgress {
  loaded: number
  total: number
  percent: number
}

export interface UploadOptions {
  // SDK-relative URL, e.g. '/bots/backup/import'. Resolved against the SDK base URL.
  url: string
  formData: FormData
  onProgress?: (progress: UploadProgress) => void
  signal?: AbortSignal
}

function bearerToken(): string | null {
  try {
    return localStorage.getItem('token')
  } catch {
    return null
  }
}

function parseBody(text: string): unknown {
  if (!text) return undefined
  try {
    return JSON.parse(text)
  } catch {
    return text
  }
}

/**
 * uploadWithProgress posts multipart form data via XMLHttpRequest so the upload
 * percentage can be reported. The fetch-based SDK client cannot expose upload
 * progress, so this is used for large file uploads (bot backup import) while
 * reusing the SDK base URL and the stored bearer token. The resolved value is
 * the parsed JSON response; on a non-2xx status it rejects with an Error whose
 * message is the server's error detail.
 */
export function uploadWithProgress<T>(options: UploadOptions): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', sdkApiUrl({ url: options.url }))

    const token = bearerToken()
    if (token) xhr.setRequestHeader('Authorization', `Bearer ${token}`)

    if (options.onProgress) {
      xhr.upload.onprogress = (event) => {
        const total = event.lengthComputable ? event.total : 0
        const percent = total > 0 ? Math.round((event.loaded / total) * 100) : 0
        options.onProgress?.({ loaded: event.loaded, total, percent })
      }
    }

    xhr.onload = () => {
      const parsed = parseBody(xhr.responseText)
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(parsed as T)
        return
      }
      const body = parsed && typeof parsed === 'object'
        ? parsed as { message?: string, error?: string }
        : undefined
      const detail = body?.message || body?.error || `Request failed with status ${xhr.status}`
      reject(new Error(detail))
    }
    xhr.onerror = () => reject(new Error('Network error'))
    xhr.onabort = () => reject(new Error('Upload aborted'))

    if (options.signal) {
      if (options.signal.aborted) {
        xhr.abort()
        return
      }
      options.signal.addEventListener('abort', () => xhr.abort(), { once: true })
    }

    xhr.send(options.formData)
  })
}
