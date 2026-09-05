export function networkNumber(value: number | null | undefined): string {
  return value == null || !Number.isFinite(value) ? '--' : value.toLocaleString(undefined, { maximumFractionDigits: 1 })
}

export function networkBytes(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '--'
  const unit = value >= 1e12 ? 'TB' : 'GB'
  return `${(value / (unit === 'TB' ? 1e12 : 1e9)).toLocaleString(undefined, { maximumFractionDigits: 3 })} ${unit}`
}

export function networkPercent(rate: number | null | undefined, capacity: number): number | null {
  return rate == null || !Number.isFinite(rate) || capacity <= 0 ? null : rate / capacity * 100
}
