import { CircleAlertIcon, CircleCheckIcon, CircleMinusIcon, TriangleAlertIcon } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { useI18n } from "@/i18n"
import type { RuleState, RuleType } from "@/lib/types"

const STATE_VARIANT = {
  running: "success",
  partial: "warning",
  error: "destructive",
  stopped: "secondary",
} as const

const STATE_ICON = {
  running: CircleCheckIcon,
  partial: TriangleAlertIcon,
  error: CircleAlertIcon,
  stopped: CircleMinusIcon,
}

/** Rule runtime state; always icon + label so it never relies on color. */
export function RuleStateBadge({
  state,
  ...props
}: { state: RuleState } & Omit<React.ComponentProps<typeof Badge>, "variant">) {
  const { t } = useI18n()
  const Icon = STATE_ICON[state] ?? CircleMinusIcon
  return (
    <Badge variant={STATE_VARIANT[state] ?? "secondary"} {...props}>
      <Icon data-icon="inline-start" />
      {t(`common.ruleState.${state}`)}
    </Badge>
  )
}

export function RuleTypeBadge({ type }: { type: RuleType }) {
  const { t } = useI18n()
  return (
    <Badge variant="outline" className="font-mono uppercase">
      {t(`common.ruleType.${type}`)}
    </Badge>
  )
}
