import { act } from '@testing-library/react'
import type { Editor, TiptapEditorHTMLElement } from '@tiptap/core'

/**
 * jsdom has no native contenteditable editing, so `fireEvent.change` / typing
 * can't reach ProseMirror. These helpers drive the REAL editor mounted in the
 * DOM instead — through the same entry points a browser uses, so input rules,
 * the `{{` suggestion plugin and the change listeners all run.
 */
export function editorFor(element: HTMLElement): Editor {
  const editor = (element as TiptapEditorHTMLElement).editor
  if (!editor) throw new Error('element is not a TipTap editor root')
  return editor
}

/** Types one character at a time via `handleTextInput`, as the browser's input handling does. */
export function typeInto(element: HTMLElement, text: string) {
  const editor = editorFor(element)
  act(() => {
    for (const char of text) {
      const { view } = editor
      const { from, to } = view.state.selection
      const insert = () => view.state.tr.insertText(char, from, to)
      const handled = view.someProp('handleTextInput', (handler) => handler(view, from, to, char, insert))
      if (!handled) view.dispatch(insert())
    }
  })
}

/**
 * Replaces the whole content: select-all + delete, then type. Typing straight
 * over a select-all would not work — an AllSelection maps to itself, so every
 * character would replace everything before it.
 */
export function replaceText(element: HTMLElement, text: string) {
  act(() => {
    editorFor(element).chain().selectAll().deleteSelection().run()
  })
  if (text !== '') typeInto(element, text)
}

/**
 * The editors are React.lazy chunks; a cold dynamic import can outlast a
 * findBy*'s one-second window. Resolve both before the tests run.
 */
export async function warmRichTextEditors() {
  await Promise.all([
    import('@/components/shared/rich-text/body-editor'),
    import('@/components/shared/rich-text/subject-editor'),
  ])
}

/** Sends a key to the editor's keydown handlers (suggestion menu, keymaps). */
export function pressKey(element: HTMLElement, key: string) {
  const editor = editorFor(element)
  act(() => {
    const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })
    const handled = editor.view.someProp('handleKeyDown', (handler) => handler(editor.view, event))
    if (!handled) editor.view.dom.dispatchEvent(event)
  })
}

/** Puts the caret at the end of the document. */
export function caretToEnd(element: HTMLElement) {
  const editor = editorFor(element)
  act(() => {
    editor.commands.setTextSelection(editor.state.doc.content.size - 1)
  })
}
