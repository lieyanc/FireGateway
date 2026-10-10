import * as React from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ChevronDownIcon,
  CircleAlertIcon,
  CircleCheckIcon,
  TriangleAlertIcon,
} from "lucide-react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/common/confirm-dialog"
import { QueryError } from "@/components/common/query-state"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
  CardFooter,
} from "@/components/ui/card"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  Field,
  FieldGroup,
  FieldLabel,
  FieldDescription,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { useI18n, type MessageKey } from "@/i18n"
import { api } from "@/lib/api"
import { toastError } from "@/lib/errors"
import { useRules } from "@/lib/queries"
import type {
  ClusterIssue,
  ClusterStatus,
  ClusterSync,
  NodeConfig,
  NodeInfo,
  RuleView,
} from "@/lib/types"
import { cn } from "@/lib/utils"
import { ClusterConnectionSettings } from "@/features/settings/cluster-connection"

export function ClusterTab() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const status = useQuery({
    queryKey: ["cluster"],
    queryFn: api.cluster.status,
    refetchInterval: 2000,
  })
  const node = useQuery({ queryKey: ["node"], queryFn: api.node.get })
  const rules = useRules()
  const bootstrap = useMutation({
    mutationFn: api.cluster.bootstrap,
    onSuccess: () => {
      toast.success(t("cluster.initialized"))
      void queryClient.invalidateQueries({ queryKey: ["cluster"] })
    },
    onError: (error) => toastError(error, t),
  })
  if (status.isError)
    return (
      <QueryError error={status.error} onRetry={() => void status.refetch()} />
    )
  if (node.isError)
    return <QueryError error={node.error} onRetry={() => void node.refetch()} />
  if (rules.isError)
    return (
      <QueryError error={rules.error} onRetry={() => void rules.refetch()} />
    )
  if (!status.data || !node.data || !rules.data)
    return <Skeleton className="h-64" />
  const state = status.data
  const canPair = state.initialWriter === state.nodeId
  return (
    <div className="flex flex-col gap-4">
      <ClusterConnectionSettings />
      <Card>
        <CardHeader>
          <CardTitle>{t("cluster.title")}</CardTitle>
          <CardDescription>{t("cluster.description")}</CardDescription>
          <CardAction>
            {state.enabled ? (
              <SyncBadge sync={state.sync} />
            ) : (
              <Badge variant="secondary">{t("cluster.standalone")}</Badge>
            )}
          </CardAction>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {state.enabled ? (
            <>
              <NodeTiles state={state} />
              <RouterTarget state={state} />
              <Issues issues={state.issues ?? []} />
            </>
          ) : (
            <Alert>
              <AlertDescription>{t("cluster.configure")}</AlertDescription>
            </Alert>
          )}
          <Details state={state} />
        </CardContent>
        {state.enabled && !state.paired && (
          <CardFooter className="flex flex-col items-start gap-3">
            <p className="text-sm text-muted-foreground">
              {t("cluster.initHint", { node: state.initialWriter || "—" })}
            </p>
            <Button
              disabled={bootstrap.isPending || !canPair}
              onClick={() => bootstrap.mutate()}
            >
              {bootstrap.isPending && <Spinner data-icon="inline-start" />}
              {t(
                bootstrap.isPending
                  ? "cluster.initializing"
                  : "cluster.initialize"
              )}
            </Button>
          </CardFooter>
        )}
      </Card>
      {state.enabled && state.paired && <ClusterActions state={state} />}
      <OverrideForm
        key={JSON.stringify(node.data.node)}
        info={node.data}
        rules={rules.data}
      />
      <Alert>
        <AlertDescription>{t("cluster.forwarding")}</AlertDescription>
      </Alert>
    </div>
  )
}

const SYNC_VARIANT: Record<
  ClusterSync,
  "success" | "secondary" | "warning" | "destructive"
> = {
  synced: "success",
  syncing: "secondary",
  waiting: "warning",
  local_only: "warning",
  conflict: "destructive",
  unpaired: "secondary",
}

function SyncBadge({ sync }: { sync: ClusterSync }) {
  const { t } = useI18n()
  return (
    <Badge variant={SYNC_VARIANT[sync] ?? "secondary"}>
      {t("cluster.syncLabel")}: {t(`cluster.sync_${sync}`)}
    </Badge>
  )
}

