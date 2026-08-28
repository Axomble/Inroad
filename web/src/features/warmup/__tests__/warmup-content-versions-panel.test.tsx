import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import { WarmupContentVersionsPanel } from '../warmup-content-versions-panel'
import type { ContentVersion } from '../content-version-copy'

function version(overrides: Partial<ContentVersion> = {}): ContentVersion {
  return {
    version: 'sl1:aaaaaaaaaaaaaaaa',
    inbox: 40,
    spam: 10,
    placement_sample: 50,
    inbox_rate: 0.8,
    spam_rate: 0.2,
    ...overrides,
  }
}

// An overview that never arrived publishes no split. Rendering "nothing observed
// yet" would describe a window nobody measured — the reading the observers and
// incidents panels each had to be corrected for.
test('nothing renders when no overview arrived', () => {
  const { container } = render(<WarmupContentVersionsPanel versions={undefined} />)

  expect(container).toBeEmptyDOMElement()
})

// The other empty answer IS rendered, because it is a real one.
test('an observed-nothing pool gets a section and a sentence', () => {
  render(<WarmupContentVersionsPanel versions={[]} />)

  expect(screen.getByRole('heading', { name: /placement by template/i })).toBeInTheDocument()
  expect(screen.getByText(/nothing to split by template/i)).toBeInTheDocument()
})

test('a template renders its fingerprint, its counts and both rates', () => {
  render(<WarmupContentVersionsPanel versions={[version()]} />)

  expect(screen.getByText('sl1:aaaaaaaa…')).toBeInTheDocument()
  // The full fingerprint stays reachable — it is the handle for matching the row
  // against the library.
  expect(screen.getByTitle('sl1:aaaaaaaaaaaaaaaa')).toBeInTheDocument()
  expect(screen.getByText(/40 inbox, 10 spam over 50 observations/)).toBeInTheDocument()
  expect(screen.getByText('80%')).toBeInTheDocument()
  expect(screen.getByText('20%')).toBeInTheDocument()
})

// The counts survive when the rates do not: below the floor the fold withholds the
// rates and keeps the evidence, and a row showing only two "Not established" cells
// would hide the observations that are actually there.
test('a below-floor template still shows the evidence behind its missing rates', () => {
  render(
    <WarmupContentVersionsPanel
      versions={[version({ inbox: 4, spam: 1, placement_sample: 5, inbox_rate: null, spam_rate: null })]}
    />,
  )

  expect(screen.getByText(/4 inbox, 1 spam over 5 observations/)).toBeInTheDocument()
  expect(screen.getAllByText('Not established')).toHaveLength(2)
  // The false-clean reading this panel must never produce.
  expect(screen.queryByText('0%')).not.toBeInTheDocument()
})

// The qualifier is the panel's whole defence against acting on a confounded number,
// so it must be on screen whenever a rate is.
test('the confound is stated wherever rates are shown', () => {
  render(<WarmupContentVersionsPanel versions={[version()]} />)

  const note = screen.getByText(/baked into its rate/i)
  expect(note).toBeInTheDocument()
  expect(note).toHaveTextContent(/never as a verdict on the content/i)
})

test('one template carries the no-comparison note and two do not', () => {
  const { unmount } = render(<WarmupContentVersionsPanel versions={[version()]} />)
  expect(screen.getByText(/nothing to compare it against/i)).toBeInTheDocument()
  unmount()

  render(
    <WarmupContentVersionsPanel versions={[version(), version({ version: 'sl1:bbbbbbbbbbbbbbbb' })]} />,
  )
  expect(screen.queryByText(/nothing to compare it against/i)).not.toBeInTheDocument()
})

// Best-evidenced first: the API's fingerprint order is alphabetical over a hash and
// tells a reader nothing about which row can support a conclusion.
test('rows render best-evidenced first, not worst-rate first', () => {
  render(
    <WarmupContentVersionsPanel
      versions={[
        version({ version: 'sl1:thin', inbox: 0, spam: 1, placement_sample: 1, inbox_rate: null, spam_rate: null }),
        version({ version: 'sl1:solid', inbox: 300, spam: 100, placement_sample: 400, inbox_rate: 0.75, spam_rate: 0.25 }),
      ]}
    />,
  )

  const labels = screen.getAllByText(/^sl1:/).map((el) => el.textContent)
  expect(labels[0]).toBe('sl1:solid')
})
