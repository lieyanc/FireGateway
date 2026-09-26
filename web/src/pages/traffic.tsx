import * as React from "react"
import { Link, useSearchParams } from "react-router"
import {
  ActivityIcon,
  ArrowDownIcon,
  ArrowUpIcon,
  ChartColumnIcon,
  SigmaIcon,
  type LucideIcon,
} from "lucide-react"

import {
  ConnectionsHistoryChart,
  TrafficHistoryChart,
} from "@/components/charts/history-charts"
import { PageContainer, PageHeader } from "@/components/common/page-header"
import { QueryError } from "@/components/common/query-state"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Progress } from "@/components/ui/progress"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useI18n } from "@/i18n"
import { useHistoryMetrics, useRules, useTopRules } from "@/lib/queries"
import type { MetricPoint, MetricRange } from "@/lib/types"
import { cn } from "@/lib/utils"

const RANGES: MetricRange[] = ["1h", "24h", "7d", "30d"]
const ALL = "__all__"

function isRange(value: string | null): value is MetricRange {
  return RANGES.includes(value as MetricRange)
}

function summarize(points: MetricPoint[]) {
  let up = 0
  let down = 0
  let conns = 0
  let peak = 0
  for (const p of points) {
    up += p.up
    down += p.down
    conns += p.conns
    peak = Math.max(peak, p.active)
  }
  return { up, down, conns, peak }
}

function TotalCard({
  title,
  icon: Icon,
  value,
  footer,
  loading,
}: {
  title: string
  icon: LucideIcon
  value: React.ReactNode
  footer?: React.ReactNode
  loading: boolean
}) {
  return (
    <Card>
      <CardHeader>
        <CardDescription className="flex items-center gap-2">
          <Icon className="size-4" aria-hidden />
          {title}
        </CardDescription>
        {loading ? (
          <Skeleton className="h-7 w-24" />
        ) : (
          <CardTitle className="text-2xl font-semibold tabular-nums">
            {value}
          </CardTitle>
        )}
      </CardHeader>
      {footer && !loading && (
        <CardContent className="text-xs text-muted-foreground">{footer}</CardContent>
      )}
    </Card>
  )
}

