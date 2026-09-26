import * as React from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  CircleAlertIcon,
  CircleCheckIcon,
  DownloadIcon,
  PackageCheckIcon,
  RefreshCwIcon,
  Trash2Icon,
} from "lucide-react"
import { toast } from "sonner"
import { z } from "zod"

import { QueryError } from "@/components/common/query-state"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
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
import { Progress } from "@/components/ui/progress"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Separator } from "@/components/ui/separator"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { InfoList } from "@/features/settings/info-list"
import { useSaveSection } from "@/features/settings/save-section"
import { useI18n, type Translate } from "@/i18n"
import { api } from "@/lib/api"
import { toastError } from "@/lib/errors"
import { qk, useSettings, useVersion } from "@/lib/queries"
import type { Settings, UpdateState, UpdateStatus } from "@/lib/types"

const BUSY_STATES = new Set<UpdateState>(["checking", "downloading", "applying"])

export function UpdateTab() {
  const settings = useSettings()
  return (
    <div className="flex flex-col gap-4">
      <UpdateStatusCard />
      {settings.isPending ? (
        <Skeleton className="h-96" />
      ) : settings.isError ? (
        <QueryError error={settings.error} onRetry={() => void settings.refetch()} />
      ) : (
        <UpdateSettingsCard value={settings.data.update} />
      )}
    </div>
  )
}

// ---- status ----

function StateBadge({ state }: { state: UpdateState }) {
  const { t } = useI18n()
  const label = t(`settings.update.states.${state}`)
  if (BUSY_STATES.has(state)) {
    return (
      <Badge variant="outline">
        <Spinner data-icon="inline-start" />
        {label}
      </Badge>
    )
  }
  if (state === "ready") {
    return (
      <Badge variant="success">
        <PackageCheckIcon data-icon="inline-start" />
        {label}
      </Badge>
    )
  }
  if (state === "failed") {
    return (
      <Badge variant="destructive">
        <CircleAlertIcon data-icon="inline-start" />
        {label}
      </Badge>
    )
  }
  return <Badge variant="secondary">{label}</Badge>
}

