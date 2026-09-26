import * as React from "react"
import { EyeIcon, EyeOffIcon } from "lucide-react"

import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group"
import { useI18n } from "@/i18n"

export function PasswordInput(
  props: Omit<React.ComponentProps<typeof InputGroupInput>, "type">
) {
  const { t } = useI18n()
  const [visible, setVisible] = React.useState(false)
  const label = visible ? t("auth.hidePassword") : t("auth.showPassword")
  return (
    <InputGroup>
      <InputGroupInput type={visible ? "text" : "password"} {...props} />
      <InputGroupAddon align="inline-end">
        <InputGroupButton
          size="icon-xs"
          aria-label={label}
          title={label}
          onClick={() => setVisible((v) => !v)}
        >
          {visible ? <EyeOffIcon /> : <EyeIcon />}
        </InputGroupButton>
      </InputGroupAddon>
    </InputGroup>
  )
}
