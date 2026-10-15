// Shared metric formatters for container/runtime surfaces (Overview runtime
// block + the Container tab). Kept in one place so both render bytes and
// percentages identically. Both return '--' for missing/invalid input so a
// not-yet-sampled metric reads as "unknown", not "0".

export function formatMetricBytes(value?: number): string {
  if (typeof value !== 'number' || Number.isNaN(value) || value < 0) return '--'
  if (value === 0) return '0 B'

  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let size = value
  let unitIndex = 0

  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024
    unitIndex += 1
  }

  const fractionDigits = size >= 100 || unitIndex === 0 ? 0 : 1
  return `${size.toFixed(fractionDigits)} ${units[unitIndex]}`
}

export function formatMetricPercent(value?: number): string {
  if (typeof value !== 'number' || Number.isNaN(value) || value < 0) return '--'
  const fractionDigits = value >= 100 ? 0 : 1
  return `${value.toFixed(fractionDigits)}%`
}

type Translate = (key: string, named?: Record<string, unknown>) => string

interface RuntimeMetricsSource {
  metrics?: {
    cpu?: { usage_percent?: number, cores?: number }
    memory?: { usage_bytes?: number, limit_bytes?: number }
    storage?: { used_bytes?: number, limit_bytes?: number }
  }
  resource_limits?: {
    desired?: { cpu_millicores?: number, memory_bytes?: number, storage_bytes?: number }
  }
}

export interface RuntimeMetricCard {
  key: 'cpu' | 'memory' | 'storage'
  label: string
  value: string
}

// The three runtime tiles shared by the Overview runtime block and the
// Container tab, so both pages show the same value and the same ceiling. The
// ceiling joins the label ("Memory · 1.0 GiB"); a backend-reported limit wins
// over the configured one, and a tile with neither shows the bare label.
export function buildRuntimeMetricCards(source: RuntimeMetricsSource | null | undefined, t: Translate): RuntimeMetricCard[] {
  const cpu = source?.metrics?.cpu
  const mem = source?.metrics?.memory
  const storage = source?.metrics?.storage
  const desired = source?.resource_limits?.desired

  const cores = cpu?.cores || (desired?.cpu_millicores ? desired.cpu_millicores / 1000 : 0)
  const memLimit = mem?.limit_bytes || desired?.memory_bytes || 0
  const storageLimit = storage?.limit_bytes || desired?.storage_bytes || 0

  const withLimit = (label: string, limit: string) => (limit ? `${label} · ${limit}` : label)
  return [
    {
      key: 'cpu',
      label: withLimit(
        t('bots.container.metricsLabels.cpu'),
        cores > 0 ? t('bots.overview.runtimeCpuCores', { count: Number(cores.toFixed(3)) }) : '',
      ),
      value: formatMetricPercent(cpu?.usage_percent),
    },
    {
      key: 'memory',
      label: withLimit(t('bots.container.metricsLabels.memory'), memLimit > 0 ? formatMetricBytes(memLimit) : ''),
      // A paused runtime reports 0 bytes; "0 B" reads as a unit glitch next to
      // a GiB limit, so zero is shown in MiB like typical usage.
      value: mem?.usage_bytes === 0 ? '0 MiB' : formatMetricBytes(mem?.usage_bytes),
    },
    {
      key: 'storage',
      label: withLimit(t('bots.container.metricsLabels.storage'), storageLimit > 0 ? formatMetricBytes(storageLimit) : ''),
      value: formatMetricBytes(storage?.used_bytes),
    },
  ]
}
