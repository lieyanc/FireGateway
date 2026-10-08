import { useNavigate, useParams } from "react-router"
import { BuildingIcon, UserIcon, type LucideIcon } from "lucide-react"

import { PageContainer, PageHeader } from "@/components/common/page-header"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { TenantsTab } from "@/features/access/tenants-tab"
import { UsersTab } from "@/features/access/users-tab"
import { useI18n } from "@/i18n"
import { useAuthState, useSession } from "@/lib/queries"
import NotFoundPage from "@/pages/not-found"

type Tab = "users" | "tenants"

const TABS: { value: Tab; icon: LucideIcon; Content: () => React.ReactNode }[] = [
  { value: "users", icon: UserIcon, Content: UsersTab },
  { value: "tenants", icon: BuildingIcon, Content: TenantsTab },
]

function isTab(value: string | undefined): value is Tab {
  return TABS.some((tab) => tab.value === value)
}

export default function AccessPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const params = useParams()
  const auth = useAuthState()
  const { isAdmin } = useSession()
  const tab: Tab = isTab(params.tab) ? params.tab : "users"

  // Right after sign-in the role is still loading; wait rather than flash 404.
  if (!auth.data?.role) return null
  if (!isAdmin) return <NotFoundPage />

  return (
    <PageContainer className="max-w-5xl">
      <PageHeader title={t("access.title")} description={t("access.description")} />
      <Tabs
        value={tab}
        onValueChange={(value) => navigate(`/access/${value}`, { replace: true })}
        className="gap-4"
      >
        <div className="-mx-4 overflow-x-auto px-4 pb-1 md:mx-0 md:px-0">
          <TabsList>
            {TABS.map(({ value, icon: Icon }) => (
              <TabsTrigger key={value} value={value} className="px-2.5">
                <Icon data-icon="inline-start" />
                {t(`access.tabs.${value}`)}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>
        {TABS.map(({ value, Content }) => (
          <TabsContent key={value} value={value}>
            <Content />
          </TabsContent>
        ))}
      </Tabs>
    </PageContainer>
  )
}
