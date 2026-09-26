import { Link } from "react-router"

import { RuleStateBadge } from "@/components/common/rule-badges"
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover"
import { useI18n } from "@/i18n"
import { useRuleStats } from "@/lib/stats-store"
import type { RuleView } from "@/lib/types"
import { formatListen, formatTarget } from "@/features/rules/utils"

// Each cell subscribes to its own slice of the stats stream, so a 1 Hz tick
// re-renders these cells only, never the whole table.

function useLiveRuleState(rule: RuleView) {
  const stats = useRuleStats(rule.id)
  return stats?.state ?? rule.runtime.state
}

/** State badge; when ports failed to bind, the badge opens the failure list. */
export function RuleStateCell({ rule }: { rule: RuleView }) {
  const { t } = useI18n()
  const state = useLiveRuleState(rule)
  const failures = rule.runtime.failures ?? []

  if (failures.length === 0 || state === "stopped" || state === "running") {
    return <RuleStateBadge state={state} />
  }

  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="rounded-4xl outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
          aria-label={t("rules.failures.summary", { count: failures.length })}
        >
          <RuleStateBadge state={state} />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-80">
        <PopoverHeader>
          <PopoverTitle>{t("rules.failures.title")}</PopoverTitle>
          <PopoverDescription>{t("rules.failures.hint")}</PopoverDescription>
        </PopoverHeader>
        <FailureList rule={rule} />
      </PopoverContent>
    </Popover>
  )
}

export function FailureList({ rule }: { rule: RuleView }) {
  const { t } = useI18n()
  return (
    <ul className="flex max-h-60 flex-col gap-1.5 overflow-y-auto text-sm">
      {(rule.runtime.failures ?? []).map((f) => (
        <li key={f.port} className="flex flex-col">
          <span className="font-medium">{t("rules.failures.port", { port: f.port })}</span>
          <span className="font-mono text-xs break-all text-muted-foreground">{f.error}</span>
        </li>
      ))}
    </ul>
  )
}

export function RuleActiveCell({ rule }: { rule: RuleView }) {
  const { fmt } = useI18n()
  const stats = useRuleStats(rule.id)
  return <>{fmt.number(stats?.activeConnections ?? rule.runtime.activeConnections)}</>
}

export function RuleRateCell({ rule }: { rule: RuleView }) {
  const { fmt } = useI18n()
  const stats = useRuleStats(rule.id)
  const up = stats?.rateUp ?? rule.runtime.rateUp
  const down = stats?.rateDown ?? rule.runtime.rateDown
  return (
    <div className="flex flex-col items-end text-xs leading-tight tabular-nums">
      <span>↑ {fmt.rate(up)}</span>
      <span className="text-muted-foreground">↓ {fmt.rate(down)}</span>
    </div>
  )
}

export function RuleTrafficCell({ rule }: { rule: RuleView }) {
  const { fmt } = useI18n()
  const stats = useRuleStats(rule.id)
  const up = stats?.bytesUp ?? rule.runtime.bytesUp
  const down = stats?.bytesDown ?? rule.runtime.bytesDown
  return (
    <span className="text-xs tabular-nums" title={`↑ ${fmt.bytes(up)} / ↓ ${fmt.bytes(down)}`}>
      {fmt.bytes(up + down)}
    </span>
  )
}

export function RuleRoute({ rule }: { rule: RuleView }) {
  return (
    <span className="font-mono text-xs">
      <span className="whitespace-nowrap">{formatListen(rule)}</span>
      <span className="px-1 text-muted-foreground">→</span>
      <wbr />
      <span className="whitespace-nowrap">{formatTarget(rule)}</span>
    </span>
  )
}

export function RuleNameCell({ rule }: { rule: RuleView }) {
  return (
    <div className="flex min-w-0 flex-col">
      <Link
        to={`/rules/${encodeURIComponent(rule.id)}`}
        className="truncate font-medium underline-offset-4 hover:underline"
      >
        {rule.name || rule.id}
      </Link>
      <span className="truncate font-mono text-xs text-muted-foreground">{rule.id}</span>
    </div>
  )
}
