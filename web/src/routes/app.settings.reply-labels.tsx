import { createFileRoute } from '@tanstack/react-router'
import { ReplyLabelsPanel } from '@/features/reply-labels/reply-labels-panel'

export const Route = createFileRoute('/app/settings/reply-labels')({
  component: ReplyLabelsPanel,
})
