import { useNavigate, useParams } from "react-router"
import {
  DownloadIcon,
  NetworkIcon,
  GlobeIcon,
  InfoIcon,
  KeyRoundIcon,
  ServerIcon,
  UserIcon,
  type LucideIcon,
} from "lucide-react"

import { PageContainer, PageHeader } from "@/components/common/page-header"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ClusterTab } from "@/features/settings/cluster-tab"
import { AboutTab } from "@/features/settings/about-tab"
import { AccountTab } from "@/features/settings/account-tab"
import { DnsTab } from "@/features/settings/dns-tab"
import { RestartBanner } from "@/features/settings/restart-banner"
import { SystemTab } from "@/features/settings/system-tab"
import { TokensTab } from "@/features/settings/tokens-tab"
import { UpdateTab } from "@/features/settings/update-tab"
import { useI18n } from "@/i18n"
import { useSession } from "@/lib/queries"

type Tab = "cluster" | "account" | "tokens" | "system" | "dns" | "update" | "about"

type TabDef = {
  value: Tab
  icon: LucideIcon
  Content: () => React.ReactNode
  /** Node-wide settings: the server rejects these for tenant members. */
  adminOnly?: boolean
}

const TABS: TabDef[] = [
  { value: "account", icon: UserIcon, Content: AccountTab },
  { value: "tokens", icon: KeyRoundIcon, Content: TokensTab },
  { value: "system", icon: ServerIcon, Content: SystemTab, adminOnly: true },
  { value: "cluster", icon: NetworkIcon, Content: ClusterTab, adminOnly: true },
  { value: "dns", icon: GlobeIcon, Content: DnsTab, adminOnly: true },
  { value: "update", icon: DownloadIcon, Content: UpdateTab, adminOnly: true },
  { value: "about", icon: InfoIcon, Content: AboutTab },
]

export default function SettingsPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const params = useParams()
  const { isAdmin } = useSession()
  // Hidden tabs are not rendered at all, so their admin-only queries never run.
  const tabs = isAdmin ? TABS : TABS.filter((tab) => !tab.adminOnly)
  const tab: Tab = tabs.find((def) => def.value === params.tab)?.value ?? "account"

  return (
    <PageContainer className="max-w-4xl">
      <PageHeader title={t("settings.title")} description={t("settings.description")} />
      {isAdmin && <RestartBanner />}
      <Tabs
        value={tab}
        onValueChange={(value) => navigate(`/settings/${value}`, { replace: true })}
        className="gap-4"
      >
        <div className="-mx-4 overflow-x-auto px-4 pb-1 md:mx-0 md:px-0">
          <TabsList>
            {tabs.map(({ value, icon: Icon }) => (
              <TabsTrigger key={value} value={value} className="px-2.5">
                <Icon data-icon="inline-start" />
                {t(`settings.tabs.${value}`)}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>
        {tabs.map(({ value, Content }) => (
          <TabsContent key={value} value={value}>
            <Content />
          </TabsContent>
        ))}
      </Tabs>
    </PageContainer>
  )
}
