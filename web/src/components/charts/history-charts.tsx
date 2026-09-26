import * as React from "react"
import { Area, AreaChart, Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"

import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart"
import { useI18n, type Formatters } from "@/i18n"
import type { MetricPoint, MetricRange } from "@/lib/types"
import { nbsp, upFirst } from "@/lib/format"
import { cn } from "@/lib/utils"

function timeTick(fmt: Formatters, range: MetricRange) {
  return (v: number) =>
    range === "1h" || range === "24h"
      ? fmt.time(v * 1000, false)
      : nbsp(fmt.shortDateTime(v * 1000))
}

function tooltipLabel(fmt: Formatters, step: number) {
  return (_: unknown, payload: ReadonlyArray<{ payload?: { t?: number } }>) => {
    const t = payload?.[0]?.payload?.t
    if (t === undefined) return ""
    return `${fmt.shortDateTime(t * 1000)} – ${fmt.time((t + step) * 1000, false)}`
  }
}

/** Stacked up/down bytes per bucket. */
export function TrafficHistoryChart({
  points,
  step,
  range,
  className,
}: {
  points: MetricPoint[]
  step: number
  range: MetricRange
  className?: string
}) {
  const { t, fmt } = useI18n()
  const config = React.useMemo<ChartConfig>(
    () => ({
      up: { label: t("common.metrics.upload"), color: "var(--chart-1)" },
      down: { label: t("common.metrics.download"), color: "var(--chart-2)" },
    }),
    [t]
  )

  return (
    <ChartContainer
      config={config}
      className={cn("aspect-auto h-72 w-full", className)}
      role="img"
      aria-label={t("common.metrics.traffic")}
    >
      <AreaChart data={points} margin={{ left: 4, right: 8, top: 8 }}>
        <CartesianGrid vertical={false} />
        <XAxis
          dataKey="t"
          type="number"
          scale="time"
          domain={["dataMin", "dataMax"]}
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          minTickGap={56}
          tickFormatter={timeTick(fmt, range)}
        />
        <YAxis
          width={72}
          tickLine={false}
          axisLine={false}
          tickCount={4}
          tickFormatter={(v: number) => nbsp(fmt.bytes(v))}
        />
        <ChartTooltip
          itemSorter={upFirst}
          content={
            <ChartTooltipContent
              indicator="line"
              labelFormatter={tooltipLabel(fmt, step)}
              valueFormatter={(v) => fmt.bytes(Number(v))}
            />
          }
        />
        <Area
          dataKey="up"
          stackId="traffic"
          type="monotone"
          stroke="var(--color-up)"
          strokeWidth={2}
          fill="var(--color-up)"
          fillOpacity={0.25}
          isAnimationActive={false}
          dot={false}
        />
        <Area
          dataKey="down"
          stackId="traffic"
          type="monotone"
          stroke="var(--color-down)"
          strokeWidth={2}
          fill="var(--color-down)"
          fillOpacity={0.25}
          isAnimationActive={false}
          dot={false}
        />
        <ChartLegend itemSorter={null} content={<ChartLegendContent />} />
      </AreaChart>
    </ChartContainer>
  )
}

/** New connections per bucket (single series, so no legend). */
export function ConnectionsHistoryChart({
  points,
  step,
  range,
  className,
}: {
  points: MetricPoint[]
  step: number
  range: MetricRange
  className?: string
}) {
  const { t, fmt } = useI18n()
  const config = React.useMemo<ChartConfig>(
    () => ({
      conns: { label: t("common.metrics.newConnections"), color: "var(--chart-3)" },
    }),
    [t]
  )

  return (
    <ChartContainer
      config={config}
      className={cn("aspect-auto h-56 w-full", className)}
      role="img"
      aria-label={t("common.metrics.newConnections")}
    >
      <BarChart data={points} margin={{ left: 4, right: 8, top: 8 }}>
        <CartesianGrid vertical={false} />
        <XAxis
          dataKey="t"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          minTickGap={56}
          tickFormatter={timeTick(fmt, range)}
        />
        <YAxis
          width={48}
          tickLine={false}
          axisLine={false}
          tickCount={4}
          allowDecimals={false}
          tickFormatter={(v: number) => fmt.number(v)}
        />
        <ChartTooltip
          cursor={{ fill: "var(--muted)" }}
          content={
            <ChartTooltipContent
              labelFormatter={tooltipLabel(fmt, step)}
              valueFormatter={(v) => fmt.number(Number(v))}
            />
          }
        />
        <Bar
          dataKey="conns"
          fill="var(--color-conns)"
          radius={[2, 2, 0, 0]}
          isAnimationActive={false}
        />
      </BarChart>
    </ChartContainer>
  )
}
