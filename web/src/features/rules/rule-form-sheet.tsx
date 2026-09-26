import * as React from "react"
import {
  Controller,
  useForm,
  useWatch,
  type UseFormRegisterReturn,
} from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { useQueryClient } from "@tanstack/react-query"
import { ArrowRightIcon, CircleAlertIcon } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Field,
  FieldContent,
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
} from "@/components/ui/input-group"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { useI18n } from "@/i18n"
import { api, isApiError } from "@/lib/api"
import { errorMessage } from "@/lib/errors"
import { qk } from "@/lib/queries"
import type { Rule, RuleView } from "@/lib/types"
import { cn } from "@/lib/utils"
import {
  emptyRuleForm,
  formToRule,
  makeRuleSchema,
  parsePort,
  ruleToForm,
  serverFieldToForm,
  type RuleFormValues,
} from "@/features/rules/schema"

export type RuleEditorTarget =
  | { kind: "create" }
  | { kind: "edit"; rule: Rule }
  | { kind: "duplicate"; rule: Rule }

const HOST_PICKS = ["0.0.0.0", "127.0.0.1", "::"]
const FORM_ID = "rule-form"

export function RuleFormSheet({
  target,
  onOpenChange,
  onSaved,
}: {
  target: RuleEditorTarget | null
  onOpenChange: (open: boolean) => void
  onSaved?: (view: RuleView, kind: RuleEditorTarget["kind"]) => void
}) {
  const { t } = useI18n()
  const [pending, setPending] = React.useState(false)
  // Keep the last target while the sheet animates out.
  const [shown, setShown] = React.useState(target)
  if (target && target !== shown) setShown(target)
  const current = target ?? shown

  const title =
    current?.kind === "edit"
      ? t("rules.form.editTitle")
      : current?.kind === "duplicate"
        ? t("rules.form.duplicateTitle")
        : t("rules.form.createTitle")
  const description =
    current?.kind === "edit"
      ? t("rules.form.editDescription")
      : t("rules.form.createDescription")

  return (
    <Sheet open={target !== null} onOpenChange={(open) => !pending && onOpenChange(open)}>
      <SheetContent className="w-full gap-0 sm:max-w-xl">
        <SheetHeader className="border-b pr-12">
          <SheetTitle>{title}</SheetTitle>
          <SheetDescription>{description}</SheetDescription>
        </SheetHeader>
        {current && (
          <RuleForm
            target={current}
            onPendingChange={setPending}
            onSaved={(view) => {
              onSaved?.(view, current.kind)
              onOpenChange(false)
            }}
          />
        )}
        <SheetFooter className="flex-row justify-end border-t">
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
            {current?.kind === "edit"
              ? t("rules.form.submitSave")
              : t("rules.form.submitCreate")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}

function initialValues(target: RuleEditorTarget, suffix: string): RuleFormValues {
  if (target.kind === "create") return emptyRuleForm
  const values = ruleToForm(target.rule)
  if (target.kind === "duplicate") {
    values.name = `${values.name} ${suffix}`.trim()
  }
  return values
}

function RuleForm({
  target,
  onPendingChange,
  onSaved,
}: {
  target: RuleEditorTarget
  onPendingChange: (pending: boolean) => void
  onSaved: (view: RuleView) => void
}) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [formError, setFormError] = React.useState<string | null>(null)
  const schema = React.useMemo(() => makeRuleSchema(t), [t])

  const form = useForm<RuleFormValues>({
    resolver: zodResolver(schema),
    defaultValues: initialValues(target, t("rules.form.duplicateSuffix")),
  })
  const { errors } = form.formState
  const mode = useWatch({ control: form.control, name: "mode" })
  const aclMode = useWatch({ control: form.control, name: "aclMode" })

  // Target end follows start + (local range length) so one-to-one ranges
  // need only three numbers.
  const syncTargetEnd = () => {
    const [ls, le, ts] = form.getValues(["localStart", "localEnd", "targetStart"])
    const a = parsePort(ls)
    const b = parsePort(le)
    const c = parsePort(ts)
    if (a === null || b === null || c === null || b < a) return
    const end = c + (b - a)
    if (end <= 65535) {
      form.setValue("targetEnd", String(end), { shouldValidate: form.formState.isSubmitted })
    }
  }

  // Mirror the listen port into an untouched target port.
  const mirror = (from: "localPort" | "localStart", to: "targetPort" | "targetStart") => {
    if (!form.getFieldState(to).isDirty) {
      form.setValue(to, form.getValues(from))
    }
  }

  const switchMode = (next: "single" | "range") => {
    const v = form.getValues()
    if (next === "range" && !v.localStart && v.localPort) {
      form.setValue("localStart", v.localPort)
      form.setValue("localEnd", v.localPort)
      form.setValue("targetStart", v.targetPort)
      form.setValue("targetEnd", v.targetPort)
    } else if (next === "single" && !v.localPort && v.localStart) {
      form.setValue("localPort", v.localStart)
      form.setValue("targetPort", v.targetStart)
    }
    form.setValue("mode", next)
    form.clearErrors()
  }

  const onSubmit = async (values: RuleFormValues) => {
    setFormError(null)
    onPendingChange(true)
    try {
      let view: RuleView
      if (target.kind === "edit") {
        view = await api.rules.update(target.rule.id, formToRule(values, target.rule.id))
      } else {
        // The server generates an id when it is empty.
        view = await api.rules.create(formToRule(values, ""))
      }
      queryClient.setQueryData(qk.rule(view.id), view)
      void queryClient.invalidateQueries({ queryKey: qk.rules })
      void queryClient.invalidateQueries({ queryKey: qk.overview })
      toast.success(
        t(target.kind === "edit" ? "rules.toast.updated" : "rules.toast.created", {
          name: view.name || view.id,
        })
      )
      onSaved(view)
    } catch (error) {
      if (isApiError(error) && error.code === "validation" && error.field) {
        const mapped = serverFieldToForm(error.field, values)
        if (mapped) {
          const message = mapped.line
            ? t("rules.form.errors.serverLine", { line: mapped.line, message: error.message })
            : error.message
          form.setError(mapped.name, { message }, { shouldFocus: true })
          return
        }
      }
      setFormError(errorMessage(error, t))
    } finally {
      onPendingChange(false)
    }
  }

  const invalid = (name: keyof RuleFormValues) => (errors[name] ? true : undefined)

  return (
    <form
      id={FORM_ID}
      noValidate
      onSubmit={form.handleSubmit(onSubmit)}
      className="flex-1 overflow-y-auto p-4"
    >
      <FieldGroup>
        {formError && (
          <Alert variant="destructive">
            <CircleAlertIcon />
            <AlertDescription>{formError}</AlertDescription>
          </Alert>
        )}

        <FieldSet>
          <FieldLegend>{t("rules.form.sections.basic")}</FieldLegend>
          <FieldGroup>
            <Field data-invalid={invalid("name")}>
              <FieldLabel htmlFor="rule-name">{t("rules.form.name")}</FieldLabel>
              <Input
                id="rule-name"
                autoComplete="off"
                placeholder={t("rules.form.namePlaceholder")}
                aria-invalid={invalid("name")}
                {...form.register("name")}
              />
              <FieldError errors={[errors.name]} />
            </Field>
            <div className="grid gap-5 sm:grid-cols-2">
              <Field>
                <FieldLabel id="rule-type-label">{t("rules.form.type")}</FieldLabel>
                <Controller
                  control={form.control}
                  name="type"
                  render={({ field }) => (
                    <ToggleGroup
                      type="single"
                      variant="outline"
                      spacing={0}
                      aria-labelledby="rule-type-label"
                      value={field.value}
                      onValueChange={(v) => v && field.onChange(v)}
                    >
                      <ToggleGroupItem value="tcp" className="px-4 font-mono">
                        TCP
                      </ToggleGroupItem>
                      <ToggleGroupItem value="udp" className="px-4 font-mono">
                        UDP
                      </ToggleGroupItem>
                    </ToggleGroup>
                  )}
                />
              </Field>
              <Controller
                control={form.control}
                name="enabled"
                render={({ field }) => (
                  <Field orientation="horizontal">
                    <FieldContent>
                      <FieldLabel htmlFor="rule-enabled">{t("rules.form.enabled")}</FieldLabel>
                      <FieldDescription>{t("rules.form.enabledHint")}</FieldDescription>
                    </FieldContent>
                    <Switch
                      id="rule-enabled"
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </Field>
                )}
              />
            </div>
            <Field>
              <FieldLabel htmlFor="rule-remark">{t("rules.form.remark")}</FieldLabel>
              <Input
                id="rule-remark"
                autoComplete="off"
                placeholder={t("rules.form.remarkPlaceholder")}
                {...form.register("remark")}
              />
            </Field>
          </FieldGroup>
        </FieldSet>

        <FieldSeparator />

        <FieldSet>
          <FieldLegend>{t("rules.form.sections.mapping")}</FieldLegend>
          <FieldGroup>
            <Field>
              <FieldLabel id="rule-mode-label">{t("rules.form.mode")}</FieldLabel>
              <ToggleGroup
                type="single"
                variant="outline"
                spacing={0}
                aria-labelledby="rule-mode-label"
                value={mode}
                onValueChange={(v) => v && switchMode(v as "single" | "range")}
              >
                <ToggleGroupItem value="single" className="px-3">
                  {t("rules.form.single")}
                </ToggleGroupItem>
                <ToggleGroupItem value="range" className="px-3">
                  {t("rules.form.range")}
                </ToggleGroupItem>
              </ToggleGroup>
            </Field>

            <Field data-invalid={invalid("localHost")}>
              <FieldLabel htmlFor="rule-local-host">{t("rules.form.localHost")}</FieldLabel>
              <Input
                id="rule-local-host"
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
                aria-invalid={invalid("localHost")}
                {...form.register("localHost")}
              />
              <div className="flex flex-wrap gap-1.5">
                {HOST_PICKS.map((host) => (
                  <Button
                    key={host}
                    type="button"
                    variant="outline"
                    size="xs"
                    className="font-mono"
                    onClick={() =>
                      form.setValue("localHost", host, {
                        shouldDirty: true,
                        shouldValidate: form.formState.isSubmitted,
                      })
                    }
                  >
                    {host}
                  </Button>
                ))}
              </div>
              {errors.localHost ? (
                <FieldError errors={[errors.localHost]} />
              ) : (
                <FieldDescription>{t("rules.form.localHostHint")}</FieldDescription>
              )}
            </Field>

            {mode === "single" ? (
              <PortField
                id="rule-local-port"
                label={t("rules.form.localPort")}
                error={errors.localPort?.message}
                registration={form.register("localPort", {
                  onChange: () => mirror("localPort", "targetPort"),
                })}
              />
            ) : (
              <RangeFields
                label={t("rules.form.localRange")}
                idPrefix="rule-local"
                startError={errors.localStart?.message}
                endError={errors.localEnd?.message}
                start={form.register("localStart", {
                  onChange: () => {
                    mirror("localStart", "targetStart")
                    syncTargetEnd()
                  },
                })}
                end={form.register("localEnd", { onChange: syncTargetEnd })}
              />
            )}

            <Field data-invalid={invalid("targetHost")}>
              <FieldLabel htmlFor="rule-target-host">{t("rules.form.targetHost")}</FieldLabel>
              <Input
                id="rule-target-host"
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
                placeholder={t("rules.form.targetHostPlaceholder")}
                aria-invalid={invalid("targetHost")}
                {...form.register("targetHost")}
              />
              <FieldError errors={[errors.targetHost]} />
            </Field>

            {mode === "single" ? (
              <PortField
                id="rule-target-port"
                label={t("rules.form.targetPort")}
                error={errors.targetPort?.message}
                registration={form.register("targetPort")}
              />
            ) : (
              <RangeFields
                label={t("rules.form.targetRange")}
                idPrefix="rule-target"
                startError={errors.targetStart?.message}
                endError={errors.targetEnd?.message}
                start={form.register("targetStart", { onChange: syncTargetEnd })}
                end={form.register("targetEnd")}
                description={`${t("rules.form.rangeHint")} ${t("rules.form.targetEndAuto")}`}
              />
            )}
          </FieldGroup>
        </FieldSet>

        <FieldSeparator />

        <FieldSet>
          <FieldLegend>{t("rules.form.sections.access")}</FieldLegend>
          <FieldGroup>
            <Field>
              <FieldLabel id="rule-acl-label">{t("rules.form.acl.mode")}</FieldLabel>
              <Controller
                control={form.control}
                name="aclMode"
                render={({ field }) => (
                  <ToggleGroup
                    type="single"
                    variant="outline"
                    spacing={0}
                    aria-labelledby="rule-acl-label"
                    value={field.value}
                    onValueChange={(v) => v && field.onChange(v)}
                  >
                    <ToggleGroupItem value="none" className="px-3">
                      {t("rules.form.acl.none")}
                    </ToggleGroupItem>
                    <ToggleGroupItem value="allow" className="px-3">
                      {t("rules.form.acl.allow")}
                    </ToggleGroupItem>
                    <ToggleGroupItem value="deny" className="px-3">
                      {t("rules.form.acl.deny")}
                    </ToggleGroupItem>
                  </ToggleGroup>
                )}
              />
              <FieldDescription>
                {aclMode === "allow"
                  ? t("rules.form.acl.allowHint")
                  : aclMode === "deny"
                    ? t("rules.form.acl.denyHint")
                    : t("rules.form.acl.noneHint")}
              </FieldDescription>
            </Field>
            {aclMode !== "none" && (
              <Field data-invalid={invalid("cidrs")}>
                <FieldLabel htmlFor="rule-cidrs">{t("rules.form.acl.cidrs")}</FieldLabel>
                <Textarea
                  id="rule-cidrs"
                  rows={5}
                  spellCheck={false}
                  className="font-mono"
                  placeholder={t("rules.form.acl.cidrsPlaceholder")}
                  aria-invalid={invalid("cidrs")}
                  {...form.register("cidrs")}
                />
                {errors.cidrs ? (
                  <FieldError errors={[errors.cidrs]} />
                ) : (
                  <FieldDescription>{t("rules.form.acl.cidrsHint")}</FieldDescription>
                )}
              </Field>
            )}
          </FieldGroup>
        </FieldSet>

        <FieldSeparator />

        <FieldSet>
          <FieldLegend>{t("rules.form.sections.limits")}</FieldLegend>
          <FieldGroup>
            <div className="grid gap-5 sm:grid-cols-2">
              <NumberField
                id="rule-max-conns"
                label={t("rules.form.limits.maxConnections")}
                description={t("rules.form.limits.maxConnectionsHint")}
                placeholder={t("rules.form.limits.unlimited")}
                error={errors.maxConnections?.message}
                registration={form.register("maxConnections")}
              />
              <NumberField
                id="rule-max-per-ip"
                label={t("rules.form.limits.maxPerIp")}
                description={t("rules.form.limits.maxPerIpHint")}
                placeholder={t("rules.form.limits.unlimited")}
                error={errors.maxConnectionsPerIp?.message}
                registration={form.register("maxConnectionsPerIp")}
              />
            </div>
            <Field data-invalid={invalid("bandwidth")}>
              <FieldLabel htmlFor="rule-bandwidth">{t("rules.form.limits.bandwidth")}</FieldLabel>
              <div className="flex gap-2">
                <Input
                  id="rule-bandwidth"
                  inputMode="decimal"
                  autoComplete="off"
                  className="flex-1"
                  placeholder={t("rules.form.limits.unlimited")}
                  aria-invalid={invalid("bandwidth")}
                  {...form.register("bandwidth")}
                />
                <Controller
                  control={form.control}
                  name="bandwidthUnit"
                  render={({ field }) => (
                    <Select value={field.value} onValueChange={field.onChange}>
                      <SelectTrigger className="w-24" aria-label={t("rules.form.limits.unit")}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectGroup>
                          <SelectItem value="KB">KB/s</SelectItem>
                          <SelectItem value="MB">MB/s</SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                  )}
                />
              </div>
              {errors.bandwidth ? (
                <FieldError errors={[errors.bandwidth]} />
              ) : (
                <FieldDescription>{t("rules.form.limits.bandwidthHint")}</FieldDescription>
              )}
            </Field>
          </FieldGroup>
        </FieldSet>
      </FieldGroup>
    </form>
  )
}

function PortField({
  id,
  label,
  error,
  registration,
}: {
  id: string
  label: string
  error?: string
  registration: UseFormRegisterReturn
}) {
  return (
    <Field data-invalid={error ? true : undefined}>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Input
        id={id}
        inputMode="numeric"
        autoComplete="off"
        className="font-mono sm:max-w-40"
        placeholder="1–65535"
        aria-invalid={error ? true : undefined}
        {...registration}
      />
      {error && <FieldError>{error}</FieldError>}
    </Field>
  )
}

function RangeFields({
  label,
  idPrefix,
  start,
  end,
  startError,
  endError,
  description,
}: {
  label: string
  idPrefix: string
  start: UseFormRegisterReturn
  end: UseFormRegisterReturn
  startError?: string
  endError?: string
  description?: string
}) {
  const { t } = useI18n()
  const error = startError ?? endError
  return (
    <Field data-invalid={error ? true : undefined}>
      <FieldLabel htmlFor={`${idPrefix}-start`}>{label}</FieldLabel>
      <div className="flex items-center gap-2">
        <InputGroup className={cn("flex-1 sm:max-w-44")}>
          <InputGroupAddon>{t("rules.form.rangeStart")}</InputGroupAddon>
          <InputGroupInput
            id={`${idPrefix}-start`}
            inputMode="numeric"
            autoComplete="off"
            className="font-mono"
            aria-invalid={startError ? true : undefined}
            {...start}
          />
        </InputGroup>
        <ArrowRightIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <InputGroup className="flex-1 sm:max-w-44">
          <InputGroupAddon>{t("rules.form.rangeEnd")}</InputGroupAddon>
          <InputGroupInput
            id={`${idPrefix}-end`}
            inputMode="numeric"
            autoComplete="off"
            className="font-mono"
            aria-label={`${label} – ${t("rules.form.rangeEnd")}`}
            aria-invalid={endError ? true : undefined}
            {...end}
          />
        </InputGroup>
      </div>
      {error ? (
        <FieldError>{error}</FieldError>
      ) : (
        description && <FieldDescription>{description}</FieldDescription>
      )}
    </Field>
  )
}

function NumberField({
  id,
  label,
  description,
  placeholder,
  error,
  registration,
}: {
  id: string
  label: string
  description: string
  placeholder: string
  error?: string
  registration: UseFormRegisterReturn
}) {
  return (
    <Field data-invalid={error ? true : undefined}>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Input
        id={id}
        inputMode="numeric"
        autoComplete="off"
        placeholder={placeholder}
        aria-invalid={error ? true : undefined}
        {...registration}
      />
      {error ? (
        <FieldError>{error}</FieldError>
      ) : (
        <FieldDescription>{description}</FieldDescription>
      )}
    </Field>
  )
}
