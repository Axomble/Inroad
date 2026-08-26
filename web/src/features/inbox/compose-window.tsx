import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { X, Minus, Loader2, CalendarClock, Trash2, ChevronDown, Check } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { httpStatus, serverDetail } from '@/lib/rtk-error'
import { cn } from '@/lib/utils'
import { useListMailboxesQuery } from '@/store/api'
import {
  useSaveInboxComposeDraftMutation,
  useDeleteInboxComposeDraftMutation,
  useSendInboxComposeMutation,
  type InboxComposeDraft,
} from './api'
import { RecipientInput } from './recipient-input'
import { looksLikeEmail } from './recipient-parsing'
import { parseCustomSnooze, toDateTimeLocalValue } from './snooze-presets'

/** Mirrors the API's own caps, so the form cannot submit what the server refuses. */
const MAX_RECIPIENTS = 25
const MAX_SUBJECT = 500
const MAX_BODY = 100_000

/** How long after the last keystroke the draft is saved. */
const AUTOSAVE_DELAY_MS = 1500

/**
 * A docked window for writing a NEW email.
 *
 * Autosaves to a server-side draft on a debounce, so closing the tab does not
 * lose the text. The draft id is minted here when the window opens, which makes
 * every save an idempotent PUT to the same row — the window never has to track
 * whether it has saved before.
 */
