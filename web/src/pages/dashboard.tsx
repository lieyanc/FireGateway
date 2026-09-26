import * as React from "react"
import { Link } from "react-router"
import {
  ActivityIcon,
  ArrowDownIcon,
  ArrowLeftRightIcon,
  ArrowUpIcon,
  ChevronRightIcon,
  GaugeIcon,
  HardDriveIcon,
  ShieldAlertIcon,
  type LucideIcon,
} from "lucide-react"

import { LiveThroughputChart } from "@/components/charts/live-throughput-chart"
import { PageContainer, PageHeader } from "@/components/common/page-header"
import { QueryError } from "@/components/common/query-state"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { useNow } from "@/hooks/use-now"
import { useI18n } from "@/i18n"
import { useOverview, useRules } from "@/lib/queries"
import { useStats } from "@/lib/stats-store"
import type { Counters, Overview, StatsEvent } from "@/lib/types"

/** Live totals when streaming, otherwise the last overview snapshot. */
function useTotals<T>(
  overview: Overview | undefined,
  select: (totals: Counters) => T
): T | undefined {
  const live = useStats((event) => (event ? select(event.totals) : undefined))
  if (live !== undefined) return live
  return overview ? select(overview.totals) : undefined
}

function StatCard({
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
  loading?: boolean
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
          <CardTitle className="text-2xl font-semibold">{value}</CardTitle>
        )}
      </CardHeader>
      {footer && (
        <CardContent className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
          {footer}
        </CardContent>
      )}
    </Card>
  )
}

type StateCounts = {
  total: number
  running: number
  partial: number
  error: number
}

function countStates(event: StatsEvent | null): StateCounts | undefined {
  if (!event) return undefined
  const counts = { total: 0, running: 0, partial: 0, error: 0 }
  for (const r of Object.values(event.rules)) {
    counts.total++
    if (r.state === "running") counts.running++
    else if (r.state === "partial") counts.partial++
    else if (r.state === "error") counts.error++
  }
  return counts
}

function RulesCard({ overview }: { overview: Overview | undefined }) {
  const { t } = useI18n()
  const live = useStats(countStates)
  const counts: StateCounts | undefined =
    live && live.total > 0 ? live : overview?.rules
  const healthy = counts && counts.partial === 0 && counts.error === 0

  return (
    <StatCard
      title={t("dashboard.cards.rules")}
      icon={ArrowLeftRightIcon}
      loading={!counts}
      value={
        counts && (
          <span className="tabular-nums">
            {counts.running + counts.partial}
            <span className="ml-1.5 text-sm font-normal text-muted-foreground">
              {t("dashboard.cards.rulesOf", { total: counts.total })}
            </span>
          </span>
        )
      }
      footer={
        counts &&
        (healthy ? (
          t("dashboard.cards.allHealthy")
        ) : (
          <>
            {counts.partial > 0 && (
              <Badge variant="warning">
                {t("dashboard.cards.partial", { count: counts.partial })}
              </Badge>
            )}
            {counts.error > 0 && (
              <Badge variant="destructive">
                {t("dashboard.cards.error", { count: counts.error })}
              </Badge>
            )}
          </>
        ))
      }
    />
  )
}

function ConnectionsCard({ overview }: { overview: Overview | undefined }) {
  const { t, fmt } = useI18n()
  const v = useTotals(overview, (c) => ({
    active: c.activeConnections,
    total: c.totalConnections,
  }))
  return (
    <StatCard
      title={t("dashboard.cards.activeConnections")}
      icon={ActivityIcon}
      loading={!v}
      value={v && <span className="tabular-nums">{fmt.number(v.active)}</span>}
      footer={v && t("dashboard.cards.totalConnections", { count: fmt.number(v.total) })}
    />
  )
}

function UpDown({ up, down }: { up: string; down: string }) {
  const { t } = useI18n()
  return (
    <span className="flex flex-col gap-0.5 text-lg tabular-nums">
      <span className="flex items-center gap-1.5">
        <ArrowUpIcon className="size-4 text-chart-1" aria-label={t("common.metrics.up")} />
        {up}
      </span>
      <span className="flex items-center gap-1.5">
        <ArrowDownIcon className="size-4 text-chart-2" aria-label={t("common.metrics.down")} />
        {down}
      </span>
    </span>
  )
}

function RateCard({ overview }: { overview: Overview | undefined }) {
  const { t, fmt } = useI18n()
  const v = useTotals(overview, (c) => ({ up: c.rateUp, down: c.rateDown }))
  return (
    <StatCard
      title={t("dashboard.cards.currentRate")}
      icon={GaugeIcon}
      loading={!v}
      value={v && <UpDown up={fmt.rate(v.up)} down={fmt.rate(v.down)} />}
    />
  )
}

function TrafficCard({ overview }: { overview: Overview | undefined }) {
  const { t, fmt } = useI18n()
  const v = useTotals(overview, (c) => ({ up: c.bytesUp, down: c.bytesDown }))
  return (
    <StatCard
      title={t("dashboard.cards.totalTraffic")}
      icon={HardDriveIcon}
      loading={!v}
      value={v && <UpDown up={fmt.bytes(v.up)} down={fmt.bytes(v.down)} />}
    />
  )
}

function ProblemsCard({ overview }: { overview: Overview | undefined }) {
  const { t, fmt } = useI18n()
  const v = useTotals(overview, (c) => ({
    errors: c.errors,
    rejected: c.rejected,
  }))
  return (
    <StatCard
      title={t("dashboard.cards.problems")}
      icon={ShieldAlertIcon}
      loading={!v}
      value={
        v && (
          <span className="tabular-nums">
            {fmt.number(v.errors)}
            <span className="mx-1.5 text-muted-foreground">/</span>
            {fmt.number(v.rejected)}
          </span>
        )
      }
      footer={t("dashboard.cards.problemsHint")}
    />
  )
}

