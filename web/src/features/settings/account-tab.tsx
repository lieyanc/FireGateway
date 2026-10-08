import * as React from "react"
import { useForm } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { CircleAlertIcon } from "lucide-react"
import { toast } from "sonner"
import { z } from "zod"

import { PasswordInput } from "@/components/common/password-input"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
import { useAuthState } from "@/lib/queries"
import { signedIn } from "@/lib/session"

export function AccountTab() {
  const { t } = useI18n()
  const auth = useAuthState()
  const currentUsername = auth.data?.username ?? ""
  const [formError, setFormError] = React.useState<string | null>(null)

  const schema = React.useMemo(
    () =>
      z
        .object({
          currentPassword: z.string().min(1, t("settings.account.currentRequired")),
          username: z
            .string()
            .trim()
            .min(1, t("auth.validation.usernameRequired"))
            .max(64, t("auth.validation.usernameLength")),
          newPassword: z
            .string()
            .min(8, t("auth.validation.passwordLength"))
            .max(128, t("auth.validation.passwordLength")),
          confirm: z.string(),
        })
        .refine((v) => v.newPassword === v.confirm, {
          path: ["confirm"],
          message: t("auth.validation.passwordMismatch"),
        }),
    [t]
  )
  type Values = z.infer<typeof schema>

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    values: {
      currentPassword: "",
      username: currentUsername,
      newPassword: "",
      confirm: "",
    },
    resetOptions: { keepDirtyValues: true },
  })
  const { errors, isSubmitting } = form.formState

  const onSubmit = async (values: Values) => {
    setFormError(null)
    try {
      const res = await api.auth.changePassword({
        currentPassword: values.currentPassword,
        newPassword: values.newPassword,
        username: values.username !== currentUsername ? values.username : undefined,
      })
      signedIn(res)
      form.reset({
        currentPassword: "",
        username: res.username,
        newPassword: "",
        confirm: "",
      })
      toast.success(t("settings.account.success"), {
        description: t("settings.account.successDescription"),
      })
    } catch (error) {
      if (isApiError(error) && (error.status === 401 || error.status === 403)) {
        form.setError("currentPassword", { message: t("settings.account.wrongPassword") })
        form.setFocus("currentPassword")
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
    <Card>
      <CardHeader>
        <CardTitle>{t("settings.account.title")}</CardTitle>
        <CardDescription>{t("settings.account.description")}</CardDescription>
      </CardHeader>
      <form onSubmit={form.handleSubmit(onSubmit)} noValidate className="contents">
        <CardContent>
          <FieldGroup className="max-w-md">
            {formError && (
              <Alert variant="destructive">
                <CircleAlertIcon />
                <AlertDescription>{formError}</AlertDescription>
              </Alert>
            )}
            {/* Lets password managers associate the credentials. */}
            <input
              type="text"
              name="current-username"
              autoComplete="username"
              value={currentUsername}
              readOnly
              hidden
            />
            <Field data-invalid={!!errors.currentPassword || undefined}>
              <FieldLabel htmlFor="currentPassword">
                {t("settings.account.currentPassword")}
              </FieldLabel>
              <PasswordInput
                id="currentPassword"
                autoComplete="current-password"
                aria-invalid={!!errors.currentPassword}
                {...form.register("currentPassword")}
              />
              <FieldError errors={[errors.currentPassword]} />
            </Field>
            <Field data-invalid={!!errors.username || undefined}>
              <FieldLabel htmlFor="username">{t("settings.account.newUsername")}</FieldLabel>
              <Input
                id="username"
                autoComplete="off"
                aria-invalid={!!errors.username}
                {...form.register("username")}
              />
              {errors.username ? (
                <FieldError errors={[errors.username]} />
              ) : (
                <FieldDescription>{t("settings.account.newUsernameHint")}</FieldDescription>
              )}
            </Field>
            <Field data-invalid={!!errors.newPassword || undefined}>
              <FieldLabel htmlFor="newPassword">{t("settings.account.newPassword")}</FieldLabel>
              <PasswordInput
                id="newPassword"
                autoComplete="new-password"
                aria-invalid={!!errors.newPassword}
                {...form.register("newPassword")}
              />
              {errors.newPassword ? (
                <FieldError errors={[errors.newPassword]} />
              ) : (
                <FieldDescription>{t("settings.account.passwordHint")}</FieldDescription>
              )}
            </Field>
            <Field data-invalid={!!errors.confirm || undefined}>
              <FieldLabel htmlFor="confirm">
                {t("settings.account.confirmNewPassword")}
              </FieldLabel>
              <PasswordInput
                id="confirm"
                autoComplete="new-password"
                aria-invalid={!!errors.confirm}
                {...form.register("confirm")}
              />
              <FieldError errors={[errors.confirm]} />
            </Field>
          </FieldGroup>
        </CardContent>
        <CardFooter>
          <Button type="submit" disabled={isSubmitting}>
            {isSubmitting && <Spinner data-icon="inline-start" />}
            {isSubmitting ? t("common.actions.saving") : t("settings.account.submit")}
          </Button>
        </CardFooter>
      </form>
    </Card>
  )
}
