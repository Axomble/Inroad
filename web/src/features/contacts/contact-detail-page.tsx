import { Link } from '@tanstack/react-router'
import { ArrowLeft, Mail } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Page, PageBody, PageTopbar, Stat, StatStrip } from '@/components/layout/page'
import { formatDateTime } from '@/lib/datetime'
import { httpStatus } from '@/lib/rtk-error'
import { ActivityPanel } from '@/features/records/activity-panel'
import { ContactDealRow } from './contact-deal-row'
import { NotesPanel } from '@/features/records/notes-panel'
import { TasksPanel } from '@/features/records/tasks-panel'
import { useOpenTasks } from '@/features/records/use-open-tasks'
import { recordErrorMessage } from '@/features/records/error-copy'
import {
  Detail,
  MutedEmpty,
  RecordPageMessage,
  RecordPageSkeleton,
  Section,
} from '@/components/shared/record-page'
import { useGetContactQuery, type ContactDetail } from './api'
import { CompanyLinkForm } from './company-link-form'
import { EngagementPanel } from './engagement-panel'
import { ContactCustomFields } from './contact-custom-fields'
import { SuppressionNotice } from './suppression-notice'

/**
 * A contact as a hub: whether they may be emailed at all, what they have done
 * with our mail, the company they belong to, the deals they are on, and the
 * notes, tasks and activity recorded against them.
 *
 * Two requests, on purpose. The detail read is cheap and paints the header; the
 * engagement rollup is four aggregates and owns its own loading state inside
 * `EngagementPanel`, so a slow rollup never holds the record back.
 */
export function ContactDetailPage({ contactId }: { contactId: string }) {
  const contactQuery = useGetContactQuery({ id: contactId })
  const { open: openTasks } = useOpenTasks('contact', contactId)
  const contact = contactQuery.data

  if (contactQuery.isLoading) return <RecordPageSkeleton label="Loading contact" />
  // A failed request is not a deleted contact: a 500 or an offline browser gets a
  // retry, not "this person is gone".
  if (contactQuery.isError && httpStatus(contactQuery.error) !== 404) {
    return (
      <RecordPageMessage
        title="This contact could not be loaded"
        description={recordErrorMessage(contactQuery.error, 'Try again in a moment.')}
        action={<Button onClick={() => void contactQuery.refetch()} disabled={contactQuery.isFetching}>Try again</Button>}
      />
    )
  }
  if (!contact) {
    return (
      <RecordPageMessage
        // The API answers 404 for another workspace's contact as well as for one
        // that never existed, and says so on purpose. "Not found" is the whole
        // message; hinting at access would leak that the id is real.
        title="Contact not found"
        description="It may have been removed, or belong to another workspace."
        action={<Button asChild><Link to="/app/contacts">Back to contacts</Link></Button>}
      />
    )
  }

  return (
    <Page>
      <PageTopbar
        eyebrow="Contact"
        title={fullName(contact) || contact.email}
        subtitle={contact.job_title || undefined}
        actions={
          <Button asChild size="sm">
            <Link to="/app/contacts"><ArrowLeft aria-hidden="true" />Contacts</Link>
          </Button>
        }
      />
      {/* Above the stats and everything else: whether you may email this person
          outranks any number on the page. */}
      {contact.suppression ? <SuppressionNotice suppression={contact.suppression} /> : null}
      <StatStrip>
        {/* `deal_count` is counted independently of the 25-deal cap, so this is
            the true total even when the list below it is short. */}
        <Stat
          label="Deals"
          value={contact.deal_count}
          sub={contact.deals_truncated ? `${contact.deals.length} shown below` : 'on this contact'}
        />
        <Stat label="Next actions" value={openTasks.length} sub="Open or in progress" />
      </StatStrip>
      <PageBody>
        <div className="grid min-w-0 gap-5 p-4 sm:p-5 xl:grid-cols-[minmax(0,1.45fr)_minmax(18rem,0.75fr)]">
          <div className="min-w-0 space-y-5">
            <EngagementPanel contactId={contactId} />
            <Section title="Deals" description="Opportunities this contact is named on.">
              {contact.deals.length === 0 ? (
                <MutedEmpty text="No deals name this contact yet." />
              ) : (
                <ul className="space-y-2">
                  {contact.deals.map((deal) => <ContactDealRow key={deal.id} deal={deal} />)}
                </ul>
              )}
              {/* The cap is a property of the response, so say so — and name the
                  true total, which the server counts uncapped, rather than
                  leaving "and some more" to the reader's imagination. */}
              {contact.deals_truncated ? (
                <p role="status" className="pt-2 text-xs text-muted-foreground">
                  Showing the first {contact.deals.length} of {contact.deal_count} deals, in board order.
                </p>
              ) : null}
            </Section>
            <Section
              title="Custom fields"
              description="Workspace-defined data on this contact, usable in sequences as {{custom.key}}."
            >
              <ContactCustomFields contactId={contactId} />
            </Section>
            <NotesPanel targetType="contact" targetId={contactId} />
          </div>
          <aside className="min-w-0 space-y-5">
            <Section title="Details" description="What this record holds about the person.">
              <dl className="grid gap-3 text-sm">
                <Detail
                  label="Email"
                  value={
                    <a
                      href={`mailto:${contact.email}`}
                      className="inline-flex items-center gap-1.5 break-all text-accent-ink underline-offset-2 hover:underline"
                    >
                      <Mail className="size-3.5 shrink-0" aria-hidden="true" />
                      {contact.email}
                    </a>
                  }
                />
                <Detail
                  label="Company"
                  value={<CompanyLinkForm contactId={contactId} company={contact.company} />}
                />
                <Detail label="Job title" value={contact.job_title || 'Not set'} />
                <Detail
                  label="LinkedIn"
                  value={
                    contact.linkedin_url ? (
                      <a
                        href={contact.linkedin_url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="break-all text-accent-ink underline-offset-2 hover:underline"
                      >
                        View profile
                      </a>
                    ) : (
                      'Not set'
                    )
                  }
                />
                <Detail label="Added" value={formatDateTime(contact.created_at)} />
              </dl>
            </Section>
            <TasksPanel targetType="contact" targetId={contactId} />
            <ActivityPanel targetType="contact" targetId={contactId} />
          </aside>
        </div>
      </PageBody>
    </Page>
  )
}

function fullName(contact: ContactDetail): string {
  return [contact.first_name, contact.last_name].filter(Boolean).join(' ')
}
