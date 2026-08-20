import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import { actorOrigin, actorTitle, parseActor } from '../actor'
import { ActorBadge } from '../actor-badge'

// Attribution is a trust claim: the badge tells a reader whether a human or an
// agent touched a record. The risks are (a) an unparseable actor being shown as
// a person and (b) an agent-created row looking identical to a hand-made one.

function badge(): HTMLElement {
  const element = document.querySelector('[data-slot="actor-badge"]')
  if (!(element instanceof HTMLElement)) throw new Error('no actor badge rendered')
  return element
}

test('parses an agent actor and keeps its run provenance', () => {
  const actor = parseActor({ type: 'agent', client_id: 'cli-9', thread_id: 'th-1', run_id: 'run-2' })
  expect(actor).toEqual({
    type: 'agent',
    client_id: 'cli-9',
    on_behalf_of_user_id: undefined,
    thread_id: 'th-1',
    run_id: 'run-2',
  })
})

test('parses a user actor', () => {
  expect(parseActor({ type: 'user', on_behalf_of_user_id: 'u-1' }).type).toBe('user')
})

test.each([
  ['an unrecognised type', { type: 'martian' }],
  ['a non-object', 'agent'],
  ['null', null],
  ['a wrongly-typed field', { type: 42 }],
])('falls back to a system actor for %s', (_name, raw) => {
  expect(parseActor(raw).type).toBe('system')
})

test('an empty object is a system actor, not a person', () => {
  expect(parseActor({}).type).toBe('system')
})

test('the actor outranks the deal source, and the source only decides the system case', () => {
  expect(actorOrigin({ type: 'user' }, 'reply')).toBe('user')
  expect(actorOrigin({ type: 'agent' }, 'manual')).toBe('agent')
  expect(actorOrigin({ type: 'system' }, 'agent')).toBe('agent')
  expect(actorOrigin({ type: 'system' }, 'reply')).toBe('reply')
  expect(actorOrigin({ type: 'system' }, 'manual')).toBe('system')
  expect(actorOrigin({ type: 'system' })).toBe('system')
})

test('renders an agent with a bot icon, the client in the label, and the run in the title', () => {
  render(<ActorBadge actor={parseActor({ type: 'agent', client_id: 'cli-9', thread_id: 'th-1', run_id: 'run-2' })} />)

  expect(badge()).toHaveTextContent('Agent / cli-9')
  expect(badge().dataset.origin).toBe('agent')
  expect(badge().querySelector('.lucide-bot')).not.toBeNull()
  expect(badge().title).toContain('Created by an AI agent')
  expect(badge().title).toContain('Agent thread th-1 / run run-2')
})

test('renders a workspace member with a person icon and no agent provenance', () => {
  render(<ActorBadge actor={parseActor({ type: 'user' })} source="manual" />)

  expect(badge()).toHaveTextContent('Workspace member')
  expect(badge().querySelector('.lucide-user-round')).not.toBeNull()
  expect(badge().querySelector('.lucide-bot')).toBeNull()
  expect(badge().title).toBe('Created by a workspace member.')
})

test('renders a reply-captured deal distinctly from a hand-made one', () => {
  const { unmount } = render(<ActorBadge actor={parseActor({ type: 'system' })} source="reply" />)
  expect(badge()).toHaveTextContent('Auto-captured')
  expect(badge().title).toContain('positive campaign reply')
  unmount()

  render(<ActorBadge actor={parseActor({ type: 'system' })} source="manual" />)
  expect(badge()).toHaveTextContent('Inroad automation')
})

test('a malformed actor renders as automation rather than as a person', () => {
  render(<ActorBadge actor={parseActor('{"type":"agent"')} />)

  expect(badge()).toHaveTextContent('Inroad automation')
  expect(badge().dataset.origin).toBe('system')
  expect(badge().querySelector('.lucide-bot')).toBeNull()
})

test('the label carries the origin, so color is never the only signal', () => {
  render(<ActorBadge actor={parseActor({ type: 'agent' })} />)
  // No client id: the bare label still says "Agent".
  expect(screen.getByText('Agent')).toBeInTheDocument()
})

test('the title names the acting user when an agent runs on a member behalf', () => {
  const title = actorTitle(parseActor({ type: 'agent', on_behalf_of_user_id: 'u-7' }))
  expect(title).toContain('On behalf of user u-7')
})
