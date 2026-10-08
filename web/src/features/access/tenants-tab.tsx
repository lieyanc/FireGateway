import * as React from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import {
  BuildingIcon,
  CirclePauseIcon,
  MoreHorizontalIcon,
  PencilIcon,
  PlusIcon,
  RotateCcwIcon,
  Trash2Icon,
} from "lucide-react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/common/confirm-dialog"
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Progress } from "@/components/ui/progress"
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
import {
  TenantFormDialog,
  type TenantEditorTarget,
} from "@/features/access/tenant-form-dialog"
import { formatPortRanges, usageRatio } from "@/features/access/utils"
import { useI18n } from "@/i18n"
import { api } from "@/lib/api"
import { toastError } from "@/lib/errors"
import { qk, useTenants } from "@/lib/queries"
import type { Tenant } from "@/lib/types"
import { cn } from "@/lib/utils"

export function TenantsTab() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const tenants = useTenants()
  const [editing, setEditing] = React.useState<TenantEditorTarget | null>(null)
  const [resetting, setResetting] = React.useState<Tenant | null>(null)
  const [deleting, setDeleting] = React.useState<Tenant | null>(null)

  const invalidateRules = () => {
    void queryClient.invalidateQueries({ queryKey: qk.rules })
    void queryClient.invalidateQueries({ queryKey: qk.overview })
  }

  const resetUsage = useMutation({
    mutationFn: (tenant: Tenant) => api.tenants.resetUsage(tenant.id),
    onSuccess: (updated, tenant) => {
      queryClient.setQueryData<Tenant[]>(qk.tenants, (old) =>
        old?.map((item) => (item.id === updated.id ? updated : item))
      )
      toast.success(t("access.tenants.usageReset", { name: tenant.name || tenant.id }))
      // A suspended tenant's rules resume.
      invalidateRules()
    },
    onError: (error) => toastError(error, t),
    onSettled: () => queryClient.invalidateQueries({ queryKey: qk.tenants }),
  })

  const remove = useMutation({
    mutationFn: (tenant: Tenant) => api.tenants.remove(tenant.id),
    onSuccess: (_data, tenant) => {
      queryClient.setQueryData<Tenant[]>(qk.tenants, (old) =>
        old?.filter((item) => item.id !== tenant.id)
      )
      toast.success(t("access.tenants.deleted", { name: tenant.name || tenant.id }))
    },
    onError: (error) => toastError(error, t),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: qk.tenants })
      void queryClient.invalidateQueries({ queryKey: qk.users })
    },
  })

  const items = tenants.data ?? []

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("access.tenants.title")}</CardTitle>
        <CardDescription>{t("access.tenants.description")}</CardDescription>
        <CardAction>
          <Button size="sm" onClick={() => setEditing("create")}>
            <PlusIcon data-icon="inline-start" />
            {t("access.tenants.create")}
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        {tenants.isPending ? (
          <div className="flex flex-col gap-2">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} className="h-12" />
            ))}
          </div>
        ) : tenants.isError ? (
          <QueryError error={tenants.error} onRetry={() => void tenants.refetch()} />
        ) : items.length === 0 ? (
          <Empty className="border py-10">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <BuildingIcon />
              </EmptyMedia>
              <EmptyTitle>{t("access.tenants.empty")}</EmptyTitle>
              <EmptyDescription>{t("access.tenants.emptyHint")}</EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button size="sm" variant="outline" onClick={() => setEditing("create")}>
                <PlusIcon data-icon="inline-start" />
                {t("access.tenants.create")}
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>{t("access.tenants.columns.tenant")}</TableHead>
                <TableHead>{t("access.tenants.columns.ports")}</TableHead>
                <TableHead className="text-right">{t("access.tenants.columns.rules")}</TableHead>
                <TableHead className="text-right">{t("access.tenants.columns.users")}</TableHead>
                <TableHead>{t("access.tenants.columns.usage")}</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">{t("access.tenants.columns.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((tenant) => (
                <TenantRow
                  key={tenant.id}
                  tenant={tenant}
                  onEdit={() => setEditing(tenant)}
                  onResetUsage={() => setResetting(tenant)}
                  onDelete={() => setDeleting(tenant)}
                />
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>

      <TenantFormDialog target={editing} onOpenChange={(open) => !open && setEditing(null)} />
      <ConfirmDialog
        open={resetting !== null}
        onOpenChange={(open) => !open && setResetting(null)}
        title={t("access.tenants.resetUsageTitle", {
          name: resetting?.name || resetting?.id || "",
        })}
        description={t("access.tenants.resetUsageDescription")}
        confirmLabel={t("access.tenants.resetUsage")}
        destructive={false}
        onConfirm={() => resetting && resetUsage.mutateAsync(resetting)}
      />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t("access.tenants.deleteTitle", {
          name: deleting?.name || deleting?.id || "",
        })}
        description={t("access.tenants.deleteDescription")}
        confirmLabel={t("common.actions.delete")}
        onConfirm={() => deleting && remove.mutateAsync(deleting)}
      />
    </Card>
  )
}

