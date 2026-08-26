import { useEffect, useId, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Loader2, Sparkles, CalendarClock } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Input } from '@/components/ui/input'
import { api } from '@/store/api'
import { useAppDispatch } from '@/store/hooks'
import { useDraftInboxReplyMutation, useScheduleInboxReplyMutation } from './api'
// Cross-feature query-hook imports are allowed for read-only reference data
// (see features/campaigns/api.ts). Cross-feature UI imports remain forbidden.
import { useListAiModelsQuery } from '@/features/ai-settings/api'
import { replyErrorMessage } from './reply-error'
import { draftReplyError, type DraftReplyError } from './draft-reply-error'
// The send-later picker reuses the snooze bounds-checker: both answer "is this
// a usable future instant?" against the same rules, and a second copy of that
// logic would be a second place for the two to disagree.
import { parseCustomSnooze, toDateTimeLocalValue } from './snooze-presets'

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
 *
 * The AI draft action only ever fills this textarea: a human reviews, edits,
 * and presses Send. Nothing here auto-sends.
 */
export function ReplyComposer({ threadId, hasInboundMessage }: { threadId: string; hasInboundMessage: boolean }) {
  const [bodyText, setBodyText] = useState('')
  const [sent, setSent] = useState(false)
  const [confirmingOverwrite, setConfirmingOverwrite] = useState(false)
  /** Inline complaint about a chosen send-later instant, before any request. */
  const [scheduleError, setScheduleError] = useState<string | null>(null)
  const [showSchedulePicker, setShowSchedulePicker] = useState(false)
  /** The send-later field's value. Controlled state, not a DOM read: reading a
   * ref back at click time is fragile and leaves the value untestable. */
  const [scheduleAt, setScheduleAt] = useState('')
  /** Bumped by every landed draft: drives the focus/caret effect and the announcement. */
  const [draftToken, setDraftToken] = useState(0)
  // Scheduling, not the immediate send: the reply goes out after the
  // workspace's undo window, during which the reader shows a countdown and an
  // Undo control (see UndoSendPill). A zero window means it leaves at once, so
  // this path also covers a workspace that opted out of undo entirely.
  const [scheduleReply, { isLoading, error }] = useScheduleInboxReplyMutation()
  const [draftReply, { isLoading: isDrafting, error: draftError, reset: resetDraftState }] =
    useDraftInboxReplyMutation()
  const modelsQuery = useListAiModelsQuery()
  const dispatch = useAppDispatch()
  const textareaId = useId()
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(
    () => () => {
      if (timeoutRef.current) clearTimeout(timeoutRef.current)
    },
    [],
  )

  // A landed draft hands the caret straight back to the human, at the end of
  // the generated text, so editing needs no extra click.
  useEffect(() => {
    if (draftToken === 0) return
    const el = textareaRef.current
    if (!el) return
    el.focus()
    el.setSelectionRange(el.value.length, el.value.length)
  }, [draftToken])

  const trimmed = bodyText.trim()
  const tooLong = bodyText.length > MAX_BODY_LENGTH
  const canSend = hasInboundMessage && trimmed !== '' && !tooLong && !isLoading && !isDrafting

  // Only a loaded, successful model list can prove there is nothing to draft
  // with; while it is loading or if it failed, the button stays live and the
  // endpoint's own "no model configured" error does the explaining.
  const noAiModel =
    !modelsQuery.isLoading && !modelsQuery.isError && !(modelsQuery.data?.models ?? []).some((m) => m.enabled)
  const canDraft = hasInboundMessage && !isDrafting && !isLoading && !noAiModel

  async function onSend(sendAt?: string) {
    if (!canSend) return
    setSent(false)
    const result = await scheduleReply({
      id: threadId,
      scheduleInboxReplyRequest: { body_text: bodyText, ...(sendAt ? { send_at: sendAt } : {}) },
    })
    if ('error' in result) return
    setBodyText('')
    setSent(true)
    setShowSchedulePicker(false)
    setScheduleAt('')
    setDraftToken(0)
    resetDraftState()
    if (timeoutRef.current) clearTimeout(timeoutRef.current)
    timeoutRef.current = setTimeout(() => {
      dispatch(api.util.invalidateTags([{ type: 'InboxThread', id: threadId }]))
    }, DELAYED_REFETCH_MS)
  }

  /** Send-later: the operator picks an instant, the reply waits for it. */
  function onSendAt(value: string) {
    const result = parseCustomSnooze(value, new Date())
    if (!result.ok) {
      setScheduleError(result.reason)
      return
    }
    setScheduleError(null)
    void onSend(result.at.toISOString())
  }

  async function runDraft() {
    setConfirmingOverwrite(false)
    setSent(false)
    const result = await draftReply({ id: threadId })
    if ('error' in result) return
    setBodyText(result.data.body_text)
    setDraftToken((token) => token + 1)
  }

  function onDraftClick() {
    if (!canDraft) return
    // Silent overwrite of something a human typed is the one unrecoverable
    // move here — confirm first, but never nag when there is nothing to lose.
    if (trimmed !== '') {
      setConfirmingOverwrite(true)
      return
    }
    void runDraft()
  }

  // One alert surface, one message: the client-side length check wins over a
  // stale server error, and a send failure is more recent news than a draft
  // failure whenever both are on the hook.
  const alert: DraftReplyError | null = tooLong
    ? {
        kind: 'message',
        text: `Reply is too long — max 100,000 characters (${bodyText.length.toLocaleString()} so far).`,
      }
    : error
      ? { kind: 'message', text: replyErrorMessage(error) }
      : draftError
        ? draftReplyError(draftError)
        : null

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
        ref={textareaRef}
        rows={4}
        value={bodyText}
        disabled={isLoading || isDrafting}
        aria-busy={isLoading || isDrafting}
        placeholder="Write a reply…"
        onChange={(e) => {
          setBodyText(e.target.value)
          setSent(false)
          setDraftToken(0)
          if (draftError) resetDraftState()
        }}
      />
      {alert && (
        <p role="alert" className="text-xs text-danger">
          {alert.text}{' '}
          {alert.kind === 'no-model' && (
            <Link to="/app/settings/ai" className="font-medium text-accent-ink underline underline-offset-2">
              Set one up in Settings → AI
            </Link>
          )}
        </p>
      )}
      {sent && !alert && (
        <p role="status" className="text-xs text-ok">
          Reply queued — you can still undo it from the thread.
        </p>
      )}

      {showSchedulePicker && (
        <div className="flex flex-wrap items-center gap-2">
          <Input
            type="datetime-local"
            min={toDateTimeLocalValue(new Date())}
            aria-label="Send this reply at a specific date and time"
            aria-invalid={scheduleError !== null}
            className="h-8 w-auto text-[12px]"
            value={scheduleAt}
            onChange={(event) => {
              setScheduleAt(event.target.value)
              setScheduleError(null)
            }}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault()
                onSendAt(scheduleAt)
              }
            }}
          />
          <Button
            type="button"
            variant="secondary"
            size="xs"
            disabled={!canSend}
            onClick={() => onSendAt(scheduleAt)}
          >
            Schedule
          </Button>
          {scheduleError && (
            <p role="alert" className="text-[11px] text-danger">
              {scheduleError}
            </p>
          )}
        </div>
      )}
      {draftToken > 0 && !sent && !alert && (
        <p role="status" className="text-xs text-muted-foreground">
          Draft ready — review and edit it before sending.
        </p>
      )}
      {noAiModel && (
        <p className="text-xs text-muted-foreground">
          Drafting needs an AI model.{' '}
          <Link to="/app/settings/ai" className="font-medium text-accent-ink underline underline-offset-2">
            Configure one in Settings → AI
          </Link>
          .
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button
          type="button"
          variant="secondary"
          size="sm"
          disabled={!canDraft}
          aria-busy={isDrafting}
          onClick={onDraftClick}
        >
          {isDrafting ? <Loader2 className="animate-spin" /> : <Sparkles />}
          Draft a reply
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={!canSend}
          aria-expanded={showSchedulePicker}
          onClick={() => {
            setShowSchedulePicker((open) => !open)
            setScheduleError(null)
          }}
        >
          <CalendarClock />
          Send later
        </Button>
        <Button type="submit" variant="primary" size="sm" disabled={!canSend}>
          {isLoading && <Loader2 className="animate-spin" />}
          Send
        </Button>
      </div>

      {confirmingOverwrite && (
        <AlertDialog open onOpenChange={(next) => !next && setConfirmingOverwrite(false)}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Replace what you've written?</AlertDialogTitle>
              <AlertDialogDescription>
                Drafting a reply overwrites the text in the reply box. Your current text can't be recovered
                afterwards.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <Button type="button" variant="ghost" size="sm" onClick={() => setConfirmingOverwrite(false)}>
                Keep my text
              </Button>
              <Button type="button" variant="primary" size="sm" onClick={() => void runDraft()}>
                Replace with a draft
              </Button>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </form>
  )
}
