import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Link } from "react-router"
import { GlobeIcon, RefreshCwIcon } from "lucide-react"
import { toast } from "sonner"

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
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
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
import { api } from "@/lib/api"
import { toastError } from "@/lib/errors"
import { qk, useDnsCache } from "@/lib/queries"
import type { DnsCache, DnsEntry } from "@/lib/types"

/** Go encodes an unset time.Time as year 1. */
const isSet = (value: string) => new Date(value).getTime() > 0

export function DnsTab() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const dns = useDnsCache()

  const refresh = useMutation({
    mutationFn: (host?: string) => api.dns.refresh(host),
    onSuccess: ({ items }) => {
      queryClient.setQueryData<DnsCache>(qk.dns, (old) =>
        old
          ? {
              ...old,
              items: old.items.map(
                (e) => items.find((u) => u.host === e.host) ?? e
              ),
            }
          : old
      )
      const failed = items.find((e) => e.error)
      if (failed) {
        toast.error(
          t("settings.dns.refreshFailed", {
            host: failed.host,
            error: failed.error ?? "",
          })
        )
      } else {
        toast.success(t("settings.dns.refreshed"))
      }
    },
    onError: (error) => toastError(error, t),
    onSettled: () => queryClient.invalidateQueries({ queryKey: qk.dns }),
  })

  const items = dns.data?.items ?? []
  const refreshingAll = refresh.isPending && refresh.variables === undefined

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("settings.dns.title")}</CardTitle>
        <CardDescription>
          {t("settings.dns.description", {
            ttl: dns.data?.ttl ?? 60,
            retry: dns.data?.retryInterval ?? 5,
          })}
        </CardDescription>
        <CardAction>
          <Button
            size="sm"
            variant="outline"
            disabled={items.length === 0 || refresh.isPending}
            onClick={() => refresh.mutate(undefined)}
          >
            {refreshingAll ? (
              <Spinner data-icon="inline-start" />
            ) : (
              <RefreshCwIcon data-icon="inline-start" />
            )}
            {t("settings.dns.refreshAll")}
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        {dns.isPending ? (
          <div className="flex flex-col gap-2">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} className="h-9" />
            ))}
          </div>
        ) : dns.isError ? (
          <QueryError error={dns.error} onRetry={() => void dns.refetch()} />
        ) : items.length === 0 ? (
          <Empty className="border py-10">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <GlobeIcon />
              </EmptyMedia>
              <EmptyTitle>{t("settings.dns.empty")}</EmptyTitle>
              <EmptyDescription>{t("settings.dns.emptyHint")}</EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>{t("settings.dns.columns.host")}</TableHead>
                <TableHead>{t("settings.dns.columns.addrs")}</TableHead>
                <TableHead>{t("settings.dns.columns.status")}</TableHead>
                <TableHead>{t("settings.dns.columns.resolved")}</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">{t("settings.dns.columns.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((entry) => (
                <DnsRow
                  key={entry.host}
                  entry={entry}
                  refreshing={refreshingAll || (refresh.isPending && refresh.variables === entry.host)}
                  disabled={refresh.isPending}
                  onRefresh={() => refresh.mutate(entry.host)}
                />
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}

function DnsRow({
  entry,
  refreshing,
  disabled,
  onRefresh,
}: {
  entry: DnsEntry
  refreshing: boolean
  disabled: boolean
  onRefresh: () => void
}) {
  const { t, fmt } = useI18n()
  const now = useNow(1000)

  return (
    <TableRow>
      <TableCell className="max-w-56 align-top">
        <div className="truncate font-mono text-xs font-medium">{entry.host}</div>
        {entry.rules.length > 0 && (
          <div className="mt-1 flex flex-wrap gap-x-2 text-xs text-muted-foreground">
            {entry.rules.map((r) => (
              <Link
                key={r.id}
                to={`/rules/${encodeURIComponent(r.id)}`}
                className="truncate underline-offset-4 hover:underline"
              >
                {r.name || r.id}
              </Link>
            ))}
          </div>
        )}
      </TableCell>
      <TableCell className="align-top font-mono text-xs">
        {entry.addrs.length > 0
          ? entry.addrs.map((a) => <div key={a}>{a}</div>)
          : <span className="text-muted-foreground">—</span>}
      </TableCell>
      <TableCell className="align-top">
        <DnsStatus entry={entry} />
      </TableCell>
      <TableCell className="align-top">
        {isSet(entry.resolvedAt) ? (
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="text-muted-foreground">
                {fmt.relative(entry.resolvedAt, now)}
              </span>
            </TooltipTrigger>
            <TooltipContent>
              <div>{fmt.dateTime(entry.resolvedAt)}</div>
              {isSet(entry.nextAt) && (
                <div>
                  {t("settings.dns.nextRefresh", {
                    time: fmt.relative(entry.nextAt, now),
                  })}
                </div>
              )}
            </TooltipContent>
          </Tooltip>
        ) : (
          <span className="text-muted-foreground">{t("settings.dns.never")}</span>
        )}
      </TableCell>
      <TableCell className="text-right align-top">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon-sm"
              disabled={disabled}
              onClick={onRefresh}
              aria-label={t("settings.dns.refresh")}
            >
              {refreshing ? <Spinner /> : <RefreshCwIcon />}
            </Button>
          </TooltipTrigger>
          <TooltipContent>{t("settings.dns.refresh")}</TooltipContent>
        </Tooltip>
      </TableCell>
    </TableRow>
  )
}

function DnsStatus({ entry }: { entry: DnsEntry }) {
  const { t } = useI18n()
  if (entry.pending) {
    return <Badge variant="secondary">{t("settings.dns.status.pending")}</Badge>
  }
  if (!entry.error) {
    return <Badge variant="success">{t("settings.dns.status.ok")}</Badge>
  }
  const stale = entry.addrs.length > 0
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge variant={stale ? "warning" : "destructive"}>
          {stale ? t("settings.dns.status.stale") : t("settings.dns.status.failed")}
        </Badge>
      </TooltipTrigger>
      <TooltipContent className="max-w-80 break-words">
        {stale ? t("settings.dns.staleHint", { error: entry.error }) : entry.error}
      </TooltipContent>
    </Tooltip>
  )
}
