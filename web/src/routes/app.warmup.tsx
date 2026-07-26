import { createFileRoute } from '@tanstack/react-router'
import { WarmupPage } from '@/features/warmup/warmup-page'

// autoCodeSplitting (vite tanstackRouter plugin) splits this route's component
// into its own chunk — the warmup feature (and its lazy sparkline) never loads
// until a user navigates here.
export const Route = createFileRoute('/app/warmup')({
  component: WarmupPage,
})
