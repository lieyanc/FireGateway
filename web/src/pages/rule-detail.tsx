import * as React from "react"
import { Link, useNavigate, useParams } from "react-router"
import {
  ArrowLeftIcon,
  CircleAlertIcon,
  PencilIcon,
  RotateCwIcon,
  SearchXIcon,
  Trash2Icon,
  TriangleAlertIcon,
} from "lucide-react"

import { LiveThroughputChart } from "@/components/charts/live-throughput-chart"
import {
  ConnectionsHistoryChart,
  TrafficHistoryChart,
} from "@/components/charts/history-charts"
import { ConfirmDialog } from "@/components/common/confirm-dialog"
import { CopyButton } from "@/components/common/copy-button"
import { PageContainer } from "@/components/common/page-header"
import { QueryError } from "@/components/common/query-state"
import { RuleTypeBadge } from "@/components/common/rule-badges"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ConnectionsPanel } from "@/features/connections/connections-panel"
import { FailureList, RuleStateCell } from "@/features/rules/rule-cells"
import { RuleEditorProvider, useRuleEditor } from "@/features/rules/rule-editor"
import {
  useDeleteRule,
  useRestartRule,
  useSetRuleEnabled,
} from "@/features/rules/use-rule-mutations"
import { formatListen, formatTarget, portCount } from "@/features/rules/utils"
import { useI18n } from "@/i18n"
import { isApiError } from "@/lib/api"
import { useHistoryMetrics, useRule } from "@/lib/queries"
import { useRuleStats } from "@/lib/stats-store"
import type { MetricRange, RuleView } from "@/lib/types"

const RANGES: MetricRange[] = ["1h", "24h", "7d", "30d"]

export default function RuleDetailPage() {
  const { id = "" } = useParams()
  return (
    <RuleEditorProvider>
      <RuleDetail id={id} />
    </RuleEditorProvider>
  )
}

function RuleDetail({ id }: { id: string }) {
  const { t } = useI18n()
  const rule = useRule(id)

  if (rule.isPending) {
    return (
      <PageContainer>
        <Skeleton className="h-8 w-64" />
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-24" />
          ))}
        </div>
        <Skeleton className="h-72" />
      </PageContainer>
    )
  }

  if (rule.isError && !rule.data) {
    if (isApiError(rule.error) && rule.error.status === 404) {
      return (
        <PageContainer>
          <Empty className="py-24">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <SearchXIcon />
              </EmptyMedia>
              <EmptyTitle>{t("rules.detail.notFound")}</EmptyTitle>
              <EmptyDescription>{t("rules.detail.notFoundHint")}</EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button asChild variant="outline">
                <Link to="/rules">
                  <ArrowLeftIcon data-icon="inline-start" />
                  {t("rules.detail.backToRules")}
                </Link>
              </Button>
            </EmptyContent>
          </Empty>
        </PageContainer>
      )
    }
    return (
      <PageContainer>
        <QueryError error={rule.error} onRetry={() => void rule.refetch()} />
      </PageContainer>
    )
  }

  const view = rule.data
  return (
    <PageContainer>
      <DetailHeader rule={view} />
      <FailuresAlert rule={view} />
      <LiveCounters rule={view} />
      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="min-w-0 lg:col-span-2">
          <CardHeader>
            <CardTitle>{t("rules.detail.live.title")}</CardTitle>
            <CardDescription>{t("rules.detail.live.description")}</CardDescription>
          </CardHeader>
          <CardContent>
            <LiveThroughputChart ruleId={view.id} />
          </CardContent>
        </Card>
        <ConfigCard rule={view} />
      </div>
      <HistoryCard ruleId={view.id} />
      <Card className="min-w-0">
        <CardHeader>
          <CardTitle>{t("rules.detail.connections.title")}</CardTitle>
          <CardDescription>{t("rules.detail.connections.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <ConnectionsPanel
            ruleId={view.id}
            ruleName={view.name || view.id}
            maxHeightClassName="max-h-96"
          />
        </CardContent>
      </Card>
    </PageContainer>
  )
}

function DetailHeader({ rule }: { rule: RuleView }) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const editor = useRuleEditor()
  const toggle = useSetRuleEnabled()
  const restart = useRestartRule()
  const remove = useDeleteRule()
  const [confirming, setConfirming] = React.useState(false)
  const name = rule.name || rule.id
  const enabled = rule.status === "active"

  return (
    <div className="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
      <div className="flex min-w-0 flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <h1 className="truncate text-xl font-semibold tracking-tight">{name}</h1>
          <RuleTypeBadge type={rule.type} />
          <RuleStateCell rule={rule} />
        </div>
        <div className="flex min-w-0 items-center gap-1 text-sm text-muted-foreground">
          <span className="truncate font-mono">{rule.id}</span>
          <CopyButton value={rule.id} variant="ghost" size="icon-xs" />
          {rule.remark && (
            <>
              <Separator orientation="vertical" className="mx-1 data-[orientation=vertical]:h-4" />
              <span className="truncate">{rule.remark}</span>
            </>
          )}
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <div className="mr-2 flex items-center gap-2">
          <Switch
            id="rule-detail-enabled"
            checked={enabled}
            onCheckedChange={(next) => toggle.mutate({ rule, enabled: next })}
          />
          <Label htmlFor="rule-detail-enabled">{t("rules.columns.enabled")}</Label>
        </div>
        <Button variant="outline" onClick={() => editor.edit(rule)}>
          <PencilIcon data-icon="inline-start" />
          {t("common.actions.edit")}
        </Button>
        <Button
          variant="outline"
          disabled={!enabled || restart.isPending}
          title={t("rules.restartHint")}
          onClick={() => restart.mutate(rule)}
        >
          <RotateCwIcon data-icon="inline-start" />
          {t("common.actions.restart")}
        </Button>
        <Button variant="destructive" onClick={() => setConfirming(true)}>
          <Trash2Icon data-icon="inline-start" />
          {t("common.actions.delete")}
        </Button>
      </div>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={t("rules.deleteTitle", { name })}
        description={t("rules.deleteDescription")}
        confirmLabel={t("common.actions.delete")}
        onConfirm={async () => {
          await remove.mutateAsync(rule)
          navigate("/rules", { replace: true })
        }}
      />
    </div>
  )
}

