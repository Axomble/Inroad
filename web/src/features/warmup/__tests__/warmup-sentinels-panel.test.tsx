import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import type { WarmupMailbox } from '@/store/api'
import { WarmupSentinelsPanel } from '../warmup-sentinels-panel'

function participant(overrides: Partial<WarmupMailbox> = {}): WarmupMailbox {
  return {
    mailbox_id: 'mb-1',
    email: 'one@acme.test',
    enabled: true,
    health_state: 'healthy',
    health_reason: '',
    lane: 'healthy',
    lane_reason: '',
    today_sent: 4,
    today_target: 4,
    placement_sample_7d: 40,
    inbox_rate_7d: 0.9,
    spam_rate_7d: 0.1,
    ...overrides,
  }
}

function pool(size: number, sentinels: number): WarmupMailbox[] {
  return Array.from({ length: size }, (_, i) =>
    participant({ mailbox_id: `mb-${i + 1}`, email: `mb${i + 1}@acme.test`, is_sentinel: i < sentinels }),
  )
}

function renderPanel(count: number | undefined, sentinels = count ?? 0, size = 6, oversized = false) {
  return render(
    <WarmupSentinelsPanel count={count} oversized={oversized} share={0.5} pool={pool(size, sentinels)} />,
  )
}

function panelText(): string {
  return document.querySelector('[data-slot="warmup-sentinels"]')?.textContent ?? ''
}

function advisory(): HTMLElement | null {
  return document.querySelector('[data-slot="sentinel-advisory"]')
}

// Absent is not zero. A build that never mentions sentinels has said nothing
// about this pool, and an empty state would answer a question nobody asked.
test('a build that does not report sentinels draws nothing', () => {
  renderPanel(undefined)

  expect(document.querySelector('[data-slot="warmup-sentinels"]')).toBeNull()
})

test('a pool with no sentinels renders the ordinary-case reading, not an alert', () => {
  renderPanel(0)

  expect(panelText()).toMatch(/ordinary arrangement/i)
  // Not an alert, not a warning: this pool is working exactly as designed.
  expect(screen.queryByRole('alert')).toBeNull()
})

// With no sentinel designated every card below reads "peer-only", so this is
// exactly where the label must be prevented from reading as a discount.
test('the not-a-penalty note is on screen even when no sentinel is designated', () => {
  renderPanel(0)

  expect(panelText()).toMatch(/never a penalty/i)
  expect(panelText()).toMatch(/not discounted/i)
})

test('a designated pool names its sentinels and keeps the not-a-penalty note', () => {
  renderPanel(2, 2)

  expect(screen.getByText('mb1@acme.test')).toBeInTheDocument()
  expect(screen.getByText('mb2@acme.test')).toBeInTheDocument()
  expect(panelText()).toMatch(/2 of 6 mailboxes/)
  expect(panelText()).toMatch(/never a penalty/i)
})

test('a pool within the advised share carries no advisory node at all', () => {
  renderPanel(2, 2)

  expect(advisory()).toBeNull()
})

// Advisory, never enforced — so it must not be announced as an alert either. A
// note rendered with the machinery of a failure IS a sanction, whatever it says.
test('an oversized pool is advised without being alarmed', () => {
  renderPanel(4, 4, 5, true)

  expect(advisory()?.textContent ?? '').toMatch(/nothing is enforced/i)
  expect(screen.queryByRole('alert')).toBeNull()
})
