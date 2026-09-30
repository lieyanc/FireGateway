import * as React from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"

import { QueryError } from "@/components/common/query-state"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
  FieldError,
  FieldSet,
  FieldLegend,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { setRestartRequired } from "@/features/settings/restart"
import { useI18n } from "@/i18n"
import { api, isApiError } from "@/lib/api"
import { toastError } from "@/lib/errors"
import type { ClusterConnection, ClusterConnectionInfo } from "@/lib/types"

const queryKey = ["cluster-connection"]
const defaults: ClusterConnection = {
  id: "",
  peerId: "",
  peerUrl: "",
  peerToken: "",
  peerCaFile: "",
  initialWriter: "",
  address: "",
  peerAddress: "",
  routerUrl: "",
  username: "",
  password: "",
  caFile: "",
  redirects: [],
  pollInterval: 2,
  failoverAfter: 10,
  allowHttpPeer: false,
}

export function ClusterConnectionSettings() {
  const connection = useQuery({
    queryKey,
    queryFn: api.cluster.connection,
    refetchOnWindowFocus: false,
  })
  React.useEffect(() => {
    if (connection.data?.restartRequired) setRestartRequired(true)
  }, [connection.data?.restartRequired])
  if (connection.isError)
    return (
      <QueryError
        error={connection.error}
        onRetry={() => void connection.refetch()}
      />
    )
  if (!connection.data) return <Skeleton className="h-48" />
  return (
    <ConnectionForm
      key={JSON.stringify(connection.data)}
      info={connection.data}
    />
  )
}

