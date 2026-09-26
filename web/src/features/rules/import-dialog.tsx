import * as React from "react"
import { useQueryClient } from "@tanstack/react-query"
import { TriangleAlertIcon } from "lucide-react"
import { toast } from "sonner"

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
  FieldLegend,
  FieldSet,
  FieldTitle,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Spinner } from "@/components/ui/spinner"
import { useI18n } from "@/i18n"
import { api } from "@/lib/api"
import { errorMessage } from "@/lib/errors"
import { qk } from "@/lib/queries"
import type { ImportMode, Rule } from "@/lib/types"

type Parsed = { file: string; rules: Rule[] }

/** Accepts `{forward: [...]}` (the export format) or a bare array. */
function parseRules(text: string): Rule[] | null {
  try {
    const data: unknown = JSON.parse(text)
    const list = Array.isArray(data)
      ? data
      : data && typeof data === "object" && Array.isArray((data as { forward?: unknown }).forward)
        ? (data as { forward: unknown[] }).forward
        : null
    if (!list || !list.every((r) => r && typeof r === "object" && !Array.isArray(r))) {
      return null
    }
    return list as Rule[]
  } catch {
    return null
  }
}

export function ImportRulesDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [parsed, setParsed] = React.useState<Parsed | null>(null)
  const [fileError, setFileError] = React.useState<string | null>(null)
  const [submitError, setSubmitError] = React.useState<string | null>(null)
  const [mode, setMode] = React.useState<ImportMode>("merge")
  const [pending, setPending] = React.useState(false)

  const reset = () => {
    setParsed(null)
    setFileError(null)
    setSubmitError(null)
    setMode("merge")
  }

  const onFile = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    setParsed(null)
    setFileError(null)
    setSubmitError(null)
    if (!file) return
    const rules = parseRules(await file.text())
    if (!rules) setFileError(t("rules.import.invalidFile"))
    else setParsed({ file: file.name, rules })
  }

  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    if (!parsed) return
    setPending(true)
    setSubmitError(null)
    try {
      const res = await api.rules.import(parsed.rules, mode)
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
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{t("rules.import.title")}</DialogTitle>
            <DialogDescription>{t("rules.import.description")}</DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field data-invalid={fileError ? true : undefined}>
              <FieldLabel htmlFor="import-file">{t("rules.import.file")}</FieldLabel>
              <Input
                id="import-file"
                type="file"
                accept="application/json,.json"
                aria-invalid={fileError ? true : undefined}
                onChange={(e) => void onFile(e)}
              />
              {fileError ? (
                <FieldError>{fileError}</FieldError>
              ) : (
                <FieldDescription>
                  {parsed
                    ? t("rules.import.found", { count: parsed.rules.length, file: parsed.file })
                    : t("rules.import.fileHint")}
                </FieldDescription>
              )}
            </Field>
            <FieldSet>
              <FieldLegend variant="label">{t("rules.import.mode")}</FieldLegend>
              <RadioGroup value={mode} onValueChange={(v) => setMode(v as ImportMode)}>
                <FieldLabel htmlFor="import-merge">
                  <Field orientation="horizontal">
                    <FieldContent>
                      <FieldTitle>{t("rules.import.merge")}</FieldTitle>
                      <FieldDescription>{t("rules.import.mergeHint")}</FieldDescription>
                    </FieldContent>
                    <RadioGroupItem value="merge" id="import-merge" />
                  </Field>
                </FieldLabel>
                <FieldLabel htmlFor="import-replace">
                  <Field orientation="horizontal">
                    <FieldContent>
                      <FieldTitle>{t("rules.import.replace")}</FieldTitle>
                      <FieldDescription>{t("rules.import.replaceHint")}</FieldDescription>
                    </FieldContent>
                    <RadioGroupItem value="replace" id="import-replace" />
                  </Field>
                </FieldLabel>
              </RadioGroup>
            </FieldSet>
            {mode === "replace" && (
              <Alert variant="destructive">
                <TriangleAlertIcon />
                <AlertDescription>{t("rules.import.replaceWarning")}</AlertDescription>
              </Alert>
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
              disabled={!parsed || pending}
            >
              {pending && <Spinner data-icon="inline-start" />}
              {pending ? t("rules.import.submitting") : t("rules.import.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
