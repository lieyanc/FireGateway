import * as React from "react"
import {
  ArrowDownIcon,
  ArrowUpDownIcon,
  ArrowUpIcon,
  PauseIcon,
  PlayIcon,
  Trash2Icon,
  XIcon,
} from "lucide-react"

import { ConfirmDialog } from "@/components/common/confirm-dialog"
import { RuleTypeBadge } from "@/components/common/rule-badges"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { useI18n } from "@/i18n"
import { statsStore } from "@/lib/stats-store"
import type { StatsEvent, RuleView } from "@/lib/types"
import { cn } from "@/lib/utils"
import {
  RuleActiveCell,
  RuleNameCell,
  RuleRateCell,
  RuleRoute,
  RuleStateCell,
  RuleTrafficCell,
} from "@/features/rules/rule-cells"
import { RuleRowActions } from "@/features/rules/rule-row-actions"
import { useBatchRules, useSetRuleEnabled } from "@/features/rules/use-rule-mutations"
import { STATE_ORDER } from "@/features/rules/utils"

type SortKey = "name" | "state" | "active" | "rate" | "traffic"
type Sort = {
  key: SortKey
  dir: "asc" | "desc"
  /** Live values are sorted by the stats tick seen when the sort was chosen. */
  snapshot: StatsEvent | null
} | null

function sortValue(rule: RuleView, key: SortKey, snapshot: StatsEvent | null) {
  const live = snapshot?.rules[rule.id]
  const rt = rule.runtime
  switch (key) {
    case "name":
      return (rule.name || rule.id).toLowerCase()
    case "state":
      return STATE_ORDER[live?.state ?? rt.state] ?? 9
    case "active":
      return live?.activeConnections ?? rt.activeConnections
    case "rate":
      return (live?.rateUp ?? rt.rateUp) + (live?.rateDown ?? rt.rateDown)
    case "traffic":
      return (live?.bytesUp ?? rt.bytesUp) + (live?.bytesDown ?? rt.bytesDown)
  }
}

function sortRules(rules: RuleView[], sort: Sort) {
  if (!sort) return rules
  const factor = sort.dir === "asc" ? 1 : -1
  return [...rules].sort((a, b) => {
    const va = sortValue(a, sort.key, sort.snapshot)
    const vb = sortValue(b, sort.key, sort.snapshot)
    if (va < vb) return -1 * factor
    if (va > vb) return 1 * factor
    return 0
  })
}

function SortHeader({
  label,
  sortKey,
  sort,
  onSort,
  align = "left",
}: {
  label: string
  sortKey: SortKey
  sort: Sort
  onSort: (key: SortKey) => void
  align?: "left" | "right"
}) {
  const { t } = useI18n()
  const active = sort?.key === sortKey
  const Icon = !active ? ArrowUpDownIcon : sort.dir === "asc" ? ArrowUpIcon : ArrowDownIcon
  return (
    <Button
      variant="ghost"
      size="sm"
      className={cn("-mx-2.5", align === "right" && "ml-auto flex")}
      aria-label={t("rules.columns.sortBy", { column: label })}
      onClick={() => onSort(sortKey)}
    >
      {label}
      <Icon data-icon="inline-end" className={cn(!active && "opacity-40")} />
    </Button>
  )
}

function EnabledSwitch({ rule }: { rule: RuleView }) {
  const { t } = useI18n()
  const toggle = useSetRuleEnabled()
  return (
    <Switch
      checked={rule.status === "active"}
      aria-label={t("rules.toggleLabel", { name: rule.name || rule.id })}
      onCheckedChange={(enabled) => toggle.mutate({ rule, enabled })}
    />
  )
}

