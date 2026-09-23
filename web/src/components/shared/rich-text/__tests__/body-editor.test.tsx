import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { expect, test, vi, type Mock } from 'vitest'
import { caretToEnd, editorFor, pressKey, replaceText, typeInto } from '@/test/rich-text'
import { BodyEditor, type EmailBody } from '../body-editor'
import type { ClassifyVariable, MergeVariable } from '../merge-tags'

const VARIABLES: MergeVariable[] = [
  { name: 'first_name', label: 'First name' },
  { name: 'company', label: 'Company' },
]
const classify: ClassifyVariable = (name) =>
  VARIABLES.some((v) => v.name === name) ? 'known' : name.startsWith('custom.') ? 'unverified' : 'unknown'

function renderEditor(props: { initialHtml?: string; initialText?: string }) {
  const onChange = vi.fn<(body: EmailBody) => void>()
  render(
    <>
      <span id="body-label">Body</span>
      <BodyEditor
        labelledBy="body-label"
        initialHtml={props.initialHtml}
        initialText={props.initialText}
        variables={VARIABLES}
        classifyVariable={classify}
        onChange={onChange}
      />
    </>,
  )
  return { textbox: screen.getByRole('textbox', { name: 'Body' }), onChange }
}

function lastChange(onChange: Mock<(body: EmailBody) => void>): EmailBody {
  const call = onChange.mock.calls.at(-1)
  if (!call) throw new Error('onChange was never called')
  return call[0]
}

// React node views mount a tick after the editor, and the `{{` menu's items
// are fetched asynchronously by the suggestion plugin — hence findBy*.

test('stored body_text loads with its merge fields as chips, flagging the unknown one', async () => {
  const { textbox } = renderEditor({ initialText: 'Hi {{first_name}}, {{firstname}} at {{custom.tier}}' })

  const known = await within(textbox).findByLabelText(/^\{\{first_name\}\} — Merge field: First name$/)
  expect(known).toHaveAttribute('data-variable-status', 'known')

  const unknown = within(textbox).getByLabelText(/^\{\{firstname\}\} — Unknown merge field/)
  expect(unknown).toHaveAttribute('data-variable-status', 'unknown')
  // Not colour alone: a visible marker rides along.
  expect(unknown).toHaveTextContent('{{firstname}}?')

  expect(within(textbox).getByLabelText(/^\{\{custom\.tier\}\} — Merge field \(not checked yet\)$/)).toBeInTheDocument()
})

test('stored body_html loads as chips and saves back byte-identical', async () => {
  const html = '<p>Hi <strong>{{first_name}}</strong>, see <a href="https://x.dev/?e={{email}}">this</a></p><ul><li><p>{{company}}</p></li></ul>'
  const { textbox, onChange } = renderEditor({ initialHtml: html })
  expect(await within(textbox).findAllByLabelText(/Merge field/)).toHaveLength(2)

  // Any edit reports the current state; undoing it must land back on the original.
  caretToEnd(textbox)
  typeInto(textbox, 'x')
  act(() => {
    editorFor(textbox).commands.undo()
  })
  const saved = lastChange(onChange)
  expect(saved.html).toBe(html)
  expect(saved.text).toBe('Hi {{first_name}}, see this (https://x.dev/?e={{email}})\n\n- {{company}}')
})

test('a plain body stays plain: no body_html until something is formatted', () => {
  const { textbox, onChange } = renderEditor({ initialText: 'Hi {{first_name}},\n\nthanks' })
  caretToEnd(textbox)
  typeInto(textbox, '!')
  expect(lastChange(onChange)).toEqual({ text: 'Hi {{first_name}},\n\nthanks!', html: '' })

  act(() => {
    editorFor(textbox).chain().selectAll().toggleBold().run()
  })
  const rich = lastChange(onChange)
  expect(rich.html).toBe('<p><strong>Hi {{first_name}},</strong></p><p><strong>thanks!</strong></p>')
  expect(rich.text).toBe('Hi {{first_name}},\n\nthanks!')
})

test('typing {{ opens the merge-field menu and Enter inserts the highlighted one as a chip', async () => {
  const { textbox, onChange } = renderEditor({ initialText: 'Hi ' })
  caretToEnd(textbox)
  typeInto(textbox, '{{comp')

  const menu = await screen.findByRole('listbox', { name: 'Merge fields' })
  expect(within(menu).getAllByRole('option').map((o) => o.textContent)).toEqual(['Company{{company}}'])
  expect(textbox).toHaveAttribute('aria-activedescendant', within(menu).getByRole('option').id)

  pressKey(textbox, 'Enter')
  expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
  expect(await within(textbox).findByLabelText(/^\{\{company\}\}/)).toBeInTheDocument()
  expect(lastChange(onChange).text).toBe('Hi {{company}}')
})

