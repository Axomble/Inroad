import { Link } from '@tanstack/react-router'
import { ArrowRight, CircleAlert, Flame, Mail, Rocket, Send, ShieldCheck, Users } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill, StatusDot, type StatusTone } from '@/components/shared/status-pill'
import { Page, PageBody, PageTopbar, SectionBar, Stat, StatStrip, EmptyBlock } from '@/components/layout/page'
import { SEVERITY_SR, attentionLabel, linkProps, sortAttention } from '@/components/layout/pulse-attention'
import { usePulse } from '@/components/layout/use-pulse'
import { useAppSelector } from '@/store/hooks'
import { useListCampaignsQuery } from '@/features/campaigns/api'
import type { PulseSeverity } from '@/features/pulse/api'
import type { Campaign } from '@/store/api'
import { cn } from '@/lib/utils'
import { SetupChecklist } from './setup-checklist'

const campaignTone: Record<string, StatusTone> = {
  running: 'running',
  draft: 'draft',
  paused: 'paused',
  done: 'done',
}

// Semantic scale, same mapping as the sidebar's pulse card — color plus the
// visually-hidden severity word, never color alone.
const severityDot: Record<PulseSeverity, string> = {
  danger: 'bg-danger',
  warn: 'bg-warn',
  info: 'bg-ok',
}

/**
 * Overview, on the same Volt vocabulary as every list page: full-bleed bands
 * separated by hairlines (StatStrip, SectionBar), no floating cards, no soft
 * shadows. The one deliberate exception is the spotlight band — the inverted
 * hero panel is this page's signature and uses the --spotlight family, dark in
 * both themes — and even it sits edge-to-edge under the topbar.
 */
