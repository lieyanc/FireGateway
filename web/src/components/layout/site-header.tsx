import { Link, useMatches, useNavigate, useParams } from "react-router"
import { useQueryClient } from "@tanstack/react-query"
import {
  CheckIcon,
  KeyRoundIcon,
  KeySquareIcon,
  LanguagesIcon,
  LogOutIcon,
  MonitorIcon,
  MoonIcon,
  SunIcon,
  UserIcon,
} from "lucide-react"
import { toast } from "sonner"

import { useTheme, type Theme } from "@/components/theme-provider"
import { Badge } from "@/components/ui/badge"
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Separator } from "@/components/ui/separator"
import { SidebarTrigger } from "@/components/ui/sidebar"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { useNow } from "@/hooks/use-now"
import { LANG_LABELS, LANGS, useI18n, type Lang, type MessageKey } from "@/i18n"
import { api } from "@/lib/api"
import { toastError } from "@/lib/errors"
import { useLiveStatus } from "@/lib/events"
import { useAuthState, useRule, useSession } from "@/lib/queries"
import { signedOut } from "@/lib/session"

export type RouteHandle = {
  /** Sidebar section this route belongs to; shown as the first crumb. */
  section?: MessageKey
  sectionPath?: string
  /** Adds the rule name as a second crumb (rule detail page). */
  ruleCrumb?: boolean
}

function RuleCrumb() {
  const { id = "" } = useParams()
  const rule = useRule(id)
  return <BreadcrumbPage>{rule.data?.name || id}</BreadcrumbPage>
}

function PageBreadcrumb() {
  const { t } = useI18n()
  const matches = useMatches()
  const handle = [...matches]
    .reverse()
    .map((m) => m.handle as RouteHandle | undefined)
    .find((h) => h?.section)

  if (!handle?.section) return null
  const title = t(handle.section)

  return (
    <Breadcrumb className="min-w-0">
      <BreadcrumbList className="flex-nowrap">
        {handle.ruleCrumb ? (
          <>
            <BreadcrumbItem className="hidden sm:inline-flex">
              <BreadcrumbLink asChild>
                <Link to={handle.sectionPath ?? "/"}>{title}</Link>
              </BreadcrumbLink>
            </BreadcrumbItem>
            <BreadcrumbSeparator className="hidden sm:block" />
            <BreadcrumbItem className="min-w-0 truncate">
              <RuleCrumb />
            </BreadcrumbItem>
          </>
        ) : (
          <BreadcrumbItem>
            <BreadcrumbPage>{title}</BreadcrumbPage>
          </BreadcrumbItem>
        )}
      </BreadcrumbList>
    </Breadcrumb>
  )
}

function LiveIndicator() {
  const { t } = useI18n()
  const { status, retryAt, reconnectNow } = useLiveStatus()
  const now = useNow(1000, status === "reconnecting")

  let label: string
  let hint: string
  let variant: "success" | "warning" | "secondary"
  switch (status) {
    case "open":
      label = t("shell.live.connected")
      hint = t("shell.live.connectedHint")
      variant = "success"
      break
    case "reconnecting":
      label = t("shell.live.reconnecting")
      hint = t("shell.live.reconnectingHint", {
        seconds: Math.max(0, Math.ceil(((retryAt ?? now) - now) / 1000)),
      })
      variant = "warning"
      break
    case "connecting":
      label = t("shell.live.connecting")
      hint = t("shell.live.connectingHint")
      variant = "secondary"
      break
    default:
      label = t("shell.live.offline")
      hint = t("shell.live.offline")
      variant = "secondary"
  }

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge
          variant={variant}
          role="status"
          aria-live="polite"
          asChild={status === "reconnecting"}
        >
          {status === "reconnecting" ? (
            <button type="button" onClick={reconnectNow}>
              <span className="size-1.5 rounded-full bg-current" />
              {label}
            </button>
          ) : (
            <>
              <span className="relative flex size-1.5">
                {status === "open" && (
                  <span className="absolute inline-flex size-full animate-ping rounded-full bg-current opacity-60" />
                )}
                <span className="relative inline-flex size-1.5 rounded-full bg-current" />
              </span>
              <span className="hidden sm:inline">{label}</span>
            </>
          )}
        </Badge>
      </TooltipTrigger>
      <TooltipContent>{hint}</TooltipContent>
    </Tooltip>
  )
}

