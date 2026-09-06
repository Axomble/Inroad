import { Link } from '@tanstack/react-router'
import {
  ArrowRight,
  CircleAlert,
  Flame,
  Gauge,
  Mail,
  Megaphone,
  Rocket,
  Send,
  ShieldCheck,
  Users,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill, type StatusTone } from '@/components/shared/status-pill'
import { Page, PageBody, PageTopbar } from '@/components/layout/page'
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
  // producers the sidebar card renders, replacing the client-side scan of the
  // full mailbox + warmup lists this panel used to derive its rows from.
  const attention = pulse ? sortAttention(pulse.attention).slice(0, 4) : []

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
        <div className="mx-auto w-full max-w-[1480px] p-4 sm:p-6 lg:p-8">
          <SetupChecklist />

          {/* Spotlight, not chrome: this hero inverts against the page in both
              themes, so it uses the --spotlight family rather than the app-shell
              tokens, which follow the light/dark theme. */}
          <section className="relative overflow-hidden rounded-2xl border border-spotlight-border bg-spotlight px-5 py-6 text-spotlight-text shadow-[0_18px_50px_rgba(13,18,9,0.18)] sm:px-7 sm:py-7">
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
            <div className="mt-4 flex items-center gap-2 rounded-xl border border-danger/25 bg-danger/5 px-4 py-3 text-sm text-danger">
              <CircleAlert className="size-4 shrink-0" />
              Some live metrics could not be loaded. Your data is unchanged; refresh to try again.
            </div>
          )}

          <section className="mt-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
            <MetricCard icon={Mail} label="Active mailboxes" value={pulse?.mailboxes.active ?? null} sub={pulse ? `${pulse.mailboxes.total} connected` : '—'} tone="primary" />
            <MetricCard icon={Gauge} label="Daily capacity" value={pulse?.sending.daily_cap ?? null} sub={pulse ? `${pulse.sending.sent_today.toLocaleString()} sent today` : '—'} tone="data" />
            <MetricCard icon={Megaphone} label="Live campaigns" value={pulse?.campaigns.running ?? null} sub={pulse ? `${pulse.campaigns.draft} drafts ready to refine` : '—'} tone="security" />
            <MetricCard icon={Flame} label="Warmup healthy" value={pulse?.warmup.healthy ?? null} sub={pulse ? `${pulse.warmup.pool} enrolled` : '—'} tone="warm" />
          </section>

          <div className="mt-4 grid gap-4 xl:grid-cols-[minmax(0,1.45fr)_minmax(320px,0.75fr)]">
            <section className="overflow-hidden rounded-2xl border border-border bg-surface shadow-[0_12px_35px_rgba(20,28,12,0.06)]">
              <PanelHeading eyebrow="Campaign pulse" title="Work in motion" action={<Link to="/app/campaigns" className="flex items-center gap-1 text-xs font-medium text-accent-ink">View all <ArrowRight className="size-3.5" /></Link>} />
              {campaignsLoading ? (
                <div className="space-y-2 p-4">{[1, 2, 3].map((item) => <Skeleton key={item} className="h-14" />)}</div>
              ) : campaigns.length === 0 ? (
                <EmptyPanel icon={Send} title="No campaigns yet" copy="Connect a sender and import a contact list, then create your first sequence." to="/app/campaigns" action="Create campaign" />
              ) : (
                <ul>
                  {campaigns.slice(0, 5).map((campaign) => <CampaignRow key={campaign.id ?? campaign.name} campaign={campaign} />)}
                </ul>
              )}
            </section>

            <div className="grid gap-4">
              <section className="rounded-2xl border border-border bg-surface p-5 shadow-[0_12px_35px_rgba(20,28,12,0.06)]">
                <div className="flex items-center justify-between gap-4">
                  <div>
                    <div className="font-mono text-[9px] uppercase tracking-[0.18em] text-faint">Delivery posture</div>
                    <h2 className="mt-1 text-base font-semibold tracking-tight">Sender health</h2>
                  </div>
                  <HealthRing score={healthScore} />
                </div>
                <div className="mt-5 grid grid-cols-3 gap-2">
                  <MiniStat label="Healthy" value={pulse?.warmup.healthy ?? 0} className="text-ok" />
                  <MiniStat label="Watching" value={pulse?.warmup.watch ?? 0} className="text-warn" />
                  {/* at_risk buckets throttled + paused on the reputation axis. */}
                  <MiniStat label="At risk" value={pulse?.warmup.at_risk ?? 0} className="text-danger" />
                </div>
                <Button asChild variant="secondary" size="sm" className="mt-4 w-full">
                  <Link to="/app/warmup"><Flame />Review warmup</Link>
                </Button>
              </section>

              <section className="overflow-hidden rounded-2xl border border-border bg-surface shadow-[0_12px_35px_rgba(20,28,12,0.06)]">
                <PanelHeading eyebrow="Priority queue" title="Needs attention" />
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
                    <div className="grid size-9 place-items-center rounded-full bg-ok/10 text-ok"><ShieldCheck className="size-4" /></div>
                    <div><div className="text-sm font-medium">Nothing urgent</div><div className="text-xs text-muted-foreground">Your connected senders look clear.</div></div>
                  </div>
                )}
              </section>
            </div>
          </div>
        </div>
      </PageBody>
    </Page>
  )
}

