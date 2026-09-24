import { InputRule, Node } from '@tiptap/core'
import { ReactNodeViewRenderer } from '@tiptap/react'
import { tokenText } from './merge-tags'
import { VariableChip } from './variable-chip'

/**
 * A `{{name}}` merge field as an inline atom: the caret steps over it and a
 * single Backspace removes it, so a token can't be half-edited into a typo.
 *
 * Rendered to HTML as `<span data-variable="name">`, the same element it parses
 * from, so copy and paste inside the editor keep chips. That span never
 * reaches the email: `unwrapTokensInHtml` turns it back into the bare
 * `{{name}}` the send path substitutes.
 */
export const VariableNode = Node.create({
  name: 'variable',
  group: 'inline',
  inline: true,
  atom: true,
  selectable: true,

  addAttributes() {
    return {
      name: {
        default: '',
        parseHTML: (element) => element.getAttribute('data-variable') ?? '',
        renderHTML: () => ({}),
      },
    }
  },

  parseHTML() {
    return [{ tag: 'span[data-variable]' }]
  },

  renderHTML({ node }) {
    return ['span', { 'data-variable': String(node.attrs.name ?? '') }]
  },

  renderText({ node }) {
    return tokenText(String(node.attrs.name ?? ''))
  },

  addNodeView() {
    return ReactNodeViewRenderer(VariableChip, { as: 'span' })
  },

  addInputRules() {
    const type = this.type
    return [
      // Typing the closing `}}` of a token by hand turns it into a chip, known
      // or not — an unknown one then shows up flagged instead of hiding in text.
      new InputRule({
        find: /\{\{([^{}]*)\}\}$/,
        // Backspace after the chip appears must delete it, as it does any other
        // chip — not "undo the rule" back into raw `{{text}}` that is no longer
        // flagged in place.
        undoable: false,
        handler: ({ state, range, match }) => {
          state.tr.replaceWith(range.from, range.to, type.create({ name: match[1] ?? '' }))
        },
      }),
    ]
  },
})