type TileState =
  "serving" | "ready" | "notReady" | "offline" | "error" | "unknown"

const TILE_STATE_CLASS: Record<TileState, string> = {
  serving: "text-success",
  ready: "text-foreground",
  notReady: "text-warning",
  offline: "text-destructive",
  error: "text-destructive",
  unknown: "text-muted-foreground",
}

function NodeTiles({ state }: { state: ClusterStatus }) {
  const { t } = useI18n()
  const local: TileState = state.serving
    ? "serving"
    : state.ready
      ? "ready"
      : "notReady"
  const peer: TileState =
    state.peer.state === "offline"
      ? "offline"
      : state.peer.state === "error"
        ? "error"
        : state.peer.state !== "online"
          ? "unknown"
          : state.peer.serving
            ? "serving"
            : state.peer.ready
              ? "ready"
              : "notReady"
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <NodeTile
        label={t("cluster.thisNode")}
        id={state.nodeId}
        role={state.role === "standalone" ? undefined : state.role}
        state={local}
      />
      <NodeTile
        label={t("cluster.peerNode")}
        id={state.peerId}
        role={state.peer.role}
        state={peer}
      />
    </div>
  )
}

function NodeTile({
  label,
  id,
  role,
  state,
}: {
  label: string
  id?: string
  role?: "primary" | "backup"
  state: TileState
}) {
  const { t } = useI18n()
  return (
    <div
      className={cn(
        "flex flex-col gap-2 rounded-lg border p-3",
        state === "serving" &&
          "border-success bg-success/5 ring-1 ring-success/30"
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm text-muted-foreground">{label}</span>
        {role ? (
          <Badge variant={role === "primary" ? "default" : "secondary"}>
            {t(`cluster.${role}`)}
          </Badge>
        ) : (
          <Badge variant="outline">{t("cluster.roleUnknown")}</Badge>
        )}
      </div>
      <span className="font-mono font-medium break-all">{id || "—"}</span>
      <span className={cn("text-sm font-medium", TILE_STATE_CLASS[state])}>
        {t(`cluster.state_${state}`)}
      </span>
    </div>
  )
}

function RouterTarget({ state }: { state: ClusterStatus }) {
  const { t } = useI18n()
  const { ingress } = state
  let target: React.ReactNode
  if (ingress.state === "switching") target = t("cluster.routerSwitching")
  else if (ingress.state === "unavailable")
    target = (
      <span className="text-destructive">{t("cluster.routerUnavailable")}</span>
    )
  else if (ingress.state === "observed" && ingress.owner)
    target = (
      <span className="font-mono">
        {ingress.owner === state.nodeId
          ? `${t("cluster.thisNode")} (${ingress.owner})`
          : ingress.owner === state.peerId
            ? `${t("cluster.peerNode")} (${ingress.owner})`
            : ingress.owner}
      </span>
    )
  else if (ingress.state === "observed")
    target = (
      <span className="text-warning">{t("cluster.routerUnknownAddress")}</span>
    )
  else target = t("cluster.routerUnknown")
  return (
    <p className="text-sm">
      <span className="text-muted-foreground">{t("cluster.router")}: </span>
      {target}
      {ingress.address && (
        <span className="ml-2 font-mono text-muted-foreground">
          {ingress.address}
        </span>
      )}
    </p>
  )
}

const ISSUE_CODES = [
  "unpaired",
  "peer_offline",
  "peer_error",
  "sync_error",
  "conflict",
  "not_ready",
  "peer_not_ready",
  "router_unavailable",
  "ingress_error",
  "ingress_unknown",
  "ingress_elsewhere",
  "local_only",
  "read_only",
] as const
type IssueCode = (typeof ISSUE_CODES)[number]
const SEVERE = new Set<string>([
  "peer_offline",
  "peer_error",
  "sync_error",
  "conflict",
  "not_ready",
  "ingress_error",
])

function isKnownIssue(code: string): code is IssueCode {
  return (ISSUE_CODES as readonly string[]).includes(code)
}

function Issues({ issues }: { issues: ClusterIssue[] }) {
  const { t } = useI18n()
  if (!issues.length)
    return (
      <Alert>
        <CircleCheckIcon className="text-success" />
        <AlertTitle className="text-success">{t("cluster.healthy")}</AlertTitle>
        <AlertDescription>{t("cluster.healthyHint")}</AlertDescription>
      </Alert>
    )
  return (
    <div className="flex flex-col gap-2">
      <p className="text-sm font-medium">{t("cluster.issuesTitle")}</p>
      {issues.map((issue, index) => {
        const code = isKnownIssue(issue.code) ? issue.code : null
        const severe = SEVERE.has(issue.code)
        const title: MessageKey = code
          ? `cluster.issue_${code}`
          : "cluster.issue_unknown"
        return (
          <Alert
            key={`${issue.code}-${index}`}
            variant={severe ? "destructive" : "default"}
          >
            {severe ? (
              <CircleAlertIcon />
            ) : (
              <TriangleAlertIcon className="text-warning" />
            )}
            <AlertTitle>
              {t(title)}
              {!code && <span className="ml-1 font-mono">({issue.code})</span>}
            </AlertTitle>
            <AlertDescription>
              {code && <p>{t(`cluster.issueHint_${code}`)}</p>}
              {issue.message && (
                <p className="font-mono text-xs break-all">{issue.message}</p>
              )}
            </AlertDescription>
          </Alert>
        )
      })}
    </div>
  )
}

function Details({ state }: { state: ClusterStatus }) {
  const { t, fmt } = useI18n()
  const d = state.details
  const short = (value?: string) =>
    value ? <span title={value}>{value.slice(0, 12)}</span> : "—"
  const rows: [string, React.ReactNode, React.ReactNode][] = [
    [t("cluster.epoch"), d.epoch ?? "—", state.enabled ? d.peerEpoch : "—"],
    [
      t("cluster.revision"),
      d.revision ?? "—",
      state.enabled ? d.peerRevision : "—",
    ],
    [t("cluster.checksum"), short(d.checksum), short(d.peerChecksum)],
  ]
  return (
    <Collapsible>
      <CollapsibleTrigger asChild>
        <Button variant="ghost" size="sm" className="group w-fit">
          {t("cluster.details")}
          <ChevronDownIcon
            data-icon="inline-end"
            className="transition-transform group-data-[state=open]:rotate-180"
          />
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent className="pt-2">
        <dl className="grid grid-cols-[auto_1fr_1fr] gap-x-4 gap-y-1 text-sm">
          <span />
          <dt className="text-muted-foreground">{t("cluster.local")}</dt>
          <dt className="text-muted-foreground">{t("cluster.remote")}</dt>
          {rows.map(([label, local, peer]) => (
            <React.Fragment key={label}>
              <dt className="text-muted-foreground">{label}</dt>
              <dd className="font-mono break-all">{local}</dd>
              <dd className="font-mono break-all">{peer}</dd>
            </React.Fragment>
          ))}
        </dl>
        <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          {state.clusterId && (
            <>
              <dt className="text-muted-foreground">
                {t("cluster.clusterId")}
              </dt>
              <dd className="font-mono break-all">{state.clusterId}</dd>
            </>
          )}
          <dt className="text-muted-foreground">{t("cluster.pending")}</dt>
          <dd className="font-mono break-all">{d.pendingUpdateId || "—"}</dd>
          <dt className="text-muted-foreground">{t("cluster.lastSync")}</dt>
          <dd>{d.lastSync ? fmt.dateTime(d.lastSync) : "—"}</dd>
        </dl>
      </CollapsibleContent>
    </Collapsible>
  )
}

type Pending =
  | { kind: "switchover"; target: string; repoint: boolean }
  | { kind: "promote" }
  | { kind: "rejoin" }

function ClusterActions({ state }: { state: ClusterStatus }) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [pending, setPending] = React.useState<Pending | null>(null)
  const [open, setOpen] = React.useState(false)
  const ask = (next: Pending) => {
    setPending(next)
    setOpen(true)
  }
  const action = useMutation({
    mutationFn: (p: Pending) =>
      p.kind === "switchover"
        ? api.cluster.switchover(p.target)
        : api.cluster[p.kind](),
    onSuccess: (data) => {
      queryClient.setQueryData(["cluster"], data)
      void queryClient.invalidateQueries({ queryKey: ["cluster"] })
      void queryClient.invalidateQueries({ queryKey: ["rules"] })
      toast.success(t("cluster.actionDone"))
    },
    onError: (error) => toastError(error, t),
  })
  const primary = state.role === "primary"
  const peerOnline = state.peer.state === "online"
  // A primary that lost the router only re-points it at itself; any other
  // switch hands over the primary role and needs the peer online and in sync.
  const repoint = primary && !state.serving
  const handoverBlocked = !peerOnline
    ? t("cluster.switchNeedsPeer")
    : state.sync !== "synced"
      ? t("cluster.switchNeedsSync")
      : null
  const showLocal = !primary || !state.serving
  const showPeer = primary && Boolean(state.peerId)
  const localBlocked = repoint ? null : handoverBlocked
  const busy = action.isPending
  const dialog = (() => {
    if (!pending) return null
    if (pending.kind === "switchover")
      return pending.repoint
        ? {
            title: t("cluster.repointConfirmTitle"),
            description: t("cluster.repointConfirmDescription"),
            label: t("cluster.switchConfirm"),
            destructive: false,
          }
        : {
            title: t("cluster.switchConfirmTitle", { node: pending.target }),
            description: t("cluster.switchConfirmDescription", {
              node: pending.target,
            }),
            label: t("cluster.switchConfirm"),
            destructive: false,
          }
    return {
      title: t(`cluster.${pending.kind}ConfirmTitle`),
      description: t(`cluster.${pending.kind}ConfirmDescription`),
      label: t(`cluster.${pending.kind}`),
      destructive: true,
    }
  })()
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("cluster.actions")}</CardTitle>
        <CardDescription>{t("cluster.actionHint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center gap-2">
          {showLocal && (
            <Button
              disabled={busy || localBlocked !== null}
              onClick={() =>
                ask({ kind: "switchover", target: state.nodeId, repoint })
              }
            >
              {t("cluster.switchToLocal")}
            </Button>
          )}
          {showPeer && (
            <Button
              variant="outline"
              disabled={busy || handoverBlocked !== null}
              onClick={() =>
                ask({
                  kind: "switchover",
                  target: state.peerId ?? "",
                  repoint: false,
                })
              }
            >
              {t("cluster.switchToPeer")}
            </Button>
          )}
          {busy && <Spinner />}
        </div>
        {((showLocal && localBlocked) || (showPeer && handoverBlocked)) && (
          <p className="text-sm text-muted-foreground">
            {(showLocal && localBlocked) || handoverBlocked}
          </p>
        )}
        <Collapsible>
          <CollapsibleTrigger asChild>
            <Button variant="ghost" size="sm" className="group w-fit">
              {t("cluster.advanced")}
              <ChevronDownIcon
                data-icon="inline-end"
                className="transition-transform group-data-[state=open]:rotate-180"
              />
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="flex flex-col gap-3 pt-2">
            <div className="flex flex-wrap items-center gap-3">
              <Button
                variant="destructive"
                disabled={busy || peerOnline}
                onClick={() => ask({ kind: "promote" })}
              >
                {t("cluster.promote")}
              </Button>
              <span className="text-sm text-muted-foreground">
                {t("cluster.promoteHint")}
              </span>
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <Button
                variant="outline"
                disabled={busy || !peerOnline}
                onClick={() => ask({ kind: "rejoin" })}
              >
                {t("cluster.rejoin")}
              </Button>
              <span className="text-sm text-muted-foreground">
                {t("cluster.rejoinHint")}
              </span>
            </div>
          </CollapsibleContent>
        </Collapsible>
      </CardContent>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={dialog?.title}
        description={dialog?.description}
        confirmLabel={dialog?.label}
        destructive={dialog?.destructive}
        onConfirm={() => pending && action.mutateAsync(pending)}
      />
    </Card>
  )
}

type Draft = Record<
  string,
  {
    localHost: string
    targetHost: string
    localPort: string
    targetPort: string
  }
>
const empty = { localHost: "", targetHost: "", localPort: "", targetPort: "" }

function OverrideForm({ info, rules }: { info: NodeInfo; rules: RuleView[] }) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [draft, setDraft] = React.useState<Draft>(() =>
    Object.fromEntries(
      Object.entries(info.node.ruleOverrides ?? {}).map(([id, value]) => [
        id,
        {
          localHost: value.localHost ?? "",
          targetHost: value.targetHost ?? "",
          localPort: value.localPort?.toString() ?? "",
          targetPort: value.targetPort?.toString() ?? "",
        },
      ])
    )
  )
  const save = useMutation({
    mutationFn: api.node.update,
    onSuccess: (data) => {
      queryClient.setQueryData(["node"], data)
      void queryClient.invalidateQueries({ queryKey: ["cluster"] })
      void queryClient.invalidateQueries({ queryKey: ["rules"] })
      toast.success(t("cluster.saved"))
    },
    onError: (error) => toastError(error, t),
  })
  const invalid = Object.values(draft).some((d) =>
    [d.localPort, d.targetPort].some(
      (p) =>
        p !== "" && (!/^\d+$/.test(p) || Number(p) < 1 || Number(p) > 65535)
    )
  )
  function submit(event: React.FormEvent) {
    event.preventDefault()
    if (invalid) return
    const ruleOverrides: NodeConfig["ruleOverrides"] = {}
    for (const [id, value] of Object.entries(draft)) {
      const override = {
        ...(value.localHost.trim()
          ? { localHost: value.localHost.trim() }
          : {}),
        ...(value.targetHost.trim()
          ? { targetHost: value.targetHost.trim() }
          : {}),
        ...(value.localPort ? { localPort: Number(value.localPort) } : {}),
        ...(value.targetPort ? { targetPort: Number(value.targetPort) } : {}),
      }
      if (Object.keys(override).length) ruleOverrides[id] = override
    }
    save.mutate({ id: info.node.id, ruleOverrides })
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("cluster.overrides")}</CardTitle>
        <CardDescription>{t("cluster.overridesHint")}</CardDescription>
      </CardHeader>
      <form className="contents" onSubmit={submit}>
        <CardContent className="flex flex-col gap-5">
          <p className="text-sm text-muted-foreground">
            {t("cluster.localAddresses")}:{" "}
            <span className="font-mono">{info.addresses.join(", ")}</span>
          </p>
          {!rules.length && (
            <p className="text-sm text-muted-foreground">
              {t("cluster.noRules")}
            </p>
          )}
          {rules.map((rule) => {
            const value = draft[rule.id] ?? empty
            return (
              <FieldGroup key={rule.id}>
                <p className="font-medium">
                  {rule.name || rule.id}{" "}
                  <span className="text-muted-foreground">#{rule.id}</span>
                </p>
                <div className="grid gap-4 sm:grid-cols-2">
                  {(
                    [
                      "localHost",
                      "targetHost",
                      "localPort",
                      "targetPort",
                    ] as const
                  ).map((field) => {
                    const port = field.endsWith("Port")
                    const range = port && Boolean(rule.localPortRange)
                    const id = `override-${rule.id}-${field}`
                    const bad =
                      port &&
                      value[field] !== "" &&
                      (!/^\d+$/.test(value[field]) ||
                        Number(value[field]) < 1 ||
                        Number(value[field]) > 65535)
                    return (
                      <Field key={field} data-invalid={bad || undefined}>
                        <FieldLabel htmlFor={id}>
                          {t(`cluster.${field}`)}
                        </FieldLabel>
                        <Input
                          id={id}
                          value={value[field]}
                          disabled={range || save.isPending}
                          aria-invalid={bad}
                          inputMode={port ? "numeric" : "text"}
                          placeholder={String(
                            rule[field] ?? t("cluster.inherit")
                          )}
                          onChange={(event) =>
                            setDraft((d) => ({
                              ...d,
                              [rule.id]: {
                                ...(d[rule.id] ?? empty),
                                [field]: event.target.value,
                              },
                            }))
                          }
                        />
                        <FieldDescription>
                          {range
                            ? t("cluster.range")
                            : `${t("cluster.shared")}: ${rule[field] ?? "—"}`}
                        </FieldDescription>
                      </Field>
                    )
                  })}
                </div>
              </FieldGroup>
            )
          })}
          {invalid && (
            <Alert variant="destructive">
              <AlertDescription>{t("cluster.portInvalid")}</AlertDescription>
            </Alert>
          )}
        </CardContent>
        <CardFooter>
          <Button
            type="submit"
            disabled={save.isPending || invalid || !rules.length}
          >
            {save.isPending && <Spinner data-icon="inline-start" />}
            {t(
              save.isPending ? "common.actions.saving" : "common.actions.save"
            )}
          </Button>
        </CardFooter>
      </form>
    </Card>
  )
}