function LanguageSwitcher() {
  const { t, lang, setLang } = useI18n()
  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" aria-label={t("shell.language")}>
              <LanguagesIcon />
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>{t("shell.language")}</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>{t("shell.language")}</DropdownMenuLabel>
        <DropdownMenuRadioGroup
          value={lang}
          onValueChange={(value) => setLang(value as Lang)}
        >
          {LANGS.map((l) => (
            <DropdownMenuRadioItem key={l} value={l}>
              {LANG_LABELS[l]}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

const THEME_ICONS = { light: SunIcon, dark: MoonIcon, system: MonitorIcon }

function ThemeToggle() {
  const { t } = useI18n()
  const { theme, resolvedTheme, setTheme } = useTheme()
  const Icon = resolvedTheme === "dark" ? MoonIcon : SunIcon
  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t("shell.theme.label")}
            >
              <Icon />
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>{t("shell.theme.label")}</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>{t("shell.theme.label")}</DropdownMenuLabel>
        <DropdownMenuGroup>
          {(["light", "dark", "system"] as Theme[]).map((value) => {
            const ItemIcon = THEME_ICONS[value]
            return (
              <DropdownMenuItem key={value} onSelect={() => setTheme(value)}>
                <ItemIcon />
                {t(`shell.theme.${value}`)}
                {theme === value && <CheckIcon className="ml-auto" />}
              </DropdownMenuItem>
            )
          })}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function UserMenu() {
  const { t } = useI18n()
  const auth = useAuthState()
  const { isAdmin, tenantId, tenantName } = useSession()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const username = auth.data?.username ?? ""

  const logout = async () => {
    try {
      await api.auth.logout()
    } catch (error) {
      toastError(error, t)
      return
    }
    await queryClient.cancelQueries()
    signedOut()
    toast.success(t("shell.user.loggedOut"))
    navigate("/login", { replace: true })
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="sm" aria-label={t("shell.user.menu")}>
          <UserIcon data-icon="inline-start" />
          <span className="hidden max-w-32 truncate md:inline">{username}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-48">
        <DropdownMenuLabel className="flex flex-col">
          <span className="text-xs font-normal text-muted-foreground">
            {t("shell.user.signedInAs")}
          </span>
          <span className="truncate">{username}</span>
          {auth.data?.role && (
            <span className="truncate text-xs font-normal text-muted-foreground">
              {isAdmin
                ? t("shell.user.admin")
                : t("shell.user.member", { tenant: tenantName || tenantId || "–" })}
            </span>
          )}
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuItem asChild>
            <Link to="/settings/account">
              <KeyRoundIcon />
              {t("shell.user.changePassword")}
            </Link>
          </DropdownMenuItem>
          <DropdownMenuItem asChild>
            <Link to="/settings/tokens">
              <KeySquareIcon />
              {t("shell.user.apiTokens")}
            </Link>
          </DropdownMenuItem>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuItem variant="destructive" onSelect={() => void logout()}>
            <LogOutIcon />
            {t("shell.user.logout")}
          </DropdownMenuItem>
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export function SiteHeader() {
  const { t } = useI18n()
  return (
    <header className="sticky top-0 z-10 flex h-14 shrink-0 items-center gap-2 border-b bg-background/95 px-3 backdrop-blur supports-[backdrop-filter]:bg-background/80 md:px-4">
      <SidebarTrigger className="-ml-1" aria-label={t("shell.toggleSidebar")} />
      <Separator orientation="vertical" className="mr-1 data-[orientation=vertical]:h-4" />
      <PageBreadcrumb />
      <div className="ml-auto flex items-center gap-1">
        <LiveIndicator />
        <LanguageSwitcher />
        <ThemeToggle />
        <UserMenu />
      </div>
    </header>
  )
}
