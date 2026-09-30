import type { MessagePath } from "@/i18n/define"
import { cluster } from "@/i18n/messages/cluster"
import { auth } from "@/i18n/messages/auth"
import { common, shell } from "@/i18n/messages/common"
import { connections } from "@/i18n/messages/connections"
import { dashboard } from "@/i18n/messages/dashboard"
import { logs } from "@/i18n/messages/logs"
import { rules } from "@/i18n/messages/rules"
import { settings } from "@/i18n/messages/settings"
import { traffic } from "@/i18n/messages/traffic"

const namespaces = {
  cluster,
  common,
  shell,
  auth,
  dashboard,
  rules,
  connections,
  traffic,
  logs,
  settings,
}

type Namespaces = typeof namespaces
export type Messages = { [K in keyof Namespaces]: Namespaces[K]["en"] }
export type MessageKey = MessagePath<Messages>
export type Lang = "en" | "zh"
export const LANGS: Lang[] = ["en", "zh"]

type Tree = { [key: string]: string | Tree }

function flatten(tree: Tree, prefix: string, out: Map<string, string>) {
  for (const [key, value] of Object.entries(tree)) {
    const path = prefix + key
    if (typeof value === "string") out.set(path, value)
    else flatten(value, path + ".", out)
  }
  return out
}

function build(lang: Lang) {
  const out = new Map<string, string>()
  for (const [ns, messages] of Object.entries(namespaces)) {
    flatten(messages[lang] as Tree, ns + ".", out)
  }
  return out
}

export const dictionaries: Record<Lang, Map<string, string>> = {
  en: build("en"),
  zh: build("zh"),
}