function TenantRow({
  tenant,
  onEdit,
  onResetUsage,
  onDelete,
}: {
  tenant: Tenant
  onEdit: () => void
  onResetUsage: () => void
  onDelete: () => void
}) {
  const { t, fmt } = useI18n()
  const ports = formatPortRanges(tenant.portRanges)
  const maxRules = tenant.quota.maxRules ?? 0
  const limit = tenant.quota.monthlyBytes ?? 0
  const ratio = usageRatio(tenant.usage.bytes, limit)

  return (
    <TableRow>
      <TableCell>
        <div className="flex min-w-0 flex-col gap-0.5">
          <div className="flex items-center gap-2">
            <span className="max-w-48 truncate font-medium">{tenant.name || tenant.id}</span>
            {tenant.suspended && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Badge variant="warning">
                    <CirclePauseIcon data-icon="inline-start" />
                    {t("access.tenants.suspended")}
                  </Badge>
                </TooltipTrigger>
                <TooltipContent className="max-w-64">
                  {t("access.tenants.suspendedHint")}
                </TooltipContent>
              </Tooltip>
            )}
          </div>
          <span className="max-w-48 truncate font-mono text-xs text-muted-foreground">
            {tenant.id}
          </span>
        </div>
      </TableCell>
      <TableCell>
        {ports ? (
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="block max-w-48 truncate font-mono text-xs">{ports}</span>
            </TooltipTrigger>
            <TooltipContent className="max-w-80 font-mono break-words">{ports}</TooltipContent>
          </Tooltip>
        ) : (
          <span className="text-muted-foreground">{t("access.tenants.noPorts")}</span>
        )}
      </TableCell>
      <TableCell className="text-right tabular-nums">
        {fmt.number(tenant.rules)}
        {maxRules > 0 && (
          <span className="text-muted-foreground"> / {fmt.number(maxRules)}</span>
        )}
      </TableCell>
      <TableCell className="text-right tabular-nums">{fmt.number(tenant.users)}</TableCell>
      <TableCell className="min-w-40">
        <Tooltip>
          <TooltipTrigger asChild>
            <div className="flex flex-col gap-1.5">
              <span className="text-xs tabular-nums">
                {limit > 0
                  ? t("access.tenants.usageOf", {
                      used: fmt.bytes(tenant.usage.bytes),
                      quota: fmt.bytes(limit),
                    })
                  : fmt.bytes(tenant.usage.bytes)}
              </span>
              {limit > 0 && (
                <Progress
                  value={ratio * 100}
                  aria-label={t("access.tenants.columns.usage")}
                  className={cn(
                    ratio >= 1 || tenant.suspended
                      ? "*:data-[slot=progress-indicator]:bg-destructive"
                      : ratio >= 0.8 && "*:data-[slot=progress-indicator]:bg-warning"
                  )}
                />
              )}
            </div>
          </TooltipTrigger>
          <TooltipContent>
            {t("access.tenants.usageSince", { date: fmt.dateTime(tenant.usage.since) })}
          </TooltipContent>
        </Tooltip>
      </TableCell>
      <TableCell className="text-right">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" aria-label={t("common.actions.more")}>
              <MoreHorizontalIcon />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-40">
            <DropdownMenuGroup>
              <DropdownMenuItem onSelect={onEdit}>
                <PencilIcon />
                {t("common.actions.edit")}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={onResetUsage}>
                <RotateCcwIcon />
                {t("access.tenants.resetUsage")}
              </DropdownMenuItem>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuItem variant="destructive" onSelect={onDelete}>
                <Trash2Icon />
                {t("common.actions.delete")}
              </DropdownMenuItem>
            </DropdownMenuGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </TableCell>
    </TableRow>
  )
}
