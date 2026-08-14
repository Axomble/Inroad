import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import type { WarmupMailbox } from '@/store/api'
import { WarmupIdentityPanel } from './warmup-identity-panel'
import type { WarmupIdentity } from './identity-copy'

const identity: WarmupIdentity = {
  dkim_domain: 'acme.test',
  return_path_domain: 'bounces.acme.test',
  spf_result: 'pass',
  dkim_result: 'pass',
  dmarc_result: 'pass',
  observed_at: '2026-08-14T09:30:00Z',
}

function renderPanel(value: WarmupMailbox['identity']) {
  return render(<WarmupIdentityPanel identity={value} />)
}

/** The panel as an operator reads it: all its text, in one string. */
function panelText(): string {
  return document.querySelector('[data-slot="warmup-identity"]')?.textContent ?? ''
}

/** One verdict's rendered node, found through its own label. */
function verdictOf(label: string): HTMLElement {
  const term = screen.getByText(label, { selector: 'dt' })
  const node = term.parentElement
  if (!node) throw new Error(`no verdict rendered for ${label}`)
  return node
}

/**
 * The verdict itself, without its label or its explanatory sentence.
 *
 * Compared alone deliberately: two verdicts whose whole nodes differ can still
 * be reading identically, because the label ("SPF" vs "DMARC") differs no matter
 * what and a long detail sentence drowns the one word that matters. Asserting on
 * the node would let `none` render as "not reported by the receiver" and pass.
 */
function verdictValue(label: string): string {
  return verdictOf(label).querySelector('[data-slot="verdict-value"]')?.textContent ?? ''
}

test('a fully stamped mailbox shows both identities and all three verdicts', () => {
  renderPanel(identity)

  expect(screen.getByText('acme.test')).toBeInTheDocument()
  expect(screen.getByText('bounces.acme.test')).toBeInTheDocument()
  expect(verdictOf('SPF')).toHaveTextContent('pass')
  expect(verdictOf('DKIM')).toHaveTextContent('pass')
  expect(verdictOf('DMARC')).toHaveTextContent('pass')
  // The panel is the last OBSERVATION, not a configuration, so it is dated.
  expect(panelText()).toMatch(/observed/i)
})

// The distinction the whole panel exists to preserve, asserted on what actually
// reaches the screen: a receiver that checked and found no SPF record has said
// something; a receiver that stamped nothing has not. Rendering them alike is
// invisible in review — both look plausible.
test('a checked-and-absent verdict does not render like an unreported one', () => {
  renderPanel({ ...identity, spf_result: 'none', dmarc_result: 'unknown' })

  // The verdicts themselves, not the nodes around them: the labels differ
  // whatever happens, so comparing whole rows would call a collapse a difference.
  expect(verdictValue('SPF')).not.toBe(verdictValue('DMARC'))
  expect(verdictValue('SPF')).toMatch(/no SPF record/i)
  expect(verdictValue('SPF')).not.toMatch(/not reported/i)
  expect(verdictValue('DMARC')).toMatch(/not reported by the receiver/i)

  // And the sentences under them say two different things too.
  expect(verdictOf('SPF')).toHaveTextContent(/looked and found/i)
  expect(verdictOf('DMARC')).not.toHaveTextContent(/looked and found/i)
})

// A provider that stamps no Authentication-Results leaves a mailbox permanently
// unknown on all three axes. That is our blind spot; presenting it in the danger
// tone would send an operator to fix authentication nobody said was broken.
test('a never-stamped mailbox reads as unreported, and never as failing', () => {
  renderPanel({ ...identity, spf_result: 'unknown', dkim_result: 'unknown', dmarc_result: 'unknown' })

  for (const label of ['SPF', 'DKIM', 'DMARC']) {
    const verdict = verdictOf(label)
    expect(verdict).toHaveTextContent(/not reported by the receiver/i)
    expect(verdict.querySelector('.text-danger')).toBeNull()
  }
  expect(panelText()).toMatch(/absence of observation, not a failed check/i)
})

// Silence on all three checks is one fact about the partners that read this
// mail, not three separate blanks that might each fill in later. Said once, and
// said as normal — an operator who reads three "unreported" rows without it goes
// looking for three things to fix.
test('a mailbox nobody stamps is told the silence is permanent, once', () => {
  renderPanel({ ...identity, spf_result: 'unknown', dkim_result: 'unknown', dmarc_result: 'unknown' })

  const note = screen.getByText(/stay unreported however well the mail authenticates/i)
  expect(note).toBeInTheDocument()
  expect(panelText().split('stay unreported however well').length - 1).toBe(1)
  expect(note).toHaveTextContent(/not a finding about this mailbox/i)
})

