import { createFileRoute } from '@tanstack/react-router'
import { DeadLettersPage } from '@/features/dead-letters/dead-letters-page'

export const Route = createFileRoute('/app/settings/dead-letters')({
  component: DeadLettersPage,
})
