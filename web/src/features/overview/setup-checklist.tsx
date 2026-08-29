import { memo } from 'react'
import { Link } from '@tanstack/react-router'
import { ArrowRight, Check, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { usePulseSelect } from '@/components/layout/use-pulse'
// Read-only RTK Query hook from another feature's api.ts (hooks only, never
// UI/state — the house exception): the sending-domain rows already answer
// "is any domain fully verified" without a new endpoint or a list download.
// (Rows derive from the domains of CONNECTED MAILBOXES — a domain with no
// mailbox never appears here, so "no rows" simply keeps the step open.)
import { useListSendingDomainsQuery } from '@/features/mailboxes/api'
import type { WorkspacePulse } from '@/features/pulse/api'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import { dismissSetupChecklist } from '@/store/slices/ui'
import { cn } from '@/lib/utils'

/**
 * Activation checklist for a workspace that has not yet reached first send.
 *
 * Every check derives live from the pulse read-model (plus one cheap
 * sending-domains read) — nothing is stored as "step done", so a deleted
 * mailbox honestly reopens its step. Two exits, with a strict precedence:
 * derived completion (all steps done) always unmounts the panel, and beats a
 * stale dismissal in either direction; an explicit dismissal (persisted in the
 * ui slice) hides an incomplete panel across reloads.
 */

// Module-scope selector, paired with the `memo` on the component: the parent
// page re-renders on every poll tick (it reads the whole pulse result), but a
// memoized, prop-less child re-renders only when its OWN subscriptions change —
// and this selector narrows the pulse one to exactly these five counts.
const selectChecklistCounts = (data: WorkspacePulse | undefined) => ({
  mailboxTotal: data?.mailboxes.total,
  warmupPool: data?.warmup.pool,
  contactTotal: data?.contacts.total,
  campaignTotal: data?.campaigns.total,
  campaignDraft: data?.campaigns.draft,
})

type StepTo = '/app/mailboxes' | '/app/warmup' | '/app/contacts' | '/app/campaigns'

interface Step {
  id: string
  title: string
  /** Guidance rendered only while the step is open. */
  detail: string
  done: boolean
  to: StepTo
  cta: string
}

export const SetupChecklist = memo(function SetupChecklist() {
  const dispatch = useAppDispatch()
  const dismissed = useAppSelector((s) => s.ui.setupChecklistDismissed)
  const counts = usePulseSelect(selectChecklistCounts)
  // A dismissed panel renders nothing whatever the data says, so don't spend
  // the domains request on it.
  const { data: domains, isLoading: domainsLoading } = useListSendingDomainsQuery(undefined, { skip: dismissed })

  // Dismissal is known synchronously from the store — bail before any
  // data-dependent branch so a dismissed panel never flashes anything.
  if (dismissed) return null

  const pool = counts.warmupPool ?? 0
  const pulseDone = {
    mailbox: (counts.mailboxTotal ?? 0) > 0,
    warmup: pool >= 2,
    contacts: (counts.contactTotal ?? 0) > 0,
    // A campaign has left draft (running/paused/done) — created-but-draft
    // does not count as launched.
    campaign: (counts.campaignTotal ?? 0) > (counts.campaignDraft ?? 0),
  }
  const somePulseStepOpen = !(pulseDone.mailbox && pulseDone.warmup && pulseDone.contacts && pulseDone.campaign)

  // Until both reads settle there is nothing truthful to assert. But this slot
  // sits ABOVE the hero, so rendering nothing while the domains request is in
  // flight would shove the whole page down when the panel lands (a large
  // layout shift on the landing route). The pulse is warm — shared with the
  // sidebar — so it already tells us whether the panel is GUARANTEED to render
  // (any pulse-derived step open means no domain answer can complete the
  // list): reserve the space with a skeleton exactly then, and render nothing
  // in the ambiguous only-the-domain-step-could-be-open case rather than flash
  // a skeleton at a finished workspace.
  if (counts.mailboxTotal === undefined) return null
  if (domainsLoading) return somePulseStepOpen ? <ChecklistSkeleton /> : null

  // If the domains read errors, the step stays open: "go look" is honest and
  // actionable, a faked checkmark is neither.
  const domainVerified = (domains ?? []).some((d) => d.state === 'passing')

  const steps: Step[] = [
    {
      id: 'mailbox',
      title: 'Connect a mailbox',
      detail: 'Add Gmail, Microsoft 365, or SMTP — the account your outreach sends from.',
      done: pulseDone.mailbox,
      to: '/app/mailboxes',
      cta: 'Connect',
    },
    {
      id: 'domain',
      title: 'Verify your sending domain',
      detail: 'Pass SPF, DKIM and DMARC so receiving servers trust your mail.',
      done: domainVerified,
      to: '/app/mailboxes',
      cta: 'Check DNS',
    },
    {
      id: 'warmup',
      title: 'Start warmup',
      detail:
        pool === 1
          ? '1 mailbox warming — enroll a second so the pool can exchange mail.'
          : 'Enroll at least two mailboxes to build sender reputation before cold volume.',
      done: pulseDone.warmup,
      to: '/app/warmup',
      cta: 'Set up warmup',
    },
    {
      id: 'contacts',
      title: 'Import contacts',
      detail: 'Upload a clean CSV into a list — the audience your first campaign enrolls.',
      done: pulseDone.contacts,
      to: '/app/contacts',
      cta: 'Import',
    },
    {
      id: 'campaign',
      title: 'Launch your first campaign',
      detail: 'Write a sequence, pick senders and audience, then take it out of draft.',
      done: pulseDone.campaign,
      to: '/app/campaigns',
      cta: 'Launch',
    },
  ]

  const doneCount = steps.filter((step) => step.done).length
  // Derived completion always unmounts the panel — a finished workspace never
  // sees it again, dismissed or not. (Dismissal itself bailed earlier.)
  if (doneCount === steps.length) return null

  const firstOpenId = steps.find((step) => !step.done)?.id

  return (
    <section
      aria-label="Setup checklist"
      className="mb-4 overflow-hidden rounded-2xl border border-border bg-surface shadow-[0_12px_35px_rgba(20,28,12,0.06)]"
    >
      <header className="flex items-start gap-3 border-b border-border px-5 py-4">
        <div>
          <div className="font-mono text-[9px] uppercase tracking-[0.18em] text-faint">Getting started</div>
          <h2 className="mt-0.5 text-base font-semibold tracking-tight">Set up your sending operation</h2>
        </div>
        <div className="ml-auto flex items-center gap-2">
          <span className="font-mono text-[10px] tabular-nums text-faint">
            {doneCount}/{steps.length} done
          </span>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Hide setup checklist"
            onClick={() => dispatch(dismissSetupChecklist())}
          >
            <X className="size-4" />
          </Button>
        </div>
      </header>
      <ul className="divide-y divide-border">
        {steps.map((step, index) => (
          <li key={step.id} className="flex items-center gap-3 px-5 py-3">
            {step.done ? (
              <span className="grid size-6 shrink-0 place-items-center rounded-full bg-ok/10 text-ok" aria-hidden="true">
                <Check className="size-3.5" />
              </span>
            ) : (
              <span
                className="grid size-6 shrink-0 place-items-center rounded-full border border-border-strong font-mono text-[10px] text-faint"
                aria-hidden="true"
              >
                {index + 1}
              </span>
            )}
            <span className="sr-only">{step.done ? 'Done:' : 'To do:'}</span>
            <span className="min-w-0 flex-1">
              <span className={cn('block truncate text-sm', step.done ? 'text-muted-foreground' : 'font-medium')}>
                {step.title}
              </span>
              {!step.done && (
                <span className="mt-0.5 line-clamp-2 block text-xs leading-5 text-muted-foreground">{step.detail}</span>
              )}
            </span>
            {!step.done &&
              // Accent discipline: the topbar's "Build campaign" already spends
              // this page's one primary button, so the checklist's lead action
              // stays a tactile secondary — still the only button in the panel.
              (step.id === firstOpenId ? (
                <Button asChild variant="secondary" size="sm" className="shrink-0">
                  <Link to={step.to}>
                    {step.cta}
                    <ArrowRight className="size-3.5" />
                  </Link>
                </Button>
              ) : (
                <Link
                  to={step.to}
                  className="flex shrink-0 items-center gap-1 rounded-md text-xs font-medium text-accent-ink outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {step.cta}
                  <ArrowRight className="size-3" />
                </Link>
              ))}
          </li>
        ))}
      </ul>
    </section>
  )
})

/**
 * Reserved-height placeholder matching the panel's shell (header + five rows),
 * rendered only when the panel is guaranteed to appear — see the gate above.
 * Same trick as the sidebar pulse card's skeleton: the page must not jump when
 * the data lands.
 */
function ChecklistSkeleton() {
  return (
    <section
      aria-hidden="true"
      data-slot="setup-checklist-skeleton"
      className="mb-4 overflow-hidden rounded-2xl border border-border bg-surface"
    >
      <div className="flex items-center border-b border-border px-5 py-4">
        <div className="flex flex-col gap-1.5">
          <Skeleton className="h-2 w-24" />
          <Skeleton className="h-4 w-56" />
        </div>
        <Skeleton className="ml-auto h-3 w-14" />
      </div>
      <ul className="divide-y divide-border">
        {[0, 1, 2, 3, 4].map((row) => (
          <li key={row} className="flex items-center gap-3 px-5 py-3">
            <Skeleton className="size-6 shrink-0 rounded-full" />
            <div className="flex min-w-0 flex-1 flex-col gap-1.5 py-0.5">
              <Skeleton className="h-3.5 w-44" />
              <Skeleton className="h-3 w-72 max-w-full" />
            </div>
          </li>
        ))}
      </ul>
    </section>
  )
}
