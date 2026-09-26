import { z } from "zod"

import type { Translate } from "@/i18n"
import type { Rule, RuleType } from "@/lib/types"

// ---- Address validation ----

export function isIPv4(value: string) {
  const parts = value.split(".")
  return (
    parts.length === 4 &&
    parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) <= 255 && (p === "0" || !p.startsWith("0")))
  )
}

export function isIPv6(value: string) {
  if (!/^[0-9a-fA-F:.]+$/.test(value)) return false
  const doubleColon = value.split("::")
  if (doubleColon.length > 2) return false

  const parseGroups = (part: string) => (part === "" ? [] : part.split(":"))
  const head = parseGroups(doubleColon[0])
  const tail = doubleColon.length === 2 ? parseGroups(doubleColon[1]) : []
  const groups = [...head, ...tail]

  // An embedded IPv4 address may only appear as the final group.
  let slots = 0
  for (let i = 0; i < groups.length; i++) {
    const g = groups[i]
    if (g.includes(".")) {
      if (i !== groups.length - 1 || !isIPv4(g)) return false
      slots += 2
    } else {
      if (!/^[0-9a-fA-F]{1,4}$/.test(g)) return false
      slots += 1
    }
  }
  return doubleColon.length === 2 ? slots < 8 : slots === 8
}

/** IPv4/IPv6 address, optionally with a CIDR prefix length. */
export function isIpOrCidr(value: string) {
  const [addr, prefix, extra] = value.split("/")
  if (extra !== undefined) return false
  const v4 = isIPv4(addr)
  const v6 = !v4 && isIPv6(addr)
  if (!v4 && !v6) return false
  if (prefix === undefined) return true
  if (!/^\d{1,3}$/.test(prefix)) return false
  return Number(prefix) <= (v4 ? 32 : 128)
}

/** Host name or IP literal: no whitespace, no port, brackets allowed around IPv6. */
export function normalizeHost(value: string) {
  const v = value.trim()
  if (v.startsWith("[") && v.endsWith("]")) return v.slice(1, -1)
  return v
}

function isValidHost(value: string) {
  const v = normalizeHost(value)
  if (!v || /\s/.test(v)) return false
  if (v.includes(":")) return isIPv6(v)
  return /^[A-Za-z0-9._-]+$/.test(v)
}

// ---- Form model ----

export type BandwidthUnit = "KB" | "MB"
export type AclModeValue = "none" | "allow" | "deny"
export type MappingMode = "single" | "range"

export type RuleFormValues = {
  name: string
  type: RuleType
  enabled: boolean
  mode: MappingMode
  localHost: string
  localPort: string
  targetHost: string
  targetPort: string
  localStart: string
  localEnd: string
  targetStart: string
  targetEnd: string
  remark: string
  aclMode: AclModeValue
  cidrs: string
  maxConnections: string
  maxConnectionsPerIp: string
  bandwidth: string
  bandwidthUnit: BandwidthUnit
}

const UNIT_BYTES: Record<BandwidthUnit, number> = { KB: 1024, MB: 1024 * 1024 }

export function parsePort(value: string) {
  const v = value.trim()
  if (!/^\d{1,5}$/.test(v)) return null
  const n = Number(v)
  return n >= 1 && n <= 65535 ? n : null
}

function parseCount(value: string) {
  const v = value.trim()
  if (v === "") return 0
  if (!/^\d+$/.test(v)) return null
  return Number(v)
}

function parseDecimal(value: string) {
  const v = value.trim()
  if (v === "") return 0
  if (!/^\d+(\.\d+)?$/.test(v)) return null
  return Number(v)
}

export function cidrLines(text: string) {
  return text
    .split(/\r?\n/)
    .map((line, index) => ({ line: index + 1, value: line.trim() }))
    .filter((l) => l.value !== "")
}

export const emptyRuleForm: RuleFormValues = {
  name: "",
  type: "tcp",
  enabled: true,
  mode: "single",
  localHost: "0.0.0.0",
  localPort: "",
  targetHost: "",
  targetPort: "",
  localStart: "",
  localEnd: "",
  targetStart: "",
  targetEnd: "",
  remark: "",
  aclMode: "none",
  cidrs: "",
  maxConnections: "",
  maxConnectionsPerIp: "",
  bandwidth: "",
  bandwidthUnit: "MB",
}

