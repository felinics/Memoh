import { defineStore } from 'pinia'
import { ref } from 'vue'
import { toast } from '@felinic/ui'
import i18n from '@/i18n'
import { useCapabilitiesStore } from './capabilities'

const GITHUB_REPO = 'felinics/memoh'
const RELEASES_URL = `https://github.com/${GITHUB_REPO}/releases/latest`

function normalizeVersion(version?: string | null): string {
  return (version ?? '').replace(/^v/i, '')
}

// Update detection is intentionally lightweight (a single GitHub release lookup,
// not a real auto-updater — that's a separate, larger effort). It lives in a
// shared store so detection can happen once at app launch and the About page
// merely reads the result instead of re-checking (and toasting) on every visit.
export const useUpdateStore = defineStore('update', () => {
  const checking = ref(false)
  const checked = ref(false)
  const hasUpdate = ref(false)
  const latestVersion = ref('')
  const releaseBody = ref('')
  const releaseUrl = ref(RELEASES_URL)

  // Fetch the latest GitHub release and compare against the running server
  // version. Throws on network / API failure so explicit callers can surface it.
  async function check(): Promise<boolean> {
    if (checking.value) return hasUpdate.value
    checking.value = true
    try {
      const capabilities = useCapabilitiesStore()
      await capabilities.load()

      const res = await fetch(`https://api.github.com/repos/${GITHUB_REPO}/releases/latest`)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data = await res.json()

      const latest = normalizeVersion(data.tag_name ?? '')
      const current = normalizeVersion(capabilities.serverVersion)

      latestVersion.value = latest
      releaseBody.value = data.body ?? ''
      releaseUrl.value = data.html_url ?? RELEASES_URL
      hasUpdate.value = Boolean(latest) && Boolean(current) && latest !== current
      checked.value = true
      return hasUpdate.value
    } finally {
      checking.value = false
    }
  }

  // Startup entry point: check quietly and only surface a toast when a newer
  // release exists, so detection no longer depends on opening the About page.
  async function checkAtStartup(): Promise<void> {
    try {
      const updated = await check()
      if (updated) {
        toast.success(i18n.global.t('about.newVersionAvailable', { version: latestVersion.value }))
      }
    } catch {
      // Silent at startup — a failed background check should never nag.
    }
  }

  return { checking, checked, hasUpdate, latestVersion, releaseBody, releaseUrl, check, checkAtStartup }
})
