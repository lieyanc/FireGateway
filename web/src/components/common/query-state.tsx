import { CircleAlertIcon, RotateCwIcon } from "lucide-react"

import { Alert, AlertAction, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { useI18n } from "@/i18n"
import { errorMessage } from "@/lib/errors"

/** Inline error for a failed query, with a retry button. */
export function QueryError({
  error,
  onRetry,
  title,
}: {
  error: unknown
  onRetry?: () => void
  title?: string
}) {
  const { t } = useI18n()
  return (
    <Alert variant="destructive">
      <CircleAlertIcon />
      <AlertTitle>{title ?? t("common.errors.loadFailed")}</AlertTitle>
      <AlertDescription>{errorMessage(error, t)}</AlertDescription>
      {onRetry && (
        <AlertAction>
          <Button variant="outline" size="sm" onClick={onRetry}>
            <RotateCwIcon data-icon="inline-start" />
            {t("common.actions.retry")}
          </Button>
        </AlertAction>
      )}
    </Alert>
  )
}

export function FullPageSpinner() {
  return (
    <div className="flex min-h-svh items-center justify-center">
      <Spinner className="size-6 text-muted-foreground" />
    </div>
  )
}

/** Suspense fallback while a lazy route chunk loads. */
export function PageSkeleton() {
  return (
    <div className="mx-auto flex w-full max-w-7xl flex-col gap-6 p-4 md:p-6">
      <Skeleton className="h-7 w-48" />
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {Array.from({ length: 4 }, (_, i) => (
          <Skeleton key={i} className="h-28" />
        ))}
      </div>
      <Skeleton className="h-72" />
    </div>
  )
}
