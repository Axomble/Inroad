import { createFileRoute } from '@tanstack/react-router'
import { ContactDetailPage } from '@/features/contacts/contact-detail-page'

export const Route = createFileRoute('/app/contacts/$id')({ component: ContactRoute })

function ContactRoute() {
  const { id } = Route.useParams()
  return <ContactDetailPage contactId={id} />
}
