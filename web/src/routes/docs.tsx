import { createFileRoute } from '@tanstack/react-router'
import { DocsHubPage } from '@/features/docs/docs-hub-page'

export const Route = createFileRoute('/docs')({
  component: DocsHubPage,
})
