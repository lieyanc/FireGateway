import { PageContainer, PageHeader } from "@/components/common/page-header"
import { ConnectionsPanel } from "@/features/connections/connections-panel"
import { useI18n } from "@/i18n"

export default function ConnectionsPage() {
  const { t } = useI18n()
  return (
    <PageContainer>
      <PageHeader
        title={t("connections.title")}
        description={t("connections.description")}
      />
      <ConnectionsPanel />
    </PageContainer>
  )
}
