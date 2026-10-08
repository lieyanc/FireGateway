import type { Rule, RuleState, RuleView } from "@/lib/types"

/** Wraps IPv6 literals in brackets so a port can follow them. */
export function displayHost(host: string) {
  if (host.includes(":") && !host.startsWith("[")) return `[${host}]`
  return host
}

function portPart(port?: number, range?: [number, number]) {
  if (range) return range[0] === range[1] ? `${range[0]}` : `${range[0]}-${range[1]}`
  return port !== undefined ? `${port}` : "?"
}

export function formatListen(rule: Rule) {
  return `${displayHost(rule.localHost)}:${portPart(rule.localPort, rule.localPortRange)}`
}

export function formatTarget(rule: Rule) {
  return `${displayHost(rule.targetHost)}:${portPart(rule.targetPort, rule.targetPortRange)}`
}

/** Number of ports (and therefore listeners) a rule maps. */
export function portCount(rule: Rule) {
  if (rule.localPortRange) {
    return Math.max(1, rule.localPortRange[1] - rule.localPortRange[0] + 1)
  }
  return 1
}

/** Case-insensitive match on name, id, hosts, remark and ports. */
export function matchesSearch(rule: Rule, query: string) {
  const q = query.trim().toLowerCase()
  if (!q) return true
  const texts = [rule.name, rule.id, rule.localHost, rule.targetHost, rule.remark ?? ""]
  if (texts.some((s) => s.toLowerCase().includes(q))) return true
  if (formatListen(rule).toLowerCase().includes(q)) return true
  if (formatTarget(rule).toLowerCase().includes(q)) return true
  if (/^\d+$/.test(q)) {
    const port = Number(q)
    const within = (p?: number, r?: [number, number]) =>
      p === port || (!!r && port >= r[0] && port <= r[1])
    return (
      within(rule.localPort, rule.localPortRange) ||
      within(rule.targetPort, rule.targetPortRange)
    )
  }
  return false
}

export const RULE_STATES: RuleState[] = [
  "running",
  "partial",
  "error",
  "suspended",
  "stopped",
]

export const STATE_ORDER: Record<RuleState, number> = {
  error: 0,
  partial: 1,
  suspended: 2,
  running: 3,
  stopped: 4,
}

/** Strips the runtime part of a view, e.g. before duplicating or exporting. */
export function toRule(view: RuleView): Rule {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const { runtime, ...rule } = view
  return rule
}

/** Local date as YYYYMMDD, for export file names. */
export function dateStamp(date = new Date()) {
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${date.getFullYear()}${pad(date.getMonth() + 1)}${pad(date.getDate())}`
}
