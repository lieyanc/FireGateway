import * as React from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { useQueryClient } from "@tanstack/react-query"
import { CircleAlertIcon } from "lucide-react"
import { toast } from "sonner"
import { z } from "zod"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { useI18n, type Translate } from "@/i18n"
import { api, isApiError } from "@/lib/api"
import { errorMessage } from "@/lib/errors"
import { qk, useAuthState, useTenants } from "@/lib/queries"
import type { Role, User, UserInput } from "@/lib/types"

/** `"create"` opens an empty form; a user opens it for editing. */
export type UserEditorTarget = "create" | User

const FORM_ID = "user-form"

function makeSchema(t: Translate, creating: boolean) {
  return z
    .object({
      username: z
        .string()
        .trim()
        .min(1, t("access.users.form.usernameRequired"))
        .max(64, t("access.users.form.usernameTooLong")),
      password: z.string(),
      role: z.enum(["admin", "member"]),
      tenantId: z.string(),
      disabled: z.boolean(),
    })
    .superRefine((v, ctx) => {
      const length = [...v.password].length
      if ((creating || length > 0) && (length < 8 || length > 128)) {
        ctx.addIssue({
          code: "custom",
          path: ["password"],
          message: t("access.users.form.passwordLength"),
        })
      }
      if (v.role === "member" && !v.tenantId) {
        ctx.addIssue({
          code: "custom",
          path: ["tenantId"],
          message: t("access.users.form.tenantRequired"),
        })
      }
    })
}

type Values = z.infer<ReturnType<typeof makeSchema>>
const SERVER_FIELDS = ["username", "password", "role", "tenantId"] as const

