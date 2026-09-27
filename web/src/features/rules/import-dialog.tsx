import * as React from "react"
import { useQueryClient } from "@tanstack/react-query"
import { InfoIcon, TriangleAlertIcon } from "lucide-react"
import { toast } from "sonner"

import { RuleTypeBadge } from "@/components/common/rule-badges"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
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
  FieldLegend,
  FieldSet,
  FieldTitle,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { formatListen, formatTarget } from "@/features/rules/utils"
import { useI18n } from "@/i18n"
import { api } from "@/lib/api"
import { errorMessage } from "@/lib/errors"
import { qk, useRules } from "@/lib/queries"
import type {
  ImportFormat,
  ImportMode,
  ImportPreview,
  ImportWarning,
  Rule,
} from "@/lib/types"

type Source = "file" | "paste" | "rinetd"

const SOURCES: Source[] = ["file", "paste", "rinetd"]
const FORMATS: ImportFormat[] = ["auto", "json", "rinetd"]

/** A parsed preview plus a label for where it came from. */
type Loaded = { preview: ImportPreview; source: string }

export function ImportRulesDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const { data: existing } = useRules()
  const [tab, setTab] = React.useState<Source>("file")
  const [text, setText] = React.useState("")
  const [format, setFormat] = React.useState<ImportFormat>("auto")
  const [loaded, setLoaded] = React.useState<Loaded | null>(null)
  const [loading, setLoading] = React.useState(false)
  const [loadError, setLoadError] = React.useState<string | null>(null)
  const [mode, setMode] = React.useState<ImportMode>("merge")
  const [keepIds, setKeepIds] = React.useState(true)
  const [asDisabled, setAsDisabled] = React.useState(false)
  const [submitError, setSubmitError] = React.useState<string | null>(null)
  const [pending, setPending] = React.useState(false)
  // Ignores responses to requests superseded by a newer one or a reset.
  const requestSeq = React.useRef(0)

  const clearPreview = () => {
    requestSeq.current++
    setLoaded(null)
    setLoading(false)
    setLoadError(null)
    setSubmitError(null)
  }

  const reset = () => {
    clearPreview()
    setTab("file")
    setText("")
    setFormat("auto")
    setMode("merge")
    setKeepIds(true)
    setAsDisabled(false)
  }

  const load = async (
    request: () => Promise<ImportPreview>,
    source: (p: ImportPreview) => string
  ) => {
    clearPreview()
    const seq = requestSeq.current
    setLoading(true)
    try {
      const preview = await request()
      if (seq === requestSeq.current)
        setLoaded({ preview, source: source(preview) })
    } catch (error) {
      if (seq === requestSeq.current) setLoadError(errorMessage(error, t))
    } finally {
      if (seq === requestSeq.current) setLoading(false)
    }
  }

  const loadRinetd = () =>
    load(
      () => api.rules.localRinetd(),
      (p) => p.path ?? "rinetd.conf"
    )

  const onFile = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    if (!file) {
      clearPreview()
      return
    }
    const content = await file.text()
    await load(
      () => api.rules.parseImport(content, "auto"),
      () => file.name
    )
  }

  const existingIds = React.useMemo(
    () => new Set((existing ?? []).map((r) => r.id)),
    [existing]
  )
  const rules = loaded?.preview.rules ?? []
  const hasIds = rules.some((r) => r.id)
  const conflicts = keepIds
    ? rules.filter((r) => r.id && existingIds.has(r.id)).length
    : 0

  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    if (!loaded || rules.length === 0) return
    setPending(true)
    setSubmitError(null)
    try {
      const forward: Rule[] = rules.map((r) => ({
        ...r,
        id: keepIds ? r.id : "",
        status: asDisabled ? "inactive" : r.status,
      }))
      const res = await api.rules.import(forward, mode)
      toast.success(t("rules.import.done", { ...res }))
      void queryClient.invalidateQueries({ queryKey: qk.rules })
      void queryClient.invalidateQueries({ queryKey: qk.overview })
      reset()
      onOpenChange(false)
    } catch (error) {
      setSubmitError(errorMessage(error, t))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (pending) return
        if (!next) reset()
        onOpenChange(next)
      }}
    >
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        <form
          onSubmit={(e) => void submit(e)}
          className="flex min-w-0 flex-col gap-4"
        >
          <DialogHeader>
            <DialogTitle>{t("rules.import.title")}</DialogTitle>
            <DialogDescription>
              {t("rules.import.description")}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Tabs
              value={tab}
              onValueChange={(v) => {
                setTab(v as Source)
                // Reading the local rinetd config has no side effects, so
                // opening its tab loads it right away.
                if (v === "rinetd") void loadRinetd()
                else clearPreview()
              }}
              className="gap-4"
            >
              <TabsList>
                {SOURCES.map((s) => (
                  <TabsTrigger key={s} value={s} disabled={pending}>
                    {t(`rules.import.tabs.${s}`)}
                  </TabsTrigger>
                ))}
              </TabsList>
              <TabsContent value="file">
                <Field data-invalid={loadError ? true : undefined}>
                  <FieldLabel htmlFor="import-file">
                    {t("rules.import.file")}
                  </FieldLabel>
                  <Input
                    id="import-file"
                    type="file"
                    accept=".json,.conf,.txt,application/json,text/plain"
                    disabled={pending}
                    aria-invalid={loadError ? true : undefined}
                    onChange={(e) => void onFile(e)}
                  />
                  <FieldDescription>
                    {t("rules.import.fileHint")}
                  </FieldDescription>
                </Field>
              </TabsContent>
              <TabsContent value="paste">
                <Field>
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <FieldLabel htmlFor="import-text">
                      {t("rules.import.paste")}
                    </FieldLabel>
                    <Select
                      value={format}
                      onValueChange={(v) => {
                        setFormat(v as ImportFormat)
                        clearPreview()
                      }}
                    >
                      <SelectTrigger
                        size="sm"
                        className="w-60"
                        aria-label={t("rules.import.format")}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectGroup>
                          {FORMATS.map((f) => (
                            <SelectItem key={f} value={f}>
                              {t(`rules.import.formats.${f}`)}
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                  </div>
                  <Textarea
                    id="import-text"
                    value={text}
                    rows={8}
                    spellCheck={false}
                    disabled={pending}
                    placeholder={t("rules.import.pastePlaceholder")}
                    aria-invalid={loadError ? true : undefined}
                    className="max-h-72 min-h-40 font-mono text-xs md:text-xs"
                    onChange={(e) => {
                      setText(e.target.value)
                      if (loaded || loadError) clearPreview()
                    }}
                  />
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <FieldDescription className="min-w-0 flex-1">
                      {t("rules.import.pasteHint")}
                    </FieldDescription>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={!text.trim() || loading || pending}
                      onClick={() =>
                        void load(
                          () => api.rules.parseImport(text, format),
                          () => t("rules.import.pasted")
                        )
                      }
                    >
                      {loading && <Spinner data-icon="inline-start" />}
                      {loading
                        ? t("rules.import.parsing")
                        : t("rules.import.parse")}
                    </Button>
                  </div>
                </Field>
              </TabsContent>
              <TabsContent value="rinetd">
                <Field>
                  <FieldDescription>
                    {t("rules.import.rinetdHint")}
                  </FieldDescription>
                  <div>
                    <Button
                      type="button"
                      variant="outline"
                      disabled={loading || pending}
                      onClick={() => void loadRinetd()}
                    >
                      {loading && <Spinner data-icon="inline-start" />}
                      {loading
                        ? t("rules.import.parsing")
                        : t("rules.import.rinetdLoad")}
                    </Button>
                  </div>
                </Field>
              </TabsContent>
            </Tabs>

            {loadError && <FieldError>{loadError}</FieldError>}

            {loaded && (
              <PreviewSection
                loaded={loaded}
                existingIds={keepIds ? existingIds : null}
                asDisabled={asDisabled}
                showPortsHint={tab === "rinetd"}
              />
            )}

            {loaded && rules.length > 0 && (
              <>
                <FieldSet>
                  <FieldLegend variant="label">
                    {t("rules.import.options")}
                  </FieldLegend>
                  <FieldGroup className="gap-3">
                    <Field orientation="horizontal">
                      <Checkbox
                        id="import-disabled"
                        checked={asDisabled}
                        onCheckedChange={(v) => setAsDisabled(v === true)}
                      />
                      <FieldContent>
                        <FieldLabel htmlFor="import-disabled">
                          {t("rules.import.disabled")}
                        </FieldLabel>
                        <FieldDescription>
                          {t("rules.import.disabledHint")}
                        </FieldDescription>
                      </FieldContent>
                    </Field>
                    {hasIds && (
                      <Field orientation="horizontal">
                        <Checkbox
                          id="import-keep-ids"
                          checked={keepIds}
                          onCheckedChange={(v) => setKeepIds(v === true)}
                        />
                        <FieldContent>
                          <FieldLabel htmlFor="import-keep-ids">
                            {t("rules.import.keepIds")}
                          </FieldLabel>
                          <FieldDescription>
                            {conflicts > 0
                              ? t("rules.import.keepIdsConflict", {
                                  count: conflicts,
                                })
                              : t("rules.import.keepIdsHint")}
                          </FieldDescription>
                        </FieldContent>
                      </Field>
                    )}
                  </FieldGroup>
                </FieldSet>
                <FieldSet>
                  <FieldLegend variant="label">
                    {t("rules.import.mode")}
                  </FieldLegend>
                  <RadioGroup
                    value={mode}
                    onValueChange={(v) => setMode(v as ImportMode)}
                  >
                    <FieldLabel htmlFor="import-merge">
                      <Field orientation="horizontal">
                        <FieldContent>
                          <FieldTitle>{t("rules.import.merge")}</FieldTitle>
                          <FieldDescription>
                            {t("rules.import.mergeHint")}
                          </FieldDescription>
                        </FieldContent>
                        <RadioGroupItem value="merge" id="import-merge" />
                      </Field>
                    </FieldLabel>
                    <FieldLabel htmlFor="import-replace">
                      <Field orientation="horizontal">
                        <FieldContent>
                          <FieldTitle>{t("rules.import.replace")}</FieldTitle>
                          <FieldDescription>
                            {t("rules.import.replaceHint")}
                          </FieldDescription>
                        </FieldContent>
                        <RadioGroupItem value="replace" id="import-replace" />
                      </Field>
                    </FieldLabel>
                  </RadioGroup>
                </FieldSet>
                {mode === "replace" && (
                  <Alert variant="destructive">
                    <TriangleAlertIcon />
                    <AlertDescription>
                      {t("rules.import.replaceWarning")}
                    </AlertDescription>
                  </Alert>
                )}
              </>
            )}
            {submitError && (
              <Alert variant="destructive">
                <TriangleAlertIcon />
                <AlertDescription>{submitError}</AlertDescription>
              </Alert>
            )}
          </FieldGroup>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={pending}
              onClick={() => {
                reset()
                onOpenChange(false)
              }}
            >
              {t("common.actions.cancel")}
            </Button>
            <Button
              type="submit"
              variant={mode === "replace" ? "destructive" : "default"}
              disabled={rules.length === 0 || loading || pending}
            >
              {pending && <Spinner data-icon="inline-start" />}
              {pending
                ? t("rules.import.submitting")
                : t("rules.import.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function PreviewSection({
  loaded,
  existingIds,
  asDisabled,
  showPortsHint,
}: {
  loaded: Loaded
  /** Ids already in use when original ids are kept, else null. */
  existingIds: Set<string> | null
  asDisabled: boolean
  showPortsHint: boolean
}) {
  const { t } = useI18n()
  const { preview, source } = loaded

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <p className="text-sm text-muted-foreground">
        {t("rules.import.found", { count: preview.rules.length, source })}{" "}
        <Badge variant="secondary">
          {t(`rules.import.formatName.${preview.format}`)}
        </Badge>
      </p>
      {preview.warnings.length > 0 && (
        <WarningList warnings={preview.warnings} />
      )}
      {preview.rules.length === 0 ? (
        <Alert variant="destructive">
          <TriangleAlertIcon />
          <AlertDescription>{t("rules.import.noRules")}</AlertDescription>
        </Alert>
      ) : (
        <div className="max-h-64 overflow-auto rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-3">
                  {t("rules.import.columns.type")}
                </TableHead>
                <TableHead>{t("rules.import.columns.listen")}</TableHead>
                <TableHead>{t("rules.import.columns.target")}</TableHead>
                <TableHead>{t("rules.import.columns.status")}</TableHead>
                <TableHead className="pr-3">
                  {t("rules.import.columns.access")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {preview.rules.map((rule, i) => {
                const active = rule.status === "active" && !asDisabled
                const replaces =
                  existingIds && rule.id && existingIds.has(rule.id)
                return (
                  <TableRow key={i}>
                    <TableCell className="pl-3">
                      <RuleTypeBadge type={rule.type} />
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-col gap-0.5">
                        {rule.name && (
                          <span className="font-medium">{rule.name}</span>
                        )}
                        <span className="font-mono text-xs">
                          {formatListen(rule)}
                        </span>
                        {replaces && (
                          <span className="text-xs text-warning">
                            {t("rules.import.replacesId", { id: rule.id })}
                          </span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {formatTarget(rule)}
                    </TableCell>
                    <TableCell>
                      <Badge variant={active ? "success" : "secondary"}>
                        {active
                          ? t("rules.import.enabled")
                          : t("rules.import.disabledState")}
                      </Badge>
                    </TableCell>
                    <TableCell className="pr-3 text-muted-foreground">
                      {rule.acl ? (
                        <span title={rule.acl.cidrs.join("\n")}>
                          {t(
                            rule.acl.mode === "allow"
                              ? "rules.import.aclAllow"
                              : "rules.import.aclDeny",
                            {
                              count: rule.acl.cidrs.length,
                            }
                          )}
                        </span>
                      ) : (
                        "—"
                      )}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      )}
      {showPortsHint && preview.rules.length > 0 && (
        <Alert>
          <InfoIcon />
          <AlertDescription>{t("rules.import.rinetdPorts")}</AlertDescription>
        </Alert>
      )}
    </div>
  )
}

function WarningList({ warnings }: { warnings: ImportWarning[] }) {
  const { t } = useI18n()
  return (
    <Alert>
      <TriangleAlertIcon className="text-warning" />
      <AlertTitle>
        {t("rules.import.warnings", { count: warnings.length })}
      </AlertTitle>
      <AlertDescription>
        <ul className="flex max-h-32 w-full flex-col gap-1 overflow-y-auto">
          {warnings.map((w, i) => (
            <li key={i}>
              {(w.line || w.index) && (
                <span className="font-medium text-foreground">
                  {w.line
                    ? t("rules.import.line", { line: w.line })
                    : t("rules.import.item", { index: w.index ?? 0 })}
                  {": "}
                </span>
              )}
              {w.message}
            </li>
          ))}
        </ul>
      </AlertDescription>
    </Alert>
  )
}
