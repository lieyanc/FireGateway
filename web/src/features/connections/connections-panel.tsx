import * as React from "react"
import { Link, useSearchParams } from "react-router"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useVirtualizer } from "@tanstack/react-virtual"
import {
  ActivityIcon,
  PauseIcon,
  PlayIcon,
  SearchIcon,
  UnplugIcon,
  XIcon,
} from "lucide-react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/common/confirm-dialog"
import { QueryError } from "@/components/common/query-state"
import { RuleTypeBadge } from "@/components/common/rule-badges"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
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
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { useNow } from "@/hooks/use-now"
import { useI18n } from "@/i18n"
import { api, isApiError } from "@/lib/api"
import { toastError } from "@/lib/errors"
import { qk, useConnections, useRules } from "@/lib/queries"
import type { Connection } from "@/lib/types"
import { cn } from "@/lib/utils"

const REFRESH_MS = 2000
const ROW_HEIGHT = 45
const ALL = "__all__"

type Props = {
  /** Fixes the panel to one rule (rule detail page); hides the rule filter. */
  ruleId?: string
  ruleName?: string
  /** Tailwind max-height for the scroll area. */
  maxHeightClassName?: string
}

/**
 * Live connection list with auto refresh, filters, and close actions.
 * Rows are virtualized so thousands of sessions stay smooth.
 */
