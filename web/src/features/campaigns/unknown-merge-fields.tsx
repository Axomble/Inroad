import { AlertTriangle } from 'lucide-react'
import { tokenText } from '@/components/shared/rich-text/merge-tags'

/**
 * The visible reason a save is blocked: placeholders nothing will fill in. The
 * send path mails an unmatched `{{token}}` verbatim (or blank, for an undefined
 * custom field) and the launch check only catches it at launch, so the step
 * editors refuse to save it at all. Renders nothing while there's nothing to fix.
 */
export function UnknownMergeFieldsNotice({ id, names }: { id: string; names: string[] }) {
  if (names.length === 0) return null
  return (
    <p
      id={id}
      role="alert"
      className="flex items-start gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger"
    >
      <AlertTriangle className="mt-px size-3.5 shrink-0" aria-hidden="true" />
      <span>
        {names.length === 1 ? 'This merge field won’t be filled in' : 'These merge fields won’t be filled in'}, so the
        email would go out with it as typed or blank:{' '}
        <span className="font-mono">{names.map(tokenText).join(', ')}</span>. Fix or remove{' '}
        {names.length === 1 ? 'it' : 'them'} to save.
      </span>
    </p>
  )
}
