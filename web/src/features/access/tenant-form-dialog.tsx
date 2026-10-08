import * as React from "react"
import { useFieldArray, useForm, type FieldPath } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { useQueryClient } from "@tanstack/react-query"
import { CircleAlertIcon, MinusIcon, PlusIcon, XIcon } from "lucide-react"
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
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSeparator,
  FieldSet,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
  InputGroupText,
} from "@/components/ui/input-group"
import { Spinner } from "@/components/ui/spinner"
import { useI18n, type Translate } from "@/i18n"
import { api, isApiError } from "@/lib/api"
import { errorMessage } from "@/lib/errors"
import { qk } from "@/lib/queries"
import type { PortRange, Tenant, TenantInput, TenantQuota } from "@/lib/types"
import { bytesToGiB, GIB } from "@/features/access/utils"

/** `"create"` opens an empty form; a tenant opens it for editing. */
export type TenantEditorTarget = "create" | Tenant

const FORM_ID = "tenant-form"
const MAX_RANGES = 64

function parsePort(value: string) {
  if (!/^\d{1,5}$/.test(value.trim())) return null
  const n = Number(value)
  return n >= 1 && n <= 65535 ? n : null
}

function makeSchema(t: Translate) {
  const optionalWhole = z
    .string()
    .trim()
    .refine((v) => v === "" || /^\d{1,9}$/.test(v), t("access.tenants.form.wholeNumber"))
  return z
    .object({
      id: z
        .string()
        .trim()
        .min(1, t("access.tenants.form.idRequired"))
        .max(64, t("access.tenants.form.idTooLong"))
        .regex(/^[A-Za-z0-9._-]+$/, t("access.tenants.form.idInvalid")),
      name: z
        .string()
        .trim()
        .min(1, t("access.tenants.form.nameRequired"))
        .max(64, t("access.tenants.form.nameTooLong")),
      ranges: z.array(z.object({ start: z.string(), end: z.string() })),
      maxRules: optionalWhole,
      monthlyGiB: z
        .string()
        .trim()
        .refine(
          (v) => v === "" || (/^\d+(\.\d+)?$/.test(v) && Number(v) * GIB <= Number.MAX_SAFE_INTEGER),
          t("access.tenants.form.numberInvalid")
        ),
      resetDay: z
        .string()
        .trim()
        .refine(
          (v) => v === "" || (/^\d{1,2}$/.test(v) && Number(v) >= 1 && Number(v) <= 28),
          t("access.tenants.form.resetDayInvalid")
        ),
    })
    .superRefine((v, ctx) => {
      const parsed: (PortRange | null)[] = v.ranges.map((row, i) => {
        const start = parsePort(row.start)
        const end = parsePort(row.end)
        if (start === null) {
          ctx.addIssue({
            code: "custom",
            path: ["ranges", i, "start"],
            message: t("access.tenants.form.portInvalid"),
          })
        }
        if (end === null) {
          ctx.addIssue({
            code: "custom",
            path: ["ranges", i, "end"],
            message: t("access.tenants.form.portInvalid"),
          })
        }
        if (start === null || end === null) return null
        if (end < start) {
          ctx.addIssue({
            code: "custom",
            path: ["ranges", i, "end"],
            message: t("access.tenants.form.rangeOrder"),
          })
          return null
        }
        return [start, end]
      })
      parsed.forEach((p, i) => {
        if (!p) return
        const overlaps = parsed
          .slice(0, i)
          .some((q) => q !== null && p[0] <= q[1] && q[0] <= p[1])
        if (overlaps) {
          ctx.addIssue({
            code: "custom",
            path: ["ranges", i, "start"],
            message: t("access.tenants.form.rangeOverlap"),
          })
        }
      })
    })
}

type Values = z.infer<ReturnType<typeof makeSchema>>

function toValues(tenant: Tenant | null): Values {
  return {
    id: tenant?.id ?? "",
    name: tenant?.name ?? "",
    ranges: (tenant?.portRanges ?? []).map(([a, b]) => ({ start: String(a), end: String(b) })),
    maxRules: tenant?.quota.maxRules ? String(tenant.quota.maxRules) : "",
    monthlyGiB: bytesToGiB(tenant?.quota.monthlyBytes),
    resetDay: tenant?.quota.resetDay ? String(tenant.quota.resetDay) : "",
  }
}

/** Maps a server validation field onto the form field to flag. */
function serverField(field: string): FieldPath<Values> | null {
  const range = /^portRanges\[(\d+)\]$/.exec(field)
  if (range) return `ranges.${Number(range[1])}.start`
  switch (field) {
    case "id":
      return "id"
    case "name":
      return "name"
    case "quota.maxRules":
      return "maxRules"
    case "quota.monthlyBytes":
      return "monthlyGiB"
    case "quota.resetDay":
      return "resetDay"
  }
  return null
}

