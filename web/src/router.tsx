import { lazy } from "react"
import { createBrowserRouter } from "react-router"

import { AppLayout } from "@/components/layout/app-layout"
import { AuthGate } from "@/components/layout/auth-gate"
import { RouteError } from "@/components/layout/route-error"
import type { RouteHandle } from "@/components/layout/site-header"

const LoginPage = lazy(() => import("@/pages/login"))
const SetupPage = lazy(() => import("@/pages/setup"))
const DashboardPage = lazy(() => import("@/pages/dashboard"))
const RulesPage = lazy(() => import("@/pages/rules"))
const RuleDetailPage = lazy(() => import("@/pages/rule-detail"))
const ConnectionsPage = lazy(() => import("@/pages/connections"))
const TrafficPage = lazy(() => import("@/pages/traffic"))
const LogsPage = lazy(() => import("@/pages/logs"))
const SettingsPage = lazy(() => import("@/pages/settings"))
const NotFoundPage = lazy(() => import("@/pages/not-found"))

const handle = (h: RouteHandle) => h

export const router = createBrowserRouter([
  {
    element: <AuthGate />,
    errorElement: <RouteError />,
    children: [
      { path: "setup", element: <SetupPage /> },
      { path: "login", element: <LoginPage /> },
      {
        element: <AppLayout />,
        errorElement: <RouteError />,
        children: [
          {
            index: true,
            element: <DashboardPage />,
            handle: handle({ section: "shell.nav.dashboard" }),
          },
          {
            path: "rules",
            element: <RulesPage />,
            handle: handle({ section: "shell.nav.rules" }),
          },
          {
            path: "rules/:id",
            element: <RuleDetailPage />,
            handle: handle({
              section: "shell.nav.rules",
              sectionPath: "/rules",
              ruleCrumb: true,
            }),
          },
          {
            path: "connections",
            element: <ConnectionsPage />,
            handle: handle({ section: "shell.nav.connections" }),
          },
          {
            path: "traffic",
            element: <TrafficPage />,
            handle: handle({ section: "shell.nav.traffic" }),
          },
          {
            path: "logs",
            element: <LogsPage />,
            handle: handle({ section: "shell.nav.logs" }),
          },
          {
            path: "settings/:tab?",
            element: <SettingsPage />,
            handle: handle({ section: "shell.nav.settings" }),
          },
          { path: "*", element: <NotFoundPage /> },
        ],
      },
    ],
  },
])