test('arrow keys move through the menu and a click picks an option', async () => {
  const { textbox, onChange } = renderEditor({ initialText: '' })
  typeInto(textbox, '{{')
  const menu = await screen.findByRole('listbox', { name: 'Merge fields' })
  expect(within(menu).getAllByRole('option')[0]).toHaveAttribute('aria-selected', 'true')

  pressKey(textbox, 'ArrowDown')
  expect(within(menu).getAllByRole('option')[1]).toHaveAttribute('aria-selected', 'true')

  fireEvent.click(within(menu).getByRole('option', { name: /First name/ }))
  expect(lastChange(onChange).text).toBe('{{first_name}}')
})

test('a token typed out by hand becomes a chip, and an unknown one is flagged', async () => {
  const { textbox, onChange } = renderEditor({ initialText: '' })
  typeInto(textbox, 'Hey {{firstname}}')
  pressKey(textbox, 'Escape')

  expect(await within(textbox).findByLabelText(/^\{\{firstname\}\} — Unknown merge field/)).toBeInTheDocument()
  expect(lastChange(onChange).text).toBe('Hey {{firstname}}')
  // That Backspace then deletes the chip whole (rather than undoing the rule
  // back into raw text) is native browser deletion — covered by Playwright.
})

test('the Insert variable button lists the variables and inserts the chosen chip', async () => {
  const { textbox, onChange } = renderEditor({ initialText: 'Hello ' })
  caretToEnd(textbox)

  const trigger = screen.getByRole('button', { name: 'Insert variable' })
  fireEvent.keyDown(trigger, { key: 'Enter' })
  fireEvent.click(await screen.findByRole('menuitem', { name: /Company/ }))

  expect(lastChange(onChange).text).toBe('Hello {{company}}')
})

test('toolbar controls are labelled and report their pressed state', () => {
  const { textbox } = renderEditor({ initialText: 'word' })
  const bold = screen.getByRole('button', { name: 'Bold' })
  expect(bold).toHaveAttribute('aria-pressed', 'false')

  act(() => {
    editorFor(textbox).chain().selectAll().run()
  })
  fireEvent.click(bold)
  expect(bold).toHaveAttribute('aria-pressed', 'true')
  for (const name of ['Italic', 'Link', 'Bulleted list', 'Numbered list', 'Undo', 'Redo']) {
    expect(screen.getByRole('button', { name })).toBeInTheDocument()
  }
})

test('the link field applies a scheme-less URL as https', () => {
  const { textbox, onChange } = renderEditor({ initialText: 'site' })
  act(() => {
    editorFor(textbox).chain().selectAll().run()
  })
  fireEvent.click(screen.getByRole('button', { name: 'Link' }))
  fireEvent.change(screen.getByLabelText('Link URL'), { target: { value: 'inroad.dev' } })
  fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

  expect(lastChange(onChange).html).toContain('href="https://inroad.dev"')
  expect(lastChange(onChange).text).toBe('site (https://inroad.dev)')
})

// --- HTML the editor can't hold -------------------------------------------

/** A body an API client might write: a heading, a table and inline styles. */
const AGENT_HTML =
  '<h2 style="margin:0">Quick idea for {{company}}</h2><p>Hi {{first_name}}, how teams compare:</p><table><tr><th>Plan</th></tr><tr><td>Starter</td></tr></table>'

test.each([
  ['links with the attributes Link keeps', '<p><a href="https://x.dev" target="_blank" rel="noopener" class="cta" title="Site">x</a> {{first_name}}</p>'],
  ['ordered lists with start and type', '<ol start="3" type="a"><li><p><em>{{company}}</em></p></li></ol>'],
])('lossless HTML (%s) opens in the editor and an edit keeps every element and attribute', async (_name, html) => {
  const { textbox, onChange } = renderEditor({ initialHtml: html })
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
  expect(await within(textbox).findAllByLabelText(/Merge field/)).toHaveLength(1)

  caretToEnd(textbox)
  typeInto(textbox, 'x')
  act(() => {
    editorFor(textbox).commands.undo()
  })
  // Same DOM, attribute order aside (the editor writes attributes in schema
  // order). Byte-identical is the untouched-save guarantee, tested in the forms.
  expect(parseFragment(lastChange(onChange).html).isEqualNode(parseFragment(html))).toBe(true)
})

function parseFragment(html: string): DocumentFragment {
  const template = document.createElement('template')
  template.innerHTML = html
  return template.content
}