export function UserFormDialog({
  target,
  onOpenChange,
}: {
  target: UserEditorTarget | null
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useI18n()
  const [pending, setPending] = React.useState(false)
  // Keep the last target while the dialog animates out.
  const [shown, setShown] = React.useState(target)
  if (target && target !== shown) setShown(target)
  const current = target ?? shown
  const editing = current && current !== "create" ? current : null

  return (
    <Dialog open={target !== null} onOpenChange={(open) => !pending && onOpenChange(open)}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {editing
              ? t("access.users.form.editTitle", { name: editing.username })
              : t("access.users.form.createTitle")}
          </DialogTitle>
          <DialogDescription>
            {editing
              ? t("access.users.form.editDescription")
              : t("access.users.form.createDescription")}
          </DialogDescription>
        </DialogHeader>
        {current && (
          <UserForm
            key={editing?.id ?? "create"}
            user={editing}
            onPendingChange={setPending}
            onSaved={() => onOpenChange(false)}
          />
        )}
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            disabled={pending}
            onClick={() => onOpenChange(false)}
          >
            {t("common.actions.cancel")}
          </Button>
          <Button type="submit" form={FORM_ID} disabled={pending}>
            {pending && <Spinner data-icon="inline-start" />}
            {editing ? t("common.actions.save") : t("access.users.form.submitCreate")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function UserForm({
  user,
  onPendingChange,
  onSaved,
}: {
  user: User | null
  onPendingChange: (pending: boolean) => void
  onSaved: () => void
}) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const tenants = useTenants()
  const auth = useAuthState()
  const [formError, setFormError] = React.useState<string | null>(null)
  const schema = React.useMemo(() => makeSchema(t, !user), [t, user])
  const self = !!user && user.id === auth.data?.userId

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      username: user?.username ?? "",
      password: "",
      role: user?.role ?? "member",
      tenantId: user?.tenantId ?? "",
      disabled: user?.disabled ?? false,
    },
  })
  const { errors } = form.formState
  const role = useWatch({ control: form.control, name: "role" })
  const tenantItems = tenants.data ?? []

  const onSubmit = async (values: Values) => {
    setFormError(null)
    onPendingChange(true)
    const body: UserInput = {
      username: values.username,
      role: values.role,
      tenantId: values.role === "member" ? values.tenantId : "",
      disabled: values.disabled,
    }
    if (values.password) body.password = values.password
    try {
      const saved = user
        ? await api.users.update(user.id, body)
        : await api.users.create(body)
      void queryClient.invalidateQueries({ queryKey: qk.users })
      void queryClient.invalidateQueries({ queryKey: qk.tenants })
      if (self) void queryClient.invalidateQueries({ queryKey: qk.authState })
      toast.success(
        t(user ? "access.users.updated" : "access.users.created", { name: saved.username })
      )
      onSaved()
    } catch (error) {
      const field = isApiError(error) ? error.field : undefined
      const name = SERVER_FIELDS.find((f) => f === field)
      if (isApiError(error) && name) {
        form.setError(name, { message: error.message }, { shouldFocus: true })
      } else {
        setFormError(errorMessage(error, t))
      }
    } finally {
      onPendingChange(false)
    }
  }

  return (
    <form id={FORM_ID} noValidate onSubmit={form.handleSubmit(onSubmit)}>
      <FieldGroup>
        {formError && (
          <Alert variant="destructive">
            <CircleAlertIcon />
            <AlertDescription>{formError}</AlertDescription>
          </Alert>
        )}
        <Field data-invalid={!!errors.username || undefined}>
          <FieldLabel htmlFor="user-username">{t("access.users.form.username")}</FieldLabel>
          <Input
            id="user-username"
            autoComplete="off"
            placeholder={t("access.users.form.usernamePlaceholder")}
            aria-invalid={!!errors.username}
            {...form.register("username")}
          />
          <FieldError errors={[errors.username]} />
        </Field>
        <Field data-invalid={!!errors.password || undefined}>
          <FieldLabel htmlFor="user-password">{t("access.users.form.password")}</FieldLabel>
          <Input
            id="user-password"
            type="password"
            autoComplete="new-password"
            aria-invalid={!!errors.password}
            {...form.register("password")}
          />
          <FieldDescription>
            {user ? t("access.users.form.passwordKeep") : t("access.users.form.passwordHint")}
          </FieldDescription>
          <FieldError errors={[errors.password]} />
        </Field>
        <Controller
          control={form.control}
          name="role"
          render={({ field }) => (
            <Field data-invalid={!!errors.role || undefined}>
              <FieldLabel htmlFor="user-role">{t("access.users.form.role")}</FieldLabel>
              <Select
                value={field.value}
                onValueChange={(v) => field.onChange(v as Role)}
                disabled={self}
              >
                <SelectTrigger id="user-role" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="member">{t("access.users.roles.member")}</SelectItem>
                    <SelectItem value="admin">{t("access.users.roles.admin")}</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              <FieldDescription>{t("access.users.form.roleHint")}</FieldDescription>
              <FieldError errors={[errors.role]} />
            </Field>
          )}
        />
        {role === "member" && (
          <Controller
            control={form.control}
            name="tenantId"
            render={({ field }) => {
              // Keep a tenant that isn't listed (yet) selectable.
              const missing =
                field.value !== "" && !tenantItems.some((tn) => tn.id === field.value)
              const empty = tenants.isSuccess && tenantItems.length === 0
              return (
                <Field data-invalid={!!errors.tenantId || undefined}>
                  <FieldLabel htmlFor="user-tenant">{t("access.users.form.tenant")}</FieldLabel>
                  <Select
                    value={field.value}
                    onValueChange={field.onChange}
                    disabled={empty && !missing}
                  >
                    <SelectTrigger
                      id="user-tenant"
                      className="w-full"
                      aria-invalid={!!errors.tenantId}
                    >
                      <SelectValue placeholder={t("access.users.form.tenantPlaceholder")} />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        {missing && (
                          <SelectItem value={field.value}>
                            <span className="font-mono">{field.value}</span>
                          </SelectItem>
                        )}
                        {tenantItems.map((tn) => (
                          <SelectItem key={tn.id} value={tn.id}>
                            {tn.name}
                            <span className="font-mono text-muted-foreground">{tn.id}</span>
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <FieldDescription>
                    {empty ? t("access.users.form.noTenants") : t("access.users.form.tenantHint")}
                  </FieldDescription>
                  <FieldError errors={[errors.tenantId]} />
                </Field>
              )
            }}
          />
        )}
        <Controller
          control={form.control}
          name="disabled"
          render={({ field }) => (
            <Field orientation="horizontal">
              <FieldContent>
                <FieldLabel htmlFor="user-disabled">{t("access.users.form.disabled")}</FieldLabel>
                <FieldDescription>{t("access.users.form.disabledHint")}</FieldDescription>
              </FieldContent>
              <Switch
                id="user-disabled"
                checked={field.value}
                onCheckedChange={field.onChange}
                disabled={self}
              />
            </Field>
          )}
        />
      </FieldGroup>
    </form>
  )
}
