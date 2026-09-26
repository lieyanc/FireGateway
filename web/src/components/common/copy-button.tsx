import * as React from "react"
import { CheckIcon, CopyIcon } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { useI18n } from "@/i18n"
import { copyText } from "@/lib/clipboard"

export function CopyButton({
  value,
  label,
  variant = "outline",
  size = "icon",
}: {
  value: string
  label?: string
  variant?: React.ComponentProps<typeof Button>["variant"]
  size?: React.ComponentProps<typeof Button>["size"]
}) {
  const { t } = useI18n()
  const [copied, setCopied] = React.useState(false)

  React.useEffect(() => {
    if (!copied) return
    const id = setTimeout(() => setCopied(false), 1500)
    return () => clearTimeout(id)
  }, [copied])

  const onClick = async () => {
    try {
      await copyText(value)
      setCopied(true)
    } catch {
      toast.error(t("common.copyFailed"))
    }
  }

  const Icon = copied ? CheckIcon : CopyIcon
  const text = copied ? t("common.actions.copied") : (label ?? t("common.actions.copy"))
  const iconOnly = size?.toString().startsWith("icon")

  return (
    <Button
      type="button"
      variant={variant}
      size={size}
      onClick={() => void onClick()}
      aria-label={iconOnly ? text : undefined}
      title={iconOnly ? text : undefined}
    >
      <Icon data-icon={iconOnly ? undefined : "inline-start"} />
      {!iconOnly && text}
    </Button>
  )
}
