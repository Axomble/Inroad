import { useId, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Loader2, Pencil } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Combobox, type ComboboxOption } from '@/components/shared/combobox'
import { useDebouncedInput } from '@/hooks/use-debounced-input'
import { httpStatus, serverDetail } from '@/lib/rtk-error'
import { recordErrorMessage } from '@/features/records/error-copy'
import { useSetContactCompanyMutation, type ContactCompany } from './api'
import { companySearchTerm, minCompanySearchLength, useCompanySearch } from './use-company-search'

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

function companyOption(company: { id: string; name: string; domain?: string }): ComboboxOption {
  return { id: company.id, label: company.name, detail: company.domain || undefined }
}

/**
 * Searches the server rather than filtering a loaded page: a workspace's
 * companies do not fit in one response, and a `<select>` over the first page made
 * every company past it unreachable. The current link is held as the selection
 * itself, so it stays visible without being re-injected into the result list.
 */
function CompanyPicker({
  contactId,
  company,
  onDone,
}: {
  contactId: string
  company: ContactCompany | null
  onDone: () => void
}) {
  const [committedQuery, setCommittedQuery] = useState('')
  const [typedQuery, setTypedQuery] = useDebouncedInput(committedQuery, setCommittedQuery)
  const term = companySearchTerm(committedQuery)
  const search = useCompanySearch(term)
  const [setCompany, state] = useSetContactCompanyMutation()
  const [selected, setSelected] = useState<ComboboxOption | null>(company ? companyOption(company) : null)
  const [error, setError] = useState<string | null>(null)
  const inputId = useId()
  const typedTooShort = typedQuery.trim() !== '' && companySearchTerm(typedQuery) === undefined

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
      contactCompanyLink: { company_id: selected?.id ?? null },
    })
    if ('error' in result) {
      setError(linkErrorMessage(result.error))
      return
    }
    onDone()
  }

  return (
    <div className="grid gap-2">
      <Label htmlFor={inputId}>Company</Label>
      <Combobox
        inputId={inputId}
        query={typedQuery}
        onQueryChange={setTypedQuery}
        options={search.items.map(companyOption)}
        selected={selected}
        onSelect={setSelected}
        disabled={state.isLoading}
        list={{
          loading: search.loading,
          // Fixed copy rather than the status mapper: what matters to the reader
          // is what this blocks, and "the server had a problem" alone says
          // nothing about why the list is empty.
          error: search.error ? 'The company list could not be loaded, so there is nothing to choose from yet.' : undefined,
          onRetry: search.retry,
        }}
        paging={{
          hasMore: search.hasMore,
          loading: search.loadingMore,
          error: search.loadMoreError ? 'More companies could not be loaded. Try again.' : undefined,
          onLoadMore: search.loadMore,
        }}
        labels={{
          placeholder: 'Search companies by name or domain',
          list: 'Companies',
          empty: term ? `No company matches “${term}”.` : 'There are no companies in this workspace yet.',
          none: 'No company',
          clear: 'Clear the selected company',
          hint: typedTooShort ? `Type at least ${minCompanySearchLength} characters to search.` : undefined,
        }}
      />
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