export function ConnectionsPanel({
  ruleId,
  ruleName,
  maxHeightClassName = "max-h-[calc(100svh-15rem)]",
}: Props) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [params, setParams] = useSearchParams()
  const fixed = ruleId !== undefined
  const ruleFilter = fixed ? ruleId : params.get("rule") || undefined

  const [paused, setPaused] = React.useState(false)
  const [search, setSearch] = React.useState("")
  const [closing, setClosing] = React.useState<Connection | null>(null)
  const [closingAll, setClosingAll] = React.useState(false)

  const rules = useRules()
  const query = useConnections(ruleFilter, {
    refetchInterval: paused ? false : REFRESH_MS,
  })

  const items = query.data ?? []
  const needle = search.trim().toLowerCase()
  const filtered = needle
    ? items.filter((c) => c.client.toLowerCase().includes(needle))
    : items

  const filterRuleName =
    ruleName ??
    rules.data?.find((r) => r.id === ruleFilter)?.name ??
    ruleFilter ??
    ""

  const setRuleFilter = (value: string) => {
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (value === ALL) next.delete("rule")
        else next.set("rule", value)
        return next
      },
      { replace: true }
    )
  }

  const removeFromCache = (id: string) => {
    queryClient.setQueriesData<Connection[]>(
      { queryKey: qk.connectionsAll },
      (old) => old?.filter((c) => c.id !== id)
    )
  }

  const closeOne = useMutation({
    mutationFn: (conn: Connection) => api.connections.close(conn.id),
    onSuccess: (_data, conn) => {
      removeFromCache(conn.id)
      toast.success(t("connections.closed"))
    },
    onError: (error, conn) => {
      if (isApiError(error) && error.status === 404) {
        removeFromCache(conn.id)
        toast.info(t("connections.closeGone"))
        return
      }
      toastError(error, t)
    },
  })

  const closeAll = useMutation({
    mutationFn: (rule: string) => api.connections.closeForRule(rule),
    onSuccess: (res) => {
      toast.success(t("connections.closedAll", { count: res.closed }))
      void queryClient.invalidateQueries({ queryKey: qk.connectionsAll })
    },
    onError: (error) => toastError(error, t),
  })

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <InputGroup className="w-full sm:w-64">
          <InputGroupAddon>
            <SearchIcon />
          </InputGroupAddon>
          <InputGroupInput
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("connections.searchPlaceholder")}
            aria-label={t("connections.searchPlaceholder")}
          />
        </InputGroup>
        {!fixed && (
          <Select value={ruleFilter ?? ALL} onValueChange={setRuleFilter}>
            <SelectTrigger
              className="w-full sm:w-52"
              aria-label={t("connections.ruleFilter")}
            >
              <SelectValue placeholder={t("connections.allRules")} />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value={ALL}>{t("connections.allRules")}</SelectItem>
              </SelectGroup>
              {!!rules.data?.length && <SelectSeparator />}
              <SelectGroup>
                {rules.data?.map((r) => (
                  <SelectItem key={r.id} value={r.id}>
                    {r.name || r.id}
                  </SelectItem>
                ))}
                {ruleFilter &&
                  rules.data &&
                  !rules.data.some((r) => r.id === ruleFilter) && (
                    <SelectItem value={ruleFilter}>{ruleFilter}</SelectItem>
                  )}
              </SelectGroup>
            </SelectContent>
          </Select>
        )}
        <div className="flex items-center gap-2 sm:ml-auto">
          <span className="text-sm text-muted-foreground tabular-nums">
            {needle
              ? t("connections.countFiltered", {
                  shown: filtered.length,
                  count: items.length,
                })
              : t("connections.count", { count: items.length })}
          </span>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="outline"
                size="sm"
                aria-pressed={!paused}
                onClick={() => setPaused((p) => !p)}
              >
                {paused ? (
                  <PlayIcon data-icon="inline-start" />
                ) : (
                  <PauseIcon data-icon="inline-start" />
                )}
                {paused ? t("common.actions.resume") : t("common.actions.pause")}
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              {paused ? t("connections.refreshPaused") : t("connections.refreshing")}
            </TooltipContent>
          </Tooltip>
          {ruleFilter && (
            <Button
              variant="destructive"
              size="sm"
              disabled={items.length === 0}
              onClick={() => setClosingAll(true)}
            >
              <UnplugIcon data-icon="inline-start" />
              {t("connections.closeAll")}
            </Button>
          )}
        </div>
      </div>

      {query.isError && !query.data ? (
        <QueryError error={query.error} onRetry={() => void query.refetch()} />
      ) : (
        <Card className="gap-0 overflow-hidden py-0">
          {query.isPending ? (
            <LoadingRows />
          ) : filtered.length === 0 ? (
            <Empty className="py-12">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <ActivityIcon />
                </EmptyMedia>
                <EmptyTitle>
                  {needle
                    ? t("common.states.noResults")
                    : fixed
                      ? t("connections.emptyForRule")
                      : t("connections.empty")}
                </EmptyTitle>
                <EmptyDescription>
                  {needle
                    ? t("common.states.noResultsHint")
                    : t("connections.emptyHint")}
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <VirtualConnectionsTable
              rows={filtered}
              showRule={!fixed}
              maxHeightClassName={maxHeightClassName}
              onClose={setClosing}
            />
          )}
        </Card>
      )}

      <ConfirmDialog
        open={closing !== null}
        onOpenChange={(open) => !open && setClosing(null)}
        title={t("connections.closeConfirmTitle", { client: closing?.client ?? "" })}
        description={t("connections.closeConfirmDescription")}
        confirmLabel={t("connections.close")}
        onConfirm={() => closing && closeOne.mutateAsync(closing)}
      />
      <ConfirmDialog
        open={closingAll}
        onOpenChange={setClosingAll}
        title={t("connections.closeAllConfirmTitle", { rule: filterRuleName })}
        description={t("connections.closeAllConfirmDescription")}
        confirmLabel={t("connections.closeAll")}
        onConfirm={() => ruleFilter && closeAll.mutateAsync(ruleFilter)}
      />
    </div>
  )
}

function LoadingRows() {
  return (
    <div className="flex flex-col gap-2 p-4">
      {Array.from({ length: 5 }, (_, i) => (
        <Skeleton key={i} className="h-8" />
      ))}
    </div>
  )
}