type TopRow = { id: string; up: number; down: number; active: number }

function selectTop(event: StatsEvent | null): TopRow[] {
  if (!event) return []
  return Object.entries(event.rules)
    .map(([id, c]) => ({
      id,
      up: c.rateUp,
      down: c.rateDown,
      active: c.activeConnections,
    }))
    .filter((r) => r.up + r.down > 0 || r.active > 0)
    .sort((a, b) => b.up + b.down - (a.up + a.down) || b.active - a.active)
    .slice(0, 6)
}

function sameRows(a: TopRow[], b: TopRow[]) {
  return (
    a.length === b.length &&
    a.every(
      (r, i) =>
        r.id === b[i].id &&
        r.up === b[i].up &&
        r.down === b[i].down &&
        r.active === b[i].active
    )
  )
}

function TopRulesCard() {
  const { t, fmt } = useI18n()
  const rows = useStats(selectTop, sameRows)
  const rules = useRules()
  const names = React.useMemo(
    () => new Map(rules.data?.map((r) => [r.id, r.name || r.id])),
    [rules.data]
  )

  return (
    <Card className="lg:col-span-1">
      <CardHeader>
        <CardTitle>{t("dashboard.topRules.title")}</CardTitle>
        <CardDescription>{t("dashboard.topRules.description")}</CardDescription>
        <CardAction>
          <Button variant="ghost" size="sm" asChild>
            <Link to="/rules">
              {t("dashboard.topRules.viewAll")}
              <ChevronRightIcon data-icon="inline-end" />
            </Link>
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-1 flex-col">
        {rows.length === 0 ? (
          <Empty className="border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <GaugeIcon />
              </EmptyMedia>
              <EmptyTitle>{t("dashboard.topRules.empty")}</EmptyTitle>
            </EmptyHeader>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="w-full">{t("dashboard.topRules.rule")}</TableHead>
                <TableHead className="text-right">
                  {t("dashboard.topRules.rate")}
                </TableHead>
                <TableHead className="text-right">
                  {t("dashboard.topRules.connections")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => (
                <TableRow key={r.id}>
                  <TableCell className="w-full max-w-0 truncate">
                    <Link
                      to={`/rules/${encodeURIComponent(r.id)}`}
                      className="font-medium underline-offset-4 hover:underline"
                    >
                      {names.get(r.id) ?? r.id}
                    </Link>
                  </TableCell>
                  <TableCell className="text-right text-xs tabular-nums">
                    <div>↑ {fmt.rate(r.up)}</div>
                    <div className="text-muted-foreground">↓ {fmt.rate(r.down)}</div>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {fmt.number(r.active)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}

function SystemCard({
  overview,
  updatedAt,
}: {
  overview: Overview
  updatedAt: number
}) {
  const { t, fmt } = useI18n()
  const now = useNow(1000)
  const uptime = overview.uptime + Math.max(0, (now - updatedAt) / 1000)

  const items: [string, React.ReactNode][] = [
    [t("dashboard.system.version"), <span className="font-mono">{overview.version}</span>],
    [
      t("dashboard.system.commit"),
      <span className="font-mono" title={overview.commit}>
        {overview.commit ? overview.commit.slice(0, 12) : "–"}
      </span>,
    ],
    [
      t("dashboard.system.uptime"),
      <span title={fmt.dateTime(overview.startedAt)}>{fmt.duration(uptime, 3)}</span>,
    ],
    [
      t("dashboard.system.memory"),
      t("dashboard.system.memoryValue", {
        heap: fmt.bytes(overview.memory.heapAlloc),
        sys: fmt.bytes(overview.memory.sys),
      }),
    ],
    [t("dashboard.system.goroutines"), fmt.number(overview.goroutines)],
    [
      t("dashboard.system.platform"),
      `${overview.os}/${overview.arch} · ${t("dashboard.system.cpus", { count: overview.cpus })}`,
    ],
    [t("dashboard.system.goVersion"), <span className="font-mono">{overview.goVersion}</span>],
    [
      t("dashboard.system.configPath"),
      <span className="font-mono break-all">{overview.configPath || "–"}</span>,
    ],
  ]

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("dashboard.system.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        <dl className="grid gap-x-6 gap-y-4 sm:grid-cols-2 lg:grid-cols-4">
          {items.map(([label, value]) => (
            <div key={label} className="flex min-w-0 flex-col gap-1">
              <dt className="text-xs text-muted-foreground">{label}</dt>
              <dd className="min-w-0 text-sm">{value}</dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  )
}

export default function DashboardPage() {
  const { t } = useI18n()
  const overview = useOverview()
  const data = overview.data

  return (
    <PageContainer>
      <PageHeader
        title={t("dashboard.title")}
        description={t("dashboard.description")}
      />

      {overview.isError && !data && (
        <QueryError error={overview.error} onRetry={() => void overview.refetch()} />
      )}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        <RulesCard overview={data} />
        <ConnectionsCard overview={data} />
        <RateCard overview={data} />
        <TrafficCard overview={data} />
        <ProblemsCard overview={data} />
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="min-w-0 lg:col-span-2">
          <CardHeader>
            <CardTitle>{t("dashboard.throughput")}</CardTitle>
            <CardDescription>{t("dashboard.throughputDescription")}</CardDescription>
          </CardHeader>
          <CardContent>
            <LiveThroughputChart />
          </CardContent>
        </Card>
        <TopRulesCard />
      </div>

      {data ? (
        <SystemCard overview={data} updatedAt={overview.dataUpdatedAt} />
      ) : (
        !overview.isError && <Skeleton className="h-40" />
      )}
    </PageContainer>
  )
}
