import * as React from "react"
import { useForm } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { CircleAlertIcon, InfoIcon } from "lucide-react"
import { toast } from "sonner"
import { z } from "zod"

import { PasswordInput } from "@/components/common/password-input"
import { AuthShell } from "@/components/layout/auth-shell"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { useI18n } from "@/i18n"
import { api, isApiError } from "@/lib/api"
import { errorMessage } from "@/lib/errors"
import { authStateQuery, queryClient } from "@/lib/queries"
import { signedIn } from "@/lib/session"

export default function SetupPage() {
  const { t } = useI18n()
  const [formError, setFormError] = React.useState<string | null>(null)

  const schema = React.useMemo(
    () =>
      z
        .object({
          setupToken: z.string().trim().min(1, t("auth.validation.tokenRequired")),
          username: z
            .string()
            .trim()
            .min(1, t("auth.validation.usernameRequired"))
            .max(64, t("auth.validation.usernameLength")),
          password: z
            .string()
            .min(8, t("auth.validation.passwordLength"))
            .max(128, t("auth.validation.passwordLength")),
          confirm: z.string(),
        })
        .refine((v) => v.password === v.confirm, {
          path: ["confirm"],
          message: t("auth.validation.passwordMismatch"),
        }),
    [t]
  )
  type Values = z.infer<typeof schema>

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { setupToken: "", username: "admin", password: "", confirm: "" },
  })
  const { errors, isSubmitting } = form.formState

  const onSubmit = async ({ setupToken, username, password }: Values) => {
    setFormError(null)
    try {
      const res = await api.auth.setup({ setupToken, username, password })
      toast.success(t("auth.setup.success"))
      signedIn(res.username)
    } catch (error) {
      if (isApiError(error) && error.status === 403) {
        form.setError("setupToken", { message: t("auth.setup.tokenInvalid") })
        form.setFocus("setupToken")
      } else if (isApiError(error) && error.status === 409) {
        toast.info(t("auth.setup.alreadyInitialized"))
        await queryClient.fetchQuery({ ...authStateQuery, staleTime: 0 })
      } else if (isApiError(error) && error.code === "validation" && error.field) {
        const field = error.field as keyof Values
        if (field in form.getValues()) form.setError(field, { message: error.message })
        else setFormError(error.message)
      } else {
        setFormError(errorMessage(error, t))
      }
    }
  }

  return (
    <AuthShell>
      <Card>
        <CardHeader>
          <CardTitle>{t("auth.setup.title")}</CardTitle>
          <CardDescription>{t("auth.setup.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={form.handleSubmit(onSubmit)} noValidate>
            <FieldGroup>
              {formError && (
                <Alert variant="destructive">
                  <CircleAlertIcon />
                  <AlertDescription>{formError}</AlertDescription>
                </Alert>
              )}
              <Alert>
                <InfoIcon />
                <AlertDescription>{t("auth.setup.tokenHelp")}</AlertDescription>
              </Alert>
              <Field data-invalid={!!errors.setupToken || undefined}>
                <FieldLabel htmlFor="setupToken">{t("auth.setup.token")}</FieldLabel>
                <Input
                  id="setupToken"
                  autoComplete="off"
                  spellCheck={false}
                  autoFocus
                  className="font-mono"
                  placeholder={t("auth.setup.tokenPlaceholder")}
                  aria-invalid={!!errors.setupToken}
                  {...form.register("setupToken")}
                />
                <FieldError errors={[errors.setupToken]} />
              </Field>
              <Field data-invalid={!!errors.username || undefined}>
                <FieldLabel htmlFor="username">{t("auth.username")}</FieldLabel>
                <Input
                  id="username"
                  autoComplete="username"
                  aria-invalid={!!errors.username}
                  {...form.register("username")}
                />
                <FieldError errors={[errors.username]} />
              </Field>
              <Field data-invalid={!!errors.password || undefined}>
                <FieldLabel htmlFor="password">{t("auth.password")}</FieldLabel>
                <PasswordInput
                  id="password"
                  autoComplete="new-password"
                  aria-invalid={!!errors.password}
                  {...form.register("password")}
                />
                {errors.password ? (
                  <FieldError errors={[errors.password]} />
                ) : (
                  <FieldDescription>{t("auth.validation.passwordLength")}</FieldDescription>
                )}
              </Field>
              <Field data-invalid={!!errors.confirm || undefined}>
                <FieldLabel htmlFor="confirm">{t("auth.confirmPassword")}</FieldLabel>
                <PasswordInput
                  id="confirm"
                  autoComplete="new-password"
                  aria-invalid={!!errors.confirm}
                  {...form.register("confirm")}
                />
                <FieldError errors={[errors.confirm]} />
              </Field>
              <Button type="submit" disabled={isSubmitting}>
                {isSubmitting && <Spinner data-icon="inline-start" />}
                {isSubmitting ? t("auth.setup.submitting") : t("auth.setup.submit")}
              </Button>
            </FieldGroup>
          </form>
        </CardContent>
      </Card>
    </AuthShell>
  )
}