function VirtualConnectionsTable({
  rows,
  showRule,
  maxHeightClassName,
  onClose,
}: {
  rows: Connection[]
  showRule: boolean
  maxHeightClassName: string
  onClose: (conn: Connection) => void
}) {
  const { t, fmt } = useI18n()
  const now = useNow(1000)
  const scrollRef = React.useRef<HTMLDivElement>(null)

  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
    getItemKey: (index) => rows[index].id,
  })

  const virtualRows = virtualizer.getVirtualItems()
  const totalSize = virtualizer.getTotalSize()
  const paddingTop = virtualRows.length ? virtualRows[0].start : 0
  const paddingBottom = virtualRows.length
    ? totalSize - virtualRows[virtualRows.length - 1].end
    : 0
  const columnCount = showRule ? 8 : 7

  return (
    <Table
      containerRef={scrollRef}
      containerClassName={cn("@container overflow-auto", maxHeightClassName)}
    >
      <TableHeader className="sticky top-0 z-10 bg-card shadow-[inset_0_-1px_0_var(--border)]">
        <TableRow className="hover:bg-transparent">
          <TableHead className="pl-4">{t("connections.columns.client")}</TableHead>
          {showRule && <TableHead>{t("connections.columns.rule")}</TableHead>}
          <TableHead className="hidden @3xl:table-cell">{t("connections.columns.type")}</TableHead>
          <TableHead className="hidden @2xl:table-cell">{t("connections.columns.route")}</TableHead>
          <TableHead className="text-right">
            {t("connections.columns.duration")}
          </TableHead>
          <TableHead className="text-right">
            {t("connections.columns.bytes")}
          </TableHead>
          <TableHead className="hidden text-right @xl:table-cell">
            {t("connections.columns.lastActive")}
          </TableHead>
          <TableHead className="w-12 pr-4">
            <span className="sr-only">{t("connections.columns.actions")}</span>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {paddingTop > 0 && (
          <tr aria-hidden style={{ height: paddingTop }}>
            <td colSpan={columnCount} />
          </tr>
        )}
        {virtualRows.map((item) => {
          const c = rows[item.index]
          const started = new Date(c.startedAt).getTime()
          return (
            <TableRow key={item.key} style={{ height: ROW_HEIGHT }}>
              <TableCell className="pl-4 font-mono text-xs">{c.client}</TableCell>
              {showRule && (
                <TableCell className="max-w-32 truncate @2xl:max-w-48">
                  <Link
                    to={`/rules/${encodeURIComponent(c.ruleId)}`}
                    className="font-medium underline-offset-4 hover:underline"
                  >
                    {c.ruleName || c.ruleId}
                  </Link>
                </TableCell>
              )}
              <TableCell className="hidden @3xl:table-cell">
                <RuleTypeBadge type={c.type} />
              </TableCell>
              <TableCell className="hidden font-mono text-xs text-muted-foreground @2xl:table-cell">
                {c.listen} → {c.target}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {fmt.duration((now - started) / 1000)}
              </TableCell>
              <TableCell className="text-right text-xs leading-tight tabular-nums">
                <div>↑ {fmt.bytes(c.bytesUp)}</div>
                <div className="text-muted-foreground">↓ {fmt.bytes(c.bytesDown)}</div>
              </TableCell>
              <TableCell
                className="hidden text-right text-muted-foreground @xl:table-cell"
                title={fmt.dateTime(c.lastActive)}
              >
                {fmt.relative(c.lastActive, now)}
              </TableCell>
              <TableCell className="pr-4 text-right">
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t("connections.close")}
                  title={t("connections.close")}
                  onClick={() => onClose(c)}
                >
                  <XIcon />
                </Button>
              </TableCell>
            </TableRow>
          )
        })}
        {paddingBottom > 0 && (
          <tr aria-hidden style={{ height: paddingBottom }}>
            <td colSpan={columnCount} />
          </tr>
        )}
      </TableBody>
    </Table>
  )
}
