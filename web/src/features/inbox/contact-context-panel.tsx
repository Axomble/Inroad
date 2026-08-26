import { skipToken } from '@reduxjs/toolkit/query'
import { Link } from '@tanstack/react-router'
import { ExternalLink, Ban } from 'lucide-react'
import { Section, MutedEmpty, InlineLoading, TruncationNotice } from '@/components/shared/record-page'
// Cross-feature READ-ONLY query hook — see the note in
// contact-engagement-strip.tsx. Hooks only; contacts' own components stay put.
import { useGetContactQuery, type ContactDetail } from '@/features/contacts/api'
// features/records is the designated neutral home for what every record type
// shares (CLAUDE.md, "Sharing frontend UI"): these components carry their own
// composers, loading, error and empty states, so notes and tasks arrive with
// one-click add already built.
import { NotesPanel } from '@/features/records/notes-panel'
import { TasksPanel } from '@/features/records/tasks-panel'
import { recordErrorMessage } from '@/features/records/error-copy'
import { httpStatus } from '@/lib/rtk-error'
import { ContactDealChip } from './contact-deal-chip'
import { ContactEngagementStrip } from './contact-engagement-strip'

/**
 * Who the sender actually is, beside the thread: identity, engagement, open
 * deals, notes and tasks — so triaging a reply does not mean opening a second
 * tab and losing the thread.
 *
 * `contactId` is nullable because a thread's is: a legacy direct-send match has
 * no contact to resolve (see InboxThreadSummary.contact_id). That case renders
 * an explanation rather than an error, because nothing is wrong — there is
 * simply no CRM record behind this reply.
 */
export function ContactContextPanel({ contactId }: { contactId: string | null | undefined }) {
  // skipToken rather than a `skip` option so the arg type stays non-nullable —
  // the hook is never called with a null id at all.
  const { data, isLoading, error } = useGetContactQuery(contactId ? { id: contactId } : skipToken)

  if (!contactId) {
    return (
      <PanelShell>
        <MutedEmpty text="This reply isn't linked to a contact, so there's nothing to show here." />
      </PanelShell>
    )
  }

  if (isLoading) {
    return (
      <PanelShell>
        <InlineLoading label="Loading contact" />
      </PanelShell>
    )
  }

  // A 404 means the contact was deleted after the thread was recorded — the
  // thread's contact_id is ON DELETE SET NULL at the DB level, but a page held
  // open across the deletion can still be holding the old id. Explained rather
  // than presented as a failure.
  if (httpStatus(error) === 404) {
    return (
      <PanelShell>
        <MutedEmpty text="This contact has been deleted." />
      </PanelShell>
    )
  }

  if (error !== undefined || !data) {
    return (
      <PanelShell>
        <p role="status" className="text-[11px] text-warn">
          {recordErrorMessage(error, "This contact couldn't be loaded.")}
        </p>
      </PanelShell>
    )
  }

  return (
    <PanelShell>
      <ContactIdentity contact={data} />

      <Section title="Engagement">
        <ContactEngagementStrip contactId={contactId} />
      </Section>

      <Section title={data.deal_count === 1 ? '1 deal' : `${data.deal_count} deals`}>
        {data.deals.length === 0 ? (
          <MutedEmpty text="No deals yet." />
        ) : (
          <>
            <ul>
              {data.deals.map((deal) => (
                <ContactDealChip key={deal.id} deal={deal} />
              ))}
            </ul>
            {/* The list is capped server-side; say so rather than let the rail
                imply this is all of them. */}
            {data.deals_truncated && <TruncationNotice noun="deals" shown={data.deals.length} />}
          </>
        )}
      </Section>

      {/* Both panels own their composer, list, and every load/error/empty state
          — nothing to reimplement, and a note added here refreshes the contact
          record page too, because they share the same cache tags. */}
      <NotesPanel targetType="contact" targetId={contactId} />
      <TasksPanel targetType="contact" targetId={contactId} />
    </PanelShell>
  )
}

/** The rail's own scroll container and spacing, shared by every state above. */
function PanelShell({ children }: { children: React.ReactNode }) {
  return (
    <aside
      aria-label="Contact context"
      className="flex w-full shrink-0 flex-col gap-3 overflow-y-auto border-t border-border p-4 xl:w-72 xl:border-t-0 xl:border-l"
    >
      {children}
    </aside>
  )
}

/** Name, email, role, company, and the one thing that changes what you can do:
 * whether this address can be mailed at all. */
function ContactIdentity({ contact }: { contact: ContactDetail }) {
  const name = fullName(contact)

  return (
    <div className="space-y-1.5">
      <div className="min-w-0">
        <Link
          to="/app/contacts/$id"
          params={{ id: contact.id }}
          className="flex min-w-0 items-center gap-1 text-sm font-semibold text-foreground underline-offset-2 hover:text-accent-ink hover:underline"
        >
          <span className="truncate">{name}</span>
          <ExternalLink className="size-3 shrink-0" aria-hidden="true" />
        </Link>
        {/* The email is shown even when it IS the display name, so the row
            never leaves you guessing which address this thread is with. */}
        <p className="truncate text-[11px] text-muted-foreground">{contact.email}</p>
      </div>

      {contact.job_title && <p className="truncate text-[11px] text-muted-foreground">{contact.job_title}</p>}

      {contact.company && (
        <Link
          to="/app/companies/$id"
          params={{ id: contact.company.id }}
          className="block truncate text-[11px] text-muted-foreground underline-offset-2 hover:text-accent-ink hover:underline"
        >
          {contact.company.name}
        </Link>
      )}

      {contact.linkedin_url && (
        <a
          href={contact.linkedin_url}
          target="_blank"
          // noreferrer as well as noopener: an outbound link from an operator's
          // inbox should not leak which thread they were reading.
          rel="noopener noreferrer"
          className="block truncate text-[11px] text-muted-foreground underline-offset-2 hover:text-accent-ink hover:underline"
        >
          LinkedIn
        </a>
      )}

      {/* Suppression is the one fact here that changes what the operator may DO,
          so it is called out rather than listed. is_primary_email distinguishes
          "this person cannot be mailed" from "an alias of theirs is suppressed",
          which are materially different situations. */}
      {contact.suppression && (
        <p
          role="status"
          className="flex items-start gap-1 rounded-md bg-warn/10 px-1.5 py-1 text-[10px] text-warn"
        >
          <Ban className="mt-px size-2.5 shrink-0" aria-hidden="true" />
          <span>
            {contact.suppression.is_primary_email
              ? `Suppressed (${contact.suppression.reason}) — replies to this address will not send.`
              : `An alias of this contact is suppressed (${contact.suppression.reason}).`}
          </span>
        </p>
      )}
    </div>
  )
}

/**
 * The contact's display name, falling back to the email when unnamed.
 * Reimplemented rather than imported: contacts' own `fullName` is a private
 * helper in its page component, and a one-line join is not worth a shared
 * module.
 */
function fullName(contact: ContactDetail): string {
  const joined = [contact.first_name, contact.last_name].filter(Boolean).join(' ').trim()
  return joined || contact.email
}
