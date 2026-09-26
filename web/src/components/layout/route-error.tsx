import { isRouteErrorResponse, Link, useRouteError } from "react-router"
import { CircleAlertIcon, RotateCwIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { useI18n } from "@/i18n"

function isChunkError(error: unknown) {
  return (
    error instanceof Error &&
    /dynamically imported module|Importing a module script failed|Failed to fetch/i.test(
      error.message
    )
  )
}

export function RouteError() {
  const { t } = useI18n()
  const error = useRouteError()
  const message = isChunkError(error)
    ? t("shell.chunkError")
    : isRouteErrorResponse(error)
      ? `${error.status} ${error.statusText}`
      : error instanceof Error
        ? error.message
        : t("common.errors.title")

  return (
    <div className="flex min-h-[60svh] items-center justify-center p-4">
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <CircleAlertIcon />
          </EmptyMedia>
          <EmptyTitle>{t("common.errors.title")}</EmptyTitle>
          <EmptyDescription>{message}</EmptyDescription>
        </EmptyHeader>
        <EmptyContent className="flex-row justify-center">
          <Button onClick={() => window.location.reload()}>
            <RotateCwIcon data-icon="inline-start" />
            {t("shell.reload")}
          </Button>
          <Button variant="outline" asChild>
            <Link to="/">{t("shell.notFound.home")}</Link>
          </Button>
        </EmptyContent>
      </Empty>
    </div>
  )
}
