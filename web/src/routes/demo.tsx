import { createFileRoute } from '@tanstack/react-router'
import { Plus, Flame, ChevronDown, MoreHorizontal } from 'lucide-react'
import { AppShell } from '@/components/layout/app-shell'
import {
  Page,
  PageTopbar,
  SectionBar,
  StatStrip,
  Stat,
  PageBody,
} from '@/components/layout/page'
import { Button } from '@/components/ui/button'
import { StatusPill } from '@/components/shared/status-pill'
import { cn } from '@/lib/utils'

export const Route = createFileRoute('/demo')({
  component: DemoScreen,
})

type Tone = 'running' | 'warming' | 'draft'
const CAMPAIGNS: { name: string; id: string; desc: string; tone: Tone; label: string }[] = [
  { name: 'Q3 Enterprise Outreach', id: 'a3f1c8b2', desc: 'Tier-1 accounts · 5 steps', tone: 'running', label: 'Running' },
  { name: 'SaaS Founders — Cold', id: '7d2e9a04', desc: 'Seed-stage founders', tone: 'warming', label: 'Warming' },
  { name: 'Reactivation Q2', id: '5c81f6aa', desc: 'Dormant trials', tone: 'draft', label: 'Draft' },
]

const dotBg: Record<Tone, string> = { running: 'bg-ok', warming: 'bg-warm', draft: 'bg-faint' }

function DemoScreen() {
  return (
    <AppShell>
      <Page>
        <PageTopbar
          eyebrow="Campaigns"
          actions={
            <>
              <Button variant="chip" size="chip">
                All folders <ChevronDown />
              </Button>
              <Button variant="warm" size="sm">
                <Flame /> Start warmup
              </Button>
              <Button variant="primary" size="sm">
                <Plus /> New campaign
              </Button>
            </>
          }
        />

        <StatStrip>
          <Stat label="All" value="8" sub="campaigns" />
          <Stat label="Sending" value="3" sub="live now" dot={<span className="size-1.5 rounded-full bg-ok" />} />
          <Stat
            label="Warming"
            value="8"
            sub="mailboxes ramping"
            dot={<span className="size-1.5 rounded-full bg-warm warm-pulse" />}
          />
          <Stat label="Needs attention" value="3" sub="paused or failing" />
        </StatStrip>

        <SectionBar label="All campaigns" count="8">
          <Button variant="chip" size="chip">
            Newest <ChevronDown />
          </Button>
        </SectionBar>

        <PageBody>
          <ul>
            {CAMPAIGNS.map((c) => (
              <li
                key={c.id}
                className="group flex items-center gap-3.5 border-b border-border px-5 py-3 text-[13.5px] hover:bg-surface"
              >
                <span className={cn('size-2 shrink-0 rounded-sm', dotBg[c.tone])} aria-hidden="true" />
                <span className="font-semibold">{c.name}</span>
                <span className="font-mono text-[11px] text-faint">{c.id}</span>
                <span className="text-[12.5px] text-muted-foreground">{c.desc}</span>
                <StatusPill tone={c.tone} className="ml-auto">
                  {c.label}
                </StatusPill>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Actions for ${c.name}`}
                  className="opacity-0 group-hover:opacity-100"
                >
                  <MoreHorizontal />
                </Button>
              </li>
            ))}
          </ul>
        </PageBody>
      </Page>
    </AppShell>
  )
}
