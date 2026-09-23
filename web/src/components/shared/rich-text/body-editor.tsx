import { useState } from 'react'
import { EditorContent } from '@tiptap/react'
import { Slice } from '@tiptap/pm/model'
import { StarterKit } from '@tiptap/starter-kit'
import { cn } from '@/lib/utils'
import { EditorToolbar } from './editor-toolbar'
import { unkeptFormatting } from './html-compat'
import {
  docToText,
  isRichDoc,
  textToDoc,
  unwrapTokensInHtml,
  wrapTokensInHtml,
  type ClassifyVariable,
  type MergeVariable,
} from './merge-tags'
import { RawHtmlBody } from './raw-html-body'
import { useVariableEditor } from './use-variable-editor'
import { VariableContext } from './variable-context'
import { VariableMenu } from './variable-menu'

/** The two stored forms of an email body. `html` is empty while nothing is formatted. */
export type EmailBody = { html: string; text: string }

export type BodyEditorProps = {
  /** id of the visible label; a contenteditable can't be the target of `htmlFor`. */
  labelledBy: string
  describedBy?: string
  /** Stored body_html. Wins over `initialText` when non-empty — it is the richer form. */
  initialHtml?: string
  initialText?: string
  placeholder?: string
  invalid?: boolean
  variables: MergeVariable[]
  classifyVariable: ClassifyVariable
  onChange: (body: EmailBody) => void
}

/**
 * Only what the toolbar offers. Headings, code, quotes, rules, strike and
 * underline are switched off so no shortcut or markdown rule can produce
 * formatting the toolbar can't show or undo. Autolink stays off too: a URL
 * typed into a plain email must not silently turn it into an HTML one.
 */
const bodyKit = StarterKit.configure({
  heading: false,
  blockquote: false,
  code: false,
  codeBlock: false,
  horizontalRule: false,
  strike: false,
  underline: false,
  // It appends an empty paragraph after a trailing list, which would reach the
  // text part as a stray blank line.
  trailingNode: false,
  link: {
    openOnClick: false,
    autolink: false,
    linkOnPaste: false,
    defaultProtocol: 'https',
    // No injected target/rel: a stored link must save back exactly as it was.
    HTMLAttributes: { target: null, rel: null },
  },
})

/**
 * The email body. Normally the rich editor; but stored HTML carrying anything
 * the editor's schema would drop (tables, headings, inline styles — HTML from
 * an API client, not from this editor) opens as its raw parts instead, and
 * only moves to the editor on an explicit, confirmed conversion.
 *
 * Neither mode reports a change until someone edits, so an untouched body is
 * saved back byte for byte from the caller's own copy of the stored values.
 */
export function BodyEditor(props: BodyEditorProps) {
  const { initialHtml = '', initialText = '', onChange } = props
  // Non-null while the stored HTML is shown raw; holds the latest of both parts.
  const [raw, setRaw] = useState<EmailBody | null>(() =>
    unkeptFormatting(initialHtml).length > 0 ? { html: initialHtml, text: initialText } : null,
  )
  const [converted, setConverted] = useState(false)

  if (raw && !converted) {
    return (
      <RawHtmlBody
        labelledBy={props.labelledBy}
        describedBy={props.describedBy}
        body={raw}
        lost={unkeptFormatting(raw.html)}
        invalid={props.invalid}
        onChange={(body) => {
          setRaw(body)
          onChange(body)
        }}
        onConvert={() => setConverted(true)}
      />
    )
  }
  // After a conversion the editor reports its content at once: what the user
  // now sees is what saves, not the HTML they just chose to leave behind.
  return <RichBody {...props} initialHtml={raw?.html ?? initialHtml} reportOnMount={converted} />
}

/**
 * The rich email body editor: a formatting toolbar, merge fields as chips, and
 * a `{{` menu. Uncontrolled — it reads its initial content once and reports
 * every change — so a parent form re-rendering never resets the caret.
 */
function RichBody({
  labelledBy,
  describedBy,
  initialHtml,
  initialText,
  placeholder,
  invalid,
  variables,
  classifyVariable,
  onChange,
  reportOnMount,
}: BodyEditorProps & { reportOnMount: boolean }) {
  const [content] = useState(() => (initialHtml ? wrapTokensInHtml(initialHtml) : textToDoc(initialText ?? '')))
  const { editor, anchorProps, menu, menuId } = useVariableEditor({
    extensions: [bodyKit],
    content,
    variables,
    placeholder,
    reportOnMount,
    onUpdate: (e) => {
      const doc = e.getJSON()
      onChange({ text: docToText(doc), html: isRichDoc(doc) ? unwrapTokensInHtml(e.getHTML()) : '' })
    },
    attributes: {
      'aria-multiline': 'true',
      'aria-labelledby': labelledBy,
      ...(describedBy ? { 'aria-describedby': describedBy } : {}),
      ...(invalid ? { 'aria-invalid': 'true' } : {}),
      class: 'rich-text-content min-h-28 px-3 py-2 text-sm leading-relaxed text-foreground outline-none [&_p]:min-h-[1lh]',
    },
    editorProps: {
      // Pasted text follows the same blank-line / newline rules as stored
      // text, and its `{{tokens}}` arrive as chips.
      clipboardTextParser: (text, _context, _plain, view) =>
        new Slice(view.state.schema.nodeFromJSON(textToDoc(text.replace(/\r\n?/g, '\n'))).content, 1, 1),
      transformPastedHTML: wrapTokensInHtml,
    },
  })

  return (
    <VariableContext value={{ variables, classify: classifyVariable }}>
      <div
        {...anchorProps}
        className={cn(
          'relative rounded-md border border-input bg-surface-2 shadow-[inset_0_1px_2px_var(--input-inset)] transition-colors',
          'focus-within:border-primary focus-within:ring-2 focus-within:ring-ring/40',
          invalid && 'border-danger ring-2 ring-danger/30',
        )}
      >
        {editor && <EditorToolbar editor={editor} variables={variables} />}
        <EditorContent editor={editor} />
        {menu && <VariableMenu id={menuId} menu={menu} />}
      </div>
    </VariableContext>
  )
}
