import { useId, useState } from 'react'
import { Loader2, Plus } from 'lucide-react'
import {
  AlertDialog,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { EmptyBlock, Page, PageBody, PageTopbar, SectionBar } from '@/components/layout/page'
import { ScrollDialogBody, ScrollDialogContent } from '@/components/shared/scroll-dialog'
import { useHasRole } from '@/hooks/use-has-role'
import { useCreateWebhookEndpointMutation, useListWebhookEndpointsQuery } from './api'
import { SecretReveal } from './secret-reveal'
import { WebhookEndpointRow } from './webhook-endpoint-row'
import {
  CREATE_DESCRIPTION,
  CREATE_TITLE,
  EMPTY_DESCRIPTION,
  EMPTY_TITLE,
  EVENT_TYPE_CATALOG,
  EVENT_TYPE_COPY,
  PAGE_INTRO,
  SECRET_REVEAL_CREATE_DESCRIPTION,
  webhookActionMessage,
  webhookErrorMessage,
  type WebhookEventType,
} from './webhook-copy'

/**
 * Settings → Webhooks. Outbound webhooks (server-side in PR #171) shipped
 * with no UI at all — an operator could not register a receiver without
 * `curl`. Server state lives entirely in RTK Query
 * (`features/webhooks/api.ts`); this component owns only the create-dialog
 * and list-error UI state, matching `dead-letters-page.tsx` and
 * `api-keys-panel.tsx`.
 *
 * Admin-gated like the API-keys, connected-apps and AI panels: an endpoint
 * streams workspace event payloads to an operator-chosen URL and carries an
 * HMAC signing secret, so `webhook.Routes()` wraps the whole router in
 * `RequireRole("admin")`. This mirrors that so a non-admin who deep-links here
 * gets an honest empty state instead of a "couldn't load" banner over a 403 —
 * the server remains the boundary.
 */
export function WebhooksPage() {
  const isAdmin = useHasRole('admin')
  const [creating, setCreating] = useState(false)
  const { data, isLoading, isError, error, refetch } = useListWebhookEndpointsQuery(undefined, { skip: !isAdmin })
  const endpoints = data?.items ?? []

  if (!isAdmin) {
    return (
      <Page>
        <PageTopbar eyebrow="Settings" title="Webhooks" subtitle="Outbound event notifications" />
        <EmptyBlock
          title="Admins only"
          description="Ask a workspace owner or admin to register a webhook receiver for this workspace."
        />
      </Page>
    )
  }

  return (
    <Page>
      <PageTopbar
        eyebrow="Settings"
        title="Webhooks"
        subtitle="Outbound event notifications"
        actions={
          <Button variant="primary" size="sm" disabled={isLoading || isError} onClick={() => setCreating(true)}>
            <Plus className="size-4" />
            New endpoint
          </Button>
        }
      />

      <PageBody>
        <p className="max-w-prose px-4 pt-3 text-[12px] leading-snug text-muted-foreground sm:px-5">{PAGE_INTRO}</p>

        {isLoading ? (
          <LoadingRows />
        ) : isError ? (
          <ListError
            message={webhookErrorMessage(error, "Couldn't load webhook endpoints. Refresh the page to try again.")}
            onRetry={() => void refetch()}
          />
        ) : endpoints.length === 0 ? (
          <EmptyBlock title={EMPTY_TITLE} description={EMPTY_DESCRIPTION} />
        ) : (
          <>
            <SectionBar label="Endpoints" count={endpoints.length} />
            <ul>
              {endpoints.map((endpoint) => (
                <WebhookEndpointRow key={endpoint.id} endpoint={endpoint} />
              ))}
            </ul>
          </>
        )}
      </PageBody>

      {creating && <CreateEndpointDialog onClose={() => setCreating(false)} />}
    </Page>
  )
}

function CreateEndpointDialog({ onClose }: { onClose: () => void }) {
  const urlId = useId()
  const descriptionId = useId()
  const [create, { isLoading }] = useCreateWebhookEndpointMutation()

  const [url, setUrl] = useState('')
  const [description, setDescription] = useState('')
  const [events, setEvents] = useState<WebhookEventType[]>([])
  const [error, setError] = useState<string | null>(null)
  const [secret, setSecret] = useState<string | null>(null)

  const canSubmit = url.trim().length > 0 && !isLoading

  function toggleEvent(value: WebhookEventType) {
    setError(null)
    setEvents((prev) => (prev.includes(value) ? prev.filter((e) => e !== value) : [...prev, value]))
  }

  async function onSubmit() {
    if (!canSubmit) return
    setError(null)
    try {
      const result = await create({
        webhookEndpointInput: {
          url: url.trim(),
          description: description.trim(),
          event_types: events,
        },
      }).unwrap()
      // Held only while this dialog is mounted. RTK Query DOES mirror a
      // mutation's `data` — secret included — into
      // `state[api.reducerPath].mutations[...]`, so "never Redux" would be
      // false; what is true is that the `api` reducer is not persisted
      // (store/index.ts whitelists UI slices only) and this dialog unmounts on
      // close, which drops its own mutation cache entry with it. The server
      // will not return this value again either way.
      setSecret(result.secret)
    } catch (err) {
      setError(webhookActionMessage(err, 'create'))
    }
  }

  const revealed = secret !== null

  return (
    <AlertDialog
      open
      onOpenChange={(next) => {
        // The secret step must be dismissed explicitly — see secret-reveal.tsx
        // for why `open` staying true is what makes Escape/outside-click a
        // no-op once `revealed` is true.
        if (!next && !revealed && !isLoading) onClose()
      }}
    >
      <ScrollDialogContent className="sm:max-w-lg">
        {revealed ? (
          <SecretReveal description={SECRET_REVEAL_CREATE_DESCRIPTION} secret={secret} onDone={onClose} />
        ) : (
          <>
            <AlertDialogHeader>
              <AlertDialogTitle>{CREATE_TITLE}</AlertDialogTitle>
              <AlertDialogDescription>{CREATE_DESCRIPTION}</AlertDialogDescription>
            </AlertDialogHeader>

            <ScrollDialogBody>
              <div>
                <Label htmlFor={urlId}>Receiver URL</Label>
                <Input
                  id={urlId}
                  className="mt-1.5"
                  autoFocus
                  type="url"
                  placeholder="https://example.com/webhooks/inroad"
                  value={url}
                  onChange={(e) => {
                    setUrl(e.target.value)
                    setError(null)
                  }}
                />
              </div>

              <div>
                <Label htmlFor={descriptionId}>Description (optional)</Label>
                <Input
                  id={descriptionId}
                  className="mt-1.5"
                  placeholder="e.g. Zapier — reply routing"
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </div>

              <fieldset>
                <legend className="text-[13px] font-medium text-foreground">Events</legend>
                <p className="mt-0.5 text-[12px] text-muted-foreground">
                  Leave every box unchecked to subscribe to all events.
                </p>
                <div className="mt-2 flex flex-col gap-1.5">
                  {EVENT_TYPE_CATALOG.map((type) => (
                    <label key={type} className="flex cursor-pointer items-start gap-2 text-[13px] text-foreground">
                      <input
                        type="checkbox"
                        className="mt-0.5 size-4 accent-primary"
                        checked={events.includes(type)}
                        onChange={() => toggleEvent(type)}
                      />
                      <span>
                        {EVENT_TYPE_COPY[type].label}
                        <span className="block text-[12px] text-muted-foreground">
                          {EVENT_TYPE_COPY[type].description}
                        </span>
                      </span>
                    </label>
                  ))}
                </div>
              </fieldset>

              {error && (
                <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
                  {error}
                </p>
              )}
            </ScrollDialogBody>

            <AlertDialogFooter>
              <Button variant="ghost" size="sm" onClick={onClose} disabled={isLoading}>
                Cancel
              </Button>
              <Button variant="primary" size="sm" disabled={!canSubmit} onClick={() => void onSubmit()}>
                {isLoading && <Loader2 className="size-3.5 animate-spin" />}
                Create endpoint
              </Button>
            </AlertDialogFooter>
          </>
        )}
      </ScrollDialogContent>
    </AlertDialog>
  )
}

function ListError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <EmptyBlock
      title="Couldn't load webhook endpoints"
      description={message}
      action={
        <Button variant="outline" size="sm" onClick={onRetry}>
          Retry
        </Button>
      }
    />
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1].map((i) => (
        <li key={i} className="border-b border-border px-4 py-3 sm:px-5">
          <div className="space-y-2">
            <Skeleton className="h-3.5 w-64" />
            <Skeleton className="h-2.5 w-40" />
          </div>
        </li>
      ))}
    </ul>
  )
}
