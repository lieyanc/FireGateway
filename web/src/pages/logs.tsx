import * as React from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useVirtualizer } from "@tanstack/react-virtual"
import {
  ArrowDownToLineIcon,
  CircleAlertIcon,
  EraserIcon,
  PauseIcon,
  PlayIcon,
  ScrollTextIcon,
  SearchIcon,
} from "lucide-react"
import { toast } from "sonner"

import { PageContainer, PageHeader } from "@/components/common/page-header"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Empty,
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
  SelectLabel,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Separator } from "@/components/ui/separator"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { MAX_LOG_ENTRIES, useLogTail, type TailStatus } from "@/features/log-tail/use-log-tail"
import { useI18n, type MessageKey } from "@/i18n"
import { api } from "@/lib/api"
import { errorMessage, toastError } from "@/lib/errors"
import { qk, useLogLevel } from "@/lib/queries"
import type { LogEntry, LogLevel } from "@/lib/types"

const LEVELS = ["error", "warn", "info", "debug", "trace"] as const
type ViewLevel = (typeof LEVELS)[number]
const DEFAULT = "__default__"

const LEVEL_VARIANT: Record<
  LogLevel,
  "destructive" | "warning" | "secondary" | "outline" | "ghost"
> = {
  ERROR: "destructive",
  WARN: "warning",
  INFO: "secondary",
  DEBUG: "outline",
  TRACE: "ghost",
}

function levelKey(level: string): MessageKey | null {
  const lower = level.toLowerCase()
  return (LEVELS as readonly string[]).includes(lower)
    ? (`logs.levels.${lower as ViewLevel}` as const)
    : null
}

function useLevelLabel() {
  const { t } = useI18n()
  return (level: string) => {
    const key = levelKey(level)
    return key ? t(key) : level
  }
}

