export function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  const decimals = unit === 0 ? 0 : value >= 100 ? 0 : 1
  return `${value.toFixed(decimals)} ${units[unit]}`
}

export function formatDuration(seconds: number): string {
  if (!seconds || seconds <= 0) return '0s'
  const s = Math.floor(seconds)
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${sec}s`
  return `${sec}s`
}

export function formatTime(epochMs: number): string {
  return new Date(epochMs).toLocaleTimeString([], { hour12: false })
}

export function formatDateTime(epochMs: number): string {
  const d = new Date(epochMs)
  return `${d.toLocaleDateString()} ${d.toLocaleTimeString([], { hour12: false })}`
}

/** Format for <input type="datetime-local"> (local time, no seconds). */
export function toLocalInputValue(epochMs: number): string {
  const d = new Date(epochMs)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function fromLocalInputValue(value: string): number {
  return new Date(value).getTime()
}
