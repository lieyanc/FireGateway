import * as React from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation } from "@tanstack/react-query"
import { FileDownIcon } from "lucide-react"
import { toast } from "sonner"
import { z } from "zod"

import { QueryError } from "@/components/common/query-state"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
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
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
  InputGroupText,
} from "@/components/ui/input-group"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { useSaveSection } from "@/features/settings/save-section"
import { useI18n, type Translate } from "@/i18n"
import { api } from "@/lib/api"
import { toastError } from "@/lib/errors"
import { useLogLevel, useSettings } from "@/lib/queries"
import type { Settings } from "@/lib/types"

const MB = 1024 * 1024
const DEFAULT_LEVELS = ["error", "warn", "info", "debug", "trace"]
const KNOWN_LEVELS = new Set(DEFAULT_LEVELS)

const isInt = (v: string) => /^\d+$/.test(v.trim())

export function SystemTab() {
  const settings = useSettings()

  if (settings.isPending) {
    return (
      <div className="flex flex-col gap-4">
        {Array.from({ length: 3 }, (_, i) => (
          <Skeleton key={i} className="h-56" />
        ))}
      </div>
    )
  }
  if (settings.isError) {
    return <QueryError error={settings.error} onRetry={() => void settings.refetch()} />
  }

  return (
    <div className="flex flex-col gap-4">
      <ApiSection value={settings.data.api} />
      <LoggingSection value={settings.data.logging} />
      <DataDirSection value={settings.data.dataDir} />
      <ReloadConfigCard />
    </div>
  )
}

function SectionCard({
  title,
  description,
  badge,
  pending,
  onSubmit,
  children,
}: {
  title: string
  description: string
  badge?: React.ReactNode
  pending: boolean
  onSubmit: React.FormEventHandler<HTMLFormElement>
  children: React.ReactNode
}) {
  const { t } = useI18n()
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription>{description}</CardDescription>
        {badge && <CardAction>{badge}</CardAction>}
      </CardHeader>
      <form onSubmit={onSubmit} noValidate className="contents">
        <CardContent>{children}</CardContent>
        <CardFooter>
          <Button type="submit" disabled={pending}>
            {pending && <Spinner data-icon="inline-start" />}
            {pending ? t("common.actions.saving") : t("common.actions.save")}
          </Button>
        </CardFooter>
      </form>
    </Card>
  )
}

function RestartBadge() {
  const { t } = useI18n()
  return <Badge variant="outline">{t("settings.restartRequired")}</Badge>
}

// ---- api ----

function apiSchema(t: Translate) {
  return z.object({
    host: z.string().trim().min(1, t("settings.system.api.hostRequired")),
    port: z
      .string()
      .refine(
        (v) => isInt(v) && Number(v) >= 1 && Number(v) <= 65535,
        t("settings.system.api.portInvalid")
      ),
    enableCors: z.boolean(),
  })
}
type ApiValues = z.infer<ReturnType<typeof apiSchema>>

function ApiSection({ value }: { value: Settings["api"] }) {
  const { t } = useI18n()
  const save = useSaveSection()
  const schema = React.useMemo(() => apiSchema(t), [t])
  const values = React.useMemo<ApiValues>(
    () => ({ host: value.host, port: String(value.port), enableCors: value.enableCors }),
    [value]
  )
  const form = useForm<ApiValues>({
    resolver: zodResolver(schema),
    values,
    resetOptions: { keepDirtyValues: true },
  })
  const { errors, isSubmitting } = form.formState

  const onSubmit = form.handleSubmit(async (v) => {
    const ok = await save(
      { api: { host: v.host.trim(), port: Number(v.port), enableCors: v.enableCors } },
      form,
      "api"
    )
    if (ok) form.reset(v)
  })

  return (
    <SectionCard
      title={t("settings.system.api.title")}
      description={t("settings.system.api.description")}
      badge={<RestartBadge />}
      pending={isSubmitting}
      onSubmit={onSubmit}
    >
      <FieldGroup>
        <div className="grid gap-5 sm:grid-cols-[1fr_10rem]">
          <Field data-invalid={!!errors.host || undefined}>
            <FieldLabel htmlFor="api-host">{t("settings.system.api.host")}</FieldLabel>
            <Input
              id="api-host"
              className="font-mono"
              spellCheck={false}
              aria-invalid={!!errors.host}
              {...form.register("host")}
            />
            {errors.host ? (
              <FieldError errors={[errors.host]} />
            ) : (
              <FieldDescription>{t("settings.system.api.hostHint")}</FieldDescription>
            )}
          </Field>
          <Field data-invalid={!!errors.port || undefined}>
            <FieldLabel htmlFor="api-port">{t("settings.system.api.port")}</FieldLabel>
            <Input
              id="api-port"
              inputMode="numeric"
              className="font-mono"
              aria-invalid={!!errors.port}
              {...form.register("port")}
            />
            <FieldError errors={[errors.port]} />
          </Field>
        </div>
        <Controller
          control={form.control}
          name="enableCors"
          render={({ field }) => (
            <Field orientation="horizontal">
              <FieldContent>
                <FieldLabel htmlFor="api-cors">{t("settings.system.api.enableCors")}</FieldLabel>
                <FieldDescription>{t("settings.system.api.enableCorsHint")}</FieldDescription>
              </FieldContent>
              <Switch
                id="api-cors"
                checked={field.value}
                onCheckedChange={field.onChange}
              />
            </Field>
          )}
        />
      </FieldGroup>
    </SectionCard>
  )
}

