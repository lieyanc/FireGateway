import * as React from "react"
import { Link } from "react-router"
import {
  CopyPlusIcon,
  EyeIcon,
  MoreHorizontalIcon,
  PencilIcon,
  RotateCwIcon,
  Trash2Icon,
} from "lucide-react"

import { ConfirmDialog } from "@/components/common/confirm-dialog"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useI18n } from "@/i18n"
import type { RuleView } from "@/lib/types"
import { useRuleEditor } from "@/features/rules/rule-editor"
import { useDeleteRule, useRestartRule } from "@/features/rules/use-rule-mutations"

export function RuleRowActions({ rule }: { rule: RuleView }) {
  const { t } = useI18n()
  const editor = useRuleEditor()
  const restart = useRestartRule()
  const remove = useDeleteRule()
  const [confirming, setConfirming] = React.useState(false)
  const name = rule.name || rule.id

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label={t("common.actions.more")}>
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-40">
          <DropdownMenuGroup>
            <DropdownMenuItem asChild>
              <Link to={`/rules/${encodeURIComponent(rule.id)}`}>
                <EyeIcon />
                {t("common.actions.view")}
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => editor.edit(rule)}>
              <PencilIcon />
              {t("common.actions.edit")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => editor.duplicate(rule)}>
              <CopyPlusIcon />
              {t("common.actions.duplicate")}
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={rule.status !== "active" || restart.isPending}
              onSelect={() => restart.mutate(rule)}
            >
              <RotateCwIcon />
              {t("common.actions.restart")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuGroup>
            <DropdownMenuItem variant="destructive" onSelect={() => setConfirming(true)}>
              <Trash2Icon />
              {t("common.actions.delete")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={t("rules.deleteTitle", { name })}
        description={t("rules.deleteDescription")}
        confirmLabel={t("common.actions.delete")}
        onConfirm={() => remove.mutateAsync(rule)}
      />
    </>
  )
}
