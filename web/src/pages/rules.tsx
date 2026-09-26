import * as React from "react"
import {
  ArrowLeftRightIcon,
  DownloadIcon,
  FilterXIcon,
  PlusIcon,
  SearchIcon,
  UploadIcon,
} from "lucide-react"
import { toast } from "sonner"

import { PageContainer, PageHeader } from "@/components/common/page-header"
import { QueryError } from "@/components/common/query-state"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { ImportRulesDialog } from "@/features/rules/import-dialog"
import { RuleEditorProvider, useRuleEditor } from "@/features/rules/rule-editor"
import { RulesTable } from "@/features/rules/rules-table"
import { dateStamp, matchesSearch, RULE_STATES } from "@/features/rules/utils"
import { useI18n } from "@/i18n"
import { api, downloadJson } from "@/lib/api"
import { toastError } from "@/lib/errors"
import { useRules } from "@/lib/queries"
import type { RuleState, RuleType } from "@/lib/types"

const ALL = "all"

export default function RulesPage() {
  return (
    <RuleEditorProvider>
      <RulesPageContent />
    </RuleEditorProvider>
  )
}

function RulesPageContent() {
  const { t } = useI18n()
  const editor = useRuleEditor()
  const rules = useRules()
  const [search, setSearch] = React.useState("")
  const [type, setType] = React.useState<RuleType | typeof ALL>(ALL)
  const [state, setState] = React.useState<RuleState | typeof ALL>(ALL)
  const [importOpen, setImportOpen] = React.useState(false)
  const [exporting, setExporting] = React.useState(false)

  const items = React.useMemo(() => rules.data ?? [], [rules.data])
  const filtered = React.useMemo(
    () =>
      items.filter(
        (r) =>
          (type === ALL || r.type === type) &&
          (state === ALL || r.runtime.state === state) &&
          matchesSearch(r, search)
      ),
    [items, type, state, search]
  )
  const filtering = search.trim() !== "" || type !== ALL || state !== ALL

  const clearFilters = () => {
    setSearch("")
    setType(ALL)
    setState(ALL)
  }

  const exportRules = async () => {
    setExporting(true)
    try {
      const data = await api.rules.export()
      downloadJson(`firegateway-rules-${dateStamp()}.json`, data)
      toast.success(t("rules.toast.exported", { count: data.forward.length }))
    } catch (error) {
      toastError(error, t)
    } finally {
      setExporting(false)
    }
  }

  return (
    <PageContainer>
      <PageHeader
        title={t("rules.title")}
        description={t("rules.description")}
        actions={
          <>
            <Button variant="outline" onClick={() => setImportOpen(true)}>
              <UploadIcon data-icon="inline-start" />
              {t("common.actions.import")}
            </Button>
            <Button
              variant="outline"
              disabled={exporting}
              onClick={() => void exportRules()}
            >
              {exporting ? (
                <Spinner data-icon="inline-start" />
              ) : (
                <DownloadIcon data-icon="inline-start" />
              )}
              {t("common.actions.export")}
            </Button>
            <Button onClick={editor.create}>
              <PlusIcon data-icon="inline-start" />
              {t("rules.newRule")}
            </Button>
          </>
        }
      />

      <div className="flex flex-wrap items-center gap-2">
        <InputGroup className="w-full sm:w-72">
          <InputGroupAddon>
            <SearchIcon />
          </InputGroupAddon>
          <InputGroupInput
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("rules.searchPlaceholder")}
            aria-label={t("rules.searchPlaceholder")}
          />
        </InputGroup>
        <Select value={type} onValueChange={(v) => setType(v as RuleType | typeof ALL)}>
          <SelectTrigger className="w-[calc(50%-0.25rem)] sm:w-36" aria-label={t("rules.filters.type")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value={ALL}>{t("rules.filters.allTypes")}</SelectItem>
            </SelectGroup>
            <SelectSeparator />
            <SelectGroup>
              <SelectItem value="tcp">{t("common.ruleType.tcp")}</SelectItem>
              <SelectItem value="udp">{t("common.ruleType.udp")}</SelectItem>
            </SelectGroup>
          </SelectContent>
        </Select>
        <Select value={state} onValueChange={(v) => setState(v as RuleState | typeof ALL)}>
          <SelectTrigger className="w-[calc(50%-0.25rem)] sm:w-40" aria-label={t("rules.filters.state")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value={ALL}>{t("rules.filters.allStates")}</SelectItem>
            </SelectGroup>
            <SelectSeparator />
            <SelectGroup>
              {RULE_STATES.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`common.ruleState.${s}`)}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        {filtering && (
          <Button variant="ghost" size="sm" onClick={clearFilters}>
            <FilterXIcon data-icon="inline-start" />
            {t("rules.filters.clear")}
          </Button>
        )}
        {rules.data && (
          <span className="text-sm text-muted-foreground tabular-nums sm:ml-auto">
            {filtering
              ? t("rules.countFiltered", { shown: filtered.length, count: items.length })
              : t("rules.count", { count: items.length })}
          </span>
        )}
      </div>

      {rules.isError && !rules.data ? (
        <QueryError error={rules.error} onRetry={() => void rules.refetch()} />
      ) : (
        <Card className="gap-0 py-0">
          {rules.isPending ? (
            <div className="flex flex-col gap-2 p-4">
              {Array.from({ length: 6 }, (_, i) => (
                <Skeleton key={i} className="h-10" />
              ))}
            </div>
          ) : items.length === 0 ? (
            <Empty className="py-16">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <ArrowLeftRightIcon />
                </EmptyMedia>
                <EmptyTitle>{t("rules.empty")}</EmptyTitle>
                <EmptyDescription>{t("rules.emptyHint")}</EmptyDescription>
              </EmptyHeader>
              <EmptyContent className="flex-row justify-center">
                <Button onClick={editor.create}>
                  <PlusIcon data-icon="inline-start" />
                  {t("rules.newRule")}
                </Button>
                <Button variant="outline" onClick={() => setImportOpen(true)}>
                  <UploadIcon data-icon="inline-start" />
                  {t("common.actions.import")}
                </Button>
              </EmptyContent>
            </Empty>
          ) : filtered.length === 0 ? (
            <Empty className="py-16">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <SearchIcon />
                </EmptyMedia>
                <EmptyTitle>{t("common.states.noResults")}</EmptyTitle>
                <EmptyDescription>{t("common.states.noResultsHint")}</EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Button variant="outline" onClick={clearFilters}>
                  <FilterXIcon data-icon="inline-start" />
                  {t("rules.filters.clear")}
                </Button>
              </EmptyContent>
            </Empty>
          ) : (
            <RulesTable rules={filtered} />
          )}
        </Card>
      )}

      <ImportRulesDialog open={importOpen} onOpenChange={setImportOpen} />
    </PageContainer>
  )
}