export function ComposeWindow({
  onClose,
  resumeDraft,
}: {
  onClose: () => void
  /** An existing draft to reopen, or undefined for a blank one. */
  resumeDraft?: InboxComposeDraft
}) {
  // Minted once per window. `useState` with an initializer, not useMemo: this
  // must survive re-renders and is state, not a derivation.
  const [draftId] = useState(() => resumeDraft?.id ?? crypto.randomUUID())
  const [minimized, setMinimized] = useState(false)
  const [mailboxId, setMailboxId] = useState(resumeDraft?.mailbox_id ?? '')
  const [to, setTo] = useState<string[]>(resumeDraft?.to_emails ?? [])
  const [cc, setCc] = useState<string[]>(resumeDraft?.cc_emails ?? [])
  const [bcc, setBcc] = useState<string[]>(resumeDraft?.bcc_emails ?? [])
  const [showCcBcc, setShowCcBcc] = useState((resumeDraft?.cc_emails?.length ?? 0) > 0 || (resumeDraft?.bcc_emails?.length ?? 0) > 0)
  const [subject, setSubject] = useState(resumeDraft?.subject ?? '')
  const [bodyText, setBodyText] = useState(resumeDraft?.body_text ?? '')
  const [scheduleAt, setScheduleAt] = useState('')
  const [showSchedule, setShowSchedule] = useState(false)
  const [scheduleError, setScheduleError] = useState<string | null>(null)

  const { data: mailboxes } = useListMailboxesQuery()
  const [saveDraft, { isLoading: isSaving }] = useSaveInboxComposeDraftMutation()
  const [deleteDraft] = useDeleteInboxComposeDraftMutation()
  const [sendCompose, { isLoading: isSending, error: sendError }] = useSendInboxComposeMutation()

  const subjectId = useId()
  const bodyId = useId()

  // A single mailbox is chosen for the operator — there is no decision to make.
  const mailboxOptions = useMemo(
    () => (mailboxes ?? []).map((m) => ({ id: m.id ?? '', label: m.email ?? 'Mailbox' })),
    [mailboxes],
  )
  useEffect(() => {
    if (mailboxId === '' && mailboxOptions.length === 1) setMailboxId(mailboxOptions[0]?.id ?? '')
  }, [mailboxId, mailboxOptions])

  // Autosave, debounced. The timer is keyed on the content, so it restarts while
  // typing and fires once the operator pauses.
  const autosaveRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const hasContent = to.length > 0 || subject !== '' || bodyText !== ''
  useEffect(() => {
    if (!hasContent) return
    if (autosaveRef.current) clearTimeout(autosaveRef.current)
    autosaveRef.current = setTimeout(() => {
      void saveDraft({
        draftId,
        saveInboxComposeDraftRequest: {
          ...(mailboxId ? { mailbox_id: mailboxId } : {}),
          to_emails: to,
          cc_emails: cc,
          bcc_emails: bcc,
          subject,
          body_text: bodyText,
        },
      })
    }, AUTOSAVE_DELAY_MS)
    return () => {
      if (autosaveRef.current) clearTimeout(autosaveRef.current)
    }
  }, [draftId, mailboxId, to, cc, bcc, subject, bodyText, hasContent, saveDraft])

  const recipientCount = to.length + cc.length + bcc.length
  const overRecipientCap = recipientCount > MAX_RECIPIENTS
  const invalidAddress = [...to, ...cc, ...bcc].some((v) => !looksLikeEmail(v))
  const overBody = bodyText.length > MAX_BODY
  const overSubject = subject.length > MAX_SUBJECT

  const canSend =
    mailboxId !== '' &&
    to.length > 0 &&
    bodyText.trim() !== '' &&
    !overRecipientCap &&
    !invalidAddress &&
    !overBody &&
    !overSubject &&
    !isSending

  const send = async (sendAt?: string) => {
    if (!canSend) return
    const result = await sendCompose({
      sendInboxComposeRequest: {
        mailbox_id: mailboxId,
        to_emails: to,
        cc_emails: cc,
        bcc_emails: bcc,
        subject,
        body_text: bodyText,
        draft_id: draftId,
        ...(sendAt ? { send_at: sendAt } : {}),
      },
    })
    if ('error' in result) return
    // The server discards the draft on a successful send; closing here is the
    // last step so a failure leaves the window open with the text intact.
    onClose()
  }

  const sendLater = () => {
    const parsed = parseCustomSnooze(scheduleAt, new Date())
    if (!parsed.ok) {
      setScheduleError(parsed.reason)
      return
    }
    setScheduleError(null)
    void send(parsed.at.toISOString())
  }

  const discard = () => {
    void deleteDraft({ draftId })
    onClose()
  }

  if (minimized) {
    return (
      <div className="fixed right-4 bottom-4 z-50 flex items-center gap-2 rounded-t-lg border border-border bg-surface px-3 py-2 shadow-lg">
        <button
          type="button"
          className="truncate text-[12px] font-medium text-foreground"
          onClick={() => setMinimized(false)}
        >
          {subject || 'New message'}
        </button>
        <Button variant="ghost" size="icon-sm" aria-label="Close compose" onClick={onClose}>
          <X className="size-3.5" />
        </Button>
      </div>
    )
  }

  return (
    <section
      aria-label="New message"
      className="fixed right-4 bottom-4 z-50 flex max-h-[80vh] w-[min(34rem,calc(100vw-2rem))] flex-col rounded-lg border border-border bg-surface shadow-xl"
    >
      <header className="flex items-center gap-2 border-b border-border px-3 py-2">
        <h2 className="min-w-0 flex-1 truncate text-[13px] font-semibold text-foreground">
          {subject || 'New message'}
        </h2>
        {/* A saving indicator, so autosave is visible rather than a promise. */}
        {isSaving && <span className="shrink-0 text-[10px] text-faint">Saving…</span>}
        <Button variant="ghost" size="icon-sm" aria-label="Minimize compose" onClick={() => setMinimized(true)}>
          <Minus className="size-3.5" />
        </Button>
        <Button variant="ghost" size="icon-sm" aria-label="Close compose" onClick={onClose}>
          <X className="size-3.5" />
        </Button>
      </header>

      <div className="flex min-h-0 flex-1 flex-col gap-1.5 overflow-y-auto p-3">
        <div className="flex items-center gap-2">
          <span className="w-10 shrink-0 font-mono text-[10px] tracking-wide text-faint uppercase">From</span>
          {/* Its own picker rather than the shared SortMenu: choosing a sending
              mailbox is not an ordering, and a trigger reading "Sort" here
              taught the wrong model of what the menu does. */}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="xs"
                aria-label={`Send from ${mailboxOptions.find((m) => m.id === mailboxId)?.label ?? 'a mailbox'}`}
                className="max-w-full font-normal"
              >
                <span className="min-w-0 truncate">
                  {mailboxOptions.find((m) => m.id === mailboxId)?.label ?? 'Choose a mailbox'}
                </span>
                <ChevronDown className="size-3.5 shrink-0" aria-hidden="true" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              {mailboxOptions.map((option) => (
                <DropdownMenuItem key={option.id} onSelect={() => setMailboxId(option.id)}>
                  <Check
                    className={option.id === mailboxId ? 'size-4 opacity-100' : 'size-4 opacity-0'}
                    aria-hidden="true"
                  />
                  {option.label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>

        <RecipientInput id="compose-to" label="To" values={to} onChange={setTo} placeholder="name@company.com" />

        {showCcBcc ? (
          <>
            <RecipientInput id="compose-cc" label="Cc" values={cc} onChange={setCc} />
            <RecipientInput id="compose-bcc" label="Bcc" values={bcc} onChange={setBcc} />
          </>
        ) : (
          <button
            type="button"
            className="self-start pl-12 text-[11px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
            onClick={() => setShowCcBcc(true)}
          >
            Add Cc / Bcc
          </button>
        )}

        <div className="flex items-center gap-2">
          <label htmlFor={subjectId} className="w-10 shrink-0 font-mono text-[10px] tracking-wide text-faint uppercase">
            Subj
          </label>
          <Input
            id={subjectId}
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
            placeholder="Subject"
            className="h-7 border-0 bg-transparent px-1 text-[12px] shadow-none focus-visible:ring-0"
          />
        </div>

        <label htmlFor={bodyId} className="sr-only">
          Message
        </label>
        <Textarea
          id={bodyId}
          rows={10}
          value={bodyText}
          onChange={(e) => setBodyText(e.target.value)}
          placeholder="Write your message…"
          className="min-h-40 flex-1 resize-none text-[13px]"
        />

        {/* One alert surface. Client-side complaints take precedence over a
            stale server error, since they describe what is on screen now. */}
        {(invalidAddress || overRecipientCap || overSubject || overBody || sendError !== undefined) && (
          <p role="alert" className="text-[11px] text-danger">
            {invalidAddress
              ? "One of the addresses doesn't look like an email — check the highlighted chips."
              : overRecipientCap
                ? `Too many recipients — ${recipientCount} of a maximum ${MAX_RECIPIENTS} across To, Cc and Bcc.`
                : overSubject
                  ? `Subject is too long — max ${MAX_SUBJECT} characters.`
                  : overBody
                    ? `Message is too long — max ${MAX_BODY.toLocaleString()} characters.`
                    : composeErrorMessage(sendError)}
          </p>
        )}

        {/* Each recipient gets their own copy, so this is worth saying before
            they hit Send rather than after someone asks why nobody could
            reply-all. */}
        {recipientCount > 1 && (
          <p className="text-[10px] text-faint">
            Each recipient receives their own copy — they won't see each other.
          </p>
        )}

        {showSchedule && (
          <div className="flex flex-wrap items-center gap-2">
            <Input
              type="datetime-local"
              value={scheduleAt}
              min={toDateTimeLocalValue(new Date())}
              aria-label="Send this message at a specific date and time"
              aria-invalid={scheduleError !== null}
              className="h-8 w-auto text-[12px]"
              onChange={(e) => {
                setScheduleAt(e.target.value)
                setScheduleError(null)
              }}
            />
            <Button variant="secondary" size="xs" disabled={!canSend} onClick={sendLater}>
              Schedule
            </Button>
            {scheduleError && (
              <p role="alert" className="text-[11px] text-danger">
                {scheduleError}
              </p>
            )}
          </div>
        )}
      </div>

      <footer className="flex items-center gap-2 border-t border-border px-3 py-2">
        <Button variant="primary" size="sm" disabled={!canSend} onClick={() => void send()}>
          {isSending && <Loader2 className="animate-spin" />}
          Send
        </Button>
        <Button
          variant="ghost"
          size="sm"
          disabled={!canSend}
          aria-expanded={showSchedule}
          onClick={() => {
            setShowSchedule((open) => !open)
            setScheduleError(null)
          }}
        >
          <CalendarClock />
          Send later
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Discard draft"
          className={cn('ml-auto', !hasContent && 'invisible')}
          onClick={discard}
        >
          <Trash2 className="size-3.5" />
        </Button>
      </footer>
    </section>
  )
}

/**
 * Human copy for a failed send. 422 carries the server's own explanation of
 * WHICH rule was broken (which recipient, which cap) and that detail is worth
 * surfacing verbatim; other statuses get a generic line, since their bodies are
 * not written for an operator to read.
 */
function composeErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === 409) return 'One of the recipients has unsubscribed or bounced.'
  if (status === 422) return serverDetail(error) ?? "That message can't be sent as written."
  if (status === 404) return 'That sending mailbox is no longer available.'
  return `Couldn't send${status ? ` (${status})` : ''} — try again.`
}