function FailuresAlert({ rule }: { rule: RuleView }) {
  const { t } = useI18n()
  const stats = useRuleStats(rule.id)
  const state = stats?.state ?? rule.runtime.state
  if (!rule.runtime.failures?.length || (state !== "partial" && state !== "error")) {
    return null
  }
  const Icon = state === "error" ? CircleAlertIcon : TriangleAlertIcon
  return (
    <Alert variant="destructive">
      <Icon />
      <AlertTitle>
        {state === "error" ? t("rules.failures.error") : t("rules.failures.partial")}
      </AlertTitle>
      <AlertDescription className="flex flex-col gap-2">
        <span>{t("rules.failures.hint")}</span>
        <FailureList rule={rule} />
      </AlertDescription>
    </Alert>
  )
}

function StatCard({
  label,
  value,
  hint,
}: {
  label: string
  value: React.ReactNode
  hint?: React.ReactNode
}) {
  return (
    <Card size="sm" className="min-w-0">
      <CardHeader>
        <CardDescription>{label}</CardDescription>
        <CardTitle className="truncate text-xl tabular-nums">{value}</CardTitle>
      </CardHeader>
      {hint && (
        <CardContent className="truncate text-xs text-muted-foreground">{hint}</CardContent>
      )}
    </Card>
  )
}

/** Counter cards; the only part of the header area that re-renders per tick. */
function LiveCounters({ rule }: { rule: RuleView }) {
  const { t, fmt } = useI18n()
  const stats = useRuleStats(rule.id)
  const c = { ...rule.runtime, ...stats }
  const total = portCount(rule)
  const running = rule.status === "active" && rule.runtime.startedAt

  return (
    <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
      <StatCard
        label={t("rules.detail.counters.active")}
        value={fmt.number(c.activeConnections)}
        hint={`${t("rules.detail.counters.total")}: ${fmt.number(c.totalConnections)}`}
      />
      <StatCard
        label={t("rules.detail.counters.rate")}
        value={`↑ ${fmt.rate(c.rateUp)}`}
        hint={`↓ ${fmt.rate(c.rateDown)}`}
      />
      <StatCard label={t("rules.detail.counters.up")} value={fmt.bytes(c.bytesUp)} />
      <StatCard label={t("rules.detail.counters.down")} value={fmt.bytes(c.bytesDown)} />
      <StatCard
        label={t("rules.detail.counters.rejected")}
        value={fmt.number(c.rejected)}
        hint={`${t("rules.detail.counters.errors")}: ${fmt.number(c.errors)}`}
      />
      <StatCard
        label={t("rules.detail.counters.listeners")}
        value={t("rules.detail.counters.listenersValue", {
          up: rule.runtime.listeners,
          total,
        })}
        hint={
          running
            ? `${t("rules.detail.counters.startedAt")}: ${fmt.relative(rule.runtime.startedAt)}`
            : t("rules.detail.counters.notStarted")
        }
      />
      {rule.type === "udp" && c.messages !== undefined && (
        <StatCard label={t("rules.detail.counters.messages")} value={fmt.number(c.messages)} />
      )}
    </div>
  )
}

function ConfigRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-sm break-words">{children}</dd>
    </div>
  )
}

function ConfigCard({ rule }: { rule: RuleView }) {
  const { t, fmt } = useI18n()
  const count = portCount(rule)
  const limits = rule.limits ?? {}
  const unlimited = t("common.states.unlimited")

  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle>{t("rules.detail.config.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        <dl className="flex flex-col gap-3">
          <ConfigRow label={t("rules.detail.config.listen")}>
            <span className="font-mono">{formatListen(rule)}</span>
            {count > 1 && (
              <Badge variant="secondary" className="ml-2">
                {t("rules.detail.config.ports", { count })}
              </Badge>
            )}
          </ConfigRow>
          <ConfigRow label={t("rules.detail.config.target")}>
            <span className="font-mono">{formatTarget(rule)}</span>
          </ConfigRow>
          <Separator />
          <ConfigRow label={t("rules.detail.config.acl")}>
            {!rule.acl ? (
              t("rules.detail.config.aclNone")
            ) : (
              <div className="flex flex-col gap-1.5">
                <span>
                  {rule.acl.mode === "allow"
                    ? t("rules.detail.config.aclAllow")
                    : t("rules.detail.config.aclDeny")}
                </span>
                <div className="flex flex-wrap gap-1">
                  {rule.acl.cidrs.map((cidr) => (
                    <Badge key={cidr} variant="outline" className="font-mono">
                      {cidr}
                    </Badge>
                  ))}
                </div>
              </div>
            )}
          </ConfigRow>
          <Separator />
          <ConfigRow label={t("rules.detail.config.maxConnections")}>
            {limits.maxConnections ? fmt.number(limits.maxConnections) : unlimited}
          </ConfigRow>
          <ConfigRow label={t("rules.detail.config.maxPerIp")}>
            {limits.maxConnectionsPerIp ? fmt.number(limits.maxConnectionsPerIp) : unlimited}
          </ConfigRow>
          <ConfigRow label={t("rules.detail.config.bandwidth")}>
            {limits.bandwidth
              ? t("rules.detail.config.perDirection", { rate: fmt.rate(limits.bandwidth) })
              : unlimited}
          </ConfigRow>
          {rule.remark && (
            <>
              <Separator />
              <ConfigRow label={t("rules.detail.config.remark")}>{rule.remark}</ConfigRow>
            </>
          )}
        </dl>
      </CardContent>
    </Card>
  )
}

function HistoryCard({ ruleId }: { ruleId: string }) {
  const { t, fmt } = useI18n()
  const [range, setRange] = React.useState<MetricRange>("1h")
  const history = useHistoryMetrics(range, ruleId)

  const totals = React.useMemo(() => {
    const points = history.data?.points ?? []
    return points.reduce(
      (acc, p) => ({ up: acc.up + p.up, down: acc.down + p.down, conns: acc.conns + p.conns }),
      { up: 0, down: 0, conns: 0 }
    )
  }, [history.data])

  return (
    <Card className="min-w-0">
      <CardHeader className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex flex-col gap-1">
          <CardTitle>{t("rules.detail.history.title")}</CardTitle>
          <CardDescription>{t("rules.detail.history.description")}</CardDescription>
        </div>
        <Tabs value={range} onValueChange={(v) => setRange(v as MetricRange)}>
          <TabsList>
            {RANGES.map((r) => (
              <TabsTrigger key={r} value={r}>
                {t(`common.rangesShort.${r}`)}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        {history.isError && !history.data ? (
          <QueryError error={history.error} onRetry={() => void history.refetch()} />
        ) : !history.data ? (
          <Skeleton className="h-72" />
        ) : (
          <>
            <div className="flex flex-wrap gap-x-6 gap-y-1 text-sm">
              <span className="text-muted-foreground">{t("rules.detail.history.totals")}</span>
              <span className="tabular-nums">
                ↑ {fmt.bytes(totals.up)}
                <span className="px-1 text-muted-foreground">/</span>↓ {fmt.bytes(totals.down)}
              </span>
              <span className="tabular-nums">
                {t("common.metrics.newConnections")}: {fmt.number(totals.conns)}
              </span>
            </div>
            <div className="flex flex-col gap-2">
              <h3 className="text-sm font-medium">{t("rules.detail.history.traffic")}</h3>
              <TrafficHistoryChart
                points={history.data.points}
                step={history.data.step}
                range={range}
              />
            </div>
            <div className="flex flex-col gap-2">
              <h3 className="text-sm font-medium">{t("rules.detail.history.connections")}</h3>
              <ConnectionsHistoryChart
                points={history.data.points}
                step={history.data.step}
                range={range}
              />
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}
