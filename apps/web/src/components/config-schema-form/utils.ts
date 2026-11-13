export function cloneConfig<T>(value: T): T {
  if (value == null) return {} as T
  return JSON.parse(JSON.stringify(value))
}