function renderRaw(initialHtml = AGENT_HTML) {
  const onChange = vi.fn<(body: EmailBody) => void>()
  render(
    <>
      <span id="body-label">Body</span>
      <BodyEditor
        labelledBy="body-label"
        initialHtml={initialHtml}
        initialText="Hi {{first_name}}, how teams compare: Starter"
        variables={VARIABLES}
        classifyVariable={classify}
        onChange={onChange}
      />
    </>,
  )
  return { onChange }
}

test('lossy HTML is kept out of the editor: a notice names what would go, and the raw parts are shown untouched', () => {
  const { onChange } = renderRaw()

  expect(screen.getByRole('status')).toHaveTextContent(
    'This email has formatting the editor can’t keep: headings, inline styles and tables.',
  )
  expect(screen.getByRole('textbox', { name: 'Body HTML' })).toHaveValue(AGENT_HTML)
  expect(screen.getByRole('textbox', { name: 'Body Plain-text version' })).toHaveValue(
    'Hi {{first_name}}, how teams compare: Starter',
  )
  // No editor mounted, and nothing reported: the stored copy stands as is.
  expect(document.querySelector('[contenteditable]')).toBeNull()
  expect(onChange).not.toHaveBeenCalled()
})

test('editing the raw HTML reports exactly what was typed, with the text part unchanged', () => {
  const { onChange } = renderRaw()
  fireEvent.change(screen.getByRole('textbox', { name: 'Body HTML' }), { target: { value: '<table><tr><td>x</td></tr></table>' } })
  expect(lastChange(onChange)).toEqual({
    html: '<table><tr><td>x</td></tr></table>',
    text: 'Hi {{first_name}}, how teams compare: Starter',
  })
  expect(screen.getByRole('status')).toHaveTextContent('can’t keep: tables.')
})

test('converting to the editor takes a second, explicit click that names what is lost', async () => {
  const { onChange } = renderRaw()

  fireEvent.click(screen.getByRole('button', { name: 'Convert to the editor…' }))
  expect(screen.getByRole('status')).toHaveTextContent(
    'Switching removes headings, inline styles and tables from this email when you save.',
  )
  fireEvent.click(screen.getByRole('button', { name: 'Keep HTML' }))
  expect(screen.queryByRole('button', { name: 'Convert and lose formatting' })).not.toBeInTheDocument()
  expect(onChange).not.toHaveBeenCalled()

  fireEvent.click(screen.getByRole('button', { name: 'Convert to the editor…' }))
  fireEvent.click(screen.getByRole('button', { name: 'Convert and lose formatting' }))

  const textbox = await screen.findByRole('textbox', { name: 'Body' })
  expect(textbox).toHaveTextContent('Starter')
  // What the editor now shows is what saves.
  await waitFor(() => expect(onChange).toHaveBeenCalled())
  expect(lastChange(onChange).html).not.toContain('<table')
  expect(lastChange(onChange).html).not.toContain('<h2')
  expect(lastChange(onChange).text).toContain('Quick idea for {{company}}')
})

test('once the raw HTML holds nothing the editor would drop, switching needs no confirmation', async () => {
  renderRaw('<div>Hi {{first_name}}</div>')
  fireEvent.change(screen.getByRole('textbox', { name: 'Body HTML' }), { target: { value: '<p>Hi {{first_name}}</p>' } })
  expect(screen.getByRole('status')).toHaveTextContent('can move to the editor without losing anything')

  fireEvent.click(screen.getByRole('button', { name: 'Switch to the editor' }))
  expect(await screen.findByRole('textbox', { name: 'Body' })).toHaveTextContent('Hi {{first_name}}')
})

// --- Plain stays plain, HTML stays HTML ------------------------------------

/** The demo seed's body_html: an HTML part with no formatting in it. */
const SEED_HTML = '<p>Hi {{first_name}},<br>quick one.</p><p>Thanks</p>'

test('a body loaded with an unformatted HTML part keeps it through an edit — tracking rides on it', () => {
  const { textbox, onChange } = renderEditor({ initialHtml: SEED_HTML, initialText: 'Hi {{first_name}},\nquick one.\n\nThanks' })
  caretToEnd(textbox)
  typeInto(textbox, '!')
  expect(lastChange(onChange)).toEqual({
    html: '<p>Hi {{first_name}},<br>quick one.</p><p>Thanks!</p>',
    text: 'Hi {{first_name}},\nquick one.\n\nThanks!',
  })
})

test('emptying a body that had an HTML part leaves no HTML part, so the empty body is still caught as empty', () => {
  const { textbox, onChange } = renderEditor({ initialHtml: SEED_HTML })
  replaceText(textbox, '')
  expect(lastChange(onChange)).toEqual({ html: '', text: '' })
})
