import { ExternalLinkIcon, FlameIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { InfoList } from "@/features/settings/info-list"
import { useI18n } from "@/i18n"
import { useVersion } from "@/lib/queries"

const REPO_URL = "https://github.com/lieyanc/FireGateway"

export function AboutTab() {
  const { t, fmt } = useI18n()
  const version = useVersion()
  const v = version.data

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center gap-3">
          <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
            <FlameIcon />
          </div>
          <div className="flex min-w-0 flex-col gap-1">
            <CardTitle>{t("settings.about.title")}</CardTitle>
            <CardDescription>{t("settings.about.description")}</CardDescription>
          </div>
        </div>
      </CardHeader>
      <CardContent>
        <InfoList
          rows={[
            { label: t("settings.about.version"), value: v?.version ?? "–", mono: true },
            { label: t("settings.about.commit"), value: v?.commit || "–", mono: true },
            {
              label: t("settings.about.buildTime"),
              value: v?.buildTime ? fmt.dateTime(v.buildTime) : "–",
            },
            { label: t("settings.about.license"), value: t("settings.about.licenseName") },
            {
              label: t("settings.about.source"),
              value: (
                <a
                  href={REPO_URL}
                  target="_blank"
                  rel="noreferrer"
                  className="underline-offset-4 hover:underline"
                >
                  github.com/lieyanc/FireGateway
                </a>
              ),
            },
          ]}
        />
      </CardContent>
      <CardFooter>
        <Button variant="outline" asChild>
          <a href={REPO_URL} target="_blank" rel="noreferrer">
            <ExternalLinkIcon data-icon="inline-start" />
            {t("settings.about.github")}
          </a>
        </Button>
      </CardFooter>
    </Card>
  )
}