export function OverviewPage() {
  const name = useAppSelector((state) => state.auth.userName)
  // Every aggregate on this page reads the one shared pulse subscription —
  // the same O(1) payload (and the same 45s cadence) the sidebar meter and
  // nav counts use, so "Daily capacity" here IS the meter's denominator.
  const { data: pulse, isError: pulseError } = usePulse()
  // The one surviving list query: the "Work in motion" panel renders actual
  // campaign rows (name, subject, sent count, status), which no aggregate
  // read-model carries.
  const { data: campaigns = [], isLoading: campaignsLoading, isError: campaignsError } = useListCampaignsQuery()

  const healthScore =
    pulse && pulse.warmup.pool > 0 ? Math.round((pulse.warmup.healthy / pulse.warmup.pool) * 100) : null
  const firstName = name?.trim().split(/\s+/)[0]
  const hasQueryError = pulseError || campaignsError

  // Server-defined attention rows (pulse.attention[]), worst-first — the same
  // producers the sidebar card renders.
  const attention = pulse ? sortAttention(pulse.attention).slice(0, 4) : []

  const stat = (value: number | undefined) =>
    value == null ? <Skeleton className="h-7 w-12" /> : value.toLocaleString()

  return (
    <Page>
      <PageTopbar
        eyebrow="Overview"
        subtitle="Your sending operation at a glance"
        actions={
          <Button asChild variant="primary" size="sm">
            <Link to="/app/campaigns">
              <Rocket className="size-4" />
              Build campaign
            </Link>
          </Button>
        }
      />
      <PageBody>
        <SetupChecklist />

        {/* Spotlight, not chrome: this band inverts against the page in both
            themes (--spotlight family). Edge-to-edge like every other band —
            the inversion is the emphasis, so it needs no card or shadow. */}
        <section className="relative overflow-hidden border-b border-spotlight-border bg-spotlight px-5 py-6 text-spotlight-text sm:px-7">
          <div className="absolute right-0 top-0 h-56 w-56 rounded-full bg-primary/10 blur-3xl" aria-hidden="true" />
          <div className="relative flex flex-col gap-5 lg:flex-row lg:items-end lg:justify-between">
            <div>
              <div className="mb-3 inline-flex items-center gap-2 rounded-full border border-spotlight-border bg-spotlight-surface px-2.5 py-1 font-mono text-[9px] uppercase tracking-[0.16em] text-spotlight-muted">
                <span className="relative flex size-1.5">
                  <span className="live-ping absolute inline-flex size-full rounded-full bg-primary opacity-70" />
                  <span className="relative inline-flex size-1.5 rounded-full bg-primary" />
                </span>
                Live workspace
              </div>
              <h1 className="max-w-2xl text-2xl font-semibold tracking-[-0.04em] sm:text-3xl">
                {firstName ? `Good to see you, ${firstName}.` : 'Your outreach command center.'}
              </h1>
              <p className="mt-2 max-w-xl text-sm leading-6 text-spotlight-muted">
                Protect sender reputation, keep capacity visible, and move the right campaign forward.
              </p>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button asChild variant="outline" size="sm" className="border-spotlight-border text-spotlight-text hover:bg-spotlight-hover">
                <Link to="/app/mailboxes"><Mail />Mailboxes</Link>
              </Button>
              <Button asChild variant="outline" size="sm" className="border-spotlight-border text-spotlight-text hover:bg-spotlight-hover">
                <Link to="/app/contacts"><Users />Contacts</Link>
              </Button>
            </div>
          </div>
        </section>

        {hasQueryError && (
          <div className="flex items-center gap-2 border-b border-danger/30 bg-danger/10 px-5 py-2.5 text-xs text-danger">
            <CircleAlert className="size-4 shrink-0" aria-hidden="true" />
            Some live metrics could not be loaded. Your data is unchanged; refresh to try again.
          </div>
        )}

        <StatStrip>
          <Stat
            label="Active mailboxes"
            value={stat(pulse?.mailboxes.active)}
            dot={<StatusDot tone="running" />}
            sub={pulse ? `${pulse.mailboxes.total} connected` : '—'}
          />
          <Stat
            label="Daily capacity"
            value={stat(pulse?.sending.daily_cap)}
            sub={pulse ? `${pulse.sending.sent_today.toLocaleString()} sent today` : '—'}
          />
          <Stat
            label="Live campaigns"
            value={stat(pulse?.campaigns.running)}
            sub={pulse ? `${pulse.campaigns.draft} drafts ready to refine` : '—'}
          />
          <Stat
            label="Warmup healthy"
            value={stat(pulse?.warmup.healthy)}
            dot={<StatusDot tone="warming" />}
            sub={pulse ? `${pulse.warmup.pool} enrolled` : '—'}
          />
        </StatStrip>

        <div className="grid border-b border-border xl:grid-cols-[minmax(0,1.5fr)_minmax(320px,1fr)]">
          <section className="min-w-0 border-b border-border xl:border-b-0 xl:border-r">
            <SectionBar label="Work in motion">
              <Link to="/app/campaigns" className="flex items-center gap-1 text-xs font-medium text-accent-ink">
                View all <ArrowRight className="size-3.5" />
              </Link>
            </SectionBar>
            {campaignsLoading ? (
              <div className="space-y-2 p-4">{[1, 2, 3].map((item) => <Skeleton key={item} className="h-14" />)}</div>
            ) : campaigns.length === 0 ? (
              <EmptyBlock
                className="py-12"
                title="No campaigns yet"
                description="Connect a sender and import a contact list, then create your first sequence."
                action={
                  // The topbar's "Build campaign" already spends this page's one
                  // primary button.
                  <Button asChild variant="secondary" size="sm">
                    <Link to="/app/campaigns"><Send className="size-4" />Create campaign</Link>
                  </Button>
                }
              />
            ) : (
              <ul>
                {campaigns.slice(0, 5).map((campaign) => <CampaignRow key={campaign.id ?? campaign.name} campaign={campaign} />)}
              </ul>
            )}
          </section>

          <div className="min-w-0">
            <section>
              <SectionBar label="Sender health" />
              <div className="flex items-center gap-5 px-5 py-4">
                <HealthRing score={healthScore} />
                <div className="grid flex-1 grid-cols-3 gap-3">
                  <MiniStat label="Healthy" value={pulse?.warmup.healthy ?? 0} className="text-ok" />
                  <MiniStat label="Watching" value={pulse?.warmup.watch ?? 0} className="text-warn" />
                  {/* at_risk buckets throttled + paused on the reputation axis. */}
                  <MiniStat label="At risk" value={pulse?.warmup.at_risk ?? 0} className="text-danger" />
                </div>
              </div>
              <div className="px-5 pb-4">
                <Button asChild variant="secondary" size="sm">
                  <Link to="/app/warmup"><Flame />Review warmup</Link>
                </Button>
              </div>
            </section>

            <section className="border-t border-border">
              <SectionBar label="Needs attention" />
              {attention.length > 0 ? (
                <ul className="divide-y divide-border">
                  {attention.map((item) => (
                    <li key={item.kind}>
                      <Link {...linkProps(item.href)} className="group flex gap-3 px-5 py-3.5 transition-colors hover:bg-surface-2/70">
                        <span className={cn('mt-1.5 size-2 shrink-0 rounded-full', severityDot[item.severity])} aria-hidden="true" />
                        <span className="sr-only">{SEVERITY_SR[item.severity]}</span>
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-sm font-medium">
                            <span className="font-mono tabular-nums">{item.count}</span> {attentionLabel(item.kind, item.count)}
                          </span>
                          <span className="mt-0.5 line-clamp-2 block text-xs leading-5 text-muted-foreground">{item.reason}</span>
                        </span>
                        <ArrowRight className="mt-1 size-4 text-faint transition-transform group-hover:translate-x-0.5" />
                      </Link>
                    </li>
                  ))}
                </ul>
              ) : (
                <div className="flex items-center gap-3 px-5 py-5">
                  <ShieldCheck className="size-4 shrink-0 text-ok" aria-hidden="true" />
                  <div>
                    <div className="text-sm font-medium">Nothing urgent</div>
                    <div className="text-xs text-muted-foreground">Your connected senders look clear.</div>
                  </div>
                </div>
              )}
            </section>
          </div>
        </div>
      </PageBody>
    </Page>
  )
}

