import * as React from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"

import { QueryError } from "@/components/common/query-state"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
  CardFooter,
} from "@/components/ui/card"
import {
  Field,
  FieldGroup,
  FieldLabel,
  FieldDescription,
} from "@/components/ui/field"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { useI18n } from "@/i18n"
import { api } from "@/lib/api"
import { toastError } from "@/lib/errors"
import { useRules } from "@/lib/queries"
import type { NodeInfo, NodeConfig, RuleView, ClusterStatus } from "@/lib/types"

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
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle>{t("cluster.title")}</CardTitle>
          <CardDescription>{t("cluster.description")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div>
            <Badge variant={state.role === "active" ? "default" : "secondary"}>
              {t(`cluster.${state.role}`)}
            </Badge>
          </div>
          <dl className="grid grid-cols-2 gap-2">
            {(
              [
                [t("cluster.node"), state.nodeId || "—"],
                [t("cluster.owner"), state.owner || "—"],
                [t("cluster.address"), state.address || "—"],
                [
                  t("cluster.desired"),
                  `${state.epoch || 0}.${state.desiredRevision}`,
                ],
                [t("cluster.applied"), state.appliedRevision],
                ...(state.enabled
                  ? [
                      [t("cluster.writer"), state.writer || "—"],
                      [
                        t("cluster.configRole"),
                        t(`cluster.role_${state.configRole}`),
                      ],
                      [
                        t("cluster.replication"),
                        t(`cluster.sync_${state.replicationState}`),
                      ],
                      [t("cluster.peer"), t(`cluster.peer_${state.peerState}`)],
                      [t("cluster.epoch"), state.epoch],
                      [
                        t("cluster.peerVersion"),
                        `${state.peerEpoch}.${state.peerRevision}`,
                      ],
                      [
                        t("cluster.readyLocal"),
                        t(
                          state.localPreparedChecksum === state.checksum
                            ? "cluster.yes"
                            : "cluster.no"
                        ),
                      ],
                      [
                        t("cluster.readyPeer"),
                        t(
                          state.peerState === "online" &&
                            state.peerPreparedChecksum === state.checksum
                            ? "cluster.yes"
                            : "cluster.no"
                        ),
                      ],
                      [
                        t("cluster.upstream"),
                        t(`cluster.upstream_${state.upstreamState}`),
                      ],
                    ]
                  : []),
              ] as const
            ).map(([label, value]) => (
              <React.Fragment key={label}>
                <dt className="text-muted-foreground">{label}</dt>
                <dd className="font-mono break-all">{value}</dd>
              </React.Fragment>
            ))}
          </dl>
          {state.error && (
            <Alert variant="destructive">
              <AlertDescription>{state.error}</AlertDescription>
            </Alert>
          )}
          {state.syncError && (
            <Alert variant="destructive">
              <AlertDescription>{state.syncError}</AlertDescription>
            </Alert>
          )}
          {state.pendingUpdateId && (
            <p className="text-sm text-muted-foreground">
              {t("cluster.pending")}:{" "}
              <code className="break-all">{state.pendingUpdateId}</code>
            </p>
          )}
          {!state.enabled && (
            <Alert>
              <AlertDescription>{t("cluster.configure")}</AlertDescription>
            </Alert>
          )}
        </CardContent>
        {state.enabled && !state.paired && (
          <CardFooter className="flex flex-col items-start gap-3">
            <p className="text-sm text-muted-foreground">
              {t("cluster.initHint")}
            </p>
            <Button
              disabled={bootstrap.isPending || state.writer !== state.nodeId}
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
      {state.enabled && <ClusterActions state={state} />}
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

function ClusterActions({ state }: { state: ClusterStatus }) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [fenced, setFenced] = React.useState(false)
  const [archive, setArchive] = React.useState(false)
  const action = useMutation({
    mutationFn: (
      name: "transfer" | "promote" | "rejoin" | "retrySwitch" | "rearm"
    ) => api.cluster[name](),
    onSuccess: () => {
      setFenced(false)
      setArchive(false)
      void queryClient.invalidateQueries({ queryKey: ["cluster"] })
      void queryClient.invalidateQueries({ queryKey: ["rules"] })
      toast.success(t("cluster.actionDone"))
    },
    onError: (error) => toastError(error, t),
  })
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("cluster.actions")}</CardTitle>
        <CardDescription>{t("cluster.actionHint")}</CardDescription>
      </CardHeader>
      <CardContent>
        <FieldGroup>
          {state.takenOver && (
            <p className="text-sm text-muted-foreground">
              {t("cluster.rearmHint")}
            </p>
          )}
          <Field orientation="horizontal">
            <Checkbox
              id="confirm-fenced"
              checked={fenced}
              onCheckedChange={(value) => setFenced(value === true)}
              disabled={action.isPending}
            />
            <FieldLabel htmlFor="confirm-fenced">
              {t("cluster.fenced")}
            </FieldLabel>
          </Field>
          <Field orientation="horizontal">
            <Checkbox
              id="confirm-archive"
              checked={archive}
              onCheckedChange={(value) => setArchive(value === true)}
              disabled={action.isPending}
            />
            <FieldLabel htmlFor="confirm-archive">
              {t("cluster.archive")}
            </FieldLabel>
          </Field>
        </FieldGroup>
      </CardContent>
      <CardFooter className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          disabled={
            action.isPending ||
            !state.paired ||
            state.writer !== state.nodeId ||
            state.peerState !== "online" ||
            Boolean(state.pendingUpdateId)
          }
          onClick={() => action.mutate("transfer")}
        >
          {t("cluster.transfer")}
        </Button>
        <Button
          variant="destructive"
          disabled={action.isPending || !state.paired || !fenced}
          onClick={() => action.mutate("promote")}
        >
          {t("cluster.promote")}
        </Button>
        <Button
          variant="outline"
          disabled={
            action.isPending || !archive || state.peerState !== "online"
          }
          onClick={() => action.mutate("rejoin")}
        >
          {t("cluster.rejoin")}
        </Button>
        {state.upstreamState === "switch_pending" && (
          <Button
            disabled={action.isPending}
            onClick={() => action.mutate("retrySwitch")}
          >
            {t("cluster.retrySwitch")}
          </Button>
        )}
        {state.takenOver && (
          <Button
            variant="outline"
            disabled={action.isPending || state.replicationState !== "synced"}
            onClick={() => action.mutate("rearm")}
          >
            {t("cluster.rearm")}
          </Button>
        )}
        {action.isPending && <Spinner />}
      </CardFooter>
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
