/* eslint-disable react-refresh/only-export-components */
import * as React from "react"

import {
  dictionaries,
  LANGS,
  type Lang,
  type MessageKey,
} from "@/i18n/messages"
import {
  formatBytes,
  formatDateTime,
  formatDuration,
  formatNumber,
  formatPercent,
  formatRate,
  formatRelative,
  formatShortDateTime,
  formatTime,
  parseDate,
} from "@/lib/format"

export type { Lang, MessageKey }
export { LANGS }

const STORAGE_KEY = "fg-lang"

export const LANG_LABELS: Record<Lang, string> = {
  en: "English",
  zh: "简体中文",
}

const LOCALES: Record<Lang, string> = { en: "en-US", zh: "zh-CN" }

export type TranslateVars = Record<string, string | number>
export type Translate = (key: MessageKey, vars?: TranslateVars) => string

function detectLang(): Lang {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored === "en" || stored === "zh") return stored
  } catch {
    // Ignore storage errors and fall back to the browser language.
  }
  const preferred = navigator.languages?.length
    ? navigator.languages
    : [navigator.language]
  for (const tag of preferred) {
    const lower = tag.toLowerCase()
    if (lower.startsWith("zh")) return "zh"
    if (lower.startsWith("en")) return "en"
  }
  return "en"
}

function interpolate(template: string, vars?: TranslateVars) {
  if (!vars) return template
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    name in vars ? String(vars[name]) : match
  )
}

function makeTranslate(lang: Lang): Translate {
  const dict = dictionaries[lang]
  const fallback = dictionaries.en
  return (key, vars) =>
    interpolate(dict.get(key) ?? fallback.get(key) ?? key, vars)
}

type DateInput = Date | string | number | undefined | null

function toDate(value: DateInput) {
  if (value instanceof Date) return value
  if (typeof value === "number") return new Date(value)
  return parseDate(value)
}

function makeFormatters(lang: Lang, t: Translate) {
  const locale = LOCALES[lang]
  const units = {
    day: t("common.units.day"),
    hour: t("common.units.hour"),
    minute: t("common.units.minute"),
    second: t("common.units.second"),
  }
  return {
    locale,
    number: (value: number, digits = 0) => formatNumber(locale, value, digits),
    bytes: (value: number) => formatBytes(locale, value),
    rate: (value: number) => formatRate(locale, value),
    percent: (ratio: number, digits = 1) => formatPercent(locale, ratio, digits),
    duration: (seconds: number, parts = 2) =>
      formatDuration(seconds, units, parts),
    relative: (value: DateInput, now?: number) =>
      formatRelative(locale, toDate(value), now),
    dateTime: (value: DateInput) => formatDateTime(locale, toDate(value)),
    shortDateTime: (value: DateInput) =>
      formatShortDateTime(locale, toDate(value)),
    time: (value: DateInput, seconds = true) =>
      formatTime(locale, toDate(value), seconds),
  }
}

export type Formatters = ReturnType<typeof makeFormatters>

type I18nContextValue = {
  lang: Lang
  setLang: (lang: Lang) => void
  t: Translate
  fmt: Formatters
}

const I18nContext = React.createContext<I18nContextValue | null>(null)

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const [lang, setLangState] = React.useState<Lang>(detectLang)

  React.useEffect(() => {
    document.documentElement.lang = LOCALES[lang]
  }, [lang])

  const setLang = React.useCallback((next: Lang) => {
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // Ignore: the choice still applies for this session.
    }
    setLangState(next)
  }, [])

  const value = React.useMemo<I18nContextValue>(() => {
    const t = makeTranslate(lang)
    return { lang, setLang, t, fmt: makeFormatters(lang, t) }
  }, [lang, setLang])

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}

export function useI18n() {
  const context = React.useContext(I18nContext)
  if (!context) throw new Error("useI18n must be used within an I18nProvider")
  return context
}
