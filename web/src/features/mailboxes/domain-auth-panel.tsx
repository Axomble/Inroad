import { useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { SectionBar } from '@/components/layout/page'
import { StatusPill } from '@/components/shared/status-pill'
import { useCheckSendingDomainMutation, useListSendingDomainsQuery } from './api'
import type { SendingDomain } from './api'
import {
  domainChecks,
  domainStateLabel,
  domainStateTone,
  domainSummary,
  lastCheckedLabel,
  listErrorMessage,
  mailboxCountLabel,
  recheckErrorMessage,
  type DomainCheck,
} from './domain-auth'

/**
 * Domain authentication for every domain this workspace sends from.
 *
 * Informational only — nothing here blocks a send. Its value is that a missing
 * SPF or DMARC record otherwise shows up weeks later as a bounce rate that looks
 * like a content problem. All wording comes from `./domain-auth`, which is where
 * the "advisory vs missing vs couldn't check" distinctions are enforced.
 */
export function DomainAuthPanel() {
  const { data, isLoading, error } = useListSendingDomainsQuery()

  if (isLoading) {
    return (
      <PanelShell>
        <div className="space-y-2 px-4 py-3 sm:px-5">
          <Skeleton className="h-4 w-56" />
          <Skeleton className="h-3 w-72" />
        </div>
      </PanelShell>
    )
  }

  // A failed load must never render as "no domains": an operator reading an
  // empty panel would conclude there is nothing to authenticate.
  if (error) {
    return (
      <PanelShell>
        <p role="alert" className="px-4 py-3 text-sm text-danger sm:px-5">
          {listErrorMessage(error)}
        </p>
      </PanelShell>
    )
  }

  const domains = data ?? []
  // No mailboxes means no sending domains; the page's own empty state already
  // says what to do, so an extra empty panel would only add noise.
  if (domains.length === 0) return null

  return (
    <PanelShell count={domains.length}>
      <ul className="max-h-72 overflow-y-auto">
        {domains.map((domain) => (
          <DomainRow key={domain.domain} domain={domain} />
        ))}
      </ul>
    </PanelShell>
  )
}

function PanelShell({ count, children }: { count?: number; children: React.ReactNode }) {
  return (
    <section aria-label="Domain authentication" className="border-b border-border">
      <SectionBar label="Domain authentication" count={count} />
      {children}
    </section>
  )
}

function DomainRow({ domain }: { domain: SendingDomain }) {
  const [check, { isLoading: isChecking }] = useCheckSendingDomainMutation()
  const [checkError, setCheckError] = useState<string | null>(null)
  const checks = domainChecks(domain)

  async function onRecheck() {
    setCheckError(null)
    const result = await check({ domain: domain.domain })
    if ('error' in result) setCheckError(recheckErrorMessage(result.error, domain.domain))
  }

  return (
    <li className="border-b border-border px-4 py-3 last:border-b-0 sm:px-5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <span className="min-w-0 truncate text-[13.5px] font-medium text-foreground">{domain.domain}</span>
        <StatusPill tone={domainStateTone(domain)}>{domainStateLabel(domain)}</StatusPill>
        <span className="font-mono text-[11px] text-faint">
          {mailboxCountLabel(domain.mailbox_count)} · {lastCheckedLabel(domain.checked_at)}
        </span>
        <Button
          variant="secondary"
          size="xs"
          className="ml-auto"
          aria-label={`Recheck DNS for ${domain.domain}`}
          disabled={isChecking}
          onClick={() => void onRecheck()}
        >
          <RefreshCw className={isChecking ? 'size-3 animate-spin' : 'size-3'} aria-hidden="true" />
          {isChecking ? 'Checking…' : 'Recheck'}
        </Button>
      </div>

      <p className="mt-1 text-xs text-muted-foreground">{domainSummary(domain)}</p>

      <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1">
        {checks.map((entry) => (
          <StatusPill key={entry.id} tone={entry.tone}>
            {entry.label} {entry.status}
          </StatusPill>
        ))}
      </div>

      {/* Every record that isn't a plain pass explains itself in full — a bare
          "not found" chip is the shape of this feature that misleads. */}
      <ul className="mt-1.5 space-y-1">
        {checks
          .filter((entry) => entry.verdict !== 'pass')
          .map((entry) => (
            <CheckNote key={entry.id} check={entry} />
          ))}
      </ul>

      {checkError && (
        <p role="alert" className="mt-1.5 text-xs text-danger">
          {checkError}
        </p>
      )}
    </li>
  )
}

/**
 * The explanatory line for one record. `attention` is the only verdict that gets
 * the danger colour; advisory, monitoring, and unknown stay muted, because none
 * of them is a fault to fix.
 */
function CheckNote({ check }: { check: DomainCheck }) {
  return (
    <li className={check.verdict === 'attention' ? 'text-xs text-danger' : 'text-xs text-muted-foreground'}>
      <span className="font-mono text-[10.5px] uppercase tracking-[0.1em]">{check.label}</span> — {check.detail}
    </li>
  )
}
