import { useState } from 'react'
import { AlertCircle, Check, Copy } from 'lucide-react'
import { AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { SECRET_REVEAL_TITLE, SECRET_REVEAL_UNRECOVERABLE } from './webhook-copy'

/**
 * The one-time signing-secret reveal, shared by create and rotate-secret —
 * both mint a secret the server will never return again.
 *
 * Deliberately NOT an `AlertDialog` of its own: it renders as the content of
 * whichever dialog the caller already has open, because the "cannot be
 * dismissed" behaviour has to live in that dialog's `open`/`onOpenChange`
 * wiring (a hardcoded `open` prop the caller stops tying to Escape/outside
 * clicks once the secret is showing — see `CreateEndpointDialog` and
 * `RotateSecretDialog`). Duplicating an `open` state in here as well would
 * give the secret two independent ways to vanish, one of which the operator
 * could not see coming.
 *
 * `onDone` is the ONLY way out, and its label says what the operator is
 * attesting to rather than just dismissing a dialog.
 */
export function SecretReveal({
  description,
  secret,
  onDone,
}: {
  description: string
  secret: string
  onDone: () => void
}) {
  const [copied, setCopied] = useState(false)

  function onCopy() {
    void navigator.clipboard?.writeText(secret).then(
      () => setCopied(true),
      () => setCopied(false),
    )
  }

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle>{SECRET_REVEAL_TITLE}</AlertDialogTitle>
        <AlertDialogDescription>{description}</AlertDialogDescription>
      </AlertDialogHeader>

      <div className="flex items-center gap-2 rounded-md border border-border bg-surface-2 p-3">
        <code className="min-w-0 flex-1 select-all break-all font-mono text-[12.5px] text-foreground">{secret}</code>
        <Button variant="outline" size="sm" onClick={onCopy} aria-label="Copy signing secret">
          {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>

      <p role="alert" className="flex items-center gap-2 text-xs text-warn">
        <AlertCircle className="size-3.5 shrink-0" aria-hidden="true" />
        {SECRET_REVEAL_UNRECOVERABLE}
      </p>

      <AlertDialogFooter>
        <Button variant="primary" size="sm" onClick={onDone}>
          I&rsquo;ve copied it
        </Button>
      </AlertDialogFooter>
    </>
  )
}
