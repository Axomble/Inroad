import { useEffect, useId, useRef, useState } from 'react'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/store/api'
import { useAppDispatch } from '@/store/hooks'
import { useSendInboxReplyMutation } from './api'
import { replyErrorMessage } from './reply-error'

/** Matches `SendInboxReplyRequest.body_text`'s `maxLength` in api/openapi.yaml. */
const MAX_BODY_LENGTH = 100_000

/**
 * How long after a 202 to fire one extra cache-tag invalidation. The send is
 * async (queued, not delivered) — the immediate invalidation from
 * `sendInboxReply`'s `invalidatesTags` can refetch before the worker has
 * written the outbound message, so this covers the gap with a single bounded
 * timer rather than polling machinery.
 */
const DELAYED_REFETCH_MS = 2000

/**
 * Plain-text reply composer for a thread. Rendered at the bottom of the
 * thread detail view; sending is fire-and-forget from the caller's
 * perspective — the outbound message shows up in the thread once the worker
 * delivers it, not the instant this resolves.
 */
export function ReplyComposer({ threadId, hasInboundMessage }: { threadId: string; hasInboundMessage: boolean }) {
  const [bodyText, setBodyText] = useState('')
  const [sent, setSent] = useState(false)
  const [sendReply, { isLoading, error }] = useSendInboxReplyMutation()
  const dispatch = useAppDispatch()
  const textareaId = useId()
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(
    () => () => {
      if (timeoutRef.current) clearTimeout(timeoutRef.current)
    },
    [],
  )

  const trimmed = bodyText.trim()
  const tooLong = bodyText.length > MAX_BODY_LENGTH
  const canSend = hasInboundMessage && trimmed !== '' && !tooLong && !isLoading

  async function onSend() {
    if (!canSend) return
    setSent(false)
    const result = await sendReply({ id: threadId, sendInboxReplyRequest: { body_text: bodyText } })
    if ('error' in result) return
    setBodyText('')
    setSent(true)
    if (timeoutRef.current) clearTimeout(timeoutRef.current)
    timeoutRef.current = setTimeout(() => {
      dispatch(api.util.invalidateTags([{ type: 'InboxThread', id: threadId }]))
    }, DELAYED_REFETCH_MS)
  }

  if (!hasInboundMessage) {
    return (
      <p className="border-t border-border pt-4 text-xs text-muted-foreground">
        You can reply once this contact has sent an inbound message.
      </p>
    )
  }

  return (
    <form
      className="flex flex-col gap-2 border-t border-border pt-4"
      onSubmit={(e) => {
        e.preventDefault()
        void onSend()
      }}
    >
      <Label htmlFor={textareaId}>Reply</Label>
      <Textarea
        id={textareaId}
        rows={4}
        value={bodyText}
        disabled={isLoading}
        aria-busy={isLoading}
        placeholder="Write a reply…"
        onChange={(e) => {
          setBodyText(e.target.value)
          setSent(false)
        }}
      />
      {tooLong && (
        <p role="alert" className="text-xs text-danger">
          Reply is too long — max 100,000 characters ({bodyText.length.toLocaleString()} so far).
        </p>
      )}
      {error && (
        <p role="alert" className="text-xs text-danger">
          {replyErrorMessage(error)}
        </p>
      )}
      {sent && !error && (
        <p role="status" className="text-xs text-ok">
          Reply sent — it will appear in the thread shortly.
        </p>
      )}
      <div className="flex justify-end">
        <Button type="submit" variant="primary" size="sm" disabled={!canSend}>
          {isLoading && <Loader2 className="animate-spin" />}
          Send
        </Button>
      </div>
    </form>
  )
}