// One reported verdict and the silence is no longer the story: the note would
// be describing partners that plainly do stamp results.
test('the permanence note is absent as soon as one verdict is reported', () => {
  renderPanel({ ...identity, spf_result: 'pass', dkim_result: 'unknown', dmarc_result: 'unknown' })

  expect(screen.queryByText(/stay unreported however well the mail authenticates/i)).not.toBeInTheDocument()
})

// Colour is never the only signal here, and neither is it the only redundancy:
// an unreported verdict carries a hollow node and a reported one a filled node,
// so "nobody looked" is distinguishable from "someone looked" at a glance and
// without relying on hue at all.
test('an unreported verdict is marked by shape, not by colour alone', () => {
  renderPanel({ ...identity, spf_result: 'pass', dmarc_result: 'unknown' })

  const reported = verdictOf('SPF').querySelector('[aria-hidden="true"]')
  const unreported = verdictOf('DMARC').querySelector('[aria-hidden="true"]')

  expect(reported?.className).toContain('bg-current')
  expect(unreported?.className).toContain('bg-transparent')
  expect(unreported?.className).toContain('border')
})

// A fail is a real negative and reads as one — and still decides nothing, so it
// carries the tabbed rate's marker verbatim (design §7).
test('a failing verdict is toned as a negative and labelled as gating nothing', () => {
  renderPanel({ ...identity, dmarc_result: 'fail' })

  const dmarc = verdictOf('DMARC')
  expect(dmarc).toHaveTextContent('fail')
  expect(dmarc.querySelector('.text-danger')).not.toBeNull()
  expect(dmarc).toHaveTextContent(/fail[^·]*· gates nothing/)
})

// The marker belongs to the failure, not to the panel wallpaper: on a clean
// mailbox there is no negative to disclaim, and the panel's own note already
// says nothing here gates anything.
test('the gates-nothing marker sits on the failure, not on every verdict', () => {
  renderPanel({ ...identity, spf_result: 'fail' })

  expect(verdictOf('SPF')).toHaveTextContent(/· gates nothing/)
  expect(verdictOf('DKIM')).not.toHaveTextContent(/gates nothing/)
})

test('the panel states that none of it gates anything', () => {
  renderPanel(identity)

  expect(panelText()).toMatch(/no threshold, lane or promotion decision reads any of it/i)
})

// Nothing has been observed yet. Five "unknown" verdicts would report five
// checks that came back empty — a different and more alarming claim.
test('an unobserved identity says so instead of showing verdicts', () => {
  renderPanel(null)

  expect(screen.getByText(/has been observed with identity facts yet/i)).toBeInTheDocument()
  expect(panelText()).toMatch(/not a failed check/i)
  expect(screen.queryByText('SPF', { selector: 'dt' })).not.toBeInTheDocument()
  expect(panelText()).not.toMatch(/not reported by the receiver/i)
  // With nothing observed there is no observation to date.
  expect(panelText()).not.toMatch(/observed 1[0-9]/)
})

test('a server that does not report identity at all reads as unobserved', () => {
  renderPanel(undefined)

  expect(screen.getByText(/has been observed with identity facts yet/i)).toBeInTheDocument()
})

// Unsigned or unparseable — the same fact by design. What it must never be is a
// blank cell, which reads as a rendering fault rather than as information.
test('an unsigned message says "not signed" rather than leaving a gap', () => {
  renderPanel({ ...identity, dkim_domain: '' })

  const signingDomain = screen.getByText('DKIM signing domain', { selector: 'dt' }).parentElement
  expect(signingDomain).toHaveTextContent(/not signed/i)
  expect(signingDomain?.textContent?.trim().length ?? 0).toBeGreaterThan(0)
  // And it is not dressed as data: only a real domain gets the mono treatment.
  expect(signingDomain?.querySelector('.font-mono.text-foreground')).toBeNull()
})

test('a recorded signing domain is rendered as the datum it is', () => {
  renderPanel(identity)

  const signingDomain = screen.getByText('DKIM signing domain', { selector: 'dt' }).parentElement
  expect(signingDomain?.querySelector('.font-mono.text-foreground')).not.toBeNull()
})

// An unusable timestamp must cost the timestamp, not the panel: Intl throws on
// an invalid date, and a thrown panel tells the operator nothing at all.
test('an unusable observation time drops the time, not the facts', () => {
  renderPanel({ ...identity, observed_at: '' })

  expect(verdictOf('SPF')).toHaveTextContent('pass')
  expect(panelText()).not.toMatch(/invalid date/i)
})
