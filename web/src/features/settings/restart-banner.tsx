import * as React from "react"
import { CircleAlertIcon, RotateCwIcon } from "lucide-react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/common/confirm-dialog"
import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { useI18n } from "@/i18n"
import { api, isApiError } from "@/lib/api"
import { toastError } from "@/lib/errors"
import {
  setRestartRequired,
  useRestartRequired,
  waitForServer,
} from "@/features/settings/restart"

type Phase = "idle" | "restarting" | "timeout"

/** Banner shown while saved settings wait for a restart, with the restart flow. */
export function RestartBanner() {
  const { t } = useI18n()
  const required = useRestartRequired()
  const [confirming, setConfirming] = React.useState(false)
  const [phase, setPhase] = React.useState<Phase>("idle")

  const waitAndReload = async () => {
    setPhase("restarting")
    if (await waitForServer()) {
      setRestartRequired(false)
      window.location.reload()
    } else {
      setPhase("timeout")
    }
  }

  const restart = async () => {
    try {
      await api.system.restart()
    } catch (error) {
      if (isApiError(error) && (error.status === 501 || error.code === "not_supported")) {
        toast.error(t("settings.system.restart.notSupported"))
      } else {
        toastError(error, t)
      }
      throw error
    }
    // The dialog closes once the server accepted; the banner shows progress.
    void waitAndReload()
  }

  if (phase === "restarting") {
    return (
      <Alert>
        <Spinner />
        <AlertTitle>{t("settings.system.restart.restarting")}</AlertTitle>
        <AlertDescription>
          {t("settings.system.restart.restartingDescription")}
        </AlertDescription>
      </Alert>
    )
  }

  if (!required && phase === "idle") return null

  return (
    <>
      <Alert variant={phase === "timeout" ? "destructive" : "default"}>
        <CircleAlertIcon />
        <AlertTitle>{t("settings.system.restartBanner.title")}</AlertTitle>
        <AlertDescription>
          {phase === "timeout"
            ? t("settings.system.restart.timeout")
            : t("settings.system.restartBanner.description")}
        </AlertDescription>
        <AlertAction>
          <Button size="sm" onClick={() => setConfirming(true)}>
            <RotateCwIcon data-icon="inline-start" />
            {t("settings.system.restartBanner.action")}
          </Button>
        </AlertAction>
      </Alert>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={t("settings.system.restart.confirmTitle")}
        description={t("settings.system.restart.confirmDescription")}
        confirmLabel={t("settings.system.restart.confirm")}
        onConfirm={restart}
      />
    </>
  )
}