export default function TrafficPage() {
  const { t, fmt } = useI18n()
  const [params, setParams] = useSearchParams()
  const rangeParam = params.get("range")
  const range: MetricRange = isRange(rangeParam) ? rangeParam : "24h"
  const rule = params.get("rule") || undefined

  const rules = useRules()
  const history = useHistoryMetrics(range, rule)
  const top = useTopRules(range)

  const update = (key: string, value: string | undefined) => {
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (value) next.set(key, value)
        else next.delete(key)
        return next
      },
      { replace: true }
    )
  }

  const points = React.useMemo(() => history.data?.points ?? [], [history.data])
  const step = history.data?.step ?? (range === "1h" || range === "24h" ? 60 : 3600)
  const totals = React.useMemo(() => summarize(points), [points])
  const bucket = step >= 3600 ? t("traffic.bucketHour") : t("traffic.bucketMinute")
  const loading = history.isPending
  const hasTraffic = points.some((p) => p.up || p.down || p.conns)

  const ruleNames = React.useMemo(
    () => new Map(rules.data?.map((r) => [r.id, r.name || r.id])),
    [rules.data]
  )
  const topItems = top.data ?? []
  const topTotal = topItems.reduce((sum, i) => sum + i.up + i.down, 0)

  return (
    <PageContainer>
      <PageHeader
        title={t("traffic.title")}
        description={t("traffic.description")}
        actions={
          <>
            <Select
              value={rule ?? ALL}
              onValueChange={(v) => update("rule", v === ALL ? undefined : v)}
            >
              <SelectTrigger className="w-full sm:w-52" aria-label={t("traffic.rule")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={ALL}>{t("traffic.allRules")}</SelectItem>
                </SelectGroup>
                {!!rules.data?.length && <SelectSeparator />}
                <SelectGroup>
                  {rules.data?.map((r) => (
                    <SelectItem key={r.id} value={r.id}>
                      {r.name || r.id}
                    </SelectItem>
                  ))}
                  {rule && rules.data && !ruleNames.has(rule) && (
                    <SelectItem value={rule}>{rule}</SelectItem>
                  )}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Tabs value={range} onValueChange={(v) => update("range", v)}>
              <TabsList aria-label={t("traffic.range")}>
                {RANGES.map((r) => (
                  <TabsTrigger key={r} value={r}>
                    {t(`common.rangesShort.${r}`)}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          </>
        }
      />

      {history.isError && !history.data && (
        <QueryError error={history.error} onRetry={() => void history.refetch()} />
      )}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <TotalCard
          title={t("traffic.totals.upload")}
          icon={ArrowUpIcon}
          loading={loading}
          value={fmt.bytes(totals.up)}
        />
        <TotalCard
          title={t("traffic.totals.download")}
          icon={ArrowDownIcon}
          loading={loading}
          value={fmt.bytes(totals.down)}
        />
        <TotalCard
          title={t("traffic.totals.total")}
          icon={SigmaIcon}
          loading={loading}
          value={fmt.bytes(totals.up + totals.down)}
          footer={t(`common.ranges.${range}`)}
        />
        <TotalCard
          title={t("traffic.totals.connections")}
          icon={ActivityIcon}
          loading={loading}
          value={fmt.number(totals.conns)}
          footer={t("traffic.totals.peakActive", { count: fmt.number(totals.peak) })}
        />
      </div>

      <Card className={cn("min-w-0", history.isFetching && !loading && "opacity-80")}>
        <CardHeader>
          <CardTitle>{t("traffic.trafficChart")}</CardTitle>
          <CardDescription>
            {t("traffic.trafficChartDescription", { bucket })}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {loading ? (
            <Skeleton className="h-72" />
          ) : (
            <div className="relative">
              <TrafficHistoryChart points={points} step={step} range={range} />
              {!hasTraffic && (
                <p className="pointer-events-none absolute inset-0 flex items-center justify-center text-sm text-muted-foreground">
                  {t("traffic.noData")}
                </p>
              )}
            </div>
          )}
        </CardContent>
      </Card>

      <div className="grid gap-4 lg:grid-cols-5">
        <Card className="min-w-0 lg:col-span-2">
          <CardHeader>
            <CardTitle>{t("traffic.connectionsChart")}</CardTitle>
            <CardDescription>
              {t("traffic.connectionsChartDescription", { bucket })}
            </CardDescription>
          </CardHeader>
          <CardContent>
            {loading ? (
              <Skeleton className="h-56" />
            ) : (
              <ConnectionsHistoryChart points={points} step={step} range={range} />
            )}
          </CardContent>
        </Card>

        <Card className="min-w-0 lg:col-span-3">
          <CardHeader>
            <CardTitle>{t("traffic.breakdown.title")}</CardTitle>
            <CardDescription>{t("traffic.breakdown.description")}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-1 flex-col">
            {top.isError && !top.data ? (
              <QueryError error={top.error} onRetry={() => void top.refetch()} />
            ) : top.isPending ? (
              <div className="flex flex-col gap-2">
                {Array.from({ length: 4 }, (_, i) => (
                  <Skeleton key={i} className="h-8" />
                ))}
              </div>
            ) : topItems.length === 0 ? (
              <Empty className="border">
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <ChartColumnIcon />
                  </EmptyMedia>
                  <EmptyTitle>{t("traffic.breakdown.empty")}</EmptyTitle>
                  <EmptyDescription>{t("traffic.breakdown.emptyHint")}</EmptyDescription>
                </EmptyHeader>
              </Empty>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead>{t("traffic.breakdown.rule")}</TableHead>
                    <TableHead className="hidden text-right sm:table-cell">
                      {t("traffic.breakdown.upload")}
                    </TableHead>
                    <TableHead className="hidden text-right sm:table-cell">
                      {t("traffic.breakdown.download")}
                    </TableHead>
                    <TableHead className="text-right">
                      {t("traffic.breakdown.total")}
                    </TableHead>
                    <TableHead className="w-32">{t("traffic.breakdown.share")}</TableHead>
                    <TableHead className="hidden text-right md:table-cell">
                      {t("traffic.breakdown.connections")}
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {topItems.map((item) => {
                    const total = item.up + item.down
                    const share = topTotal > 0 ? total / topTotal : 0
                    const exists = !rules.data || ruleNames.has(item.ruleId)
                    return (
                      <TableRow
                        key={item.ruleId}
                        data-state={item.ruleId === rule ? "selected" : undefined}
                      >
                        <TableCell className="max-w-44 truncate">
                          {exists ? (
                            <Link
                              to={`/rules/${encodeURIComponent(item.ruleId)}`}
                              className="font-medium underline-offset-4 hover:underline"
                            >
                              {ruleNames.get(item.ruleId) ?? item.name ?? item.ruleId}
                            </Link>
                          ) : (
                            <span className="flex items-center gap-2 text-muted-foreground">
                              {item.name || item.ruleId}
                              <Badge variant="outline">{t("traffic.breakdown.deleted")}</Badge>
                            </span>
                          )}
                        </TableCell>
                        <TableCell className="hidden text-right tabular-nums sm:table-cell">
                          {fmt.bytes(item.up)}
                        </TableCell>
                        <TableCell className="hidden text-right tabular-nums sm:table-cell">
                          {fmt.bytes(item.down)}
                        </TableCell>
                        <TableCell className="text-right font-medium tabular-nums">
                          {fmt.bytes(total)}
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-2">
                            <Progress
                              value={share * 100}
                              className="h-1.5"
                              aria-label={t("traffic.breakdown.share")}
                            />
                            <span className="w-12 shrink-0 text-right text-xs text-muted-foreground tabular-nums">
                              {fmt.percent(share)}
                            </span>
                          </div>
                        </TableCell>
                        <TableCell className="hidden text-right tabular-nums md:table-cell">
                          {fmt.number(item.conns)}
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      </div>
    </PageContainer>
  )
}
