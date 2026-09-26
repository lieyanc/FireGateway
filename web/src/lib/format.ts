// Locale-aware formatting helpers. Pure functions; components get bound
// versions through useI18n().fmt so the active language is applied.

export type DurationUnits = {
  day: string
  hour: string
  minute: string
  second: string
}

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB", "PB"]

const numberFormats = new Map<string, Intl.NumberFormat>()

function numberFormat(locale: string, maximumFractionDigits: number) {
  const key = `${locale}|${maximumFractionDigits}`
  let nf = numberFormats.get(key)
  if (!nf) {
    nf = new Intl.NumberFormat(locale, { maximumFractionDigits })
    numberFormats.set(key, nf)
  }
  return nf
}

export function formatNumber(locale: string, value: number, digits = 0) {
  if (!Number.isFinite(value)) return "–"
  return numberFormat(locale, digits).format(value)
}

/** 1024-based byte size, e.g. "1.5 MB". */
export function formatBytes(locale: string, bytes: number) {
  if (!Number.isFinite(bytes)) return "–"
  let value = Math.abs(bytes)
  let unit = 0
  // Step up at 1000 rather than 1024 so values never read "1,021 KB".
  while (value >= 1000 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit++
  }
  const digits = unit === 0 ? 0 : value < 10 ? 2 : value < 100 ? 1 : 0
  const sign = bytes < 0 ? "-" : ""
  return `${sign}${formatNumber(locale, value, digits)} ${BYTE_UNITS[unit]}`
}

export function formatRate(locale: string, bytesPerSecond: number) {
  return `${formatBytes(locale, bytesPerSecond)}/s`
}

/** Compact duration using the two most significant units, e.g. "2h 5m". */
export function formatDuration(
  seconds: number,
  units: DurationUnits,
  parts = 2
) {
  if (!Number.isFinite(seconds) || seconds < 0) seconds = 0
  const s = Math.floor(seconds)
  const values: [number, string][] = [
    [Math.floor(s / 86400), units.day],
    [Math.floor((s % 86400) / 3600), units.hour],
    [Math.floor((s % 3600) / 60), units.minute],
    [s % 60, units.second],
  ]
  const first = values.findIndex(([v]) => v > 0)
  if (first === -1) return `0${units.second}`
  return values
    .slice(first, first + parts)
    .filter(([v], i) => i === 0 || v > 0)
    .map(([v, u]) => `${v}${u}`)
    .join(" ")
}

const relativeFormats = new Map<string, Intl.RelativeTimeFormat>()

export function formatRelative(locale: string, date: Date, now = Date.now()) {
  let rtf = relativeFormats.get(locale)
  if (!rtf) {
    rtf = new Intl.RelativeTimeFormat(locale, { numeric: "auto" })
    relativeFormats.set(locale, rtf)
  }
  const diff = (date.getTime() - now) / 1000
  const abs = Math.abs(diff)
  if (abs < 5) return rtf.format(0, "second")
  if (abs < 60) return rtf.format(Math.round(diff), "second")
  if (abs < 3600) return rtf.format(Math.round(diff / 60), "minute")
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), "hour")
  if (abs < 86400 * 30) return rtf.format(Math.round(diff / 86400), "day")
  if (abs < 86400 * 365) return rtf.format(Math.round(diff / 86400 / 30), "month")
  return rtf.format(Math.round(diff / 86400 / 365), "year")
}

const dateFormats = new Map<string, Intl.DateTimeFormat>()

function dateFormat(locale: string, options: Intl.DateTimeFormatOptions) {
  const key = locale + JSON.stringify(options)
  let df = dateFormats.get(key)
  if (!df) {
    df = new Intl.DateTimeFormat(locale, options)
    dateFormats.set(key, df)
  }
  return df
}

export function formatDateTime(locale: string, date: Date) {
  if (Number.isNaN(date.getTime())) return "–"
  return dateFormat(locale, { dateStyle: "medium", timeStyle: "medium" }).format(
    date
  )
}

export function formatTime(locale: string, date: Date, seconds = true) {
  if (Number.isNaN(date.getTime())) return "–"
  return dateFormat(locale, {
    hour: "2-digit",
    minute: "2-digit",
    second: seconds ? "2-digit" : undefined,
    hour12: false,
  }).format(date)
}

export function formatShortDateTime(locale: string, date: Date) {
  if (Number.isNaN(date.getTime())) return "–"
  return dateFormat(locale, {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date)
}

export function formatPercent(locale: string, ratio: number, digits = 1) {
  if (!Number.isFinite(ratio)) return "–"
  return new Intl.NumberFormat(locale, {
    style: "percent",
    maximumFractionDigits: digits,
  }).format(ratio)
}

export function parseDate(value: string | undefined | null) {
  return value ? new Date(value) : new Date(NaN)
}

/** Keeps "1.5 MB" on one line (chart ticks wrap at regular spaces). */
export function nbsp(text: string) {
  return text.replace(/ /g, "\u00a0")
}

/** Tooltip order for up/down series: upload first, like the legend. */
export function upFirst(item: { dataKey?: unknown }) {
  return item.dataKey === "up" ? 0 : 1
}
