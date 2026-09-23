import { useEffect, useEffectEvent, useId, useState } from 'react'
import { useEditor, type Editor } from '@tiptap/react'
import type { AnyExtension } from '@tiptap/core'
import type { EditorProps } from '@tiptap/pm/view'
import { Placeholder } from '@tiptap/extensions'
import type { MergeVariable } from './merge-tags'
import { VariableNode } from './variable-node'
import { MENU_ANCHOR_ATTRIBUTE, createVariableSuggestion, type SuggestionMenuState } from './variable-suggestion'

type Options = {
  /** The editor's own extensions; the variable node and `{{` menu are added here. */
  extensions: AnyExtension[]
  content: Parameters<typeof useEditor>[0]['content']
  variables: MergeVariable[]
  onUpdate: (editor: Editor) => void
  /** Call `onUpdate` once as soon as the editor exists, not only on the first edit. */
  reportOnMount?: boolean
  placeholder?: string
  attributes: Record<string, string>
  editorProps?: Omit<EditorProps, 'attributes'>
}

/**
 * What the body and subject editors share: the variable node, the `{{` menu
 * and its state, and a change listener that always calls the latest callback.
 * The extensions are built once — the editor is created once — so what can
 * change between renders reaches them through effects: the variable list into
 * extension storage, the change callback through an effect event.
 */
export function useVariableEditor({
  extensions,
  content,
  variables,
  onUpdate,
  reportOnMount = false,
  placeholder,
  attributes,
  editorProps,
}: Options) {
  const menuId = useId()
  const [menu, setMenu] = useState<SuggestionMenuState | null>(null)
  const [allExtensions] = useState(() => [
    ...extensions,
    VariableNode,
    Placeholder.configure({ placeholder: placeholder ?? '' }),
    createVariableSuggestion({ onMenuChange: setMenu, menuId }),
  ])

  // Nullable on purpose. TipTap destroys and recreates its instance across a
  // remount (StrictMode does one in development), and between the two the hook
  // hands back null even when typed otherwise; `immediatelyRender: false` makes
  // the types say so, and every use below checks.
  const editor = useEditor({
    immediatelyRender: false,
    extensions: allExtensions,
    content,
    editorProps: {
      ...editorProps,
      attributes: { role: 'textbox', ...attributes },
    },
  })

  useEffect(() => {
    editor?.commands.setMergeVariables(variables)
  }, [editor, variables])

  const emitUpdate = useEffectEvent((current: Editor) => onUpdate(current))
  useEffect(() => {
    if (!editor) return
    const listener = () => emitUpdate(editor)
    editor.on('update', listener)
    if (reportOnMount) listener()
    return () => {
      editor.off('update', listener)
    }
  }, [editor, reportOnMount])

  return { editor, menu, menuId, anchorProps: { [MENU_ANCHOR_ATTRIBUTE]: '' } }
}
