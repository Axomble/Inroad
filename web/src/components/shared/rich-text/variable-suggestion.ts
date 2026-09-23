import { Extension, type Editor, type Range } from '@tiptap/core'
import { PluginKey } from '@tiptap/pm/state'
import { Suggestion, type SuggestionProps } from '@tiptap/suggestion'
import type { MergeVariable } from './merge-tags'

const MAX_ITEMS = 8

/** What the menu renders. `null` means closed. */
export type SuggestionMenuState = {
  items: MergeVariable[]
  activeIndex: number
  /** Relative to the editor's own wrapper, so the menu works inside dialogs and panels alike. */
  position: { top: number; left: number }
  select: (item: MergeVariable) => void
}

export function filterVariables(variables: MergeVariable[], query: string): MergeVariable[] {
  const needle = query.trim().toLowerCase()
  const matches = needle
    ? variables.filter((v) => v.name.toLowerCase().includes(needle) || v.label.toLowerCase().includes(needle))
    : variables
  return matches.slice(0, MAX_ITEMS)
}

export function insertVariable(editor: Editor, name: string, range?: Range) {
  const node = { type: 'variable', attrs: { name } }
  const chain = editor.chain().focus()
  void (range ? chain.insertContentAt(range, node) : chain.insertContent(node)).run()
}

/** Marks the element the menu is positioned inside (the editor's own wrapper). */
export const MENU_ANCHOR_ATTRIBUTE = 'data-variable-menu-anchor'

declare module '@tiptap/core' {
  interface Storage {
    variableSuggestion: {
      /** Read at keystroke time; replaced via `setMergeVariables`. */
      variables: MergeVariable[]
    }
  }
  interface Commands<ReturnType> {
    variableSuggestion: {
      /** Replaces the list the `{{` menu offers. Touches no document state. */
      setMergeVariables: (variables: MergeVariable[]) => ReturnType
    }
  }
}

type Options = {
  onMenuChange: (menu: SuggestionMenuState | null) => void
  /** DOM id of the menu's listbox, for aria-controls / aria-activedescendant. */
  menuId: string
}

export function optionId(menuId: string, index: number): string {
  return `${menuId}-option-${index}`
}

/**
 * Typing `{{` opens a filtered list of merge fields; Enter / Tab / click
 * inserts the chosen one as a chip in place of what was typed. The keyboard
 * cursor lives in this closure (the editor keeps focus the whole time), and
 * every change is pushed to React as a snapshot for the menu to render.
 */
export function createVariableSuggestion({ onMenuChange, menuId }: Options) {
  return Extension.create({
    name: 'variableSuggestion',

    addStorage() {
      return { variables: [] as MergeVariable[] }
    },

    addCommands() {
      return {
        setMergeVariables: (variables) => () => {
          this.storage.variables = variables
          return true
        },
      }
    },
    // Ahead of the base keymap, so Enter picks an item instead of splitting
    // the paragraph (or submitting the subject's form).
    priority: 200,

    addProseMirrorPlugins() {
      return [
        Suggestion<MergeVariable, MergeVariable>({
          editor: this.editor,
          pluginKey: new PluginKey(`variableSuggestion-${menuId}`),
          char: '{{',
          // Tokens often abut text ("Hi{{first_name}}"), so no prefix is required.
          allowedPrefixes: null,
          // Read from storage at keystroke time, so a custom-field list that
          // loads after the editor was created is still offered.
          items: ({ query, editor }) => filterVariables(editor.storage.variableSuggestion.variables, query),
          command: ({ editor, range, props }) => insertVariable(editor, props.name, range),
          render: () => {
            let current: SuggestionProps<MergeVariable, MergeVariable> | null = null
            let activeIndex = 0

            const publish = () => {
              const dom = current?.editor.view.dom
              if (!current || current.items.length === 0) {
                dom?.removeAttribute('aria-activedescendant')
                onMenuChange(null)
                return
              }
              const anchor = dom?.closest(`[${MENU_ANCHOR_ATTRIBUTE}]`)?.getBoundingClientRect()
              const caret = current.clientRect?.()
              const props = current
              dom?.setAttribute('aria-controls', menuId)
              dom?.setAttribute('aria-activedescendant', optionId(menuId, activeIndex))
              onMenuChange({
                items: props.items,
                activeIndex,
                position: {
                  top: caret && anchor ? caret.bottom - anchor.top + 4 : 0,
                  left: caret && anchor ? Math.max(0, caret.left - anchor.left) : 0,
                },
                select: (item) => props.command(item),
              })
            }

            return {
              onStart: (props) => {
                current = props
                activeIndex = 0
                publish()
              },
              onUpdate: (props) => {
                current = props
                activeIndex = Math.min(activeIndex, Math.max(0, props.items.length - 1))
                publish()
              },
              onKeyDown: ({ event }) => {
                const items = current?.items ?? []
                if (items.length === 0) return false
                if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
                  const step = event.key === 'ArrowDown' ? 1 : -1
                  activeIndex = (activeIndex + step + items.length) % items.length
                  publish()
                  return true
                }
                if (event.key === 'Enter' || event.key === 'Tab') {
                  const item = items[activeIndex]
                  if (item) current?.command(item)
                  return true
                }
                return false
              },
              onExit: () => {
                const dom = current?.editor.view.dom
                dom?.removeAttribute('aria-activedescendant')
                dom?.removeAttribute('aria-controls')
                current = null
                onMenuChange(null)
              },
            }
          },
        }),
      ]
    },
  })
}
