import { useState } from 'react'
import { MoreVertical, Plus, Rocket } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { StatusPill } from '@/components/shared/status-pill'
import { Page, PageTopbar, StatStrip, Stat, SectionBar, PageBody, EmptyBlock } from '@/components/layout/page'
import { cn } from '@/lib/utils'
import type { Campaign } from '@/store/api'
import { useListCampaignsQuery, useGetCampaignQuery, useLaunchCampaignMutation } from './api'
import { campaignTone, campaignLabel } from './status'
import { CampaignForm } from './campaign-form'

export function CampaignsPage() {
  const [showForm, setShowForm] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)
  const { data: campaigns = [], isLoading } = useListCampaignsQuery()

  const count = (s: string) => campaigns.filter((c) => c.status === s).length

  return (
    <Page>
      <PageTopbar
        eyebrow="Campaigns"
        actions={
          <Button variant="primary" size="sm" onClick={() => setShowForm((v) => !v)}>
            <Plus className="size-4" />
            New campaign
          </Button>
        }
      />

      <StatStrip>
        <Stat label="Total" value={campaigns.length} />
        <Stat label="Running" value={count('running')} dot={<Dot className="bg-ok" />} />
        <Stat label="Draft" value={count('draft')} dot={<Dot className="bg-faint" />} />
        <Stat label="Done" value={count('done')} dot={<Dot className="bg-muted-foreground" />} />
      </StatStrip>

      <PageBody>
        {showForm && (
          <CampaignForm
            onDone={() => setShowForm(false)}
            onCancel={() => setShowForm(false)}
          />
        )}

        {selected && <CampaignDetail id={selected} onClose={() => setSelected(null)} />}

        {isLoading ? (
          <LoadingRows />
        ) : campaigns.length === 0 && !showForm ? (
          <EmptyBlock
            title="No campaigns yet"
            description="Create a campaign from a connected mailbox to a contact list, then launch it to start sending."
            action={
              <Button variant="primary" size="sm" onClick={() => setShowForm(true)}>
                <Plus className="size-4" />
                New campaign
              </Button>
            }
          />
        ) : (
          <ul>
            {campaigns.map((c) => (
              <CampaignRow
                key={c.id}
                campaign={c}
                selected={c.id === selected}
                onSelect={() => setSelected(c.id === selected ? null : (c.id ?? null))}
              />
            ))}
          </ul>
        )}
      </PageBody>
    </Page>
  )
}

function CampaignRow({
  campaign,
  selected,
  onSelect,
}: {
  campaign: Campaign
  selected: boolean
  onSelect: () => void
}) {
  const [launch, { isLoading }] = useLaunchCampaignMutation()
  const [error, setError] = useState<string | null>(null)
  const id = campaign.id ?? ''

  async function onLaunch() {
    setError(null)
    const res = await launch({ id })
    if ('error' in res) {
      const status = (res.error as { status?: number })?.status
      setError(status === 409 ? 'Already launched.' : status === 422 ? 'Target list is empty.' : 'Launch failed.')
    }
  }

  return (
    <li
      className={cn(
        'flex cursor-pointer items-center gap-4 border-b border-border px-5 py-3 transition-colors hover:bg-surface-2/40',
        selected && 'bg-surface-2/40',
      )}
      onClick={onSelect}
    >
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13.5px] font-medium text-foreground">{campaign.name}</div>
        <div className="truncate font-mono text-[11px] text-faint">
          {campaign.subject}
          {error && <span className="text-danger"> · {error}</span>}
        </div>
      </div>

      <StatusPill tone={campaignTone(campaign.status)}>{campaignLabel(campaign.status)}</StatusPill>

      {campaign.status === 'draft' && (
        <Button
          variant="secondary"
          size="xs"
          disabled={isLoading}
          onClick={(e) => {
            e.stopPropagation()
            onLaunch()
          }}
        >
          <Rocket className="size-3.5" />
          Launch
        </Button>
      )}

      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${campaign.name}`} onClick={(e) => e.stopPropagation()}>
            <MoreVertical className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onClick={onSelect}>{selected ? 'Hide stats' : 'View stats'}</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </li>
  )
}

function CampaignDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const { data, isLoading } = useGetCampaignQuery({ id })
  const stats = data?.stats ?? {}
  const n = (k: string) => stats[k] ?? 0
  return (
    <div className="border-b border-border bg-surface/40">
      <SectionBar label={`Sends · ${data?.name ?? ''}`}>
        <Button variant="ghost" size="xs" onClick={onClose}>
          Close
        </Button>
      </SectionBar>
      {isLoading ? (
        <div className="px-5 py-4">
          <Skeleton className="h-6 w-64" />
        </div>
      ) : (
        <div className="grid grid-cols-2 md:grid-cols-4">
          <Stat label="Queued" value={n('queued')} dot={<Dot className="bg-faint" />} />
          <Stat label="Sent" value={n('sent')} dot={<Dot className="bg-ok" />} />
          <Stat label="Failed" value={n('failed')} dot={<Dot className="bg-danger" />} />
          <Stat label="Skipped" value={n('skipped')} dot={<Dot className="bg-warn" />} />
        </div>
      )}
    </div>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1, 2].map((i) => (
        <li key={i} className="flex items-center gap-4 border-b border-border px-5 py-3.5">
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-48" />
            <Skeleton className="h-2.5 w-64" />
          </div>
          <Skeleton className="h-4 w-16" />
        </li>
      ))}
    </ul>
  )
}

function Dot({ className }: { className?: string }) {
  return <span className={cn('size-1.5 rounded-full', className)} aria-hidden="true" />
}