function ServerLevelSelect() {
  const { t } = useI18n()
  const label = useLevelLabel()
  const queryClient = useQueryClient()
  const query = useLogLevel()
  const levels = query.data?.levels?.length ? query.data.levels : [...LEVELS]

  const mutation = useMutation({
    mutationFn: (level: string) => api.logs.setLevel(level),
    onSuccess: (res) => {
      queryClient.setQueryData(qk.logLevel, (old: typeof query.data) =>
        old ? { ...old, level: res.level } : old
      )
      void queryClient.invalidateQueries({ queryKey: qk.settings })
      toast.success(t("logs.serverLevelChanged", { level: label(res.level) }))
    },
    onError: (error) => toastError(error, t),
  })

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div className="flex items-center gap-2">
          <span className="hidden text-sm text-muted-foreground sm:inline">
            {t("logs.serverLevel")}
          </span>
          <Select
            value={query.data?.level ?? ""}
            onValueChange={(v) => mutation.mutate(v)}
            disabled={!query.data || mutation.isPending}
          >
            <SelectTrigger className="w-32" aria-label={t("logs.serverLevel")}>
              <SelectValue placeholder="…" />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {levels.map((l) => (
                  <SelectItem key={l} value={l}>
                    {label(l)}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
      </TooltipTrigger>
      <TooltipContent>{t("logs.serverLevelHint")}</TooltipContent>
    </Tooltip>
  )
}

function StatusBadge({ status, paused }: { status: TailStatus; paused: boolean }) {
  const { t } = useI18n()
  if (paused) return <Badge variant="secondary">{t("logs.status.paused")}</Badge>
  if (status === "open") {
    return (
      <Badge variant="success">
        <span className="size-1.5 animate-pulse rounded-full bg-current" />
        {t("logs.status.live")}
      </Badge>
    )
  }
  return (
    <Badge variant={status === "reconnecting" ? "warning" : "secondary"}>
      {t(`logs.status.${status}`)}
    </Badge>
  )
}

function matches(entry: LogEntry, needle: string) {
  if (entry.msg.toLowerCase().includes(needle)) return true
  if (!entry.attrs) return false
  for (const [k, v] of Object.entries(entry.attrs)) {
    if (k.toLowerCase().includes(needle) || v.toLowerCase().includes(needle)) {
      return true
    }
  }
  return false
}

const LogRow = React.memo(function LogRow({
  entry,
  timeFormat,
}: {
  entry: LogEntry
  timeFormat: Intl.DateTimeFormat
}) {
  const date = new Date(entry.time)
  return (
    <div className="flex flex-wrap items-start gap-x-2 gap-y-1 border-b border-border/50 px-3 py-1.5 font-mono text-xs leading-5 hover:bg-muted/50">
      <time
        dateTime={entry.time}
        className="shrink-0 text-muted-foreground tabular-nums"
        title={entry.time}
      >
        {Number.isNaN(date.getTime()) ? entry.time : timeFormat.format(date)}
      </time>
      <Badge
        variant={LEVEL_VARIANT[entry.level] ?? "outline"}
        className="w-14 shrink-0 font-mono"
      >
        {entry.level}
      </Badge>
      <span className="min-w-0 flex-1 break-words whitespace-pre-wrap">
        {entry.msg}
        {entry.attrs &&
          Object.entries(entry.attrs).map(([k, v]) => (
            <Badge
              key={k}
              variant="outline"
              className="ml-1.5 h-auto max-w-full font-mono font-normal break-all whitespace-normal"
            >
              <span>
                <span className="text-muted-foreground">{k}=</span>
                {v}
              </span>
            </Badge>
          ))}
      </span>
    </div>
  )
})

function LogViewer({ level }: { level: ViewLevel | undefined }) {
  const { t, fmt } = useI18n()
  const tail = useLogTail(level)
  const [search, setSearch] = React.useState("")
  const deferredSearch = React.useDeferredValue(search.trim().toLowerCase())
  const scrollRef = React.useRef<HTMLDivElement>(null)
  const stickRef = React.useRef(true)
  const [atBottom, setAtBottom] = React.useState(true)

  const visible = React.useMemo(
    () =>
      deferredSearch
        ? tail.entries.filter((e) => matches(e, deferredSearch))
        : tail.entries,
    [tail.entries, deferredSearch]
  )

  const timeFormat = React.useMemo(
    () =>
      new Intl.DateTimeFormat(fmt.locale, {
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        fractionalSecondDigits: 3,
        hour12: false,
      }),
    [fmt.locale]
  )

  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: visible.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => 33,
    overscan: 20,
    getItemKey: (index) => visible[index].seq,
  })

  const scrollToBottom = React.useCallback(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [])

  // Follow new entries while the user is at the bottom.
  React.useLayoutEffect(() => {
    if (stickRef.current && visible.length) {
      virtualizer.scrollToIndex(visible.length - 1, { align: "end" })
      requestAnimationFrame(scrollToBottom)
    }
  }, [visible, virtualizer, scrollToBottom])

  const onScroll = () => {
    const el = scrollRef.current
    if (!el) return
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    stickRef.current = bottom
    if (bottom !== atBottom) setAtBottom(bottom)
  }

  const jumpToLatest = () => {
    stickRef.current = true
    setAtBottom(true)
    if (visible.length) virtualizer.scrollToIndex(visible.length - 1, { align: "end" })
    requestAnimationFrame(scrollToBottom)
  }

  const items = virtualizer.getVirtualItems()

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <InputGroup className="w-full sm:w-72">
          <InputGroupAddon>
            <SearchIcon />
          </InputGroupAddon>
          <InputGroupInput
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("logs.searchPlaceholder")}
            aria-label={t("logs.searchPlaceholder")}
          />
        </InputGroup>
        <Button
          variant="outline"
          size="sm"
          aria-pressed={tail.paused}
          onClick={() => tail.setPaused(!tail.paused)}
        >
          {tail.paused ? (
            <PlayIcon data-icon="inline-start" />
          ) : (
            <PauseIcon data-icon="inline-start" />
          )}
          {tail.paused ? t("common.actions.resume") : t("common.actions.pause")}
          {tail.paused && tail.buffered > 0 && (
            <Badge variant="secondary" className="ml-1 tabular-nums">
              {t("logs.buffered", { count: fmt.number(tail.buffered) })}
            </Badge>
          )}
        </Button>
        <Button variant="outline" size="sm" onClick={tail.clear}>
          <EraserIcon data-icon="inline-start" />
          {t("logs.clear")}
        </Button>
        <div className="flex items-center gap-2 sm:ml-auto">
          <span className="text-sm text-muted-foreground tabular-nums">
            {deferredSearch
              ? t("logs.shown", {
                  shown: fmt.number(visible.length),
                  count: fmt.number(tail.entries.length),
                })
              : t("logs.count", { count: fmt.number(tail.entries.length) })}
          </span>
          <StatusBadge status={tail.status} paused={tail.paused} />
        </div>
      </div>

      {tail.backlogError != null && (
        <Alert variant="destructive">
          <CircleAlertIcon />
          <AlertTitle>{t("logs.backlogFailed")}</AlertTitle>
          <AlertDescription>{errorMessage(tail.backlogError, t)}</AlertDescription>
        </Alert>
      )}

      <Card className="relative gap-0 py-0">
        <div
          ref={scrollRef}
          onScroll={onScroll}
          className="h-[calc(100svh-17rem)] min-h-80 overflow-auto"
          role="log"
          aria-live="off"
          aria-label={t("logs.title")}
        >
          {visible.length === 0 ? (
            <Empty className="h-full">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  {deferredSearch ? <SearchIcon /> : <ScrollTextIcon />}
                </EmptyMedia>
                <EmptyTitle>
                  {deferredSearch ? t("common.states.noResults") : t("logs.empty")}
                </EmptyTitle>
                <EmptyDescription>
                  {deferredSearch
                    ? t("common.states.noResultsHint")
                    : t("logs.emptyHint")}
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
              <div
                className="absolute inset-x-0 top-0"
                style={{ transform: `translateY(${items[0]?.start ?? 0}px)` }}
              >
                {items.map((item) => (
                  <div
                    key={item.key}
                    data-index={item.index}
                    ref={virtualizer.measureElement}
                  >
                    <LogRow entry={visible[item.index]} timeFormat={timeFormat} />
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
        {!atBottom && visible.length > 0 && (
          <Button
            size="sm"
            variant="secondary"
            className="absolute right-4 bottom-4 shadow-md"
            onClick={jumpToLatest}
          >
            <ArrowDownToLineIcon data-icon="inline-start" />
            {t("logs.jumpToLatest")}
          </Button>
        )}
      </Card>
    </div>
  )
}

export default function LogsPage() {
  const { t, fmt } = useI18n()
  const label = useLevelLabel()
  const [level, setLevel] = React.useState<ViewLevel | undefined>(undefined)

  return (
    <PageContainer>
      <PageHeader
        title={t("logs.title")}
        description={t("logs.description", { limit: fmt.number(MAX_LOG_ENTRIES) })}
        actions={
          <>
            <Select
              value={level ?? DEFAULT}
              onValueChange={(v) => setLevel(v === DEFAULT ? undefined : (v as ViewLevel))}
            >
              <SelectTrigger className="w-40" aria-label={t("logs.viewLevel")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectLabel>{t("logs.viewLevel")}</SelectLabel>
                  <SelectItem value={DEFAULT}>{t("logs.levelDefault")}</SelectItem>
                </SelectGroup>
                <SelectSeparator />
                <SelectGroup>
                  {LEVELS.map((l) => (
                    <SelectItem key={l} value={l}>
                      {label(l)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Separator orientation="vertical" className="hidden h-6 sm:block" />
            <ServerLevelSelect />
          </>
        }
      />
      {/* Remount on level change: the stream and backlog start fresh. */}
      <LogViewer key={level ?? DEFAULT} level={level} />
    </PageContainer>
  )
}
