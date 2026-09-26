import { FlameIcon } from "lucide-react"

import { useI18n } from "@/i18n"

/** Centered frame for the login and setup screens. */
export function AuthShell({ children }: { children: React.ReactNode }) {
  const { t } = useI18n()
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-6 bg-muted/40 p-4">
      <div className="flex items-center gap-2 font-semibold">
        <div className="flex size-8 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
          <FlameIcon className="size-4" />
        </div>
        {t("common.appName")}
      </div>
      <div className="w-full max-w-sm">{children}</div>
    </div>
  )
}
