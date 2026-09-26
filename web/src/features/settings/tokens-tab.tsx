import * as React from "react"
import { useForm } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { KeyRoundIcon, PlusIcon, TriangleAlertIcon } from "lucide-react"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/common/confirm-dialog"
import { CopyButton } from "@/components/common/copy-button"
import { QueryError } from "@/components/common/query-state"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
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
import { useI18n } from "@/i18n"
import { api, isApiError } from "@/lib/api"
import { errorMessage, toastError } from "@/lib/errors"
import { qk, useTokens } from "@/lib/queries"
import type { ApiToken } from "@/lib/types"

export function TokensTab() {
  const { t, fmt } = useI18n()
  const queryClient = useQueryClient()
  const tokens = useTokens()
  const [creating, setCreating] = React.useState(false)
  const [revoking, setRevoking] = React.useState<ApiToken | null>(null)

  const revoke = useMutation({
    mutationFn: (token: ApiToken) => api.auth.revokeToken(token.id),
    onSuccess: (_data, token) => {
      queryClient.setQueryData<ApiToken[]>(qk.tokens, (old) =>
        old?.filter((item) => item.id !== token.id)
      )
      toast.success(t("settings.tokens.revoked"))
    },
    onError: (error) => toastError(error, t),
    onSettled: () => queryClient.invalidateQueries({ queryKey: qk.tokens }),
  })

  const items = tokens.data ?? []

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("settings.tokens.title")}</CardTitle>
        <CardDescription>{t("settings.tokens.description")}</CardDescription>
        <CardAction>
          <Button size="sm" onClick={() => setCreating(true)}>
            <PlusIcon data-icon="inline-start" />
            {t("settings.tokens.create")}
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        {tokens.isPending ? (
          <div className="flex flex-col gap-2">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} className="h-9" />
            ))}
          </div>
        ) : tokens.isError ? (
          <QueryError error={tokens.error} onRetry={() => void tokens.refetch()} />
        ) : items.length === 0 ? (
          <Empty className="border py-10">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <KeyRoundIcon />
              </EmptyMedia>
              <EmptyTitle>{t("settings.tokens.empty")}</EmptyTitle>
              <EmptyDescription>{t("settings.tokens.emptyHint")}</EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button size="sm" variant="outline" onClick={() => setCreating(true)}>
                <PlusIcon data-icon="inline-start" />
                {t("settings.tokens.create")}
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>{t("settings.tokens.columns.name")}</TableHead>
                <TableHead>{t("settings.tokens.columns.prefix")}</TableHead>
                <TableHead>{t("settings.tokens.columns.created")}</TableHead>
                <TableHead className="w-24">
                  <span className="sr-only">{t("settings.tokens.columns.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((token) => (
                <TableRow key={token.id}>
                  <TableCell className="max-w-56 truncate font-medium">{token.name}</TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">
                    {token.prefix}…
                  </TableCell>
                  <TableCell>
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <span className="text-muted-foreground">
                          {fmt.relative(token.createdAt)}
                        </span>
                      </TooltipTrigger>
                      <TooltipContent>{fmt.dateTime(token.createdAt)}</TooltipContent>
                    </Tooltip>
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="destructive"
                      size="sm"
                      onClick={() => setRevoking(token)}
                    >
                      {t("settings.tokens.revoke")}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>

      <CreateTokenDialog open={creating} onOpenChange={setCreating} />
      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(open) => !open && setRevoking(null)}
        title={t("settings.tokens.revokeTitle", { name: revoking?.name ?? "" })}
        description={t("settings.tokens.revokeDescription")}
        confirmLabel={t("settings.tokens.revoke")}
        onConfirm={() => revoking && revoke.mutateAsync(revoking)}
      />
    </Card>
  )
}

function CreateTokenDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [plainToken, setPlainToken] = React.useState<string | null>(null)

  const schema = React.useMemo(
    () =>
      z.object({
        name: z
          .string()
          .trim()
          .min(1, t("settings.tokens.nameRequired"))
          .max(64, t("settings.tokens.nameTooLong")),
      }),
    [t]
  )
  type Values = z.infer<typeof schema>
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { name: "" },
  })
  const { errors, isSubmitting } = form.formState

  const close = (next: boolean) => {
    if (next || isSubmitting) return
    onOpenChange(false)
    // Wait for the close animation before wiping the token from view.
    setTimeout(() => {
      setPlainToken(null)
      form.reset({ name: "" })
    }, 200)
  }

  const onSubmit = async ({ name }: Values) => {
    try {
      const res = await api.auth.createToken(name)
      setPlainToken(res.token)
      void queryClient.invalidateQueries({ queryKey: qk.tokens })
    } catch (error) {
      if (isApiError(error) && error.field === "name") {
        form.setError("name", { message: error.message })
      } else {
        form.setError("root", { message: errorMessage(error, t) })
      }
    }
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="sm:max-w-lg">
        {plainToken ? (
          <>
            <DialogHeader>
              <DialogTitle>{t("settings.tokens.createdTitle")}</DialogTitle>
              <DialogDescription>{t("settings.tokens.usage")}</DialogDescription>
            </DialogHeader>
            <div className="flex min-w-0 flex-col gap-3">
              <code className="rounded-md bg-muted px-2.5 py-1.5 text-xs break-all">
                Authorization: Bearer &lt;token&gt;
              </code>
              <InputGroup>
                <InputGroupInput
                  readOnly
                  value={plainToken}
                  className="font-mono text-xs"
                  aria-label={t("settings.tokens.columns.prefix")}
                  onFocus={(e) => e.currentTarget.select()}
                />
                <InputGroupAddon align="inline-end">
                  <CopyButton value={plainToken} variant="ghost" size="icon-xs" />
                </InputGroupAddon>
              </InputGroup>
              <Alert>
                <TriangleAlertIcon />
                <AlertDescription>{t("settings.tokens.createdWarning")}</AlertDescription>
              </Alert>
            </div>
            <DialogFooter>
              <CopyButton value={plainToken} />
              <DialogClose asChild>
                <Button>{t("settings.tokens.done")}</Button>
              </DialogClose>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={form.handleSubmit(onSubmit)} noValidate className="contents">
            <DialogHeader>
              <DialogTitle>{t("settings.tokens.createTitle")}</DialogTitle>
              <DialogDescription>{t("settings.tokens.createDescription")}</DialogDescription>
            </DialogHeader>
            <FieldGroup>
              <Field data-invalid={!!errors.name || undefined}>
                <FieldLabel htmlFor="token-name">{t("settings.tokens.name")}</FieldLabel>
                <Input
                  id="token-name"
                  autoComplete="off"
                  placeholder={t("settings.tokens.namePlaceholder")}
                  aria-invalid={!!errors.name}
                  {...form.register("name")}
                />
                <FieldError errors={[errors.name, errors.root]} />
              </Field>
            </FieldGroup>
            <DialogFooter>
              <DialogClose asChild>
                <Button type="button" variant="outline" disabled={isSubmitting}>
                  {t("common.actions.cancel")}
                </Button>
              </DialogClose>
              <Button type="submit" disabled={isSubmitting}>
                {isSubmitting && <Spinner data-icon="inline-start" />}
                {isSubmitting ? t("settings.tokens.creating") : t("common.actions.create")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
