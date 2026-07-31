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
import { useAppSelector } from '@/store/hooks'
import { useListMailboxesQuery } from '@/features/mailboxes/api'
import { useGetWarmupOverviewQuery } from '@/features/warmup/api'
import { useListCampaignsQuery } from '@/features/campaigns/api'
import { useListListsQuery } from '@/features/contacts/api'
import type { Campaign } from '@/store/api'
import { cn } from '@/lib/utils'

const campaignTone: Record<string, StatusTone> = {
  running: 'running',
  draft: 'draft',
  paused: 'paused',
  done: 'done',
}

export function OverviewPage() {
  const name = useAppSelector((state) => state.auth.userName)
  const { data: mailboxes = [], isLoading: mailboxesLoading, isError: mailboxesError } = useListMailboxesQuery()
  const { data: campaigns = [], isLoading: campaignsLoading, isError: campaignsError } = useListCampaignsQuery()
  const { data: warmup, isLoading: warmupLoading, isError: warmupError } = useGetWarmupOverviewQuery()
  const { data: lists = [], isLoading: listsLoading } = useListListsQuery()

  const activeMailboxes = mailboxes.filter((mailbox) => mailbox.status === 'active')
  const runningCampaigns = campaigns.filter((campaign) => campaign.status === 'running')
  const draftCampaigns = campaigns.filter((campaign) => campaign.status === 'draft')
  const enabledWarmup = warmup?.mailboxes.filter((mailbox) => mailbox.enabled) ?? []
  const healthyWarmup = enabledWarmup.filter((mailbox) => mailbox.health_state === 'healthy')
  const dailyCapacity = activeMailboxes.reduce((total, mailbox) => total + (mailbox.daily_cap ?? 0), 0)
  const healthScore = enabledWarmup.length > 0 ? Math.round((healthyWarmup.length / enabledWarmup.length) * 100) : null
  const firstName = name?.trim().split(/\s+/)[0]
  const hasQueryError = mailboxesError || campaignsError || warmupError

  const attention = [
    ...mailboxes
      .filter((mailbox) => mailbox.status === 'error')
      .map((mailbox) => ({
        id: `mailbox-${mailbox.id}`,
        label: mailbox.email ?? 'Mailbox connection',
        detail: mailbox.last_error || 'The mailbox needs to be reconnected.',
        to: '/app/mailboxes' as const,
        tone: 'danger' as const,
      })),
    ...enabledWarmup
      .filter((mailbox) => mailbox.health_state === 'watch' || mailbox.health_state === 'throttled')
      .map((mailbox) => ({
        id: `warmup-${mailbox.mailbox_id}`,
        label: mailbox.email,
        detail: mailbox.health_reason || 'Warmup reputation needs review.',
        to: '/app/warmup' as const,
        tone: 'warm' as const,
      })),
  ].slice(0, 4)

  const loading = mailboxesLoading || campaignsLoading || warmupLoading || listsLoading

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
          <section className="relative overflow-hidden rounded-2xl border border-chrome-border bg-chrome px-5 py-6 text-chrome-text shadow-[0_18px_50px_rgba(13,18,9,0.18)] sm:px-7 sm:py-7">
            <div className="absolute right-0 top-0 h-56 w-56 rounded-full bg-primary/10 blur-3xl" aria-hidden="true" />
            <div className="relative flex flex-col gap-5 lg:flex-row lg:items-end lg:justify-between">
              <div>
                <div className="mb-3 inline-flex items-center gap-2 rounded-full border border-chrome-border bg-chrome-surface px-2.5 py-1 font-mono text-[9px] uppercase tracking-[0.16em] text-chrome-muted">
                  <span className="relative flex size-1.5">
                    <span className="live-ping absolute inline-flex size-full rounded-full bg-primary opacity-70" />
                    <span className="relative inline-flex size-1.5 rounded-full bg-primary" />
                  </span>
                  Live workspace
                </div>
                <h1 className="max-w-2xl text-2xl font-semibold tracking-[-0.04em] sm:text-3xl">
                  {firstName ? `Good to see you, ${firstName}.` : 'Your outreach command center.'}
                </h1>
                <p className="mt-2 max-w-xl text-sm leading-6 text-chrome-muted">
                  Protect sender reputation, keep capacity visible, and move the right campaign forward.
                </p>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button asChild variant="outline" size="sm" className="border-chrome-border text-chrome-text hover:bg-chrome-hover">
                  <Link to="/app/mailboxes"><Mail />Mailboxes</Link>
                </Button>
                <Button asChild variant="outline" size="sm" className="border-chrome-border text-chrome-text hover:bg-chrome-hover">
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
            <MetricCard icon={Mail} label="Active mailboxes" value={loading ? null : activeMailboxes.length} sub={`${mailboxes.length} connected`} tone="primary" />
            <MetricCard icon={Gauge} label="Daily capacity" value={loading ? null : dailyCapacity} sub="messages across active senders" tone="data" />
            <MetricCard icon={Megaphone} label="Live campaigns" value={loading ? null : runningCampaigns.length} sub={`${draftCampaigns.length} drafts ready to refine`} tone="security" />
            <MetricCard icon={Flame} label="Warmup healthy" value={loading ? null : healthyWarmup.length} sub={`${enabledWarmup.length} enrolled`} tone="warm" />
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
                  <MiniStat label="Healthy" value={healthyWarmup.length} className="text-ok" />
                  <MiniStat label="Watching" value={enabledWarmup.filter((m) => m.health_state === 'watch').length} className="text-warn" />
                  <MiniStat label="Throttled" value={enabledWarmup.filter((m) => m.health_state === 'throttled').length} className="text-danger" />
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
                      <li key={item.id}>
                        <Link to={item.to} className="group flex gap-3 px-5 py-3.5 transition-colors hover:bg-surface-2/70">
                          <span className={cn('mt-1.5 size-2 shrink-0 rounded-full', item.tone === 'danger' ? 'bg-danger' : 'bg-warm')} />
                          <span className="min-w-0 flex-1">
                            <span className="block truncate text-sm font-medium">{item.label}</span>
                            <span className="mt-0.5 line-clamp-2 block text-xs leading-5 text-muted-foreground">{item.detail}</span>
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

          <section className="mt-4 grid gap-3 md:grid-cols-3">
            <LaunchCard step="01" icon={Mail} title="Connect sending" copy={mailboxes.length ? `${mailboxes.length} sender${mailboxes.length === 1 ? '' : 's'} connected` : 'Add Gmail, Microsoft 365, or SMTP'} to="/app/mailboxes" complete={mailboxes.length > 0} />
            <LaunchCard step="02" icon={Users} title="Build an audience" copy={lists.length ? `${lists.length} contact list${lists.length === 1 ? '' : 's'} ready` : 'Import a clean CSV into a list'} to="/app/contacts" complete={lists.length > 0} />
            <LaunchCard step="03" icon={Rocket} title="Launch outreach" copy={campaigns.length ? `${campaigns.length} campaign${campaigns.length === 1 ? '' : 's'} created` : 'Create and review your first sequence'} to="/app/campaigns" complete={campaigns.length > 0} />
          </section>
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

function LaunchCard({ step, icon: Icon, title, copy, to, complete }: { step: string; icon: typeof Mail; title: string; copy: string; to: '/app/mailboxes' | '/app/contacts' | '/app/campaigns'; complete: boolean }) {
  return <Link to={to} className="group flex items-center gap-3 rounded-2xl border border-border bg-surface p-4 transition-all hover:-translate-y-0.5 hover:border-border-strong hover:shadow-[0_12px_30px_rgba(20,28,12,0.08)]"><div className={cn('grid size-10 shrink-0 place-items-center rounded-xl', complete ? 'bg-ok/10 text-ok' : 'bg-surface-2 text-muted-foreground')}><Icon className="size-4" /></div><div className="min-w-0 flex-1"><div className="flex items-center gap-2"><span className="font-mono text-[9px] text-faint">{step}</span><span className="text-sm font-semibold">{title}</span></div><div className="mt-0.5 truncate text-xs text-muted-foreground">{copy}</div></div><ArrowRight className="size-4 text-faint transition-transform group-hover:translate-x-0.5" /></Link>
}