export function RulesTable({ rules }: { rules: RuleView[] }) {
  const { t } = useI18n()
  const [sort, setSort] = React.useState<Sort>(null)
  const [selected, setSelected] = React.useState<Set<string>>(() => new Set())
  const [confirmDelete, setConfirmDelete] = React.useState(false)
  const batch = useBatchRules()

  const sorted = React.useMemo(() => sortRules(rules, sort), [rules, sort])

  // Selection only ever refers to rows that are currently visible.
  const visibleIds = React.useMemo(() => new Set(rules.map((r) => r.id)), [rules])
  const selectedIds = [...selected].filter((id) => visibleIds.has(id))
  const allSelected = rules.length > 0 && selectedIds.length === rules.length
  const someSelected = selectedIds.length > 0 && !allSelected

  const onSort = (key: SortKey) => {
    setSort((prev) => {
      const snapshot = statsStore.getLatest()
      if (prev?.key !== key) {
        return { key, dir: key === "name" ? "asc" : "desc", snapshot }
      }
      if (prev.dir === (key === "name" ? "asc" : "desc")) {
        return { key, dir: prev.dir === "asc" ? "desc" : "asc", snapshot }
      }
      return null
    })
  }

  const toggleRow = (id: string, checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  const toggleAll = (checked: boolean) => {
    setSelected(checked ? new Set(rules.map((r) => r.id)) : new Set())
  }

  const runBatch = async (action: "enable" | "disable" | "delete") => {
    const res = await batch.mutateAsync({ action, ids: selectedIds })
    setSelected((prev) => {
      const next = new Set(prev)
      for (const id of res.ok) next.delete(id)
      return next
    })
  }

  return (
    <div className="flex flex-col">
      {selectedIds.length > 0 && (
        <div
          role="toolbar"
          aria-label={t("common.selectedCount", { count: selectedIds.length })}
          className="flex flex-wrap items-center gap-2 border-b bg-muted/50 px-4 py-2"
        >
          <span className="text-sm font-medium">
            {t("common.selectedCount", { count: selectedIds.length })}
          </span>
          <div className="ml-auto flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={batch.isPending}
              onClick={() => void runBatch("enable").catch(() => {})}
            >
              <PlayIcon data-icon="inline-start" />
              {t("rules.batch.enable")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={batch.isPending}
              onClick={() => void runBatch("disable").catch(() => {})}
            >
              <PauseIcon data-icon="inline-start" />
              {t("rules.batch.disable")}
            </Button>
            <Button
              variant="destructive"
              size="sm"
              disabled={batch.isPending}
              onClick={() => setConfirmDelete(true)}
            >
              <Trash2Icon data-icon="inline-start" />
              {t("rules.batch.delete")}
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t("rules.batch.clear")}
              title={t("rules.batch.clear")}
              onClick={() => setSelected(new Set())}
            >
              <XIcon />
            </Button>
          </div>
        </div>
      )}
      <Table containerClassName="@container">
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="w-10 pl-4">
              <Checkbox
                checked={allSelected ? true : someSelected ? "indeterminate" : false}
                onCheckedChange={(v) => toggleAll(v === true)}
                aria-label={t("rules.columns.selectAll")}
              />
            </TableHead>
            <TableHead className="w-14">
              <span className="sr-only">{t("rules.columns.enabled")}</span>
            </TableHead>
            <TableHead>
              <SortHeader label={t("rules.columns.name")} sortKey="name" sort={sort} onSort={onSort} />
            </TableHead>
            <TableHead className="hidden @6xl:table-cell">{t("rules.columns.type")}</TableHead>
            <TableHead className="hidden @4xl:table-cell">{t("rules.columns.route")}</TableHead>
            <TableHead>
              <SortHeader label={t("rules.columns.state")} sortKey="state" sort={sort} onSort={onSort} />
            </TableHead>
            <TableHead className="hidden text-right @xl:table-cell">
              <SortHeader
                label={t("rules.columns.active")}
                sortKey="active"
                sort={sort}
                onSort={onSort}
                align="right"
              />
            </TableHead>
            <TableHead className="hidden text-right @2xl:table-cell">
              <SortHeader
                label={t("rules.columns.rate")}
                sortKey="rate"
                sort={sort}
                onSort={onSort}
                align="right"
              />
            </TableHead>
            <TableHead className="hidden text-right @6xl:table-cell">
              <SortHeader
                label={t("rules.columns.traffic")}
                sortKey="traffic"
                sort={sort}
                onSort={onSort}
                align="right"
              />
            </TableHead>
            <TableHead className="w-12 pr-4">
              <span className="sr-only">{t("rules.columns.actions")}</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {sorted.map((rule) => {
            const isSelected = selected.has(rule.id)
            return (
              <TableRow key={rule.id} data-state={isSelected ? "selected" : undefined}>
                <TableCell className="pl-4">
                  <Checkbox
                    checked={isSelected}
                    onCheckedChange={(v) => toggleRow(rule.id, v === true)}
                    aria-label={t("rules.columns.selectRow", { name: rule.name || rule.id })}
                  />
                </TableCell>
                <TableCell>
                  <EnabledSwitch rule={rule} />
                </TableCell>
                <TableCell className="max-w-56 whitespace-normal">
                  <RuleNameCell rule={rule} />
                  <div className="mt-1 flex min-w-0 items-center gap-1.5 @4xl:hidden">
                    <RuleTypeBadge type={rule.type} />
                    <span className="min-w-0 truncate">
                      <RuleRoute rule={rule} />
                    </span>
                  </div>
                </TableCell>
                <TableCell className="hidden @6xl:table-cell">
                  <RuleTypeBadge type={rule.type} />
                </TableCell>
                <TableCell className="hidden whitespace-normal @4xl:table-cell">
                  <span className="mr-2 @6xl:hidden">
                    <RuleTypeBadge type={rule.type} />
                  </span>
                  <RuleRoute rule={rule} />
                </TableCell>
                <TableCell>
                  <RuleStateCell rule={rule} />
                </TableCell>
                <TableCell className="hidden text-right tabular-nums @xl:table-cell">
                  <RuleActiveCell rule={rule} />
                </TableCell>
                <TableCell className="hidden text-right @2xl:table-cell">
                  <RuleRateCell rule={rule} />
                </TableCell>
                <TableCell className="hidden text-right @6xl:table-cell">
                  <RuleTrafficCell rule={rule} />
                </TableCell>
                <TableCell className="pr-4 text-right">
                  <RuleRowActions rule={rule} />
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={t("rules.batch.deleteTitle", { count: selectedIds.length })}
        description={t("rules.batch.deleteDescription")}
        confirmLabel={t("common.actions.delete")}
        onConfirm={() => runBatch("delete")}
      />
    </div>
  )
}
