import { createFileRoute } from '@tanstack/react-router'
import { DeliverabilityPage } from '@/features/deliverability/deliverability-page'

// autoCodeSplitting (vite tanstackRouter plugin) puts this route's component in
// its own chunk, and the page lazy-loads the chart on top of that — neither
// reaches a user who never opens this screen.
export const Route = createFileRoute('/app/deliverability')({
  component: DeliverabilityPage,
})
