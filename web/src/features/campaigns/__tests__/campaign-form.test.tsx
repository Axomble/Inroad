import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { replaceText, warmRichTextEditors } from '@/test/rich-text'
import { CampaignForm } from '../campaign-form'

// The new-campaign form's copy becomes the campaign's first step, so it goes
// through the same editors and the same merge-field rule as the step form.

const MAILBOX_ID = '11111111-1111-4111-8111-111111111111'
const LIST_ID = '22222222-2222-4222-8222-222222222222'
const jsonHeaders = { 'content-type': 'application/json' }
const json = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status, headers: jsonHeaders })

let posts: unknown[]

beforeAll(async () => {
  await warmRichTextEditors()
}, 30_000)

beforeEach(() => {
  posts = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : String(input)
      const method = (isRequest ? input.method : (init?.method ?? 'GET')).toUpperCase()
      if (method === 'POST' && url.endsWith('/campaigns')) {
        posts.push(isRequest ? await input.clone().json() : undefined)
        return json({ id: 'c-1' }, 201)
      }
      if (url.endsWith('/mailboxes')) return json([{ id: MAILBOX_ID, email: 'me@inroad.dev', status: 'active' }])
      if (url.endsWith('/lists')) return json([{ id: LIST_ID, name: 'Founders' }])
      if (url.endsWith('/custom-fields')) return json([])
      return json({})
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function fillRequired() {
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Q3' } })
  fireEvent.change(await screen.findByLabelText('Send from'), { target: { value: MAILBOX_ID } })
  await screen.findByRole('option', { name: 'Founders' })
  fireEvent.change(screen.getByLabelText('To list'), { target: { value: LIST_ID } })
}

test('creates the campaign with the subject and body the editors produced', async () => {
  const onDone = vi.fn()
  renderWithProviders(<CampaignForm onDone={onDone} onCancel={vi.fn()} />)
  await screen.findByRole('option', { name: 'me@inroad.dev' })
  await fillRequired()
  replaceText(await screen.findByRole('textbox', { name: 'Subject' }), 'Hi {{first_name}}')
  replaceText(await screen.findByRole('textbox', { name: 'Body' }), 'About {{company}}')
  fireEvent.click(screen.getByRole('button', { name: 'Create campaign' }))

  await waitFor(() => expect(posts).toHaveLength(1))
  expect(posts[0]).toMatchObject({
    name: 'Q3',
    mailbox_id: MAILBOX_ID,
    list_id: LIST_ID,
    subject: 'Hi {{first_name}}',
    body_text: 'About {{company}}',
    body_html: '',
  })
  await waitFor(() => expect(onDone).toHaveBeenCalled())
})

test('an unknown merge field in the subject blocks creating the campaign', async () => {
  renderWithProviders(<CampaignForm onDone={vi.fn()} onCancel={vi.fn()} />)
  await screen.findByRole('option', { name: 'me@inroad.dev' })
  await fillRequired()
  replaceText(await screen.findByRole('textbox', { name: 'Subject' }), 'Hi {{name}}')

  expect(await screen.findByRole('alert')).toHaveTextContent('{{name}}')
  fireEvent.click(screen.getByRole('button', { name: 'Create campaign' }))
  await new Promise((resolve) => setTimeout(resolve, 50))
  expect(posts).toHaveLength(0)
})