function CampaignRow({ campaign }: { campaign: Campaign }) {
  const sent = campaign.stats?.sent ?? 0
  const content = (
    <>
        <div className="min-w-0 flex-1"><div className="truncate text-sm font-medium">{campaign.name || 'Untitled campaign'}</div><div className="mt-0.5 truncate text-xs text-muted-foreground">{campaign.subject || 'No subject yet'}</div></div>
        <div className="hidden text-right sm:block"><div className="font-mono text-xs tabular-nums">{sent.toLocaleString()}</div><div className="text-[10px] text-faint">sent</div></div>
        <StatusPill tone={campaignTone[campaign.status ?? ''] ?? 'draft'}>{campaign.status ?? 'draft'}</StatusPill>
        <ArrowRight className="size-4 text-faint transition-transform group-hover:translate-x-0.5" />
    </>
  )
  return (
    <li className="border-b border-border last:border-b-0">
      {campaign.id ? (
        <Link to="/app/campaigns/$id" params={{ id: campaign.id }} className="group flex items-center gap-3 px-5 py-3.5 transition-colors hover:bg-surface-2/70">
          {content}
        </Link>
      ) : (
        <div className="flex items-center gap-3 px-5 py-3.5">{content}</div>
      )}
    </li>
  )
}

/** Ring chart — round because it IS a ring, not container decoration. */
function HealthRing({ score }: { score: number | null }) {
  const degrees = score == null ? 0 : score * 3.6
  return <div className="grid size-16 shrink-0 place-items-center rounded-full" style={{ background: `conic-gradient(var(--primary) ${degrees}deg, var(--surface-2) ${degrees}deg)` }}><div className="grid size-12 place-items-center rounded-full bg-surface font-mono text-sm font-semibold tabular-nums">{score == null ? '—' : `${score}%`}</div></div>
}

function MiniStat({ label, value, className }: { label: string; value: number; className: string }) {
  return (
    <div>
      <div className={cn('font-mono text-base font-semibold tabular-nums', className)}>{value}</div>
      <div className="mt-0.5 text-[10px] text-muted-foreground">{label}</div>
    </div>
  )
}
