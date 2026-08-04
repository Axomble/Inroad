import { useState } from 'react'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { CampaignPreflightCheck } from '../api'
import { PreflightDialog } from '../preflight-dialog'

// Radix AlertDialog drives open/close through pointer events jsdom doesn't
// fully implement; polyfill what it touches (same shim campaigns-page.test.tsx
// and lifecycle-menu.test.tsx use).
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

function check(overrides: Partial<CampaignPreflightCheck> = {}): CampaignPreflightCheck {
  return {
    id: 'sequence_steps',
    severity: 'pass',
    title: 'Sequence has steps',
    detail: '3 step(s) configured.',
    remedy: '',
    ...overrides,
  }
}

let preflightResponder: () => Response
let requests: Array<{ method: string; url: string }>

beforeEach(() => {
  requests = []
  preflightResponder = () => jsonResponse({ ready: true, checks: [check()] })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()
      requests.push({ method, url })
      if (url.endsWith('/preflight')) return preflightResponder()
      return jsonResponse({})
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** Controlled harness: the dialog is always caller-controlled, never self-managing `open`. */
function Harness({
  initialOpen = true,
  onConfirm = vi.fn(),
}: {
  initialOpen?: boolean
  onConfirm?: () => void
}) {
  const [open, setOpen] = useState(initialOpen)
  return (
    <PreflightDialog
      open={open}
      onOpenChange={setOpen}
      campaignId="c-1"
      campaignName="Q3 Outbound"
      onConfirm={onConfirm}
      isLaunching={false}
    />
  )
}

test('is labelled by the campaign name and lists every check with a visible severity label, not color alone', async () => {
  preflightResponder = () =>
    jsonResponse({
      ready: true,
      checks: [
        check({ id: 'sequence_steps', severity: 'pass', title: 'Sequence has steps' }),
        check({
          id: 'schedule_windows',
          severity: 'warn',
          title: 'Narrow send window',
          detail: 'Only 2 hours/week.',
          remedy: 'Widen the schedule for faster delivery.',
        }),
      ],
    })

  renderWithProviders(<Harness />)

  const dialog = await screen.findByRole('alertdialog', { name: /launch.*q3 outbound/i })
  expect(await within(dialog).findByText('Sequence has steps')).toBeInTheDocument()
  expect(within(dialog).getByText('Narrow send window')).toBeInTheDocument()
  // Severity is a readable word, not just a colored dot.
  expect(within(dialog).getByText('Pass')).toBeInTheDocument()
  expect(within(dialog).getByText('Warn')).toBeInTheDocument()
  expect(within(dialog).getByText(/widen the schedule/i)).toBeInTheDocument()
})

test('a fail check disables the primary action; Cancel stays enabled', async () => {
  preflightResponder = () =>
    jsonResponse({
      ready: false,
      checks: [
        check({ id: 'sequence_steps', severity: 'pass' }),
        check({
          id: 'sender_pool',
          severity: 'fail',
          title: 'No eligible sender',
          detail: 'No enabled sender has an active mailbox.',
          remedy: 'Add a connected mailbox to the sender pool.',
        }),
      ],
    })
  const onConfirm = vi.fn()

  renderWithProviders(<Harness onConfirm={onConfirm} />)

  const dialog = await screen.findByRole('alertdialog')
  const launchButton = await within(dialog).findByRole('button', { name: /^launch campaign$/i })
  await waitFor(() => expect(launchButton).toBeDisabled())
  expect(within(dialog).getByRole('button', { name: /cancel/i })).toBeEnabled()

  // A disabled button does not fire on click — the mutation must never be called.
  fireEvent.click(launchButton)
  expect(onConfirm).not.toHaveBeenCalled()
})

test('a warn-only report relabels the primary action "Launch anyway" and confirming calls onConfirm and closes', async () => {
  preflightResponder = () =>
    jsonResponse({
      ready: true,
      checks: [check({ id: 'sequence_steps', severity: 'pass' }), check({ id: 'tracking', severity: 'warn' })],
    })
  const onConfirm = vi.fn()

  renderWithProviders(<Harness onConfirm={onConfirm} />)

  const dialog = await screen.findByRole('alertdialog')
  const launchButton = await within(dialog).findByRole('button', { name: /^launch anyway$/i })
  await waitFor(() => expect(launchButton).toBeEnabled())

  fireEvent.click(launchButton)

  expect(onConfirm).toHaveBeenCalledTimes(1)
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
})

test('an all-pass report shows "Launch campaign" enabled; confirming calls onConfirm and closes', async () => {
  preflightResponder = () => jsonResponse({ ready: true, checks: [check(), check({ id: 'audience' })] })
  const onConfirm = vi.fn()

  renderWithProviders(<Harness onConfirm={onConfirm} />)

  const dialog = await screen.findByRole('alertdialog')
  const launchButton = await within(dialog).findByRole('button', { name: /^launch campaign$/i })
  await waitFor(() => expect(launchButton).toBeEnabled())

  fireEvent.click(launchButton)

  expect(onConfirm).toHaveBeenCalledTimes(1)
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
})

test('Escape closes the dialog without calling onConfirm', async () => {
  const onConfirm = vi.fn()
  renderWithProviders(<Harness onConfirm={onConfirm} />)

  const dialog = await screen.findByRole('alertdialog')
  fireEvent.keyDown(dialog, { key: 'Escape' })

  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
  expect(onConfirm).not.toHaveBeenCalled()
})

test('a failed preflight fetch shows a retryable error and keeps the primary action disabled', async () => {
  preflightResponder = () => jsonResponse({ error: 'boom' }, 500)

  renderWithProviders(<Harness />)

  const dialog = await screen.findByRole('alertdialog')
  expect(await within(dialog).findByRole('alert')).toBeInTheDocument()
  const launchButton = within(dialog).getByRole('button', { name: /^launch campaign$/i })
  expect(launchButton).toBeDisabled()

  // Retrying re-fires the GET.
  preflightResponder = () => jsonResponse({ ready: true, checks: [check()] })
  fireEvent.click(within(dialog).getByRole('button', { name: /try again/i }))
  await waitFor(() => expect(within(dialog).getByRole('button', { name: /^launch campaign$/i })).toBeEnabled())
})
