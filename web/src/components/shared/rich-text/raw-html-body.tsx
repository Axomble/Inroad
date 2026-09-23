import { useId, useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import type { EmailBody } from './body-editor'

function listOf(items: string[]): string {
  if (items.length <= 1) return items.join('')
  return `${items.slice(0, -1).join(', ')} and ${items.at(-1)}`
}

/**
 * A stored HTML body the editor can't hold without losing something, shown as
 * its two raw parts instead. Nothing is rewritten here: each textarea reports
 * exactly what is in it, so an untouched save sends the stored bytes back and
 * an edit changes only what was typed. Merge fields are still checked by the
 * surrounding form, which scans both parts.
 *
 * Switching to the editor is offered, but dropping formatting takes a second,
 * explicit click that names what goes.
 */
export function RawHtmlBody({
  labelledBy,
  describedBy,
  body,
  lost,
  invalid,
  onChange,
  onConvert,
}: {
  labelledBy: string
  describedBy?: string
  body: EmailBody
  /** What the editor would drop from the HTML as it stands now; empty once nothing would be. */
  lost: string[]
  invalid?: boolean
  onChange: (body: EmailBody) => void
  onConvert: () => void
}) {
  const htmlLabelId = useId()
  const textLabelId = useId()
  const textHintId = useId()
  const [confirming, setConfirming] = useState(false)
  const lossless = lost.length === 0

  return (
    <div className="grid gap-2">
      <div role="status" className="grid gap-2 rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn">
        <p className="flex items-start gap-2">
          <AlertTriangle className="mt-px size-3.5 shrink-0" aria-hidden="true" />
          {lossless ? (
            <span>Nothing in this HTML needs the raw view any more — it can move to the editor without losing anything.</span>
          ) : (
            <span>
              This email has formatting the editor can’t keep: {listOf(lost)}. It’s shown as HTML so nothing is lost —
              both parts below are saved exactly as written.
            </span>
          )}
        </p>
        {lossless ? (
          <div>
            <Button type="button" variant="outline" size="xs" onClick={onConvert}>
              Switch to the editor
            </Button>
          </div>
        ) : confirming ? (
          <div className="flex flex-wrap items-center gap-2">
            <span>Switching removes {listOf(lost)} from this email when you save.</span>
            <Button type="button" variant="destructive" size="xs" onClick={onConvert}>
              Convert and lose formatting
            </Button>
            <Button type="button" variant="ghost" size="xs" onClick={() => setConfirming(false)}>
              Keep HTML
            </Button>
          </div>
        ) : (
          <div>
            <Button type="button" variant="outline" size="xs" onClick={() => setConfirming(true)}>
              Convert to the editor…
            </Button>
          </div>
        )}
      </div>

      <span id={htmlLabelId} className="font-mono text-[10.5px] tracking-[0.12em] text-faint uppercase">
        HTML
      </span>
      <Textarea
        aria-labelledby={`${labelledBy} ${htmlLabelId}`}
        aria-describedby={describedBy}
        aria-invalid={invalid || undefined}
        rows={10}
        spellCheck={false}
        className="font-mono text-xs"
        value={body.html}
        onChange={(event) => onChange({ ...body, html: event.target.value })}
      />

      <span id={textLabelId} className="font-mono text-[10.5px] tracking-[0.12em] text-faint uppercase">
        Plain-text version
      </span>
      <Textarea
        aria-labelledby={`${labelledBy} ${textLabelId}`}
        aria-describedby={textHintId}
        rows={5}
        value={body.text}
        onChange={(event) => onChange({ ...body, text: event.target.value })}
      />
      <span id={textHintId} className="text-xs text-muted-foreground">
        Sent to mail clients that don’t show HTML — edit it alongside the HTML.
      </span>
    </div>
  )
}
