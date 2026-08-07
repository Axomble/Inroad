import { Page, PageTopbar, PageBody } from '@/components/layout/page'
import { QuickStartSection } from './quick-start-section'
import { McpSection } from './mcp-section'
import { EnvVarsSection } from './env-vars-section'
import { GuidesSection } from './guides-section'

/**
 * Docs hub, rendered by both /docs (public) and /app/docs. A deliberately small
 * shell over four fact-checked sections — every claim on this page is
 * transcribed from the code it documents (see docs-data.ts for the mapping),
 * replacing the earlier aspirational page whose tool schemas, deploy targets,
 * and integrations did not exist.
 */
export function DocsHubPage() {
  return (
    <Page>
      <PageTopbar
        eyebrow="Documentation"
        title="Docs & MCP"
        subtitle="Run Inroad, configure it, and connect AI agents to it"
      />
      <PageBody>
        <QuickStartSection />
        <McpSection />
        <EnvVarsSection />
        <GuidesSection />
      </PageBody>
    </Page>
  )
}