function MetricCard({ icon: Icon, label, value, sub, tone }: { icon: typeof Mail; label: string; value: number | null; sub: string; tone: 'primary' | 'data' | 'security' | 'warm' }) {
  const tones = { primary: 'bg-primary/15 text-accent-ink', data: 'bg-data/10 text-data', security: 'bg-security/10 text-security', warm: 'bg-warm/10 text-warm' }
  return (
    <article className="rounded-2xl border border-border bg-surface p-4 shadow-[0_10px_28px_rgba(20,28,12,0.055)] sm:p-5">
      <div className={cn('grid size-9 place-items-center rounded-xl', tones[tone])}><Icon className="size-4" /></div>
      <div className="mt-4 font-mono text-[9px] uppercase tracking-[0.16em] text-faint">{label}</div>
      {value == null ? <Skeleton className="mt-2 h-8 w-16" /> : <div className="mt-1 text-3xl font-light tracking-[-0.05em] tabular-nums">{value.toLocaleString()}</div>}
      <div className="mt-1 truncate text-xs text-muted-foreground">{sub}</div>
    </article>
  )
}

function PanelHeading({ eyebrow, title, action }: { eyebrow: string; title: string; action?: React.ReactNode }) {
  return <header className="flex items-center gap-3 border-b border-border px-5 py-4"><div><div className="font-mono text-[9px] uppercase tracking-[0.18em] text-faint">{eyebrow}</div><h2 className="mt-0.5 text-base font-semibold tracking-tight">{title}</h2></div>{action && <div className="ml-auto">{action}</div>}</header>
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

function HealthRing({ score }: { score: number | null }) {
  const degrees = score == null ? 0 : score * 3.6
  return <div className="grid size-16 place-items-center rounded-full" style={{ background: `conic-gradient(var(--primary) ${degrees}deg, var(--surface-2) ${degrees}deg)` }}><div className="grid size-12 place-items-center rounded-full bg-surface font-mono text-sm font-semibold tabular-nums">{score == null ? '—' : `${score}%`}</div></div>
}

function MiniStat({ label, value, className }: { label: string; value: number; className: string }) {
  return <div className="rounded-lg bg-surface-2/70 p-2.5 text-center"><div className={cn('font-mono text-base font-semibold tabular-nums', className)}>{value}</div><div className="mt-0.5 text-[10px] text-muted-foreground">{label}</div></div>
}

function EmptyPanel({ icon: Icon, title, copy, to, action }: { icon: typeof Send; title: string; copy: string; to: '/app/campaigns'; action: string }) {
  return <div className="flex flex-col items-center px-6 py-12 text-center"><div className="grid size-11 place-items-center rounded-xl bg-primary/15 text-accent-ink"><Icon className="size-5" /></div><h3 className="mt-3 text-sm font-semibold">{title}</h3><p className="mt-1 max-w-sm text-sm text-muted-foreground">{copy}</p><Button asChild variant="primary" size="sm" className="mt-4"><Link to={to}>{action}</Link></Button></div>
}
