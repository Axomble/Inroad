import { createFileRoute } from '@tanstack/react-router'
import { ContactsPage } from '@/features/contacts/contacts-page'

export const Route = createFileRoute('/app/contacts')({
  component: ContactsPage,
})
