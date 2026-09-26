import * as React from "react"
import { useForm } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { CircleAlertIcon } from "lucide-react"
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
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { useI18n } from "@/i18n"
import { api, isApiError } from "@/lib/api"
import { errorMessage } from "@/lib/errors"
import { signedIn } from "@/lib/session"

export default function LoginPage() {
  const { t } = useI18n()
  const [formError, setFormError] = React.useState<string | null>(null)

  const schema = React.useMemo(
    () =>
      z.object({
        username: z.string().trim().min(1, t("auth.validation.usernameRequired")),
        password: z.string().min(1, t("auth.validation.passwordLength")),
      }),
    [t]
  )
  type Values = z.infer<typeof schema>

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { username: "", password: "" },
  })
  const { errors, isSubmitting } = form.formState

  const onSubmit = async (values: Values) => {
    setFormError(null)
    try {
      const res = await api.auth.login(values)
      // The auth gate redirects to ?next= once the state flips.
      signedIn(res.username)
    } catch (error) {
      if (isApiError(error) && error.status === 401) {
        setFormError(t("auth.login.invalid"))
      } else if (isApiError(error) && error.status === 429) {
        setFormError(
          error.retryAfter
            ? t("auth.login.rateLimited", { seconds: error.retryAfter })
            : t("auth.login.rateLimitedNoWait")
        )
      } else {
        setFormError(errorMessage(error, t))
      }
      form.setFocus("password")
    }
  }

  return (
    <AuthShell>
      <Card>
        <CardHeader>
          <CardTitle>{t("auth.login.title")}</CardTitle>
          <CardDescription>{t("auth.login.description")}</CardDescription>
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
              <Field data-invalid={!!errors.username || undefined}>
                <FieldLabel htmlFor="username">{t("auth.username")}</FieldLabel>
                <Input
                  id="username"
                  autoComplete="username"
                  autoFocus
                  aria-invalid={!!errors.username}
                  {...form.register("username")}
                />
                <FieldError errors={[errors.username]} />
              </Field>
              <Field data-invalid={!!errors.password || undefined}>
                <FieldLabel htmlFor="password">{t("auth.password")}</FieldLabel>
                <PasswordInput
                  id="password"
                  autoComplete="current-password"
                  aria-invalid={!!errors.password}
                  {...form.register("password")}
                />
                <FieldError errors={[errors.password]} />
              </Field>
              <Button type="submit" disabled={isSubmitting}>
                {isSubmitting && <Spinner data-icon="inline-start" />}
                {isSubmitting ? t("auth.login.submitting") : t("auth.login.submit")}
              </Button>
            </FieldGroup>
          </form>
        </CardContent>
      </Card>
    </AuthShell>
  )
}
