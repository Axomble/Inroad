import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { z } from 'zod'
import { Loader2, NotebookPen } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useCrmCreateNoteMutation, useCrmListNotesQuery, type CrmTargetType } from './api'
import { recordErrorMessage } from './error-copy'
import { formatDateTime } from '@/lib/datetime'
import { listPageSize } from './query-args'
import { parseActor } from './actor'
import { ActorBadge } from './actor-badge'
import { InlineLoading, MoreExist, MutedEmpty, QueryErrorBanner, Section } from '@/components/shared/record-page'

const noteSchema = z.object({
  title: z.string().trim().max(200),
  body: z.string().trim().min(1, 'Add a note before saving.').max(20_000),
})
type NoteValues = z.infer<typeof noteSchema>

/**
 * Notes on any CRM record. Notes are polymorphic in the API (`target_type` /
 * `target_id`), so contacts, companies and deals share this one panel instead of
 * each page growing its own composer that drifts from the others.
 */
export function NotesPanel({ targetType, targetId }: { targetType: CrmTargetType; targetId: string }) {
  const notesQuery = useCrmListNotesQuery({ targetType, targetId, limit: listPageSize })
  const notes = notesQuery.data?.items ?? []

  return (
    <Section title="Notes" description="Shared context, visible to everyone and to agents working this record.">
      <NoteComposer targetType={targetType} targetId={targetId} />
      <div className="mt-4 space-y-2">
        {notesQuery.isLoading ? <InlineLoading label="Loading notes" /> : null}
        {notesQuery.isError ? (
          <QueryErrorBanner
            className=""
            message={recordErrorMessage(notesQuery.error, 'Notes could not be loaded.')}
            onRetry={() => void notesQuery.refetch()}
            retrying={notesQuery.isFetching}
          />
        ) : null}
        {notes.map((note) => (
          <article key={note.id} className="rounded-lg border border-border bg-background p-3">
            {note.title ? <h3 className="text-sm font-semibold">{note.title}</h3> : null}
            <p className="mt-1 whitespace-pre-wrap text-sm text-muted-foreground">{note.body}</p>
            <p className="mt-2 flex flex-wrap items-center gap-2 text-xs text-faint">
              <ActorBadge actor={parseActor(note.created_by_actor)} />
              <time dateTime={note.created_at}>{formatDateTime(note.created_at)}</time>
            </p>
          </article>
        ))}
        {/* An empty list and a failed load are different facts and read
            differently. */}
        {!notesQuery.isLoading && !notesQuery.isError && notes.length === 0 ? (
          <MutedEmpty text="No notes yet." />
        ) : null}
        {notesQuery.data?.next_cursor != null ? <MoreExist noun="notes" /> : null}
      </div>
    </Section>
  )
}

function NoteComposer({ targetType, targetId }: { targetType: CrmTargetType; targetId: string }) {
  const [createNote, state] = useCrmCreateNoteMutation()
  const form = useForm<NoteValues>({ resolver: zodResolver(noteSchema), defaultValues: { title: '', body: '' } })
  const submit = form.handleSubmit(async ({ title, body }) => {
    try {
      await createNote({ crmNoteInput: { title, body, target_type: targetType, target_id: targetId } }).unwrap()
      form.reset()
    } catch (error) {
      form.setError('root', { message: recordErrorMessage(error, 'The note could not be saved. Try again.') })
    }
  })
  const titleId = `note-title-${targetId}`
  const bodyId = `note-body-${targetId}`

  return (
    <form onSubmit={(event) => void submit(event)} className="grid gap-2">
      <Label htmlFor={titleId}>Title <span className="text-muted-foreground">(optional)</span></Label>
      <Input id={titleId} {...form.register('title')} />
      <Label htmlFor={bodyId}>Note</Label>
      <textarea
        id={bodyId}
        rows={3}
        className="w-full resize-y rounded-md border border-input bg-surface-2 p-2.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
        aria-invalid={Boolean(form.formState.errors.body)}
        aria-describedby={form.formState.errors.body ? `${bodyId}-error` : undefined}
        {...form.register('body')}
      />
      {form.formState.errors.body ? (
        <p id={`${bodyId}-error`} className="text-xs text-danger">{form.formState.errors.body.message}</p>
      ) : null}
      {form.formState.errors.root ? <p role="alert" className="text-xs text-danger">{form.formState.errors.root.message}</p> : null}
      <Button type="submit" size="sm" className="justify-self-start" disabled={state.isLoading}>
        {state.isLoading ? <Loader2 className="animate-spin" aria-hidden="true" /> : <NotebookPen aria-hidden="true" />}
        Save note
      </Button>
    </form>
  )
}
