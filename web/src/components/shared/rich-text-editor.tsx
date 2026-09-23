import { Suspense, lazy } from 'react'
import type { BodyEditorProps } from './rich-text/body-editor'
import type { SubjectEditorProps } from './rich-text/subject-editor'

// TipTap + ProseMirror are the heaviest thing a step form carries and only the
// email editors need them, so both editors live in their own chunk, fetched the
// first time one mounts. Type-only imports above keep this module eager-safe.
const LazyBodyEditor = lazy(() => import('./rich-text/body-editor').then((m) => ({ default: m.BodyEditor })))
const LazySubjectEditor = lazy(() => import('./rich-text/subject-editor').then((m) => ({ default: m.SubjectEditor })))

export type { EmailBody } from './rich-text/body-editor'
export type { ClassifyVariable, MergeVariable, VariableStatus } from './rich-text/merge-tags'

/** Holds the field's footprint while the editor chunk loads, so the form doesn't jump. */
function FieldPlaceholder({ className }: { className: string }) {
  return <div aria-hidden="true" className={`animate-pulse rounded-md border border-input bg-surface-2 ${className}`} />
}

/** Rich email body: toolbar, merge-field chips, `{{` menu. See `rich-text/body-editor.tsx`. */
export function RichTextEditor(props: BodyEditorProps) {
  return (
    <Suspense fallback={<FieldPlaceholder className="h-40" />}>
      <LazyBodyEditor {...props} />
    </Suspense>
  )
}

/** One-line subject with merge-field chips. See `rich-text/subject-editor.tsx`. */
export function SubjectEditor(props: SubjectEditorProps) {
  return (
    <Suspense fallback={<FieldPlaceholder className="h-9" />}>
      <LazySubjectEditor {...props} />
    </Suspense>
  )
}
