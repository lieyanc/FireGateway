import { Link } from "react-router"
import { CompassIcon } from "lucide-react"

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

export default function NotFoundPage() {
  const { t } = useI18n()
  return (
    <div className="flex flex-1 items-center justify-center p-4">
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <CompassIcon />
          </EmptyMedia>
          <EmptyTitle>{t("shell.notFound.title")}</EmptyTitle>
          <EmptyDescription>{t("shell.notFound.description")}</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button asChild>
            <Link to="/">{t("shell.notFound.home")}</Link>
          </Button>
        </EmptyContent>
      </Empty>
    </div>
  )
}