function bandwidthToForm(bytes?: number): { value: string; unit: BandwidthUnit } {
  if (!bytes) return { value: "", unit: "MB" }
  // Prefer MB when two decimals of MB reproduce the stored byte count exactly.
  const mb = Math.round((bytes / UNIT_BYTES.MB) * 100) / 100
  if (bytes >= UNIT_BYTES.MB && Math.round(mb * UNIT_BYTES.MB) === bytes) {
    return { value: String(mb), unit: "MB" }
  }
  return { value: String(Math.round((bytes / UNIT_BYTES.KB) * 100) / 100), unit: "KB" }
}

const str = (n?: number) => (n ? String(n) : "")

export function ruleToForm(rule: Rule): RuleFormValues {
  const bw = bandwidthToForm(rule.limits?.bandwidth)
  const range = !!rule.localPortRange
  return {
    name: rule.name,
    type: rule.type,
    enabled: rule.status === "active",
    mode: range ? "range" : "single",
    localHost: rule.localHost,
    localPort: str(rule.localPort),
    targetHost: rule.targetHost,
    targetPort: str(rule.targetPort),
    localStart: str(rule.localPortRange?.[0]),
    localEnd: str(rule.localPortRange?.[1]),
    targetStart: str(rule.targetPortRange?.[0]),
    targetEnd: str(rule.targetPortRange?.[1]),
    remark: rule.remark ?? "",
    aclMode: rule.acl ? rule.acl.mode : "none",
    cidrs: rule.acl?.cidrs.join("\n") ?? "",
    maxConnections: str(rule.limits?.maxConnections),
    maxConnectionsPerIp: str(rule.limits?.maxConnectionsPerIp),
    bandwidth: bw.value,
    bandwidthUnit: bw.unit,
  }
}

/** Builds the API payload. Call only with values that passed the schema. */
export function formToRule(values: RuleFormValues, id: string): Rule {
  const rule: Rule = {
    id,
    name: values.name.trim(),
    type: values.type,
    status: values.enabled ? "active" : "inactive",
    localHost: normalizeHost(values.localHost),
    targetHost: normalizeHost(values.targetHost),
  }
  if (values.mode === "single") {
    rule.localPort = parsePort(values.localPort) ?? undefined
    rule.targetPort = parsePort(values.targetPort) ?? undefined
  } else {
    rule.localPortRange = [parsePort(values.localStart)!, parsePort(values.localEnd)!]
    rule.targetPortRange = [parsePort(values.targetStart)!, parsePort(values.targetEnd)!]
  }
  const remark = values.remark.trim()
  if (remark) rule.remark = remark
  if (values.aclMode !== "none") {
    rule.acl = { mode: values.aclMode, cidrs: cidrLines(values.cidrs).map((l) => l.value) }
  }
  const maxConnections = parseCount(values.maxConnections) ?? 0
  const maxConnectionsPerIp = parseCount(values.maxConnectionsPerIp) ?? 0
  const bandwidth = Math.round(
    (parseDecimal(values.bandwidth) ?? 0) * UNIT_BYTES[values.bandwidthUnit]
  )
  if (maxConnections || maxConnectionsPerIp || bandwidth) {
    rule.limits = { maxConnections, maxConnectionsPerIp, bandwidth }
  }
  return rule
}