function UpdateStatusCard() {
  const { t, fmt } = useI18n()
  const queryClient = useQueryClient()
  const version = useVersion()
  // Set once an install was requested: failures after that mean "restarting".
  const [restarting, setRestarting] = React.useState(false)

  const status = useQuery({
    queryKey: qk.updateStatus,
    queryFn: api.update.status,
    retry: false,
    staleTime: 0,
    refetchInterval: (query) => {
      const state = query.state.data?.state
      return restarting || (state && BUSY_STATES.has(state)) ? 1500 : 5000
    },
  })

  // Reload once the server comes back running a different version.
  const firstVersion = React.useRef<string | null>(null)
  React.useEffect(() => {
    const current = status.data?.currentVersion
    if (!current) return
    if (firstVersion.current === null) firstVersion.current = current
    else if (firstVersion.current !== current) window.location.reload()
  }, [status.data?.currentVersion])

  const refresh = () => queryClient.invalidateQueries({ queryKey: qk.updateStatus })

  const check = useMutation({
    mutationFn: api.update.check,
    onSuccess: (res) => {
      if (res.hasUpdate && res.latestVersion) {
        toast.info(t("settings.update.available", { version: res.latestVersion }))
      } else {
        toast.success(t("settings.update.upToDate"))
      }
    },
    onError: (error) => toastError(error, t),
    onSettled: refresh,
  })

  const apply = useMutation({
    mutationFn: api.update.apply,
    onSuccess: () => {
      if (status.data?.state === "ready") setRestarting(true)
      toast.success(t("settings.update.started"))
    },
    onError: (error) => toastError(error, t),
    onSettled: refresh,
  })

  const dismiss = useMutation({
    mutationFn: api.update.dismiss,
    onSuccess: () => toast.success(t("settings.update.dismissed")),
    onError: (error) => toastError(error, t),
    onSettled: refresh,
  })

  const data = status.data
  const serverDown = status.isError && (restarting || data?.state === "applying")

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("settings.update.title")}</CardTitle>
        <CardDescription>{t("settings.update.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <InfoList
          rows={[
            {
              label: t("settings.update.version"),
              value: data?.currentVersion ?? version.data?.version ?? "–",
              mono: true,
            },
            { label: t("settings.update.commit"), value: version.data?.commit || "–", mono: true },
            {
              label: t("settings.update.buildTime"),
              value: version.data?.buildTime ? fmt.dateTime(version.data.buildTime) : "–",
            },
            {
              label: t("settings.update.channel"),
              value: version.data?.updateChannel || "–",
            },
            {
              label: t("settings.update.lastCheck"),
              value: data?.lastCheck ? (
                <span title={fmt.dateTime(data.lastCheck)}>{fmt.relative(data.lastCheck)}</span>
              ) : (
                t("common.states.never")
              ),
            },
          ]}
        />
        <Separator />
        {serverDown ? (
          <Alert>
            <Spinner />
            <AlertTitle>{t("settings.update.restarting")}</AlertTitle>
          </Alert>
        ) : status.isPending ? (
          <Skeleton className="h-16" />
        ) : !data ? (
          <QueryError
            error={status.error}
            title={t("settings.update.statusError")}
            onRetry={() => void status.refetch()}
          />
        ) : (
          <StatusDetails status={data} />
        )}
      </CardContent>
      <CardFooter className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          disabled={check.isPending || serverDown || (data && BUSY_STATES.has(data.state))}
          onClick={() => check.mutate()}
        >
          {check.isPending ? (
            <Spinner data-icon="inline-start" />
          ) : (
            <RefreshCwIcon data-icon="inline-start" />
          )}
          {check.isPending ? t("settings.update.checking") : t("settings.update.check")}
        </Button>
        {data?.state === "ready" ? (
          <>
            <Button disabled={apply.isPending || serverDown} onClick={() => apply.mutate()}>
              {apply.isPending ? (
                <Spinner data-icon="inline-start" />
              ) : (
                <PackageCheckIcon data-icon="inline-start" />
              )}
              {t("settings.update.apply")}
            </Button>
            <Button
              variant="ghost"
              disabled={dismiss.isPending || serverDown}
              onClick={() => dismiss.mutate()}
            >
              {dismiss.isPending ? (
                <Spinner data-icon="inline-start" />
              ) : (
                <Trash2Icon data-icon="inline-start" />
              )}
              {t("settings.update.dismiss")}
            </Button>
          </>
        ) : (
          data &&
          hasNewer(data) &&
          !BUSY_STATES.has(data.state) && (
            <Button disabled={apply.isPending || serverDown} onClick={() => apply.mutate()}>
              {apply.isPending ? (
                <Spinner data-icon="inline-start" />
              ) : (
                <DownloadIcon data-icon="inline-start" />
              )}
              {t("settings.update.updateNow")}
            </Button>
          )
        )}
      </CardFooter>
    </Card>
  )
}

function hasNewer(status: UpdateStatus) {
  return !!status.latestVersion && status.latestVersion !== status.currentVersion
}

function StatusDetails({ status }: { status: UpdateStatus }) {
  const { t } = useI18n()
  const percent = Math.round(status.downloadProgress ?? status.progress ?? 0)
  const showProgress =
    status.state === "downloading" ||
    (status.state === "applying" && status.progress !== undefined)

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <StateBadge state={status.state} />
        {hasNewer(status) ? (
          <span className="text-sm font-medium">
            {t("settings.update.available", { version: status.latestVersion ?? "" })}
          </span>
        ) : (
          status.lastCheck &&
          status.state === "idle" && (
            <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
              <CircleCheckIcon className="size-4 text-success" />
              {t("settings.update.upToDate")}
            </span>
          )
        )}
        {status.isPrerelease && hasNewer(status) && (
          <Badge variant="warning">{t("settings.update.prerelease")}</Badge>
        )}
      </div>

      {showProgress && (
        <div className="flex flex-col gap-1.5">
          <Progress value={percent} aria-label={t("settings.update.states.downloading")} />
          <span className="text-xs text-muted-foreground tabular-nums">
            {status.state === "downloading"
              ? t("settings.update.downloading", { percent })
              : t("settings.update.progress", { percent })}
          </span>
        </div>
      )}

      {status.state === "applying" && (
        <Alert>
          <Spinner />
          <AlertDescription>{t("settings.update.applying")}</AlertDescription>
        </Alert>
      )}

      {status.state === "ready" && (
        <Alert>
          <PackageCheckIcon />
          <AlertTitle>{t("settings.update.readyHint")}</AlertTitle>
          <AlertDescription>{t("settings.update.applyHint")}</AlertDescription>
        </Alert>
      )}

      {status.state === "failed" && (
        <Alert variant="destructive">
          <CircleAlertIcon />
          <AlertTitle>{t("settings.update.failed")}</AlertTitle>
          {status.error && <AlertDescription>{status.error}</AlertDescription>}
        </Alert>
      )}

      {status.releaseNotes && hasNewer(status) && (
        <div className="flex flex-col gap-2">
          <h3 className="text-sm font-medium">{t("settings.update.releaseNotes")}</h3>
          <pre className="max-h-64 overflow-auto rounded-lg bg-muted p-3 text-xs whitespace-pre-wrap">
            {status.releaseNotes}
          </pre>
        </div>
      )}
    </div>
  )
}

