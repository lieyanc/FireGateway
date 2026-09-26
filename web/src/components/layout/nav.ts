import {
  ActivityIcon,
  ArrowLeftRightIcon,
  ChartColumnIcon,
  LayoutDashboardIcon,
  ScrollTextIcon,
  SettingsIcon,
  type LucideIcon,
} from "lucide-react"

import type { MessageKey } from "@/i18n"

type NavItem = { to: string; label: MessageKey; icon: LucideIcon }

export const NAV_ITEMS: NavItem[] = [
  { to: "/", label: "shell.nav.dashboard", icon: LayoutDashboardIcon },
  { to: "/rules", label: "shell.nav.rules", icon: ArrowLeftRightIcon },
  { to: "/connections", label: "shell.nav.connections", icon: ActivityIcon },
  { to: "/traffic", label: "shell.nav.traffic", icon: ChartColumnIcon },
  { to: "/logs", label: "shell.nav.logs", icon: ScrollTextIcon },
  { to: "/settings", label: "shell.nav.settings", icon: SettingsIcon },
]

export function isNavActive(pathname: string, to: string) {
  if (to === "/") return pathname === "/"
  return pathname === to || pathname.startsWith(to + "/")
}
