import { useId, useState } from 'react'
import { Loader2, ShieldCheck } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { httpStatus } from '@/lib/rtk-error'
import { SENTINEL_MARK_DETAIL, designationPrompt } from './sentinel-copy'
import { useSetWarmupSentinelMutation } from './api'

/**
 * Copy for a designation that did not happen.
 *
 * Its own mapper rather than `warmupErrorMessage`, which answers for a failed
 * READ — "this could not be loaded" is the wrong sentence for a write, and the
 * one thing an operator needs to know here is that nothing changed. 404 is the
 * one status with a specific meaning: the mailbox left the pool between the page
 * load and the click, so retrying will not help.
 */
function designationErrorMessage(error: unknown, next: boolean): string {
  const status = httpStatus(error)
  if (status === 404) {
    return 'This mailbox is no longer a warmup participant, so there is nothing to designate — refresh the page.'
  }
  if (status === 403) return "You don't have access to change this workspace's warmup pool."
  const verb = next ? 'designate this mailbox as a sentinel' : 'stop using this mailbox as a sentinel'
  return `Couldn't ${verb}. Nothing changed — try again.`
}

/**
 * Designate a mailbox as a measurement sentinel, or stop using it as one.
 *
 * The control is a TWO-STEP on purpose, and that is the whole point of the
 * component. Designating is a real decision with a real cost: the mailbox starts
 * receiving warmup mail from degrading members that the rest of the pool is
 * shielded from. A one-click switch reveals that afterwards, by which time the
 * operator has already taken it — so the first click asks and writes nothing, and
 * the sentence naming the exposure is on screen before the request exists.
 *
 * Undesignating asks its own question rather than reusing the first: its
 * consequence is different (evidence already gathered stops counting as
 * corroboration), and a shared "are you sure?" would state neither.
 *
 * Absent — not false — is silence. `isSentinel` is undefined on a build that does
 * not report sentinels, which is also a build with no endpoint behind this button,
 * and a control that 404s is worse than a feature that is not there.
 */
export function WarmupSentinelToggle({
  mailboxId,
  email,
  isSentinel,
}: {
  mailboxId: string
  email: string
  /** Undefined on a build that does not report sentinels — renders nothing. */
  isSentinel: boolean | undefined
}) {
  const [asking, setAsking] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [setSentinel, { isLoading }] = useSetWarmupSentinelMutation()
  const titleId = useId()

  if (isSentinel === undefined) return null

  const next = !isSentinel
  const prompt = designationPrompt(email, next)

  async function onConfirm() {
    setError(null)
    const result = await setSentinel({ mailboxId, warmupSentinelRequest: { is_sentinel: next } })
    if ('error' in result) {
      setError(designationErrorMessage(result.error, next))
      return
    }
    setAsking(false)
  }

  return (
    <div className="px-4 pb-3 sm:px-5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Button
          variant={isSentinel ? 'outline' : 'ghost'}
          size="xs"
          onClick={() => {
            setError(null)
            setAsking((open) => !open)
          }}
          aria-expanded={asking}
          aria-label={next ? `Designate as sentinel for ${email}` : `Stop using as sentinel for ${email}`}
        >
          <ShieldCheck className="size-3.5" />
          {isSentinel ? 'Sentinel' : 'Make sentinel'}
        </Button>
        {isSentinel && (
          <span data-slot="sentinel-mark-detail" className="max-w-prose text-[10.5px] leading-snug text-faint">
            {SENTINEL_MARK_DETAIL}
          </span>
        )}
      </div>

      {asking && (
        // A group rather than a dialog: nothing is being interrupted, the card
        // stays readable behind the question, and a modal over a list of ten
        // mailboxes hides the very pool the decision is about.
        <div
          role="group"
          aria-labelledby={titleId}
          data-slot="sentinel-prompt"
          className="mt-2 max-w-prose border-l border-border pl-3"
        >
          <p id={titleId} className="text-[12px] font-medium text-foreground">
            {prompt.title}
          </p>
          <p className="mt-1 text-[11.5px] leading-snug text-muted-foreground">{prompt.body}</p>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Button variant="outline" size="xs" disabled={isLoading} onClick={() => void onConfirm()}>
              {isLoading && <Loader2 className="size-3.5 animate-spin" />}
              {prompt.confirm}
            </Button>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => {
                setError(null)
                setAsking(false)
              }}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}

      {error && (
        <p role="alert" className="mt-1.5 text-[11px] text-danger">
          {error}
        </p>
      )}
    </div>
  )
}