// ---- settings ----

type IntervalUnit = "minutes" | "hours"

function updateSchema(t: Translate) {
  return z
    .object({
      enabled: z.boolean(),
      channel: z.enum(["stable", "dev"]),
      interval: z.string(),
      intervalUnit: z.enum(["minutes", "hours"]),
      source: z.enum(["github", "proxy"]),
      proxyBaseUrl: z.string().trim(),
      repo: z
        .string()
        .trim()
        .regex(/^[\w.-]+\/[\w.-]+$/, t("settings.update.settings.repoInvalid")),
    })
    .superRefine((v, ctx) => {
      const n = Number(v.interval)
      if (!/^\d+$/.test(v.interval.trim()) || n < 1) {
        ctx.addIssue({
          code: "custom",
          path: ["interval"],
          message: t("settings.update.settings.intervalInvalid"),
        })
      }
      if (v.source === "proxy" && !/^https?:\/\/\S+$/i.test(v.proxyBaseUrl)) {
        ctx.addIssue({
          code: "custom",
          path: ["proxyBaseUrl"],
          message: t("settings.update.settings.proxyBaseUrlInvalid"),
        })
      }
    })
}
type UpdateValues = z.infer<ReturnType<typeof updateSchema>>

function toFormValues(value: Settings["update"]): UpdateValues {
  const seconds = value.checkInterval > 0 ? value.checkInterval : 3600
  const hours = seconds % 3600 === 0
  return {
    enabled: value.enabled,
    channel: value.channel === "dev" ? "dev" : "stable",
    interval: String(hours ? seconds / 3600 : Math.max(1, Math.round(seconds / 60))),
    intervalUnit: hours ? "hours" : "minutes",
    source: value.source === "proxy" ? "proxy" : "github",
    proxyBaseUrl: value.proxyBaseUrl ?? "",
    repo: value.repo ?? "",
  }
}

