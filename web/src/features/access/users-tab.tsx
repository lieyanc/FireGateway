import * as React from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import {
  MoreHorizontalIcon,
  PencilIcon,
  PlusIcon,
  Trash2Icon,
  UsersRoundIcon,
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
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { UserFormDialog, type UserEditorTarget } from "@/features/access/user-form-dialog"
import { useI18n } from "@/i18n"
import { api } from "@/lib/api"
import { toastError } from "@/lib/errors"
import { qk, useAuthState, useTenants, useUsers } from "@/lib/queries"
import type { User } from "@/lib/types"

export function UsersTab() {
  const { t, fmt } = useI18n()
  const queryClient = useQueryClient()
  const users = useUsers()
  const tenants = useTenants()
  const auth = useAuthState()
  const [editing, setEditing] = React.useState<UserEditorTarget | null>(null)
  const [deleting, setDeleting] = React.useState<User | null>(null)

  const remove = useMutation({
    mutationFn: (user: User) => api.users.remove(user.id),
    onSuccess: (_data, user) => {
      queryClient.setQueryData<User[]>(qk.users, (old) =>
        old?.filter((item) => item.id !== user.id)
      )
      toast.success(t("access.users.deleted", { name: user.username }))
    },
    onError: (error) => toastError(error, t),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: qk.users })
      void queryClient.invalidateQueries({ queryKey: qk.tenants })
    },
  })

  const tenantName = (id: string) =>
    tenants.data?.find((tn) => tn.id === id)?.name ?? id
  const items = users.data ?? []

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("access.users.title")}</CardTitle>
        <CardDescription>{t("access.users.description")}</CardDescription>
        <CardAction>
          <Button size="sm" onClick={() => setEditing("create")}>
            <PlusIcon data-icon="inline-start" />
            {t("access.users.create")}
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        {users.isPending ? (
          <div className="flex flex-col gap-2">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} className="h-9" />
            ))}
          </div>
        ) : users.isError ? (
          <QueryError error={users.error} onRetry={() => void users.refetch()} />
        ) : items.length === 0 ? (
          <Empty className="border py-10">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <UsersRoundIcon />
              </EmptyMedia>
              <EmptyTitle>{t("access.users.empty")}</EmptyTitle>
            </EmptyHeader>
            <EmptyContent>
              <Button size="sm" variant="outline" onClick={() => setEditing("create")}>
                <PlusIcon data-icon="inline-start" />
                {t("access.users.create")}
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>{t("access.users.columns.username")}</TableHead>
                <TableHead>{t("access.users.columns.role")}</TableHead>
                <TableHead>{t("access.users.columns.tenant")}</TableHead>
                <TableHead>{t("access.users.columns.status")}</TableHead>
                <TableHead className="text-right">{t("access.users.columns.tokens")}</TableHead>
                <TableHead>{t("access.users.columns.created")}</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">{t("access.users.columns.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((user) => {
                const self = user.id === auth.data?.userId
                return (
                  <TableRow key={user.id}>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <span className="max-w-48 truncate font-medium">{user.username}</span>
                        {self && <Badge variant="outline">{t("access.users.you")}</Badge>}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant={user.role === "admin" ? "default" : "secondary"}>
                        {t(`access.users.roles.${user.role}`)}
                      </Badge>
                    </TableCell>
                    <TableCell className="max-w-40 truncate">
                      {user.tenantId ? (
                        tenantName(user.tenantId)
                      ) : (
                        <span className="text-muted-foreground">–</span>
                      )}
                    </TableCell>
                    <TableCell>
                      {user.disabled ? (
                        <Badge variant="warning">{t("access.users.disabled")}</Badge>
                      ) : (
                        <Badge variant="success">{t("access.users.active")}</Badge>
                      )}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {fmt.number(user.tokens)}
                    </TableCell>
                    <TableCell>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <span className="text-muted-foreground">
                            {fmt.relative(user.createdAt)}
                          </span>
                        </TooltipTrigger>
                        <TooltipContent>{fmt.dateTime(user.createdAt)}</TooltipContent>
                      </Tooltip>
                    </TableCell>
                    <TableCell className="text-right">
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={t("common.actions.more")}
                          >
                            <MoreHorizontalIcon />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end" className="min-w-36">
                          <DropdownMenuGroup>
                            <DropdownMenuItem onSelect={() => setEditing(user)}>
                              <PencilIcon />
                              {t("common.actions.edit")}
                            </DropdownMenuItem>
                          </DropdownMenuGroup>
                          <DropdownMenuSeparator />
                          <DropdownMenuGroup>
                            <DropdownMenuItem
                              variant="destructive"
                              disabled={self}
                              onSelect={() => setDeleting(user)}
                            >
                              <Trash2Icon />
                              {t("common.actions.delete")}
                            </DropdownMenuItem>
                          </DropdownMenuGroup>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>

      <UserFormDialog target={editing} onOpenChange={(open) => !open && setEditing(null)} />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t("access.users.deleteTitle", { name: deleting?.username ?? "" })}
        description={t("access.users.deleteDescription")}
        confirmLabel={t("common.actions.delete")}
        onConfirm={() => deleting && remove.mutateAsync(deleting)}
      />
    </Card>
  )
}