export function TenantFormDialog({
  target,
  onOpenChange,
}: {
  target: TenantEditorTarget | null
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
      {/* Header and footer stay put; only the form scrolls. */}
      <DialogContent className="flex max-h-[calc(100dvh-2rem)] flex-col gap-0 p-0 sm:max-w-lg">
        <DialogHeader className="border-b p-4 pr-12">
          <DialogTitle>
            {editing
              ? t("access.tenants.form.editTitle", { name: editing.name || editing.id })
              : t("access.tenants.form.createTitle")}
          </DialogTitle>
          <DialogDescription>
            {editing
              ? t("access.tenants.form.editDescription")
              : t("access.tenants.form.createDescription")}
          </DialogDescription>
        </DialogHeader>
        {current && (
          <TenantForm
            key={editing?.id ?? "create"}
            tenant={editing}
            onPendingChange={setPending}
            onSaved={() => onOpenChange(false)}
          />
        )}
        <DialogFooter className="m-0">
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
            {editing ? t("common.actions.save") : t("access.tenants.form.submitCreate")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function TenantForm({
  tenant,
  onPendingChange,
  onSaved,
}: {
  tenant: Tenant | null
  onPendingChange: (pending: boolean) => void
  onSaved: () => void
}) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [formError, setFormError] = React.useState<string | null>(null)
  const schema = React.useMemo(() => makeSchema(t), [t])
  const initial = React.useMemo(() => toValues(tenant), [tenant])

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: initial,
  })
  const { errors } = form.formState
  const ranges = useFieldArray({ control: form.control, name: "ranges" })

  const onSubmit = async (values: Values) => {
    setFormError(null)
    onPendingChange(true)
    const quota: TenantQuota = {}
    if (values.maxRules) quota.maxRules = Number(values.maxRules)
    if (values.monthlyGiB) {
      // Keep the exact byte count when the rounded GiB value was not touched.
      quota.monthlyBytes =
        tenant && values.monthlyGiB === initial.monthlyGiB
          ? tenant.quota.monthlyBytes
          : Math.round(Number(values.monthlyGiB) * GIB)
    }
    if (values.resetDay) quota.resetDay = Number(values.resetDay)
    const body: Omit<TenantInput, "id"> = {
      name: values.name,
      portRanges: values.ranges.map((r) => [Number(r.start), Number(r.end)]),
      quota,
    }
    try {
      const saved = tenant
        ? await api.tenants.update(tenant.id, body)
        : await api.tenants.create({ id: values.id, ...body })
      void queryClient.invalidateQueries({ queryKey: qk.tenants })
      if (tenant) {
        // Quota changes can suspend or resume the tenant's rules.
        void queryClient.invalidateQueries({ queryKey: qk.users })
        void queryClient.invalidateQueries({ queryKey: qk.rules })
        void queryClient.invalidateQueries({ queryKey: qk.overview })
      }
      toast.success(
        t(tenant ? "access.tenants.updated" : "access.tenants.created", {
          name: saved.name || saved.id,
        })
      )
      onSaved()
    } catch (error) {
      const name = isApiError(error) && error.field ? serverField(error.field) : null
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
    <form
      id={FORM_ID}
      noValidate
      onSubmit={form.handleSubmit(onSubmit)}
      className="overflow-y-auto p-4"
    >
      <FieldGroup>
        {formError && (
          <Alert variant="destructive">
            <CircleAlertIcon />
            <AlertDescription>{formError}</AlertDescription>
          </Alert>
        )}

        <FieldSet>
          <FieldLegend>{t("access.tenants.form.sections.basic")}</FieldLegend>
          <FieldGroup>
            <Field data-invalid={!!errors.id || undefined}>
              <FieldLabel htmlFor="tenant-id">{t("access.tenants.form.id")}</FieldLabel>
              <Input
                id="tenant-id"
                autoComplete="off"
                className="font-mono read-only:bg-muted read-only:text-muted-foreground"
                readOnly={!!tenant}
                placeholder={t("access.tenants.form.idPlaceholder")}
                aria-invalid={!!errors.id}
                {...form.register("id")}
              />
              {errors.id ? (
                <FieldError errors={[errors.id]} />
              ) : (
                !tenant && <FieldDescription>{t("access.tenants.form.idHint")}</FieldDescription>
              )}
            </Field>
            <Field data-invalid={!!errors.name || undefined}>
              <FieldLabel htmlFor="tenant-name">{t("access.tenants.form.name")}</FieldLabel>
              <Input
                id="tenant-name"
                autoComplete="off"
                placeholder={t("access.tenants.form.namePlaceholder")}
                aria-invalid={!!errors.name}
                {...form.register("name")}
              />
              <FieldError errors={[errors.name]} />
            </Field>
          </FieldGroup>
        </FieldSet>

        <FieldSeparator />

        <FieldSet>
          <FieldLegend>{t("access.tenants.form.sections.ports")}</FieldLegend>
          <FieldDescription>{t("access.tenants.form.portsHint")}</FieldDescription>
          <FieldGroup className="gap-3">
            {ranges.fields.length === 0 && (
              <p className="text-sm text-muted-foreground">{t("access.tenants.form.noRanges")}</p>
            )}
            {ranges.fields.map((row, i) => {
              const rowErrors = errors.ranges?.[i]
              const error = rowErrors?.start ?? rowErrors?.end
              const label = t("access.tenants.form.rangeLabel", { index: i + 1 })
              return (
                <Field key={row.id} data-invalid={error ? true : undefined}>
                  <div className="flex items-center gap-2">
                    <InputGroup className="flex-1">
                      <InputGroupAddon>{t("access.tenants.form.start")}</InputGroupAddon>
                      <InputGroupInput
                        inputMode="numeric"
                        autoComplete="off"
                        className="font-mono"
                        placeholder="1–65535"
                        aria-label={`${label} – ${t("access.tenants.form.start")}`}
                        aria-invalid={rowErrors?.start ? true : undefined}
                        {...form.register(`ranges.${i}.start`)}
                      />
                    </InputGroup>
                    <MinusIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                    <InputGroup className="flex-1">
                      <InputGroupAddon>{t("access.tenants.form.end")}</InputGroupAddon>
                      <InputGroupInput
                        inputMode="numeric"
                        autoComplete="off"
                        className="font-mono"
                        placeholder="1–65535"
                        aria-label={`${label} – ${t("access.tenants.form.end")}`}
                        aria-invalid={rowErrors?.end ? true : undefined}
                        {...form.register(`ranges.${i}.end`)}
                      />
                    </InputGroup>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`${t("access.tenants.form.removeRange")} (${label})`}
                      onClick={() => ranges.remove(i)}
                    >
                      <XIcon />
                    </Button>
                  </div>
                  {error && <FieldError>{error.message}</FieldError>}
                </Field>
              )
            })}
            <div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={ranges.fields.length >= MAX_RANGES}
                onClick={() => ranges.append({ start: "", end: "" })}
              >
                <PlusIcon data-icon="inline-start" />
                {t("access.tenants.form.addRange")}
              </Button>
            </div>
          </FieldGroup>
        </FieldSet>

        <FieldSeparator />

        <FieldSet>
          <FieldLegend>{t("access.tenants.form.sections.quota")}</FieldLegend>
          <FieldGroup>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field data-invalid={!!errors.maxRules || undefined}>
                <FieldLabel htmlFor="tenant-max-rules">
                  {t("access.tenants.form.maxRules")}
                </FieldLabel>
                <Input
                  id="tenant-max-rules"
                  inputMode="numeric"
                  autoComplete="off"
                  placeholder={t("common.states.unlimited")}
                  aria-invalid={!!errors.maxRules}
                  {...form.register("maxRules")}
                />
                {errors.maxRules ? (
                  <FieldError errors={[errors.maxRules]} />
                ) : (
                  <FieldDescription>{t("access.tenants.form.maxRulesHint")}</FieldDescription>
                )}
              </Field>
              <Field data-invalid={!!errors.resetDay || undefined}>
                <FieldLabel htmlFor="tenant-reset-day">
                  {t("access.tenants.form.resetDay")}
                </FieldLabel>
                <Input
                  id="tenant-reset-day"
                  inputMode="numeric"
                  autoComplete="off"
                  placeholder="1"
                  aria-invalid={!!errors.resetDay}
                  {...form.register("resetDay")}
                />
                {errors.resetDay ? (
                  <FieldError errors={[errors.resetDay]} />
                ) : (
                  <FieldDescription>{t("access.tenants.form.resetDayHint")}</FieldDescription>
                )}
              </Field>
            </div>
            <Field data-invalid={!!errors.monthlyGiB || undefined}>
              <FieldLabel htmlFor="tenant-monthly">
                {t("access.tenants.form.monthlyTraffic")}
              </FieldLabel>
              <InputGroup>
                <InputGroupInput
                  id="tenant-monthly"
                  inputMode="decimal"
                  autoComplete="off"
                  placeholder={t("common.states.unlimited")}
                  aria-invalid={!!errors.monthlyGiB}
                  {...form.register("monthlyGiB")}
                />
                <InputGroupAddon align="inline-end">
                  <InputGroupText>GiB</InputGroupText>
                </InputGroupAddon>
              </InputGroup>
              {errors.monthlyGiB ? (
                <FieldError errors={[errors.monthlyGiB]} />
              ) : (
                <FieldDescription>{t("access.tenants.form.monthlyTrafficHint")}</FieldDescription>
              )}
            </Field>
          </FieldGroup>
        </FieldSet>
      </FieldGroup>
    </form>
  )
}
