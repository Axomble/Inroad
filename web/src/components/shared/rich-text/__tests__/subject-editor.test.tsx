import { act, render, screen, within } from '@testing-library/react'
import { expect, test, vi } from 'vitest'
import { caretToEnd, editorFor, pressKey, typeInto } from '@/test/rich-text'
import { SubjectEditor } from '../subject-editor'
import type { ClassifyVariable } from '../merge-tags'

const classify: ClassifyVariable = (name) => (name === 'first_name' ? 'known' : 'unknown')

function renderSubject(initialText: string, onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault())) {
  const onChange = vi.fn()
  render(
    <form onSubmit={onSubmit}>
      <span id="subject-label">Subject</span>
      <SubjectEditor
        labelledBy="subject-label"
        initialText={initialText}
        variables={[{ name: 'first_name', label: 'First name' }]}
        classifyVariable={classify}
        onChange={onChange}
      />
    </form>,
  )
  return { textbox: screen.getByRole('textbox', { name: 'Subject' }), onChange, onSubmit }
}

test('a stored subject loads its merge fields as chips and edits report the exact text', async () => {
  const { textbox, onChange } = renderSubject('{{first_name}}, {a|b} {{first_name}}{{x}}')
  expect(await within(textbox).findAllByLabelText(/^\{\{first_name\}\}/)).toHaveLength(2)
  expect(within(textbox).getByLabelText(/^\{\{x\}\} — Unknown merge field/)).toBeInTheDocument()

  caretToEnd(textbox)
  typeInto(textbox, '!')
  expect(onChange).toHaveBeenLastCalledWith('{{first_name}}, {a|b} {{first_name}}{{x}}!')
})

test('Enter submits the surrounding form and never adds a line', () => {
  const { textbox, onChange, onSubmit } = renderSubject('Quick question')
  caretToEnd(textbox)
  pressKey(textbox, 'Enter')

  expect(onSubmit).toHaveBeenCalledTimes(1)
  expect(onChange).not.toHaveBeenCalled()
  expect(editorFor(textbox).state.doc.childCount).toBe(1)
})

test('formatting has nowhere to go: the schema holds only text and chips', () => {
  const { textbox } = renderSubject('Plain')
  const { schema } = editorFor(textbox)
  expect(Object.keys(schema.marks)).toEqual([])
  expect(Object.keys(schema.nodes).sort()).toEqual(['doc', 'paragraph', 'text', 'variable'])
})

test('pasted multi-line text collapses to one line and its tokens become chips', async () => {
  const { textbox, onChange } = renderSubject('')
  const editor = editorFor(textbox)
  const clipboardData = { getData: (type: string) => (type === 'text/plain' ? 'Hi {{first_name}}\n  and more' : '') }
  act(() => {
    const event = Object.assign(new Event('paste', { bubbles: true, cancelable: true }), { clipboardData })
    editor.view.someProp('handlePaste', (handler) => handler(editor.view, event as ClipboardEvent, editor.state.selection.content()))
  })
  expect(onChange).toHaveBeenLastCalledWith('Hi {{first_name}} and more')
  expect(await within(textbox).findByLabelText(/^\{\{first_name\}\}/)).toBeInTheDocument()
})