function UpdateSettingsCard({ value }: { value: Settings["update"] }) {
  const { t } = useI18n()
  const save = useSaveSection()
  const schema = React.useMemo(() => updateSchema(t), [t])
  const values = React.useMemo(() => toFormValues(value), [value])
  const form = useForm<UpdateValues>({
    resolver: zodResolver(schema),
    values,
    resetOptions: { keepDirtyValues: true },
  })
  const { errors, isSubmitting } = form.formState
  const source = useWatch({ control: form.control, name: "source" })

  const onSubmit = form.handleSubmit(async (v) => {
    const unit = v.intervalUnit === "hours" ? 3600 : 60
    const ok = await save(
      {
        update: {
          enabled: v.enabled,
          channel: v.channel,
          checkInterval: Number(v.interval) * unit,
          source: v.source,
          proxyBaseUrl: v.proxyBaseUrl,
          repo: v.repo,
        },
      },
      form,
      "update",
      { checkInterval: "interval" }
    )
    if (ok) form.reset(v)
  })

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("settings.update.settings.title")}</CardTitle>
        <CardDescription>{t("settings.update.settings.description")}</CardDescription>
      </CardHeader>
      <form onSubmit={onSubmit} noValidate className="contents">
        <CardContent>
          <FieldGroup>
            <Controller
              control={form.control}
              name="enabled"
              render={({ field }) => (
                <Field orientation="horizontal">
                  <FieldContent>
                    <FieldLabel htmlFor="update-enabled">
                      {t("settings.update.settings.enabled")}
                    </FieldLabel>
                    <FieldDescription>{t("settings.update.settings.enabledHint")}</FieldDescription>
                  </FieldContent>
                  <Switch
                    id="update-enabled"
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </Field>
              )}
            />
            <Controller
              control={form.control}
              name="channel"
              render={({ field }) => (
                <Field>
                  <FieldLabel>{t("settings.update.settings.channel")}</FieldLabel>
                  <ToggleGroup
                    type="single"
                    variant="outline"
                    value={field.value}
                    onValueChange={(v) => v && field.onChange(v)}
                    aria-label={t("settings.update.settings.channel")}
                  >
                    <ToggleGroupItem value="stable">
                      {t("settings.update.settings.channelStable")}
                    </ToggleGroupItem>
                    <ToggleGroupItem value="dev">
                      {t("settings.update.settings.channelDev")}
                    </ToggleGroupItem>
                  </ToggleGroup>
                  <FieldDescription>{t("settings.update.settings.channelHint")}</FieldDescription>
                </Field>
              )}
            />
            <Field data-invalid={!!errors.interval || undefined}>
              <FieldLabel htmlFor="update-interval">
                {t("settings.update.settings.interval")}
              </FieldLabel>
              <div className="flex gap-2">
                <Input
                  id="update-interval"
                  inputMode="numeric"
                  className="w-28"
                  aria-invalid={!!errors.interval}
                  {...form.register("interval")}
                />
                <Controller
                  control={form.control}
                  name="intervalUnit"
                  render={({ field }) => (
                    <Select
                      value={field.value}
                      onValueChange={(v) => field.onChange(v as IntervalUnit)}
                    >
                      <SelectTrigger className="w-32" aria-label={t("settings.update.settings.interval")}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectGroup>
                          <SelectItem value="minutes">
                            {t("settings.update.settings.intervalMinutes")}
                          </SelectItem>
                          <SelectItem value="hours">
                            {t("settings.update.settings.intervalHours")}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                  )}
                />
              </div>
              <FieldError errors={[errors.interval]} />
            </Field>
            <Controller
              control={form.control}
              name="source"
              render={({ field }) => (
                <Field>
                  <FieldLabel>{t("settings.update.settings.source")}</FieldLabel>
                  <ToggleGroup
                    type="single"
                    variant="outline"
                    value={field.value}
                    onValueChange={(v) => v && field.onChange(v)}
                    aria-label={t("settings.update.settings.source")}
                  >
                    <ToggleGroupItem value="github">
                      {t("settings.update.settings.sourceGithub")}
                    </ToggleGroupItem>
                    <ToggleGroupItem value="proxy">
                      {t("settings.update.settings.sourceProxy")}
                    </ToggleGroupItem>
                  </ToggleGroup>
                  <FieldDescription>{t("settings.update.settings.sourceHint")}</FieldDescription>
                </Field>
              )}
            />
            {source === "proxy" && (
              <Field data-invalid={!!errors.proxyBaseUrl || undefined}>
                <FieldLabel htmlFor="update-proxy">
                  {t("settings.update.settings.proxyBaseUrl")}
                </FieldLabel>
                <Input
                  id="update-proxy"
                  type="url"
                  className="font-mono"
                  spellCheck={false}
                  placeholder="https://"
                  aria-invalid={!!errors.proxyBaseUrl}
                  {...form.register("proxyBaseUrl")}
                />
                <FieldError errors={[errors.proxyBaseUrl]} />
              </Field>
            )}
            <Field data-invalid={!!errors.repo || undefined}>
              <FieldLabel htmlFor="update-repo">{t("settings.update.settings.repo")}</FieldLabel>
              <Input
                id="update-repo"
                className="font-mono"
                spellCheck={false}
                placeholder="lieyanc/FireGateway"
                aria-invalid={!!errors.repo}
                {...form.register("repo")}
              />
              {errors.repo ? (
                <FieldError errors={[errors.repo]} />
              ) : (
                <FieldDescription>{t("settings.update.settings.repoHint")}</FieldDescription>
              )}
            </Field>
          </FieldGroup>
        </CardContent>
        <CardFooter>
          <Button type="submit" disabled={isSubmitting}>
            {isSubmitting && <Spinner data-icon="inline-start" />}
            {isSubmitting ? t("common.actions.saving") : t("common.actions.save")}
          </Button>
        </CardFooter>
      </form>
    </Card>
  )
}
