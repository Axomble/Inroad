import { useId, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Loader2, Pencil } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
import { httpStatus, serverDetail } from '@/lib/rtk-error'
import { recordErrorMessage } from '@/features/records/error-copy'
import { listPageSize } from '@/features/records/query-args'
// Read-only RTK Query hook from another feature's `api.ts`, which is the one
// cross-feature import CLAUDE.md allows (hooks only, never UI): the company list
// is CRM's endpoint, and there is nowhere neutral for it to live.
import { useCrmListCompaniesQuery } from '@/features/crm/api'
import { useSetContactCompanyMutation, type ContactCompany } from './api'

/**
 * Which company this contact belongs to, and the control that changes it.
 *
 * This is the only thing in the product that writes `contacts.company_id`, so
 * without it a contact's company is always null and every company's roster is
 * always empty — the panels either side of this were correct and permanently
 * unpopulated.
 *
 * Note the link is a *stated* fact: creating a deal that names both a company and
 * a contact deliberately does not imply one, because you routinely sell into a
 * company through someone at an agency. The same reason the CSV importer's
 * free-text `company` string is provenance rather than truth — imported contacts
 * arrive unlinked and are linked here.
 */
export function CompanyLinkForm({
  contactId,
  company,
}: {
  contactId: string
  company: ContactCompany | null
}) {
  const [editing, setEditing] = useState(false)

  if (!editing) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        {company ? (
          <Link
            to="/app/companies/$id"
            params={{ id: company.id }}
            className="text-accent-ink underline-offset-2 hover:underline"
          >
            {company.name}
          </Link>
        ) : (
          <span className="text-muted-foreground">Not linked</span>
        )}
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => setEditing(true)}
          // Icon-only would be ambiguous next to a company name, and the label
          // has to say which record it edits for anyone hearing it out of context.
          aria-label={company ? `Change the company linked to this contact` : 'Link this contact to a company'}
        >
          <Pencil aria-hidden="true" />
          {company ? 'Change' : 'Link'}
        </Button>
      </div>
    )
  }

  return <CompanyPicker contactId={contactId} company={company} onDone={() => setEditing(false)} />
}

function CompanyPicker({
  contactId,
  company,
  onDone,
}: {
  contactId: string
  company: ContactCompany | null
  onDone: () => void
}) {
  const companiesQuery = useCrmListCompaniesQuery({ limit: listPageSize })
  const [setCompany, state] = useSetContactCompanyMutation()
  const [selected, setSelected] = useState(company?.id ?? '')
  const [error, setError] = useState<string | null>(null)
  const selectId = useId()

  const save = async () => {
    setError(null)
    // Explicitly `null` to unlink — the API rejects an omitted `company_id`
    // rather than let "absent" quietly mean "detach".
    //
    // Result-checked rather than `.unwrap()`-and-catch, which is the convention
    // everywhere else here for a concrete reason: unwrap derives a REJECTING
    // promise, and a rejection that anything fails to observe surfaces as an
    // unhandled rejection — which vitest turns into a non-zero exit even when
    // every test passes. This shape never creates one.
    const result = await setCompany({
      id: contactId,
      contactCompanyLink: { company_id: selected || null },
    })
    if ('error' in result) {
      setError(linkErrorMessage(result.error))
      return
    }
    onDone()
  }

  return (
    <div className="grid gap-2">
      <Label htmlFor={selectId}>Company</Label>
      <Select
        id={selectId}
        value={selected}
        disabled={companiesQuery.isLoading || state.isLoading}
        onChange={(event) => setSelected(event.target.value)}
      >
        <option value="">No company</option>
        {/* The currently-linked company may sit past the page cap, so it is always
            offered — otherwise opening this form would silently propose unlinking. */}
        {company && !(companiesQuery.data?.items ?? []).some(({ id }) => id === company.id) ? (
          <option value={company.id}>{company.name}</option>
        ) : null}
        {(companiesQuery.data?.items ?? []).map((option) => (
          <option key={option.id} value={option.id}>{option.name}</option>
        ))}
      </Select>
      {companiesQuery.isError ? (
        <p role="alert" className="text-xs text-danger">
          The company list could not be loaded, so there is nothing to choose from yet.
        </p>
      ) : null}
      {error ? <p role="alert" className="text-xs text-danger">{error}</p> : null}
      <div className="flex items-center gap-2">
        <Button type="button" variant="primary" size="sm" onClick={() => void save()} disabled={state.isLoading}>
          {state.isLoading ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
          Save
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={onDone} disabled={state.isLoading}>
          Cancel
        </Button>
      </div>
    </div>
  )
}

/**
 * The endpoint answers 404 for a missing *company* and for a missing *contact*,
 * distinguishably on purpose — they need opposite responses from the reader, so
 * collapsing them into "not found" would waste that.
 */
function linkErrorMessage(error: unknown): string {
  if (httpStatus(error) === 404) {
    const detail = serverDetail(error) ?? ''
    if (/company/i.test(detail)) return 'That company no longer exists. Reload the page to refresh the list.'
    if (/contact/i.test(detail)) return 'This contact no longer exists — it may have been deleted.'
  }
  return recordErrorMessage(error, 'The company link could not be saved. Try again.')
}
