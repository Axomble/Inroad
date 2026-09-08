import { useState } from 'react'
import { AlertCircle, ChevronDown, Loader2, Webhook } from 'lucide-react'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { StatusPill } from '@/components/shared/status-pill'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import type { WebhookEndpoint } from '@/store/api'
import { useDeleteWebhookEndpointMutation, usePingWebhookEndpointMutation, useRotateWebhookEndpointSecretMutation } from './api'
import { DeliveryLog } from './delivery-log'
import { SecretReveal } from './secret-reveal'
import {
  DELETE_CONFIRM,
  eventTypeBadgeLabels,
  eventTypesSummary,
  PING_QUEUED_NOTICE,
  ROTATE_CONFIRM,
  SECRET_REVEAL_ROTATE_DESCRIPTION,
  webhookActionMessage,
} from './webhook-copy'

type Confirming = 'delete' | 'rotate' | null

/**
 * One registered webhook endpoint, and every action available on it:
 * send a test ping, rotate its secret, delete it, and inspect its delivery
 * log. Actions live on the row rather than behind a navigation, the same
 * reasoning `dead-letter-row.tsx` gives — an operator managing several
 * endpoints should not need a page load per decision.
 */
export function WebhookEndpointRow({ endpoint }: { endpoint: WebhookEndpoint }) {
  const [confirming, setConfirming] = useState<Confirming>(null)
  const [rotatedSecret, setRotatedSecret] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [pingNotice, setPingNotice] = useState<string | null>(null)
  const [showDeliveries, setShowDeliveries] = useState(false)

  const [deleteEndpoint, { isLoading: deleting }] = useDeleteWebhookEndpointMutation()
  const [rotateSecret, { isLoading: rotating }] = useRotateWebhookEndpointSecretMutation()
  const [ping, { isLoading: pinging }] = usePingWebhookEndpointMutation()
  const busy = deleting || rotating || pinging

  async function onDelete() {
    setActionError(null)
    try {
      await deleteEndpoint({ id: endpoint.id }).unwrap()
      setConfirming(null)
    } catch (error) {
      // Close first so the error banner isn't hidden under the dialog.
      setConfirming(null)
      setActionError(webhookActionMessage(error, 'delete'))
    }
  }

  async function onRotate() {
    setActionError(null)
    try {
      const result = await rotateSecret({ id: endpoint.id }).unwrap()
      // The secret lives only in this row's local state, never Redux/persist —
      // it is unrecoverable the moment this component unmounts, which is the
      // point (see secret-reveal.tsx).
      setRotatedSecret(result.secret)
    } catch (error) {
      setConfirming(null)
      setActionError(webhookActionMessage(error, 'rotate'))
    }
  }

  async function onPing() {
    setActionError(null)
    setPingNotice(null)
    try {
      await ping({ id: endpoint.id }).unwrap()
      setPingNotice(PING_QUEUED_NOTICE)
    } catch (error) {
      setActionError(webhookActionMessage(error, 'ping'))
    }
  }

  return (
    <li data-slot="webhook-endpoint" className="border-b border-border">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2 px-4 py-3 sm:px-5">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <Webhook className="size-4 shrink-0 text-muted-foreground" strokeWidth={1.75} aria-hidden="true" />
            <span data-slot="webhook-endpoint-url" className="truncate font-mono text-[13px] text-foreground">
              {endpoint.url}
            </span>
            <StatusPill tone={endpoint.active ? 'running' : 'paused'}>
              {endpoint.active ? 'Active' : 'Inactive'}
            </StatusPill>
          </div>

          {endpoint.description && (
            <p className="mt-1 text-[12px] text-muted-foreground">{endpoint.description}</p>
          )}

          <div
            className="mt-1.5 flex flex-wrap gap-1"
            aria-label={`Subscribed events: ${eventTypesSummary(endpoint.event_types)}`}
          >
            {eventTypeBadgeLabels(endpoint.event_types).map((label) => (
              <Badge key={label} variant="secondary">
                {label}
              </Badge>
            ))}
          </div>

          <p className="mt-1.5 font-mono text-[11px] text-faint">created {relativeTime(endpoint.created_at)}</p>
        </div>

        <div className="flex shrink-0 flex-wrap gap-1.5">
          <Button variant="outline" size="sm" disabled={busy} onClick={() => void onPing()}>
            {pinging && <Loader2 className="size-3.5 animate-spin" />}
            Send test ping
          </Button>
          <Button variant="outline" size="sm" disabled={busy} onClick={() => setConfirming('rotate')}>
            Rotate secret
          </Button>
          <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConfirming('delete')}>
            Delete
          </Button>
        </div>
      </div>

      {actionError && (
        <p
          role="alert"
          data-slot="webhook-endpoint-action-error"
          className="flex items-start gap-1.5 px-4 pb-2.5 text-[12px] leading-snug text-danger sm:px-5"
        >
          <AlertCircle className="mt-px size-3.5 shrink-0" aria-hidden="true" />
          <span>{actionError}</span>
        </p>
      )}

      {pingNotice && (
        <p
          role="status"
          data-slot="webhook-endpoint-ping-notice"
          className="px-4 pb-2.5 text-[12px] leading-snug text-muted-foreground sm:px-5"
        >
          {pingNotice}
        </p>
      )}

      <button
        type="button"
        onClick={() => setShowDeliveries((open) => !open)}
        aria-expanded={showDeliveries}
        className="flex w-full items-center gap-1 border-t border-border px-4 py-2 font-mono text-[10px] uppercase tracking-[0.12em] text-faint hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary sm:px-5"
      >
        <ChevronDown className={cn('size-3 transition-transform', showDeliveries && 'rotate-180')} aria-hidden="true" />
        Deliveries
      </button>
      {showDeliveries && <DeliveryLog endpointId={endpoint.id} />}

      <AlertDialog open={confirming === 'delete'} onOpenChange={(next) => !next && !deleting && setConfirming(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete this endpoint?</AlertDialogTitle>
            <AlertDialogDescription>{DELETE_CONFIRM}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(null)} disabled={deleting}>
              Cancel
            </Button>
            <Button variant="destructive" size="sm" disabled={deleting} onClick={() => void onDelete()}>
              {deleting && <Loader2 className="size-3.5 animate-spin" />}
              Delete endpoint
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/*
        One dialog, two steps: confirm, then the one-time reveal. `open` stays
        true across the transition and `onOpenChange` only ever closes the
        CONFIRM step — once `rotatedSecret` is set, an Escape/outside-click
        no-ops (see secret-reveal.tsx), so the operator can leave only via
        SecretReveal's "I've copied it" button.
      */}
      <AlertDialog
        open={confirming === 'rotate' || rotatedSecret !== null}
        onOpenChange={(next) => !next && rotatedSecret === null && !rotating && setConfirming(null)}
      >
        <AlertDialogContent>
          {rotatedSecret !== null ? (
            <SecretReveal
              description={SECRET_REVEAL_ROTATE_DESCRIPTION}
              secret={rotatedSecret}
              onDone={() => {
                setRotatedSecret(null)
                setConfirming(null)
              }}
            />
          ) : (
            <>
              <AlertDialogHeader>
                <AlertDialogTitle>Rotate this endpoint&rsquo;s secret?</AlertDialogTitle>
                <AlertDialogDescription>{ROTATE_CONFIRM}</AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <Button variant="ghost" size="sm" onClick={() => setConfirming(null)} disabled={rotating}>
                  Cancel
                </Button>
                <Button variant="destructive" size="sm" disabled={rotating} onClick={() => void onRotate()}>
                  {rotating && <Loader2 className="size-3.5 animate-spin" />}
                  Confirm rotation
                </Button>
              </AlertDialogFooter>
            </>
          )}
        </AlertDialogContent>
      </AlertDialog>
    </li>
  )
}
