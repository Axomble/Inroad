import { useState } from 'react'
import { EditorContent } from '@tiptap/react'
import { Extension } from '@tiptap/core'
import { Slice } from '@tiptap/pm/model'
import { Document } from '@tiptap/extension-document'
import { Paragraph } from '@tiptap/extension-paragraph'
import { Text as TextNode } from '@tiptap/extension-text'
import { UndoRedo } from '@tiptap/extensions'
import { cn } from '@/lib/utils'
import { docToText, inlineFromText, type ClassifyVariable, type MergeVariable } from './merge-tags'
import { useVariableEditor } from './use-variable-editor'
import { VariableContext } from './variable-context'
import { VariableMenu } from './variable-menu'

export type SubjectEditorProps = {
  labelledBy: string
  describedBy?: string
  initialText?: string
  placeholder?: string
  invalid?: boolean
  variables: MergeVariable[]
  classifyVariable: ClassifyVariable
  onChange: (text: string) => void
}

/** Exactly one paragraph of plain text and chips: a subject has no formatting and no line breaks. */
const SingleLineDocument = Document.extend({ content: 'paragraph' })

/**
 * Enter behaves as it did in the plain input it replaces — it submits the
 * surrounding form — and never inserts a line break.
 */
const SubmitOnEnter = Extension.create({
  name: 'submitOnEnter',
  addKeyboardShortcuts() {
    return {
      Enter: ({ editor }) => {
        editor.view.dom.closest('form')?.requestSubmit()
        return true
      },
      'Shift-Enter': () => true,
    }
  },
})

/** A pasted multi-line block collapses to one line, spaces for newlines. */
function singleLine(text: string): string {
  return text.replace(/\s*\r?\n\s*/g, ' ')
}

/**
 * A one-line subject field with merge-field chips and the `{{` menu, skinned
 * like `Input`. Uncontrolled: it reads `initialText` once and reports changes.
 */
export function SubjectEditor({
  labelledBy,
  describedBy,
  initialText,
  placeholder,
  invalid,
  variables,
  classifyVariable,
  onChange,
}: SubjectEditorProps) {
  const [content] = useState(() => ({
    type: 'doc',
    content: [{ type: 'paragraph', content: inlineFromText(singleLine(initialText ?? '')) }],
  }))
  const { editor, anchorProps, menu, menuId } = useVariableEditor({
    extensions: [SingleLineDocument, Paragraph, TextNode, UndoRedo, SubmitOnEnter],
    content,
    variables,
    placeholder,
    onUpdate: (e) => onChange(docToText(e.getJSON())),
    attributes: {
      'aria-multiline': 'false',
      'aria-labelledby': labelledBy,
      ...(describedBy ? { 'aria-describedby': describedBy } : {}),
      ...(invalid ? { 'aria-invalid': 'true' } : {}),
      class: 'rich-text-content px-3 py-1.5 text-sm leading-6 text-foreground outline-none',
    },
    editorProps: {
      // Both plain and HTML pastes go through the text form, so formatting
      // and line breaks never enter a subject.
      handlePaste: (view, event) => {
        const text = event.clipboardData?.getData('text/plain')
        if (text == null) return false
        const paragraph = view.state.schema.nodeFromJSON({ type: 'paragraph', content: inlineFromText(singleLine(text)) })
        view.dispatch(view.state.tr.replaceSelection(new Slice(paragraph.content, 0, 0)))
        return true
      },
    },
  })

  return (
    <VariableContext value={{ variables, classify: classifyVariable }}>
      <div
        {...anchorProps}
        className={cn(
          'relative min-h-9 w-full rounded-md border border-input bg-surface-2 shadow-[inset_0_1px_2px_var(--input-inset)] transition-colors',
          'focus-within:border-primary focus-within:ring-2 focus-within:ring-ring/40',
          invalid && 'border-danger ring-2 ring-danger/30',
        )}
      >
        <EditorContent editor={editor} />
        {menu && <VariableMenu id={menuId} menu={menu} />}
      </div>
    </VariableContext>
  )
}
