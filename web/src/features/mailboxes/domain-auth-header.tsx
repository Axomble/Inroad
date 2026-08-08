import { useId, useState } from 'react'
import { ChevronDown, RefreshCw } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill } from '@/components/shared/status-pill'
import { useCheckSendingDomainMutation } from './api'
import {
  domainChecks,
  domainStateLabel,
  domainStateTone,
  domainSummary,
  lastCheckedLabel,
  listErrorMessage,
  mailboxCountLabel,
  recheckErrorMessage,
  shortStatus,
  type DomainCheck,
} from './domain-auth'
import { domainGroupLabel, type MailboxDomainGroup } from './domain-group'

/**
 * The domain heading above one group of mailbox rows: which domain these
 * mailboxes send from, whether its DNS authenticates them, and a recheck.
 *
 * Informational — nothing here blocks a send. Its value is that a missing SPF or
 * DMARC record otherwise shows up weeks later as a bounce rate that looks like a
 * content problem. So it has to be readable at a glance and never in the way:
 * one line collapsed, with the sentences that explain each record behind a
 * disclosure. All wording comes from `./domain-auth`, which is where the
 * "advisory vs missing vs couldn't check" distinctions are enforced.
 */
export function DomainAuthHeader({
  group,
  isLoadingAuth,
}: {
  group: MailboxDomainGroup
  /** The domains query is still in flight, so a missing verdict isn't a verdict. */
  isLoadingAuth: boolean
}) {
  const detailId = useId()
  const [open, setOpen] = useState(false)
  const [check, { isLoading: isChecking }] = useCheckSendingDomainMutation()
  const [checkError, setCheckError] = useState<string | null>(null)
  const { auth } = group
  const label = domainGroupLabel(group)
  const checks = auth ? domainChecks(auth) : []

  async function onRecheck() {
    if (!auth) return
    setCheckError(null)
    const result = await check({ domain: auth.domain })
    if ('error' in result) setCheckError(recheckErrorMessage(result.error, auth.domain))
  }

  return (
    <>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border bg-surface-2/50 px-4 py-1.5 sm:px-5">
        <span className="min-w-0 truncate text-[13px] font-medium tracking-[-0.01em] text-foreground">{label}</span>

        {auth ? (
          <StatusPill tone={domainStateTone(auth)}>{domainStateLabel(auth)}</StatusPill>
        ) : isLoadingAuth ? (
          <Skeleton className="h-3 w-24" />
        ) : null}

        {/* The three records, one token each. Hidden on narrow viewports, where
            the domain-level pill plus the disclosure carry the same answer
            without pushing the row onto a second line. */}
        {checks.length > 0 && (
          <div className="hidden items-center gap-3 md:flex">
            {checks.map((entry) => (
              <StatusPill key={entry.id} tone={entry.tone}>
                {entry.label} {shortStatus(entry.verdict)}
              </StatusPill>
            ))}
          </div>
        )}

        <span className="ml-auto shrink-0 font-mono text-[11px] text-faint">
          {mailboxCountLabel(group.mailboxes.length)}
          {auth ? ` · ${lastCheckedLabel(auth.checked_at)}` : null}
        </span>

        {auth && (
          <>
            <Button
              variant="secondary"
              size="xs"
              aria-label={`Recheck DNS for ${auth.domain}`}
              disabled={isChecking}
              onClick={() => void onRecheck()}
            >
              <RefreshCw className={isChecking ? 'size-3 animate-spin' : 'size-3'} aria-hidden="true" />
              {isChecking ? 'Checking…' : 'Recheck'}
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-expanded={open}
              aria-controls={detailId}
              aria-label={`${open ? 'Hide' : 'Show'} DNS records for ${auth.domain}`}
              onClick={() => setOpen((current) => !current)}
            >
              <ChevronDown className={cn('size-4 transition-transform', open && 'rotate-180')} aria-hidden="true" />
            </Button>
          </>
        )}
      </div>

      {checkError && (
        <p role="alert" className="border-b border-border px-4 py-1.5 text-xs text-danger sm:px-5">
          {checkError}
        </p>
      )}

      {auth && open && (
        <div id={detailId} className="border-b border-border bg-surface-2/20 px-4 py-2.5 sm:px-5">
          <p className="max-w-[80ch] text-xs text-muted-foreground">{domainSummary(auth)}</p>
          {/* Every record explains itself in full here, passing ones included: a
              bare "not found" token is the shape of this feature that misleads. */}
          <ul className="mt-2 space-y-1.5">
            {checks.map((entry) => (
              <CheckNote key={entry.id} check={entry} />
            ))}
          </ul>
        </div>
      )}
    </>
  )
}

/**
 * One record's line. `attention` is the only verdict that gets the danger
 * colour; advisory, monitoring, and unknown stay muted, because none of them is
 * a fault to fix.
 */
function CheckNote({ check }: { check: DomainCheck }) {
  return (
    <li className="flex flex-wrap items-baseline gap-x-2 text-xs">
      <span className="w-14 shrink-0 font-mono text-[10.5px] uppercase tracking-[0.1em] text-faint">
        {check.label}
      </span>
      <span
        className={cn(
          'min-w-0 max-w-[78ch] flex-1',
          check.verdict === 'attention' ? 'text-danger' : 'text-muted-foreground',
        )}
      >
        <span className="font-medium text-foreground">{check.status}.</span> {check.detail}
      </span>
    </li>
  )
}

/**
 * A failed load of the domain list, on one line above the mailboxes.
 *
 * It must never render as "no domains to authenticate": an operator reading a
 * silent list would conclude there is nothing to fix.
 */
export function DomainAuthNotice({ error }: { error: unknown }) {
  return (
    <p role="alert" className="border-b border-border px-4 py-2 text-xs text-danger sm:px-5">
      {listErrorMessage(error)}
    </p>
  )
}