// ---- logging ----

function loggingSchema(t: Translate) {
  return z.object({
    level: z.string().min(1),
    enableConsole: z.boolean(),
    enableFile: z.boolean(),
    logDir: z.string().trim().min(1, t("settings.system.logging.logDirRequired")),
    maxFileSizeMb: z
      .string()
      .refine(
        (v) => /^\d+(\.\d+)?$/.test(v.trim()) && Number(v) > 0,
        t("settings.system.logging.maxFileSizeInvalid")
      ),
    maxFiles: z
      .string()
      .refine((v) => isInt(v) && Number(v) >= 1, t("settings.system.logging.maxFilesInvalid")),
  })
}
type LoggingValues = z.infer<ReturnType<typeof loggingSchema>>

function formatMb(bytes: number) {
  const mb = bytes / MB
  return String(Number.isInteger(mb) ? mb : Number(mb.toFixed(2)))
}

function levelLabel(t: Translate, level: string) {
  const key = level.toLowerCase()
  return KNOWN_LEVELS.has(key)
    ? t(`settings.system.levels.${key as "error" | "warn" | "info" | "debug" | "trace"}`)
    : level
}

function LoggingSection({ value }: { value: Settings["logging"] }) {
  const { t } = useI18n()
  const save = useSaveSection()
  const logLevel = useLogLevel()
  const schema = React.useMemo(() => loggingSchema(t), [t])
  const values = React.useMemo<LoggingValues>(
    () => ({
      level: value.level,
      enableConsole: value.enableConsole,
      enableFile: value.enableFile,
      logDir: value.logDir,
      maxFileSizeMb: formatMb(value.maxFileSize),
      maxFiles: String(value.maxFiles),
    }),
    [value]
  )
  const form = useForm<LoggingValues>({
    resolver: zodResolver(schema),
    values,
    resetOptions: { keepDirtyValues: true },
  })
  const { errors, isSubmitting } = form.formState

  const level = useWatch({ control: form.control, name: "level" })
  const levels = React.useMemo(() => {
    const list = logLevel.data?.levels?.length ? logLevel.data.levels : DEFAULT_LEVELS
    return list.includes(level) || !level ? list : [...list, level]
  }, [logLevel.data, level])

  const onSubmit = form.handleSubmit(async (v) => {
    const ok = await save(
      {
        logging: {
          level: v.level,
          enableConsole: v.enableConsole,
          enableFile: v.enableFile,
          logDir: v.logDir.trim(),
          maxFileSize: Math.round(Number(v.maxFileSizeMb) * MB),
          maxFiles: Number(v.maxFiles),
        },
      },
      form,
      "logging",
      { maxFileSize: "maxFileSizeMb" }
    )
    if (ok) form.reset(v)
  })

  return (
    <SectionCard
      title={t("settings.system.logging.title")}
      description={t("settings.system.logging.description")}
      badge={<RestartBadge />}
      pending={isSubmitting}
      onSubmit={onSubmit}
    >
      <FieldGroup>
        <Controller
          control={form.control}
          name="level"
          render={({ field }) => (
            <Field>
              <FieldLabel htmlFor="log-level">{t("settings.system.logging.level")}</FieldLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <SelectTrigger id="log-level" className="w-full sm:w-48">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {levels.map((l) => (
                      <SelectItem key={l} value={l}>
                        {levelLabel(t, l)}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <FieldDescription>{t("settings.system.logging.levelHint")}</FieldDescription>
            </Field>
          )}
        />
        <Controller
          control={form.control}
          name="enableConsole"
          render={({ field }) => (
            <Field orientation="horizontal">
              <FieldLabel htmlFor="log-console">
                {t("settings.system.logging.enableConsole")}
              </FieldLabel>
              <Switch id="log-console" checked={field.value} onCheckedChange={field.onChange} />
            </Field>
          )}
        />
        <Controller
          control={form.control}
          name="enableFile"
          render={({ field }) => (
            <Field orientation="horizontal">
              <FieldLabel htmlFor="log-file">{t("settings.system.logging.enableFile")}</FieldLabel>
              <Switch id="log-file" checked={field.value} onCheckedChange={field.onChange} />
            </Field>
          )}
        />
        <Field data-invalid={!!errors.logDir || undefined}>
          <FieldLabel htmlFor="log-dir">{t("settings.system.logging.logDir")}</FieldLabel>
          <Input
            id="log-dir"
            className="font-mono"
            spellCheck={false}
            aria-invalid={!!errors.logDir}
            {...form.register("logDir")}
          />
          <FieldError errors={[errors.logDir]} />
        </Field>
        <div className="grid gap-5 sm:grid-cols-2">
          <Field data-invalid={!!errors.maxFileSizeMb || undefined}>
            <FieldLabel htmlFor="log-size">{t("settings.system.logging.maxFileSize")}</FieldLabel>
            <InputGroup>
              <InputGroupInput
                id="log-size"
                inputMode="decimal"
                aria-invalid={!!errors.maxFileSizeMb}
                {...form.register("maxFileSizeMb")}
              />
              <InputGroupAddon align="inline-end">
                <InputGroupText>MB</InputGroupText>
              </InputGroupAddon>
            </InputGroup>
            {errors.maxFileSizeMb ? (
              <FieldError errors={[errors.maxFileSizeMb]} />
            ) : (
              <FieldDescription>{t("settings.system.logging.maxFileSizeHint")}</FieldDescription>
            )}
          </Field>
          <Field data-invalid={!!errors.maxFiles || undefined}>
            <FieldLabel htmlFor="log-files">{t("settings.system.logging.maxFiles")}</FieldLabel>
            <Input
              id="log-files"
              inputMode="numeric"
              aria-invalid={!!errors.maxFiles}
              {...form.register("maxFiles")}
            />
            {errors.maxFiles ? (
              <FieldError errors={[errors.maxFiles]} />
            ) : (
              <FieldDescription>{t("settings.system.logging.maxFilesHint")}</FieldDescription>
            )}
          </Field>
        </div>
      </FieldGroup>
    </SectionCard>
  )
}

// ---- dataDir ----

function DataDirSection({ value }: { value: string }) {
  const { t } = useI18n()
  const save = useSaveSection()
  const schema = React.useMemo(
    () => z.object({ dataDir: z.string().trim().min(1, t("settings.system.dataDir.required")) }),
    [t]
  )
  type Values = z.infer<typeof schema>
  const values = React.useMemo(() => ({ dataDir: value }), [value])
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    values,
    resetOptions: { keepDirtyValues: true },
  })
  const { errors, isSubmitting } = form.formState

  const onSubmit = form.handleSubmit(async (v) => {
    const ok = await save({ dataDir: v.dataDir.trim() }, form, "dataDir")
    if (ok) form.reset(v)
  })

  return (
    <SectionCard
      title={t("settings.system.dataDir.title")}
      description={t("settings.system.dataDir.description")}
      badge={<RestartBadge />}
      pending={isSubmitting}
      onSubmit={onSubmit}
    >
      <FieldGroup>
        <Field data-invalid={!!errors.dataDir || undefined}>
          <FieldLabel htmlFor="data-dir">{t("settings.system.dataDir.label")}</FieldLabel>
          <Input
            id="data-dir"
            className="font-mono"
            spellCheck={false}
            aria-invalid={!!errors.dataDir}
            {...form.register("dataDir")}
          />
          <FieldError errors={[errors.dataDir]} />
        </Field>
      </FieldGroup>
    </SectionCard>
  )
}

// ---- reload ----

function ReloadConfigCard() {
  const { t } = useI18n()
  const reload = useMutation({
    mutationFn: api.system.reloadConfig,
    onSuccess: (res) =>
      toast.success(t("settings.system.reload.success"), {
        description: t("settings.system.reload.summary", {
          added: res.added,
          updated: res.updated,
          removed: res.removed,
        }),
      }),
    onError: (error) => toastError(error, t),
  })

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("settings.system.reload.title")}</CardTitle>
        <CardDescription>{t("settings.system.reload.description")}</CardDescription>
      </CardHeader>
      <CardFooter>
        <Button variant="outline" disabled={reload.isPending} onClick={() => reload.mutate()}>
          {reload.isPending ? (
            <Spinner data-icon="inline-start" />
          ) : (
            <FileDownIcon data-icon="inline-start" />
          )}
          {t("settings.system.reload.action")}
        </Button>
      </CardFooter>
    </Card>
  )
}
