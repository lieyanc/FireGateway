import * as React from "react"
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from "recharts"

import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart"
import { useI18n } from "@/i18n"
import { useRealtimeMetrics } from "@/lib/queries"
import {
  SERIES_CAPACITY,
  statsStore,
  TOTAL_SERIES,
  useLiveSeries,
} from "@/lib/stats-store"
import { nbsp, upFirst } from "@/lib/format"
import { cn } from "@/lib/utils"

/**
 * Live up/down throughput for the last five minutes: pre-filled from
 * /api/metrics/realtime, then extended by the SSE `stats` ticks.
 */
export function LiveThroughputChart({
  ruleId,
  className,
}: {
  ruleId?: string
  className?: string
}) {
  const { t, fmt } = useI18n()
  const points = useLiveSeries(ruleId)
  const realtime = useRealtimeMetrics(ruleId)

  React.useEffect(() => {
    if (realtime.data) {
      statsStore.seed(ruleId ?? TOTAL_SERIES, realtime.data.points)
    }
  }, [realtime.data, ruleId])

  const config = React.useMemo<ChartConfig>(
    () => ({
      up: { label: t("common.metrics.upload"), color: "var(--chart-1)" },
      down: { label: t("common.metrics.download"), color: "var(--chart-2)" },
    }),
    [t]
  )

  const last = points.length ? points[points.length - 1].t : 0
  const domain: [number, number] = last
    ? [last - SERIES_CAPACITY + 1, last]
    : [0, 1]

  return (
    <ChartContainer
      config={config}
      className={cn("aspect-auto h-64 w-full", className)}
      role="img"
      aria-label={t("dashboard.throughput")}
    >
      <AreaChart data={points} margin={{ left: 4, right: 8, top: 8 }}>
        <defs>
          <linearGradient id="fill-up" x1="0" y1="0" x2="0" y2="1">
            <stop offset="5%" stopColor="var(--color-up)" stopOpacity={0.35} />
            <stop offset="95%" stopColor="var(--color-up)" stopOpacity={0.02} />
          </linearGradient>
          <linearGradient id="fill-down" x1="0" y1="0" x2="0" y2="1">
            <stop offset="5%" stopColor="var(--color-down)" stopOpacity={0.35} />
            <stop offset="95%" stopColor="var(--color-down)" stopOpacity={0.02} />
          </linearGradient>
        </defs>
        <CartesianGrid vertical={false} />
        <XAxis
          dataKey="t"
          type="number"
          scale="time"
          domain={domain}
          allowDataOverflow
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          minTickGap={48}
          tickFormatter={(v: number) => fmt.time(v * 1000, false)}
        />
        <YAxis
          width={72}
          tickLine={false}
          axisLine={false}
          tickCount={4}
          tickFormatter={(v: number) => nbsp(fmt.rate(v))}
        />
        <ChartTooltip
          cursor
          isAnimationActive={false}
          itemSorter={upFirst}
          content={
            <ChartTooltipContent
              indicator="line"
              labelFormatter={(_, payload) =>
                payload?.[0] ? fmt.time(Number(payload[0].payload.t) * 1000) : ""
              }
              valueFormatter={(v) => fmt.rate(Number(v))}
            />
          }
        />
        <Area
          dataKey="up"
          type="monotone"
          stroke="var(--color-up)"
          strokeWidth={2}
          fill="url(#fill-up)"
          isAnimationActive={false}
          dot={false}
        />
        <Area
          dataKey="down"
          type="monotone"
          stroke="var(--color-down)"
          strokeWidth={2}
          fill="url(#fill-down)"
          isAnimationActive={false}
          dot={false}
        />
        <ChartLegend itemSorter={null} content={<ChartLegendContent />} />
      </AreaChart>
    </ChartContainer>
  )
}