export function makeRuleSchema(t: Translate) {
  const text = z.string()
  return z
    .object({
      name: text.trim().min(1, t("rules.form.errors.nameRequired")),
      type: z.enum(["tcp", "udp"]),
      enabled: z.boolean(),
      mode: z.enum(["single", "range"]),
      localHost: text,
      localPort: text,
      targetHost: text,
      targetPort: text,
      localStart: text,
      localEnd: text,
      targetStart: text,
      targetEnd: text,
      remark: text,
      aclMode: z.enum(["none", "allow", "deny"]),
      cidrs: text,
      maxConnections: text,
      maxConnectionsPerIp: text,
      bandwidth: text,
      bandwidthUnit: z.enum(["KB", "MB"]),
    })
    .superRefine((v, ctx) => {
      const issue = (path: keyof RuleFormValues, message: string) =>
        ctx.addIssue({ code: "custom", path: [path], message })

      for (const key of ["localHost", "targetHost"] as const) {
        if (!v[key].trim()) issue(key, t("rules.form.errors.hostRequired"))
        else if (!isValidHost(v[key])) issue(key, t("rules.form.errors.hostInvalid"))
      }

      const portInvalid = t("rules.form.errors.portInvalid")
      if (v.mode === "single") {
        if (parsePort(v.localPort) === null) issue("localPort", portInvalid)
        if (parsePort(v.targetPort) === null) issue("targetPort", portInvalid)
      } else {
        const ls = parsePort(v.localStart)
        const le = parsePort(v.localEnd)
        const ts = parsePort(v.targetStart)
        const te = parsePort(v.targetEnd)
        if (ls === null) issue("localStart", portInvalid)
        if (le === null) issue("localEnd", portInvalid)
        if (ts === null) issue("targetStart", portInvalid)
        if (te === null) issue("targetEnd", portInvalid)
        if (ls !== null && le !== null && le < ls) {
          issue("localEnd", t("rules.form.errors.rangeOrder"))
        } else if (ts !== null && te !== null && te < ts) {
          issue("targetEnd", t("rules.form.errors.rangeOrder"))
        } else if (ls !== null && le !== null && ts !== null && te !== null) {
          const count = le - ls + 1
          if (te - ts + 1 !== count) {
            issue("targetEnd", t("rules.form.errors.rangeLength", { count }))
          }
        }
      }

      if (v.aclMode !== "none") {
        const lines = cidrLines(v.cidrs)
        if (lines.length === 0) issue("cidrs", t("rules.form.errors.cidrsRequired"))
        const bad = lines.find((l) => !isIpOrCidr(l.value))
        if (bad) {
          issue(
            "cidrs",
            t("rules.form.errors.cidrInvalid", { line: bad.line, value: bad.value })
          )
        }
      }

      for (const key of ["maxConnections", "maxConnectionsPerIp"] as const) {
        if (parseCount(v[key]) === null) issue(key, t("rules.form.errors.numberInvalid"))
      }
      if (parseDecimal(v.bandwidth) === null) {
        issue("bandwidth", t("rules.form.errors.bandwidthInvalid"))
      }
    })
}

/**
 * Maps a server validation `field` (e.g. "localPortRange", "acl.cidrs[2]")
 * onto a form field. Returns null when there is no matching input.
 */
export function serverFieldToForm(
  field: string,
  values: RuleFormValues
): { name: keyof RuleFormValues; line?: number } | null {
  const f = field.replace(/^forward\[\d+\]\./, "")
  const index = /\[(\d+)\]$/.exec(f)
  const base = f.replace(/\[\d+\]$/, "")
  const i = index ? Number(index[1]) : undefined

  switch (base) {
    case "name":
    case "type":
    case "localHost":
    case "targetHost":
    case "remark":
      return { name: base }
    case "status":
      return { name: "enabled" }
    case "localPort":
      return { name: values.mode === "single" ? "localPort" : "localStart" }
    case "targetPort":
      return { name: values.mode === "single" ? "targetPort" : "targetStart" }
    case "localPortRange":
      if (values.mode === "single") return { name: "localPort" }
      return { name: i === 1 ? "localEnd" : "localStart" }
    case "targetPortRange":
      if (values.mode === "single") return { name: "targetPort" }
      return { name: i === 0 ? "targetStart" : "targetEnd" }
    case "acl":
    case "acl.mode":
      return { name: "aclMode" }
    case "acl.cidrs": {
      if (i === undefined) return { name: "cidrs" }
      // Server indexes count non-empty lines only; map back to the text line.
      const line = cidrLines(values.cidrs)[i]?.line
      return { name: "cidrs", line }
    }
    case "limits.maxConnections":
      return { name: "maxConnections" }
    case "limits.maxConnectionsPerIp":
      return { name: "maxConnectionsPerIp" }
    case "limits.bandwidth":
      return { name: "bandwidth" }
    default:
      return null
  }
}
