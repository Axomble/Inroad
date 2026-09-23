import { useId, useState } from 'react'
import { useEditorState, type Editor } from '@tiptap/react'
import { Bold, Braces, Italic, Link2, List, ListOrdered, Redo2, Undo2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'
import { tokenText, type MergeVariable } from './merge-tags'
import { insertVariable } from './variable-suggestion'

/**
 * A bare scheme-less address ("inroad.dev/x") is almost always meant as https;
 * anything carrying its own scheme is left for the Link extension's allowlist
 * to accept or refuse (it refuses javascript:).
 */
function normalizeHref(raw: string): string {
  const href = raw.trim()
  if (href === '' || /^[a-z][a-z0-9+.-]*:/i.test(href)) return href
  return `https://${href}`
}

function ToolbarButton({
  label,
  active,
  disabled,
  onClick,
  children,
}: {
  label: string
  active?: boolean
  disabled?: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      aria-label={label}
      title={label}
      aria-pressed={active}
      disabled={disabled}
      className={cn('size-7', active && 'bg-accent text-accent-foreground')}
      // Keep the editor's selection: a focused button would collapse it.
      onMouseDown={(event) => event.preventDefault()}
      onClick={onClick}
    >
      {children}
    </Button>
  )
}

export function EditorToolbar({ editor, variables }: { editor: Editor; variables: MergeVariable[] }) {
  const state = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      bold: e.isActive('bold'),
      italic: e.isActive('italic'),
      link: e.isActive('link'),
      bulletList: e.isActive('bulletList'),
      orderedList: e.isActive('orderedList'),
      canUndo: e.can().undo(),
      canRedo: e.can().redo(),
    }),
  })
  // Non-null while the link field is open; holds the URL being typed.
  const [linkDraft, setLinkDraft] = useState<string | null>(null)
  const linkInputId = useId()

  function openLink() {
    const current = editor.getAttributes('link').href
    setLinkDraft(typeof current === 'string' ? current : '')
  }

  function applyLink(event: React.FormEvent | React.KeyboardEvent) {
    event.preventDefault()
    const href = normalizeHref(linkDraft ?? '')
    const chain = editor.chain().focus().extendMarkRange('link')
    void (href === '' ? chain.unsetLink() : chain.setLink({ href })).run()
    setLinkDraft(null)
  }

  return (
    <div className="border-b border-border">
      <div role="toolbar" aria-label="Formatting" className="flex flex-wrap items-center gap-0.5 px-1.5 py-1">
        <ToolbarButton label="Bold" active={state.bold} onClick={() => void editor.chain().focus().toggleBold().run()}>
          <Bold />
        </ToolbarButton>
        <ToolbarButton
          label="Italic"
          active={state.italic}
          onClick={() => void editor.chain().focus().toggleItalic().run()}
        >
          <Italic />
        </ToolbarButton>
        <ToolbarButton label="Link" active={state.link || linkDraft !== null} onClick={openLink}>
          <Link2 />
        </ToolbarButton>
        <span aria-hidden="true" className="mx-1 h-4 w-px bg-border" />
        <ToolbarButton
          label="Bulleted list"
          active={state.bulletList}
          onClick={() => void editor.chain().focus().toggleBulletList().run()}
        >
          <List />
        </ToolbarButton>
        <ToolbarButton
          label="Numbered list"
          active={state.orderedList}
          onClick={() => void editor.chain().focus().toggleOrderedList().run()}
        >
          <ListOrdered />
        </ToolbarButton>
        <span aria-hidden="true" className="mx-1 h-4 w-px bg-border" />
        <ToolbarButton label="Undo" disabled={!state.canUndo} onClick={() => void editor.chain().focus().undo().run()}>
          <Undo2 />
        </ToolbarButton>
        <ToolbarButton label="Redo" disabled={!state.canRedo} onClick={() => void editor.chain().focus().redo().run()}>
          <Redo2 />
        </ToolbarButton>

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button type="button" variant="ghost" size="xs" className="ml-auto h-7 gap-1.5 text-[12px]">
              <Braces className="size-3.5" />
              Insert variable
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            align="end"
            className="max-h-72 w-64 overflow-y-auto"
            // Hand focus back to the editor, where the chip just landed, not
            // to the trigger.
            onCloseAutoFocus={(event) => {
              event.preventDefault()
              editor.commands.focus()
            }}
          >
            {variables.map((variable) => (
              <DropdownMenuItem key={variable.name} onSelect={() => insertVariable(editor, variable.name)}>
                <span className="truncate">{variable.label}</span>
                <span className="ml-auto shrink-0 font-mono text-[11px] text-muted-foreground">
                  {tokenText(variable.name)}
                </span>
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {linkDraft !== null && (
        <div className="flex items-center gap-2 border-t border-border px-2 py-1.5">
          <label htmlFor={linkInputId} className="font-mono text-[10.5px] tracking-[0.12em] text-faint uppercase">
            Link URL
          </label>
          <Input
            id={linkInputId}
            autoFocus
            type="url"
            placeholder="https://"
            className="h-7 flex-1 text-[13px]"
            value={linkDraft}
            onChange={(event) => setLinkDraft(event.target.value)}
            onKeyDown={(event) => {
              // This row sits inside the step's <form>: Enter must apply the
              // link, not submit the step.
              if (event.key === 'Enter') applyLink(event)
              if (event.key === 'Escape') {
                event.preventDefault()
                setLinkDraft(null)
                editor.commands.focus()
              }
            }}
          />
          <Button type="button" variant="outline" size="xs" onClick={applyLink}>
            {linkDraft.trim() === '' ? 'Remove link' : 'Apply'}
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="xs"
            onClick={() => {
              setLinkDraft(null)
              editor.commands.focus()
            }}
          >
            Cancel
          </Button>
        </div>
      )}
    </div>
  )
}