function ConnectionForm({ info }: { info: ClusterConnectionInfo }) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [enabled, setEnabled] = React.useState(info.cluster !== null)
  const [nodeId, setNodeId] = React.useState(info.nodeId)
  const [draft, setDraft] = React.useState({ ...defaults, ...info.cluster })
  const [redirects, setRedirects] = React.useState(draft.redirects.join(", "))
  const [errorField, setErrorField] = React.useState("")
  const [fieldMessage, setFieldMessage] = React.useState("")
  const payload = {
    nodeId: nodeId.trim(),
    cluster: enabled
      ? {
          ...draft,
          redirects: redirects.split(/[\s,]+/).filter(Boolean),
        }
      : null,
  }
  const dirty =
    JSON.stringify(payload) !==
    JSON.stringify({
      nodeId: info.nodeId,
      cluster: info.cluster ? { ...defaults, ...info.cluster } : null,
    })
  const save = useMutation({
    mutationFn: api.cluster.saveConnection,
    onSuccess: (saved) => {
      // Do not keep newly entered credentials in the form after saving.
      setDraft((current) => ({ ...current, peerToken: "", password: "" }))
      queryClient.setQueryData(queryKey, saved)
      if (saved.restartRequired) setRestartRequired(true)
      toast.success(
        t(saved.restartRequired ? "settings.savedRestart" : "connection.saved")
      )
    },
    onError: (error) => {
      if (isApiError(error)) {
        setErrorField(error.field ?? "")
        setFieldMessage(error.message)
      }
      toastError(error, t)
    },
  })
  const test = useMutation({
    mutationFn: api.cluster.testPeer,
    onSuccess: () => toast.success(t("connection.testSuccess")),
    onError: (error) => toastError(error, t),
  })
  const busy = save.isPending || test.isPending
  type TextKey = Exclude<
    keyof ClusterConnection,
    "redirects" | "allowHttpPeer" | "pollInterval" | "failoverAfter"
  >
  const textField = (
    key: TextKey,
    options: {
      placeholder?: string
      secret?: boolean
      configured?: boolean
      optional?: boolean
      locked?: boolean
      hint?: string
    } = {}
  ) => {
    const id = `connection-${key}`
    const invalid = errorField === `cluster.${key}`
    const disabled = busy || (options.locked && info.identityLocked)
    return (
      <Field data-invalid={invalid} data-disabled={disabled}>
        <FieldLabel htmlFor={id}>{t(`connection.${key}`)}</FieldLabel>
        <Input
          id={id}
          value={draft[key] ?? ""}
          type={options.secret ? "password" : "text"}
          autoComplete={options.secret ? "new-password" : "off"}
          spellCheck={false}
          disabled={disabled}
          aria-invalid={invalid}
          required={!options.optional && !options.configured}
          minLength={key === "peerToken" ? 32 : undefined}
          placeholder={
            options.configured
              ? t("connection.keepSecret")
              : options.placeholder
          }
          onChange={(event) => {
            const value = event.target.value
            setDraft((current) => ({
              ...current,
              [key]: options.secret ? value : value.trim(),
            }))
            setErrorField("")
          }}
        />
        {options.hint && <FieldDescription>{options.hint}</FieldDescription>}
        {invalid && <FieldError>{fieldMessage}</FieldError>}
      </Field>
    )
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("connection.title")}</CardTitle>
        <CardDescription>{t("connection.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          id="cluster-connection-form"
          onSubmit={(event) => {
            event.preventDefault()
            setErrorField("")
            save.mutate(payload)
          }}
        >
          <FieldGroup>
            {info.restartRequired && (
              <Alert>
                <AlertDescription>{t("connection.pending")}</AlertDescription>
              </Alert>
            )}
            <Field
              orientation="horizontal"
              data-disabled={busy || info.identityLocked}
            >
              <Switch
                id="connection-enabled"
                checked={enabled}
                disabled={busy || info.identityLocked}
                onCheckedChange={setEnabled}
              />
              <FieldLabel htmlFor="connection-enabled">
                {t("connection.enabled")}
              </FieldLabel>
            </Field>
            {enabled && (
              <>
                <FieldSet>
                  <FieldLegend>{t("connection.identity")}</FieldLegend>
                  <FieldDescription>
                    {t(
                      info.identityLocked
                        ? "connection.locked"
                        : "connection.identityHint"
                    )}
                  </FieldDescription>
                  <FieldGroup className="grid gap-4 sm:grid-cols-2">
                    <Field
                      data-disabled={busy || info.identityLocked}
                      data-invalid={errorField === "node.id"}
                    >
                      <FieldLabel htmlFor="connection-nodeId">
                        {t("connection.nodeId")}
                      </FieldLabel>
                      <Input
                        id="connection-nodeId"
                        value={nodeId}
                        required
                        placeholder="gateway-a"
                        disabled={busy || info.identityLocked}
                        aria-invalid={errorField === "node.id"}
                        onChange={(event) => {
                          setNodeId(event.target.value.trim())
                          setErrorField("")
                        }}
                      />
                      {errorField === "node.id" && (
                        <FieldError>{fieldMessage}</FieldError>
                      )}
                    </Field>
                    {textField("id", { placeholder: "dmz", locked: true })}
                    {textField("peerId", {
                      placeholder: "gateway-b",
                      locked: true,
                    })}
                    {textField("initialWriter", {
                      placeholder: "gateway-a",
                      locked: true,
                      hint: t("connection.writerHint"),
                    })}
                  </FieldGroup>
                </FieldSet>
                <FieldSet>
                  <FieldLegend>{t("connection.peer")}</FieldLegend>
                  <FieldDescription>
                    {t("connection.peerHint")}
                  </FieldDescription>
                  <FieldGroup className="grid gap-4 sm:grid-cols-2">
                    {textField("address", { placeholder: "192.168.1.10" })}
                    {textField("peerAddress", { placeholder: "192.168.1.11" })}
                    {textField("peerUrl", {
                      placeholder: "https://gateway-b:9090",
                    })}
                    {textField("peerToken", {
                      secret: true,
                      configured: info.peerTokenConfigured,
                      hint: t("connection.tokenHint"),
                    })}
                    {textField("peerCaFile", {
                      optional: true,
                      hint: t("connection.caHint"),
                    })}
                    <Field orientation="horizontal" data-disabled={busy}>
                      <Switch
                        id="connection-http"
                        checked={draft.allowHttpPeer}
                        disabled={busy}
                        onCheckedChange={(allowHttpPeer) =>
                          setDraft((current) => ({ ...current, allowHttpPeer }))
                        }
                      />
                      <FieldLabel htmlFor="connection-http">
                        {t("connection.allowHttpPeer")}
                      </FieldLabel>
                    </Field>
                  </FieldGroup>
                </FieldSet>
                <FieldSet>
                  <FieldLegend>{t("connection.upstream")}</FieldLegend>
                  <FieldDescription>
                    {t("connection.upstreamHint")}
                  </FieldDescription>
                  <FieldGroup className="grid gap-4 sm:grid-cols-2">
                    {textField("routerUrl", {
                      placeholder: "https://192.168.1.1/ubus",
                    })}
                    {textField("username")}
                    {textField("password", {
                      secret: true,
                      configured: info.routerPasswordConfigured,
                    })}
                    {textField("caFile", {
                      optional: true,
                      hint: t("connection.caHint"),
                    })}
                    <Field
                      data-disabled={busy}
                      data-invalid={errorField === "cluster.redirects"}
                    >
                      <FieldLabel htmlFor="connection-redirects">
                        {t("connection.redirects")}
                      </FieldLabel>
                      <Input
                        id="connection-redirects"
                        value={redirects}
                        required
                        disabled={busy}
                        aria-invalid={errorField === "cluster.redirects"}
                        placeholder="dmz"
                        onChange={(event) => {
                          setRedirects(event.target.value)
                          setErrorField("")
                        }}
                      />
                      <FieldDescription>
                        {t("connection.redirectsHint")}
                      </FieldDescription>
                      {errorField === "cluster.redirects" && (
                        <FieldError>{fieldMessage}</FieldError>
                      )}
                    </Field>
                    {(["pollInterval", "failoverAfter"] as const).map((key) => (
                      <Field
                        key={key}
                        data-disabled={busy}
                        data-invalid={errorField === `cluster.${key}`}
                      >
                        <FieldLabel htmlFor={`connection-${key}`}>
                          {t(`connection.${key}`)}
                        </FieldLabel>
                        <Input
                          id={`connection-${key}`}
                          type="number"
                          required
                          step={1}
                          min={
                            key === "pollInterval"
                              ? 1
                              : Math.max(10, 3 * draft.pollInterval)
                          }
                          max={key === "pollInterval" ? 5 : 300}
                          value={draft[key]}
                          disabled={busy}
                          aria-invalid={errorField === `cluster.${key}`}
                          onChange={(event) => {
                            setDraft((current) => ({
                              ...current,
                              [key]: Number(event.target.value),
                            }))
                            setErrorField("")
                          }}
                        />
                        {errorField === `cluster.${key}` && (
                          <FieldError>{fieldMessage}</FieldError>
                        )}
                      </Field>
                    ))}
                  </FieldGroup>
                </FieldSet>
              </>
            )}
          </FieldGroup>
        </form>
      </CardContent>
      <CardFooter className="flex flex-wrap gap-2">
        <Button
          form="cluster-connection-form"
          type="submit"
          disabled={busy || !dirty}
        >
          {save.isPending && <Spinner data-icon="inline-start" />}
          {t("connection.save")}
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={busy || dirty || !info.cluster}
          onClick={() => test.mutate()}
        >
          {test.isPending && <Spinner data-icon="inline-start" />}
          {t("connection.test")}
        </Button>
        <p className="w-full text-sm text-muted-foreground">
          {t("connection.testHint")}
        </p>
      </CardFooter>
    </Card>
  )
}
