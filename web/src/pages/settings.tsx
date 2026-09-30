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

type Tab = "cluster" | "account" | "tokens" | "system" | "dns" | "update" | "about"

const TABS: { value: Tab; icon: LucideIcon; Content: () => React.ReactNode }[] = [
  { value: "account", icon: UserIcon, Content: AccountTab },
  { value: "tokens", icon: KeyRoundIcon, Content: TokensTab },
  { value: "system", icon: ServerIcon, Content: SystemTab },
  { value: "cluster", icon: NetworkIcon, Content: ClusterTab },
  { value: "dns", icon: GlobeIcon, Content: DnsTab },
  { value: "update", icon: DownloadIcon, Content: UpdateTab },
  { value: "about", icon: InfoIcon, Content: AboutTab },
]

function isTab(value: string | undefined): value is Tab {
  return TABS.some((tab) => tab.value === value)
}

export default function SettingsPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const params = useParams()
  const tab: Tab = isTab(params.tab) ? params.tab : "account"

  return (
    <PageContainer className="max-w-4xl">
      <PageHeader title={t("settings.title")} description={t("settings.description")} />
      <RestartBanner />
      <Tabs
        value={tab}
        onValueChange={(value) => navigate(`/settings/${value}`, { replace: true })}
        className="gap-4"
      >
        <div className="-mx-4 overflow-x-auto px-4 pb-1 md:mx-0 md:px-0">
          <TabsList>
            {TABS.map(({ value, icon: Icon }) => (
              <TabsTrigger key={value} value={value} className="px-2.5">
                <Icon data-icon="inline-start" />
                {t(`settings.tabs.${value}`)}
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
