import { createFileRoute } from '@tanstack/react-router'
import { CustomFieldsPanel } from '@/features/contacts/custom-fields-panel'

export const Route = createFileRoute('/app/settings/custom-fields')({
  component: CustomFieldsPanel,
})
