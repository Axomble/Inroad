import { createFileRoute } from '@tanstack/react-router'
import { AiSettingsPage } from '@/features/ai-settings/ai-settings-page'

export const Route = createFileRoute('/app/settings/ai')({
  component: AiSettingsPage,
})
