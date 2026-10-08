import { toast } from "sonner"

import type { Translate } from "@/i18n"
import { isApiError } from "@/lib/api"

/** Human-readable message for any thrown value. */
export function errorMessage(error: unknown, t: Translate) {
  if (isApiError(error)) {
    if (error.code === "network") return t("common.errors.network")
    if (error.code === "quota_exceeded") {
      return t("common.errors.quotaExceeded", { message: error.message })
    }
    if (error.code === "unavailable" && error.retryAfter) {
      return t("common.errors.unavailable")
    }
    return error.message
  }
  if (error instanceof Error) return error.message
  return t("common.errors.title")
}

export function toastError(error: unknown, t: Translate, title?: string) {
  const message = errorMessage(error, t)
  if (title) toast.error(title, { description: message })
  else toast.error(message)
}
